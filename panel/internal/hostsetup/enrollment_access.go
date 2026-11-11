package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"syscall"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"golang.org/x/sys/unix"
)

const enrollmentBundle = "access-bundle"

func enrollmentBundleAbsent(parent *os.Root, name string) bool {
	_, err := parent.Lstat(name)
	return errors.Is(err, os.ErrNotExist)
}

func stageEnrollmentAccess(ctx context.Context, root *os.Root, intent enrollmentIntent) error {
	if root.Mkdir(enrollmentBundle, 0700) != nil {
		return ErrEnrollment
	}
	bundle, err := root.OpenRoot(enrollmentBundle)
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = bundle.Close() }()
	for _, file := range intent.Files {
		if bundle.Mkdir(file.User, 0755) != nil {
			return ErrEnrollment
		}
		user, err := bundle.OpenRoot(file.User)
		if err != nil {
			return ErrEnrollment
		}
		directory, openErr := user.Open(".")
		if openErr != nil {
			_ = user.Close()
			return ErrEnrollment
		}
		err = directory.Chmod(0755)
		_ = directory.Close()
		if err == nil {
			err = copyPrepared(ctx, user, "authorized_keys", bytes.NewReader(file.Bytes), int64(len(file.Bytes)))
		}
		if err == nil {
			var opened *os.File
			opened, err = privatefile.OpenNoFollow(user, "authorized_keys", os.O_RDWR, 0)
			if err == nil {
				err = opened.Chmod(0644)
				if err == nil {
					err = opened.Sync()
				}
				_ = opened.Close()
			}
		}
		if err == nil {
			err = syncEnrollmentDirectory(user)
		}
		_ = user.Close()
		if err != nil {
			return ErrEnrollment
		}
	}
	if syncEnrollmentDirectory(bundle) != nil || syncEnrollmentDirectory(root) != nil {
		return ErrEnrollment
	}
	return nil
}

func syncEnrollmentDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func checkEnrollmentAccess(ctx context.Context, parent *os.Root, name string, files []enrollmentKeyFile, partial bool) error {
	before, err := parent.Lstat(name)
	if partial && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || before == nil || !before.IsDir() || !sshOwned(before, 0) {
		return ErrEnrollment
	}
	bundle, err := parent.OpenRoot(name)
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = bundle.Close() }()
	if !sameSSHDirectory(bundle, before, 0) {
		return ErrEnrollment
	}
	directory, err := bundle.Open(".")
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(4)
	if (err != nil && !errors.Is(err, io.EOF)) || len(names) > len(files) || (!partial && len(names) != len(files)) {
		return ErrEnrollment
	}
	for _, name := range names {
		index := slices.IndexFunc(files, func(file enrollmentKeyFile) bool { return file.User == name })
		if index < 0 {
			return ErrEnrollment
		}
		user, err := openSSHChild(bundle, name, 0)
		if err != nil {
			return ErrEnrollment
		}
		child, err := user.Open(".")
		if err != nil {
			_ = user.Close()
			return ErrEnrollment
		}
		leaves, err := child.Readdirnames(2)
		_ = child.Close()
		if (err != nil && !errors.Is(err, io.EOF)) || len(leaves) > 1 || (!partial && len(leaves) != 1) || (len(leaves) == 1 && leaves[0] != "authorized_keys") {
			_ = user.Close()
			return ErrEnrollment
		}
		file, err := readSSHAuthorityFile(ctx, user, "authorized_keys", sshAuthorityFile{}, 0)
		_ = user.Close()
		if err != nil || (!partial && !file.Present) || (file.Present && (file.UID != 0 || file.Mode != 0644 || !bytes.Equal(file.Bytes, files[index].Bytes))) {
			return ErrEnrollment
		}
	}
	return nil
}

func publishEnrollmentAccess(ctx context.Context, root *os.Root, intent enrollmentIntent, parent *os.Root) error {
	if checkEnrollmentAccess(ctx, root, enrollmentBundle, intent.Files, false) != nil || !enrollmentBundleAbsent(parent, "panel-access") {
		return ErrEnrollment
	}
	bundle, err := root.OpenRoot(enrollmentBundle)
	if err != nil {
		return ErrEnrollment
	}
	directory, err := bundle.Open(".")
	if err != nil {
		_ = bundle.Close()
		return ErrEnrollment
	}
	err = directory.Chmod(0755)
	if err == nil {
		err = directory.Sync()
	}
	_ = directory.Close()
	_ = bundle.Close()
	if err != nil {
		return ErrEnrollment
	}
	source, err := root.Open(".")
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = source.Close() }()
	target, err := parent.Open(".")
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = target.Close() }()
	if unix.Renameat2(int(source.Fd()), enrollmentBundle, int(target.Fd()), "panel-access", unix.RENAME_NOREPLACE) != nil {
		return ErrEnrollment
	}
	if source.Sync() != nil || target.Sync() != nil {
		return ErrUnresolved
	}
	return nil
}

// Removal is bounded to the already verified exact owned tree. Missing leaves
// support interruption during cleanup; extra or changed content is never erased.
func removeEnrollmentAccess(ctx context.Context, parent *os.Root, intent enrollmentIntent) error {
	if checkEnrollmentAccess(ctx, parent, "panel-access", intent.Files, true) != nil {
		return ErrEnrollment
	}
	if enrollmentBundleAbsent(parent, "panel-access") {
		return nil
	}
	bundle, err := parent.OpenRoot("panel-access")
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = bundle.Close() }()
	for _, file := range intent.Files {
		user, err := openSSHChild(bundle, file.User, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ErrEnrollment
		}
		if err = user.Remove("authorized_keys"); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = user.Close()
			return ErrEnrollment
		}
		if syncEnrollmentDirectory(user) != nil {
			_ = user.Close()
			return ErrUnresolved
		}
		_ = user.Close()
		if bundle.Remove(file.User) != nil {
			return ErrEnrollment
		}
	}
	if syncEnrollmentDirectory(bundle) != nil || parent.Remove("panel-access") != nil || syncEnrollmentDirectory(parent) != nil {
		return ErrUnresolved
	}
	return nil
}

func enrollmentRootInfo(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == 0 && info.Mode().Perm()&0022 == 0
}
