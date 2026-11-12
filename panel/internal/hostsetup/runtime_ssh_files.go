package hostsetup

import (
	"context"
	"io"
	"os"
	"syscall"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

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
