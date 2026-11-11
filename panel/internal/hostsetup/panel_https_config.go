package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func publishPanelOrigin(ctx context.Context, setup *os.Root, origin string) error {
	parent, err := openSSHDirectory("/var/lib", 0)
	if err != nil {
		return ErrPanelHTTPS
	}
	defer func() { _ = parent.Close() }()
	return publishPanelOriginAt(ctx, setup, parent, origin)
}
func publishPanelOriginAt(ctx context.Context, setup, parent *os.Root, origin string) error {
	// Only a previously bound root setup receipt permits recovery of an interrupted
	// publication. A same-looking file without this intent cannot authorize adoption.
	var intent panelHTTPSIntent
	if readState(setup, "panel-https.json", &intent) != nil || intent.Origin != origin {
		return ErrPanelHTTPS
	}
	if err := parent.Mkdir("panel-config", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrPanelHTTPS
	}
	if syncEnrollmentDirectory(parent) != nil {
		return ErrUnresolved
	}
	info, err := parent.Lstat("panel-config")
	if err != nil || info == nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return ErrPanelHTTPS
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(owner.Uid) != int64(os.Geteuid()) {
		return ErrPanelHTTPS
	}
	root, err := openSSHChild(parent, "panel-config", owner.Uid)
	if err != nil {
		return ErrPanelHTTPS
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Open(".")
	if err != nil {
		return ErrPanelHTTPS
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrPanelHTTPS
	}
	if len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != "serve.env") {
		return ErrPanelHTTPS
	}
	expected := []byte("PANEL_ORIGIN=" + origin + "\n")
	if len(entries) == 1 {
		file, err := openPrepared(root, "serve.env", 1024, int64(os.Geteuid()))
		if err != nil {
			return ErrPanelHTTPS
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(io.LimitReader(file, 1025))
		if err != nil || !bytes.Equal(data, expected) {
			return ErrPanelHTTPS
		}
		return nil
	}
	// Anonymous native staging cannot leave a blocking named orphan after a crash.
	// Unsupported filesystems fail closed; no unsafe named-temp fallback.
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrPanelConfigStorage
	}
	temporary := os.NewFile(uintptr(fd), "panel-origin-publication")
	if temporary == nil {
		_ = unix.Close(fd)
		return ErrPanelConfigStorage
	}
	defer func() { _ = temporary.Close() }()
	if ctx.Err() != nil {
		return ErrUnresolved
	}
	if _, err := temporary.Write(expected); err != nil || temporary.Sync() != nil || ctx.Err() != nil {
		return ErrUnresolved
	}
	// Linking the complete inode is atomic and never replaces an existing file.
	if err := unix.Linkat(fd, "", int(directory.Fd()), "serve.env", unix.AT_EMPTY_PATH); err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return ErrPanelConfigStorage
		}
		return ErrUnresolved
	}
	if directory.Sync() != nil {
		return ErrUnresolved
	}
	return nil
}
