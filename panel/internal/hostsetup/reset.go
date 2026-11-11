package hostsetup

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrActivationStarted = errors.New("enrollment has started; recorded setup can no longer be reset")

// ResetUnactivated discards the recorded build and its GC roots so a corrected
// installer can rediscover the host. Once enrollment intent exists, the record
// is the only rollback evidence and must be kept.
func ResetUnactivated() error {
	if os.Geteuid() != 0 {
		return ErrPrivilege
	}
	root, err := os.OpenRoot(hostStateDirectory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrIdentity
	}
	if err == nil {
		lock, lockErr := stateLock(root)
		if lockErr != nil {
			_ = root.Close()
			return lockErr
		}
		_, started := root.Lstat("enrollment.json")
		_ = lock.Close()
		_ = root.Close()
		if !errors.Is(started, os.ErrNotExist) {
			return ErrActivationStarted
		}
		if os.RemoveAll(hostStateDirectory) != nil {
			return ErrUnresolved
		}
	}
	roots, _ := filepath.Glob("/nix/var/nix/gcroots/panel-host-*")
	for _, path := range roots {
		if os.RemoveAll(path) != nil {
			return ErrUnresolved
		}
	}
	return nil
}
