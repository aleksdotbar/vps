package hosthelper

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

const hostStateDirectory = "/var/lib/panel-host"

// ErrHostBusy reports that setup or another host mutation holds the host lock.
var ErrHostBusy = errors.New("host is busy with setup or another mutation")

// AcquireHost serializes host mutations with `panel setup` and binds the
// operation to the generation that is both running and the system profile. A
// different booted generation is normal after nixos-rebuild switch.
func AcquireHost(ctx context.Context) (HostLease, error) {
	if err := ctx.Err(); err != nil {
		return HostLease{}, err
	}
	if privatefile.PrivateDirectory(hostStateDirectory) != nil {
		return HostLease{}, ErrIdentity
	}
	root, err := os.OpenRoot(hostStateDirectory)
	if err != nil {
		return HostLease{}, ErrIdentity
	}
	defer func() { _ = root.Close() }()
	lock, err := privatefile.OpenNoFollow(root, "setup.lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return HostLease{}, ErrIdentity
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return HostLease{}, ErrHostBusy
		}
		return HostLease{}, ErrIdentity
	}
	retained, err := readIdentity()
	if err != nil || retained.Role != "panel" {
		_ = lock.Close()
		return HostLease{}, ErrIdentity
	}
	observed, err := observeSystems()
	if err != nil || observed.current != observed.profile {
		_ = lock.Close()
		return HostLease{}, ErrIdentity
	}
	return HostLease{InstallationID: retained.InstallationID, ServerID: retained.ServerID, System: observed.current, Release: lock.Close}, nil
}
