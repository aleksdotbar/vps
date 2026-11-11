package privatefile

import (
	"os"

	"golang.org/x/sys/unix"
)

// OpenNoFollow uses the supported Linux target's native path resolution boundary.
// os.Root.OpenFile resolves in-root symlinks itself, even with O_NOFOLLOW, so that
// flag alone cannot protect key files, lock files or installation handoffs.
func OpenNoFollow(root *os.Root, name string, flags int, permissions os.FileMode) (*os.File, error) {
	if flags < 0 {
		return nil, ErrProtection
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	how := unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	}
	if flags&unix.O_CREAT != 0 {
		how.Mode = uint64(permissions.Perm())
	}
	fd, err := unix.Openat2(int(directory.Fd()), name, &how)
	if err != nil {
		return nil, &os.PathError{Op: "open protected path", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrProtection
	}
	return file, nil
}
