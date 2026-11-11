package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

const runtimeStateDirectory = "/var/lib/panel-routes"
const maxRuntimeStateBytes = 8 << 20

var runtimeSystemPath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-nixos-system-[A-Za-z0-9.+_-]+$`)

type runtimeKey struct {
	OperationID    string `json:"operation_id"`
	ReleaseID      string `json:"release_id"`
	EnvironmentID  string `json:"environment_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
}

func operationKey(operation RuntimeOperation) runtimeKey {
	return runtimeKey{operation.Snapshot.OperationID, operation.Snapshot.ReleaseID, operation.Snapshot.EnvironmentID, operation.ManifestSHA256, operation.Snapshot.SHA256}
}

type runtimeAllocation struct {
	Key           runtimeKey           `json:"key"`
	Origins       []Origin             `json:"origins"`
	PriorCurrent  *runtimeContribution `json:"prior_current,omitempty"`
	PriorPrevious *runtimeContribution `json:"prior_previous,omitempty"`
}

type runtimeContribution struct {
	Key          runtimeKey   `json:"key"`
	Contribution Contribution `json:"contribution"`
	Origins      []Origin     `json:"origins"`
}

type runtimeEnvironment struct {
	EnvironmentID string               `json:"environment_id"`
	Current       *runtimeContribution `json:"current,omitempty"`
	Previous      *runtimeContribution `json:"previous,omitempty"`
	Restored      *runtimeKey          `json:"restored,omitempty"`
}

type runtimePending struct {
	Key              runtimeKey           `json:"key"`
	Restore          bool                 `json:"restore"`
	FromRevision     uint64               `json:"from_revision"`
	FromConfigSHA256 string               `json:"from_config_sha256"`
	Target           *runtimeContribution `json:"target,omitempty"`
	ConfigSHA256     string               `json:"config_sha256"`
	BeforeProcess    string               `json:"before_process"`
	RestartRequested bool                 `json:"restart_requested"`
}

type runtimeLedger struct {
	SchemaVersion  int                  `json:"schema_version"`
	InstallationID string               `json:"installation_id"`
	ServerID       string               `json:"server_id"`
	AccountID      string               `json:"account_id"`
	TunnelID       string               `json:"tunnel_id"`
	Revision       uint64               `json:"revision"`
	ConfigSHA256   string               `json:"config_sha256"`
	Environments   []runtimeEnvironment `json:"environments"`
	Allocations    []runtimeAllocation  `json:"allocations"`
	Pending        *runtimePending      `json:"pending,omitempty"`
}

func protectedRuntimeFile(info os.FileInfo, mode os.FileMode, directory bool) bool {
	if info == nil || info.IsDir() != directory || !directory && !info.Mode().IsRegular() || info.Mode().Perm() != mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Geteuid()
}

func openRuntimeRoot(path string, mode os.FileMode) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !protectedRuntimeFile(before, mode, true) {
		return nil, ErrRuntimeState
	}
	// #nosec G304 -- Production callers select only fixed native route/Tunnel state directories; tests use isolated roots.
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrRuntimeState
	}
	after, err := root.Lstat(".")
	if err != nil || !protectedRuntimeFile(after, mode, true) || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, ErrRuntimeState
	}
	return root, nil
}

func readRuntimeFile(root *os.Root, name string, mode os.FileMode, maximum int) ([]byte, error) {
	file, err := privatefile.OpenNoFollow(root, name, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, ErrRuntimeState
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !protectedRuntimeFile(info, mode, false) || info.Size() > int64(maximum) {
		return nil, ErrRuntimeState
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		clear(data)
		return nil, ErrRuntimeState
	}
	return data, nil
}

func runtimeDirectoryEntries(root *os.Root, allowed ...string) error {
	file, err := root.Open(".")
	if err != nil {
		return ErrRuntimeState
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(len(allowed) + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > len(allowed) {
		return ErrRuntimeState
	}
	for _, entry := range entries {
		found := false
		for _, name := range allowed {
			if name == entry.Name() {
				found = true
				break
			}
		}
		if !found {
			return ErrRuntimeState
		}
	}
	return nil
}

func readRuntimeRecord(root *os.Root, name string, value any) error {
	data, err := readRuntimeFile(root, name, 0600, maxRuntimeStateBytes)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, value) != nil {
		return ErrRuntimeState
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return ErrRuntimeState
	}
	return nil
}

func runtimeLock(root *os.Root) (*os.File, error) {
	file, err := privatefile.OpenNoFollow(root, "routes.lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, ErrRuntimeState
	}
	info, err := file.Stat()
	if err != nil || !protectedRuntimeFile(info, 0600, false) {
		_ = file.Close()
		return nil, ErrRuntimeState
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrRuntimeBusy
		}
		return nil, ErrRuntimeState
	}
	return file, nil
}

func syncRuntimeDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return ErrRuntimeState
	}
	defer func() { _ = directory.Close() }()
	if directory.Sync() != nil {
		return ErrRuntimeState
	}
	return nil
}

// The fixed .next inode is staging only, never mutation authority. Under the
// route lock an interrupted, protected staging file can be discarded; external
// effects begin only after the complete ledger has been renamed and fsynced.
func writeRuntimeFile(ctx context.Context, root *os.Root, name string, data []byte, mode os.FileMode, maximum int) error {
	if len(data) > maximum || ctx.Err() != nil {
		return ErrRuntimeState
	}
	temporary := name + ".next"
	staged, err := readRuntimeFile(root, temporary, mode, maximum)
	clear(staged)
	if err == nil {
		if root.Remove(temporary) != nil {
			return ErrRuntimeState
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrRuntimeState
	}
	directory, err := root.Open(".")
	if err != nil {
		return ErrRuntimeState
	}
	defer func() { _ = directory.Close() }()
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrRuntimeState
	}
	file := os.NewFile(uintptr(fd), "route-publication")
	if file == nil {
		_ = unix.Close(fd)
		return ErrRuntimeState
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil || file.Chmod(mode) != nil || file.Sync() != nil || ctx.Err() != nil {
		return ErrRuntimeState
	}
	if unix.Linkat(fd, "", int(directory.Fd()), temporary, unix.AT_EMPTY_PATH) != nil {
		return ErrRuntimeState
	}
	defer func() { _ = root.Remove(temporary) }()
	if unix.Renameat2(int(directory.Fd()), temporary, int(directory.Fd()), name, 0) != nil || directory.Sync() != nil {
		return ErrRuntimeState
	}
	return nil
}

func saveRuntimeLedger(ctx context.Context, root *os.Root, ledger runtimeLedger) error {
	data, err := json.Marshal(ledger)
	if err != nil || len(data) > maxRuntimeStateBytes {
		return ErrCapacity
	}
	return writeRuntimeFile(ctx, root, "ledger.json", data, 0600, maxRuntimeStateBytes)
}
