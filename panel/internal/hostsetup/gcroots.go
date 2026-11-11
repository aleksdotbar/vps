package hostsetup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

func gcLockPath(tools importTools) string {
	path := "/nix/var/nix/gc.lock"
	if strings.HasPrefix(tools.store, "local?root=") {
		path = strings.TrimPrefix(tools.store, "local?root=") + path
	}
	return path
}

// Nix 2.34.9's singleton gc.lock uses flock (not POSIX record locks). Publishing
// permanent roots and observing them under its shared lock excludes a collector
// already past root discovery. Never unlink/replace this native lock inode.
func observeRootedImport(ctx context.Context, tools importTools, build localBuild) (bool, error) {
	lock, err := privatefile.OpenPrivate(gcLockPath(tools), os.O_RDWR|os.O_CREATE|syscall.O_NONBLOCK)
	if err != nil {
		return false, ErrUnresolved
	}
	defer func() { _ = lock.Close() }()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		return false, ErrUnresolved
	}
	if !protectImportRoots(tools, build) {
		return false, ErrUnresolved
	}
	return observedImport(ctx, tools, build), nil
}

func rootsDirectory(tools importTools, build localBuild) string {
	parent := "/nix/var/nix/gcroots"
	if strings.HasPrefix(tools.store, "local?root=") {
		parent = strings.TrimPrefix(tools.store, "local?root=") + parent
	}
	return filepath.Join(parent, "panel-host-"+build.InstallationID)
}

// Direct roots may name not-yet-valid paths. Native import adds temporary roots
// before restoration; those roots bridge concurrent GC until these links protect
// the complete imported closure. No realise/build/substitution operation is used.
func protectImportRoots(tools importTools, build localBuild) bool {
	path := rootsDirectory(tools, build)
	parentInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0022 != 0 {
		return false
	}
	parentOwner, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || int64(parentOwner.Uid) != int64(os.Geteuid()) {
		return false
	}
	if privatefile.PrivateDirectory(path) != nil {
		return false
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	for name, target := range map[string]string{"enrollment": build.EnrollmentSystem, "final": build.FinalSystem, "panel": build.PanelStorePath} {
		if err := root.Symlink(target, name); err != nil && !os.IsExist(err) {
			return false
		}
		info, err := root.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return false
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int64(owner.Uid) != int64(os.Geteuid()) {
			return false
		}
		actual, err := root.Readlink(name)
		if err != nil || actual != target {
			return false
		}
	}
	directory, err := root.Open(".")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	if directory.Sync() != nil {
		return false
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return false
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync() == nil
}
