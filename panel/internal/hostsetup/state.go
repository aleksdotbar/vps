package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

var (
	ErrPrivilege  = errors.New("installer import requires root")
	ErrIdentity   = errors.New("setup operation or installation identity does not match")
	ErrUnresolved = errors.New("setup import outcome is unresolved; inspect the retained operation before retrying")
	ErrResources  = errors.New("setup import requires supported native tools and sufficient disk and memory")
)

type hostIdentity struct {
	InstallationID string          `json:"installation_id"`
	ServerID       string          `json:"server_id"`
	Role           foundation.Role `json:"role"`
}

type importIntent struct {
	OperationID string       `json:"operation_id"`
	Build       localBuild   `json:"build"`
	Pins        adoptionPins `json:"pins"`
}

type importReceipt struct {
	OperationID string `json:"operation_id"`
	Phase       string `json:"phase"`
}

func canonicalOperation(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func readState(root *os.Root, name string, value any) error {
	file, err := openPrepared(root, name, maxStateBytes, int64(os.Geteuid()))
	if err != nil {
		if _, statErr := root.Lstat(name); errors.Is(statErr, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return ErrIdentity
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil || len(data) > maxStateBytes || json.Unmarshal(data, value) != nil {
		return ErrIdentity
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return ErrIdentity
	}
	return nil
}

// writeState publishes only synced complete records. Identity/intent use native
// no-replace; phase updates replace only the previous private receipt atomically.
func writeState(ctx context.Context, root *os.Root, name string, value any, replace bool) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxStateBytes {
		return ErrIdentity
	}
	temporary := ".record-" + uuid.NewString()
	defer func() { _ = root.Remove(temporary) }()
	if err := copyPrepared(ctx, root, temporary, bytes.NewReader(data), int64(len(data))); err != nil {
		return ErrUnresolved
	}
	directory, err := root.Open(".")
	if err != nil {
		return ErrUnresolved
	}
	defer func() { _ = directory.Close() }()
	flags := uint(unix.RENAME_NOREPLACE)
	if replace {
		flags = 0
	}
	if unix.Renameat2(int(directory.Fd()), temporary, int(directory.Fd()), name, flags) != nil || directory.Sync() != nil {
		return ErrUnresolved
	}
	return nil
}

func stateLock(root *os.Root) (*os.File, error) {
	file, err := privatefile.OpenNoFollow(root, "setup.lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, ErrHandoff
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = file.Close()
		return nil, ErrHandoff
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(owner.Uid) != int64(os.Geteuid()) {
		_ = file.Close()
		return nil, ErrHandoff
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, ErrHandoff
	}
	return file, nil
}

// Detached runners must not lose their single recovery attempt to an ordinary
// status request. Wait only for exclusion; callers still re-read durable state
// before any effect, and cancellation never authorizes repeating a switch.
func waitStateLock(ctx context.Context, root *os.Root) (*os.File, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := stateLock(root)
		if !errors.Is(err, ErrBusy) {
			return file, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// adoptionPins carries the build record hash every later stage must present.
type adoptionPins struct {
	BuildSHA256 string `json:"build_sha256"`
}

func (pins adoptionPins) valid() bool { return sha256Hex.MatchString(pins.BuildSHA256) }
