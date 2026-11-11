package hosthelper

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

var ErrTunnelState = errors.New("managed Tunnel state does not match this operation")

const tunnelPrivateDirectory = "/var/lib/panel-tunnel"
const tunnelRuntimeDirectory = "/var/lib/panel-cloudflared"

type tunnelBinding struct {
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	OperationID    string `json:"operation_id"`
	AccountID      string `json:"account_id"`
	Name           string `json:"name"`
	FinalSystem    string `json:"final_system"`
}

type tunnelIntent struct {
	Binding tunnelBinding `json:"binding"`
	Secret  []byte        `json:"secret"`
}

type tunnelFinal struct {
	Binding  tunnelBinding `json:"binding"`
	TunnelID string        `json:"tunnel_id"`
}

type tunnelCredentials struct {
	AccountTag   string `json:"AccountTag"`
	TunnelSecret []byte `json:"TunnelSecret"`
	TunnelID     string `json:"TunnelID"`
}

func bindTunnel(request TunnelRequest, lease FinalLease) tunnelBinding {
	return tunnelBinding{request.InstallationID, request.ServerID, request.OperationID, request.AccountID, request.Name, lease.FinalSystem}
}

func openTunnelDirectory(name string, mode os.FileMode) (*os.Root, error) {
	before, err := os.Lstat(name)
	if err != nil || !protectedTunnelFile(before, mode, true) {
		return nil, ErrTunnelState
	}
	// #nosec G304 -- Production callers select only the two fixed root-owned Tunnel directories; tests use isolated temporary roots.
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, ErrTunnelState
	}
	info, err := root.Lstat(".")
	if err != nil || !protectedTunnelFile(info, mode, true) || !os.SameFile(before, info) {
		_ = root.Close()
		return nil, ErrTunnelState
	}
	return root, nil
}

func protectedTunnelFile(info os.FileInfo, mode os.FileMode, directory bool) bool {
	if info == nil || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm() != mode {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Geteuid() && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func tunnelDirectoryEntries(root *os.Root, allowed ...string) error {
	file, err := root.Open(".")
	if err != nil {
		return ErrTunnelState
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(len(allowed) + 1)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > len(allowed) {
		return ErrTunnelState
	}
	for _, entry := range entries {
		found := false
		for _, name := range allowed {
			found = found || entry.Name() == name
		}
		if !found {
			return ErrTunnelState
		}
	}
	return nil
}

func tunnelRead(root *os.Root, name string, mode os.FileMode) ([]byte, error) {
	file, err := privatefile.OpenNoFollow(root, name, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, ErrTunnelState
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !protectedTunnelFile(info, mode, false) || info.Size() > 4096 {
		return nil, ErrTunnelState
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		clear(data)
		return nil, ErrTunnelState
	}
	return data, nil
}

func tunnelReadJSON(root *os.Root, name string, value any) error {
	data, err := tunnelRead(root, name, 0600)
	if err != nil {
		return err
	}
	defer clear(data)
	if json.Unmarshal(data, value) != nil {
		return ErrTunnelState
	}
	canonical, err := json.Marshal(value)
	defer clear(canonical)
	if err != nil || !bytes.Equal(data, canonical) {
		return ErrTunnelState
	}
	return nil
}

func syncTunnelDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return ErrTunnelState
	}
	defer func() { _ = directory.Close() }()
	if directory.Sync() != nil {
		return ErrTunnelState
	}
	return nil
}

// Anonymous staging cannot leave a named orphan. The completed inode is linked
// without replacement and synced; replay accepts only exact protected contents.
func publishTunnelFile(ctx context.Context, root *os.Root, name string, data []byte, mode os.FileMode) error {
	if len(data) > 4096 {
		return ErrTunnelState
	}
	existing, err := tunnelRead(root, name, mode)
	defer clear(existing)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return ErrTunnelState
		}
		return syncTunnelDirectory(root)
	}
	if !errors.Is(err, os.ErrNotExist) || ctx.Err() != nil {
		return ErrTunnelState
	}
	directory, err := root.Open(".")
	if err != nil {
		return ErrTunnelState
	}
	defer func() { _ = directory.Close() }()
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrTunnelState
	}
	file := os.NewFile(uintptr(fd), "tunnel-publication")
	if file == nil {
		_ = unix.Close(fd)
		return ErrTunnelState
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil || file.Chmod(mode) != nil || file.Sync() != nil || ctx.Err() != nil {
		return ErrTunnelState
	}
	if unix.Linkat(fd, "", int(directory.Fd()), name, unix.AT_EMPTY_PATH) != nil || directory.Sync() != nil {
		return ErrTunnelState
	}
	return nil
}

func prepareTunnelIntent(ctx context.Context, private, runtime *os.Root, binding tunnelBinding) (tunnelIntent, error) {
	if tunnelDirectoryEntries(private, "intent.json", "final.json") != nil || tunnelDirectoryEntries(runtime, "credentials.json", "config.json") != nil {
		return tunnelIntent{}, ErrTunnelState
	}
	var intent tunnelIntent
	err := tunnelReadJSON(private, "intent.json", &intent)
	if err == nil {
		if intent.Binding != binding || len(intent.Secret) != 32 {
			clear(intent.Secret)
			return tunnelIntent{}, ErrTunnelState
		}
		if syncTunnelDirectory(private) != nil {
			clear(intent.Secret)
			return tunnelIntent{}, ErrTunnelState
		}
		return intent, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		clear(intent.Secret)
		return tunnelIntent{}, ErrTunnelState
	}
	// No existing runtime file or final receipt can authorize adopting a secret.
	if tunnelDirectoryEntries(private) != nil || tunnelDirectoryEntries(runtime) != nil {
		return tunnelIntent{}, ErrTunnelState
	}
	intent = tunnelIntent{Binding: binding, Secret: make([]byte, 32)}
	if _, err := rand.Read(intent.Secret); err != nil {
		clear(intent.Secret)
		return tunnelIntent{}, ErrTunnelState
	}
	// #nosec G117 -- Target-only secret and its authority are atomically retained in one root-owned 0600 file; encoded bytes are cleared and never enter panel persistence or logs.
	data, err := json.Marshal(intent)
	defer clear(data)
	if err != nil || publishTunnelFile(ctx, private, "intent.json", data, 0600) != nil {
		clear(intent.Secret)
		return tunnelIntent{}, ErrTunnelState
	}
	return intent, nil
}

func readTunnelIntent(private *os.Root, binding tunnelBinding) (tunnelIntent, error) {
	if tunnelDirectoryEntries(private, "intent.json", "final.json") != nil {
		return tunnelIntent{}, ErrTunnelState
	}
	var intent tunnelIntent
	if tunnelReadJSON(private, "intent.json", &intent) != nil || intent.Binding != binding || len(intent.Secret) != 32 {
		clear(intent.Secret)
		return tunnelIntent{}, ErrTunnelState
	}
	return intent, nil
}

func readTunnelFinal(private *os.Root, binding tunnelBinding) (tunnelFinal, error) {
	var final tunnelFinal
	if err := tunnelReadJSON(private, "final.json", &final); err != nil {
		return tunnelFinal{}, err
	}
	if final.Binding != binding || !validID(final.TunnelID) {
		return tunnelFinal{}, ErrTunnelState
	}
	return final, nil
}
