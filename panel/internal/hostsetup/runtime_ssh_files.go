package hostsetup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

func readRenderedSSHKeys(ctx context.Context, result sshAuthorityFile, expected string) (sshAuthorityFile, error) {
	_, wanted := os.Stat(expected)
	_, actual := os.Lstat(result.Path)
	if os.IsNotExist(wanted) && os.IsNotExist(actual) {
		return result, nil
	}
	if wanted != nil || actual != nil || !sameNativeFile(result.Path, expected) {
		return result, ErrRuntimeSSH
	}
	resolved, err := filepath.EvalSymlinks(result.Path)
	if err != nil || !strings.HasPrefix(resolved, "/nix/store/") {
		return result, ErrRuntimeSSH
	}
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return result, ErrRuntimeSSH
	}
	defer func() { _ = root.Close() }()
	return readSSHAuthorityFile(ctx, root, filepath.Base(resolved), result, 0)
}

func openSSHDirectory(path string, uid uint32) (*os.Root, error) {
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, os.ErrNotExist
	}
	if err != nil || before == nil || !before.IsDir() || !sshOwned(before, uid) {
		return nil, ErrRuntimeSSH
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrRuntimeSSH
	}
	if !sameSSHDirectory(root, before, uid) {
		_ = root.Close()
		return nil, ErrRuntimeSSH
	}
	return root, nil
}

func openSSHChild(parent *os.Root, name string, uid uint32) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return nil, os.ErrNotExist
	}
	if err != nil || before == nil || !before.IsDir() || !sshOwned(before, uid) {
		return nil, ErrRuntimeSSH
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, ErrRuntimeSSH
	}
	if !sameSSHDirectory(root, before, uid) {
		_ = root.Close()
		return nil, ErrRuntimeSSH
	}
	return root, nil
}

func sameSSHDirectory(root *os.Root, before os.FileInfo, uid uint32) bool {
	directory, err := root.Open(".")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	after, err := directory.Stat()
	return err == nil && after != nil && os.SameFile(before, after) && sshOwned(after, uid)
}

func readHomeSSHKeys(ctx context.Context, result sshAuthorityFile, path string, uid uint32) (sshAuthorityFile, error) {
	home, err := openSSHDirectory(path, uid)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer func() { _ = home.Close() }()
	directory, err := openSSHChild(home, ".ssh", uid)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer func() { _ = directory.Close() }()
	return readSSHAuthorityFile(ctx, directory, "authorized_keys", result, uid)
}

func readRuntimeSSHKeys(ctx context.Context, result sshAuthorityFile, user string) (sshAuthorityFile, error) {
	parent, err := openSSHDirectory("/var/lib/panel-access", 0)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer func() { _ = parent.Close() }()
	directory, err := openSSHChild(parent, user, 0)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer func() { _ = directory.Close() }()
	return readSSHAuthorityFile(ctx, directory, "authorized_keys", result, 0)
}

func readSSHAuthorityFile(ctx context.Context, root *os.Root, name string, result sshAuthorityFile, uid uint32) (sshAuthorityFile, error) {
	file, err := privatefile.OpenNoFollow(root, name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, ErrRuntimeSSH
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || before == nil || !before.Mode().IsRegular() || before.Size() > maxSSHFileBytes || !sshOwned(before, uid) {
		return result, ErrRuntimeSSH
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return result, ErrRuntimeSSH
	}
	data, err := io.ReadAll(io.LimitReader(setupReader{ctx, file}, maxSSHFileBytes+1))
	if err != nil || len(data) > maxSSHFileBytes {
		return result, ErrRuntimeSSH
	}
	after, err := file.Stat()
	if err != nil || after == nil || before.Size() != after.Size() || before.ModTime() != after.ModTime() || !os.SameFile(before, after) {
		return result, ErrRuntimeSSH
	}
	result.Present = true
	result.UID = owner.Uid
	result.Mode = uint32(before.Mode().Perm())
	result.Bytes = data
	return result, nil
}
