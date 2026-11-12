package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	"tailscale.com/client/local"

	"github.com/aleksdotbar/vps/panel/internal/installation"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/transport"
)

const (
	hostState         = "/var/lib/panel-host"
	identityPath      = hostState + "/identity.json"
	registeredPath    = hostState + "/registered"
	provenPath        = hostState + "/proven"
	managementKeyPath = hostState + "/management_ed25519"
	runnerKeysPath    = "/var/lib/panel-access/panel-runner/authorized_keys"
	serveEnvPath      = "/var/lib/panel-config/serve.env"
	systemBin         = "/run/current-system/sw/bin/"
	publicSSHHint     = "Public SSH is still open. Once `ssh root@<tailscale address>` works, add\n  services.vpsPanel.publicSSH = false;\nto /etc/nixos/panel-local.nix (create it as `{ services.vpsPanel.publicSSH = false; }` if absent) and run\n  nixos-rebuild switch --flake /etc/nixos#panel"
)

var tailnetIPv4 = netip.MustParsePrefix("100.64.0.0/10")

type hostIdentity struct {
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	Role           string `json:"role"`
}

// setup brings a host that runs the panel NixOS module to a serving panel. Every
// step observes what is already done, so rerunning it after any failure is safe.
func setup(arguments []string, output io.Writer) error {
	if len(arguments) == 1 && (arguments[0] == "bootstrap" || arguments[0] == "proof") {
		return setupAsPanel(arguments[0], os.Stdin, output)
	}
	if len(arguments) != 0 {
		return errors.New("usage: sudo panel setup")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root: sudo panel setup")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log := os.Stderr

	system, err := runningProfile()
	if err != nil {
		return err
	}
	if err := privatefile.PrivateDirectory(hostState); err != nil {
		return fmt.Errorf("host state directory: %w", err)
	}
	// Host-helper mutations take the same lock; they cannot run during setup.
	lock, err := privatefile.OpenPrivate(hostState+"/setup.lock", os.O_RDWR|os.O_CREATE)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("another setup or host operation is running; retry shortly")
	}

	identity, err := ensureIdentity()
	if err != nil {
		return err
	}
	public, err := ensureManagementKey(ctx)
	if err != nil {
		return err
	}
	if err := publishRunnerKey(public); err != nil {
		return err
	}
	address, err := tailscaleAddress(ctx, log)
	if err != nil {
		return err
	}
	self, err := panelSelf(ctx)
	if err != nil {
		return err
	}
	registered, err := os.ReadFile(registeredPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := registerPanel(ctx, system, identity, address); err != nil {
			return err
		}
		if err := writeFileAtomic(registeredPath, []byte(address.String()), 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := sameRegisteredAddress(registered, address); err != nil {
		return err
	}
	if _, err := os.Stat(provenPath); errors.Is(err, os.ErrNotExist) {
		// The proof opens the panel database, which a running panel.service holds.
		// #nosec G204 -- Fixed native tool, unit and action.
		if exec.CommandContext(ctx, system+"/sw/bin/systemctl", "stop", "panel.service").Run() != nil {
			return errors.New("panel.service could not be stopped for the private access proof")
		}
		_, _ = fmt.Fprintln(log, "Proving private management access over Tailscale.")
		if _, err := runAsPanel(ctx, system, "proof", nil, "private_proven"); err != nil {
			return err
		}
		if err := writeFileAtomic(provenPath, nil, 0600); err != nil {
			return err
		}
	}
	changed, err := publishOrigin(self.origin)
	if err != nil {
		return err
	}
	action := "start"
	if changed {
		action = "restart"
	}
	// #nosec G204 -- Fixed native tool, unit and action.
	if exec.CommandContext(ctx, system+"/sw/bin/systemctl", action, "panel.service").Run() != nil {
		return errors.New("panel.service did not start; inspect journalctl -u panel.service")
	}
	if err := awaitPanelHTTPS(ctx, self); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Panel is ready at %s (Tailscale only).\n%s\n", self.origin, publicSSHHint)
	return err
}

// sameRegisteredAddress refuses a tailnet address that differs from the one the
// panel registered; registration is not repeated, so the operator must decide.
// An empty marker predates address recording and is not compared.
func sameRegisteredAddress(marker []byte, current netip.Addr) error {
	recorded := strings.TrimSpace(string(marker))
	if recorded == "" || recorded == current.String() {
		return nil
	}
	return fmt.Errorf("this host's Tailscale address changed from %s (registered) to %s; restore the original tailnet address or reinstall the panel", recorded, current)
}

// runningProfile returns the running generation, which must also be the system
// profile so that setup never configures a generation about to be replaced.
func runningProfile() (string, error) {
	current, err := filepath.EvalSymlinks("/run/current-system")
	if err != nil {
		return "", errors.New("running NixOS system unavailable")
	}
	profile, err := filepath.EvalSymlinks("/nix/var/nix/profiles/system")
	if err != nil || profile != current {
		return "", errors.New("the system profile differs from the running system; finish nixos-rebuild switch first")
	}
	return current, nil
}

func ensureIdentity() (hostIdentity, error) {
	file, err := privatefile.OpenPrivate(identityPath, os.O_RDONLY)
	if err == nil {
		defer func() { _ = file.Close() }()
		var identity hostIdentity
		data, err := io.ReadAll(io.LimitReader(file, 4096))
		if err != nil || json.Unmarshal(data, &identity) != nil || !validIdentity(identity) {
			return hostIdentity{}, errors.New("host identity record is invalid; inspect " + identityPath)
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return hostIdentity{}, err
	}
	identity := hostIdentity{InstallationID: uuid.NewString(), ServerID: uuid.NewString(), Role: "panel"}
	data, err := json.Marshal(identity)
	if err != nil {
		return hostIdentity{}, err
	}
	return identity, writeFileAtomic(identityPath, data, 0600)
}

func validIdentity(identity hostIdentity) bool {
	return validUUID(identity.InstallationID) && validUUID(identity.ServerID) && identity.InstallationID != identity.ServerID && identity.Role == "panel"
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

// ensureManagementKey creates the panel's machine-management key once. It stays
// on this host; registration stores an encrypted copy in the panel database.
func ensureManagementKey(ctx context.Context) ([]byte, error) {
	if _, err := os.Lstat(managementKeyPath); errors.Is(err, os.ErrNotExist) {
		// #nosec G204 -- Fixed native tool and paths.
		if exec.CommandContext(ctx, systemBin+"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "panel-management", "-f", managementKeyPath).Run() != nil {
			return nil, errors.New("management key generation failed")
		}
	}
	key, err := os.Lstat(managementKeyPath)
	if err != nil || !key.Mode().IsRegular() || key.Mode().Perm() != 0600 {
		return nil, errors.New("management key must be a private regular file")
	}
	// Derive from the private key: an interrupted run may have left no .pub file.
	// #nosec G204 -- Fixed native tool and path.
	public, err := exec.CommandContext(ctx, systemBin+"ssh-keygen", "-y", "-f", managementKeyPath).Output()
	if err != nil || len(public) > 1024 {
		return nil, errors.New("management public key unavailable")
	}
	return bytes.TrimSpace(public), nil
}

// runnerAuthorizedKeys restricts the management key to the sshd ForceCommand.
func runnerAuthorizedKeys(public []byte) ([]byte, error) {
	key, _, options, rest, err := ssh.ParseAuthorizedKey(public)
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("management public key must be one plain Ed25519 key")
	}
	return []byte("restrict " + string(ssh.MarshalAuthorizedKey(key))), nil
}

func publishRunnerKey(public []byte) error {
	content, err := runnerAuthorizedKeys(public)
	if err != nil {
		return err
	}
	// sshd StrictModes needs root-owned directories without group/other write.
	for _, directory := range []string{filepath.Dir(filepath.Dir(runnerKeysPath)), filepath.Dir(runnerKeysPath)} {
		if err := os.Mkdir(directory, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return errors.New("management key directory unavailable")
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !rootOwned(info) {
			return errors.New("management key directories must be root-owned and not writable by others")
		}
	}
	if existing, err := os.ReadFile(runnerKeysPath); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	return writeFileAtomic(runnerKeysPath, content, 0644)
}

func rootOwned(info os.FileInfo) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == 0
}

// writeFileAtomic replaces path with complete content; a crash leaves the old file.
func writeFileAtomic(path string, data []byte, permissions os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".setup-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	_, writeErr := temporary.Write(data)
	if err := errors.Join(writeErr, temporary.Chmod(permissions), temporary.Sync(), temporary.Close()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	parent, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("sync %s: %w", directory, err)
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync()
}

// tailscaleAddress logs the host into the tailnet when needed and returns its IPv4 address.
func tailscaleAddress(ctx context.Context, log io.Writer) (netip.Addr, error) {
	if address, ok := tailscaleIPv4(ctx); ok {
		return address, nil
	}
	_, _ = fmt.Fprintln(log, "Log this host into your tailnet with the URL below.")
	// The module applies these preferences with `tailscale set`; `up` must repeat them.
	// #nosec G204 -- Fixed native tool and arguments.
	command := exec.CommandContext(ctx, systemBin+"tailscale", "up", "--timeout=15m", "--ssh=false", "--accept-dns=false", "--accept-routes=false", "--netfilter-mode=off")
	command.Stdout, command.Stderr = log, log
	if command.Run() != nil {
		return netip.Addr{}, errors.New("tailscale login did not complete; rerun sudo panel setup")
	}
	if address, ok := tailscaleIPv4(ctx); ok {
		return address, nil
	}
	return netip.Addr{}, errors.New("tailscale address unavailable")
}

func tailscaleIPv4(ctx context.Context) (netip.Addr, bool) {
	// #nosec G204 -- Fixed native tool and arguments.
	output, err := exec.CommandContext(ctx, systemBin+"tailscale", "ip", "-4").Output()
	if err != nil {
		return netip.Addr{}, false
	}
	address, err := netip.ParseAddr(strings.TrimSpace(string(output)))
	return address, err == nil && tailnetIPv4.Contains(address)
}

type selfOrigin struct {
	origin    string
	addresses []netip.Addr
}

// panelSelf derives the private origin with the production listener's identity
// rules, which require MagicDNS and HTTPS certificates in the tailnet.
func panelSelf(ctx context.Context) (selfOrigin, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := local.Client{UseSocketOnly: true}
	status, err := client.StatusWithoutPeers(bounded)
	if err != nil {
		return selfOrigin{}, errors.New("tailscale status unavailable")
	}
	origin, _, addresses, err := transport.NativeSelf(status)
	if err != nil {
		return selfOrigin{}, errors.New("enable MagicDNS and HTTPS certificates for your tailnet, then rerun sudo panel setup")
	}
	return selfOrigin{origin, addresses}, nil
}

func registerPanel(ctx context.Context, system string, identity hostIdentity, address netip.Addr) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("administrator setup needs an interactive terminal; connect with ssh -t")
	}
	_, _ = fmt.Fprintln(os.Stderr, "Set the panel administrator password (at least 12 characters).")
	password, err := readPassword()
	if err != nil {
		return err
	}
	defer clear(password)
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("administrator password must contain 12-1024 bytes")
	}
	private, err := os.ReadFile(managementKeyPath)
	if err != nil {
		return errors.New("management key unavailable")
	}
	defer clear(private)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		return errors.New("management key unavailable")
	}
	hostKey, fingerprint, err := hostPublicKey()
	if err != nil {
		return err
	}
	// The handoff's string fields keep unwiped copies until this short-lived process exits.
	handoff := installation.Handoff{SchemaVersion: 1, InstallationID: identity.InstallationID, ServerID: identity.ServerID,
		TailscaleAddress: address.String(), HostPublicKey: hostKey, HostFingerprint: fingerprint,
		ManagementFingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		AdministratorPassword: string(password), ManagementPrivateKey: string(private)}
	encoded, err := json.Marshal(handoff)
	if err != nil {
		return errors.New("panel setup input unavailable")
	}
	defer clear(encoded)
	_, err = runAsPanel(ctx, system, "bootstrap", encoded, "registered")
	return err
}

// panelStepScript prepares the panel-owned state directories and runs one setup
// step as the panel user. Registration needs the panel service stopped.
const panelStepScript = `set -euo pipefail
umask 077
ulimit -c 0
system=$1
step=$2
export PATH="$system/sw/bin:/run/wrappers/bin"
test "$(id -u)" = 0
if test "$step" = bootstrap; then
  state=$(systemctl show panel.service --property=ActiveState --value)
  test "$state" != active && test "$state" != activating && test "$state" != reloading
fi
panel_uid=$(id -u panel)
panel_gid=$(id -g panel)
test "$panel_uid" != 0
for directory in /var/lib/panel /var/lib/panel-key /run/panel; do
  if test -e "$directory" || test -L "$directory"; then
    test ! -L "$directory"
    test -d "$directory"
    test "$(stat -c '%u:%g:%a' -- "$directory")" = "$panel_uid:$panel_gid:700"
  else
    install -d -m 0700 -o "$panel_uid" -g "$panel_gid" -- "$directory"
  fi
done
exec "$system/sw/bin/runuser" --user panel --group panel -- \
  "$system/sw/bin/env" -i HOME=/var/lib/panel USER=panel LOGNAME=panel \
  PATH="$system/sw/bin:/run/wrappers/bin" \
  "$system/sw/bin/panel" setup "$step"
`

func runAsPanel(parent context.Context, system, step string, input []byte, status string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	// #nosec G204 -- The system is the verified running generation; the step is fixed.
	command := exec.CommandContext(ctx, system+"/sw/bin/bash", "-c", panelStepScript, "--", system, step)
	command.Env = []string{"LANG=C", "LC_ALL=C"}
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var output, diagnostics bytes.Buffer
	command.Stdout = &limitedWriter{buffer: &output, remaining: 8 << 10}
	command.Stderr = &limitedWriter{buffer: &diagnostics, remaining: 8 << 10}
	runErr := command.Run()
	var result struct {
		Status string `json:"status"`
	}
	if runErr != nil || json.Unmarshal(output.Bytes(), &result) != nil || result.Status != status {
		return nil, fmt.Errorf("panel %s step failed: %s", step, strings.TrimSpace(diagnostics.String()))
	}
	return output.Bytes(), nil
}

type limitedWriter struct {
	buffer    *bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(data []byte) (int, error) {
	if len(data) > writer.remaining {
		return 0, errors.New("output exceeds its bound")
	}
	writer.remaining -= len(data)
	return writer.buffer.Write(data)
}

func hostPublicKey() (string, string, error) {
	data, err := os.ReadFile("/etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		return "", "", errors.New("SSH host key unavailable")
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil || key.Type() != ssh.KeyAlgoED25519 {
		return "", "", errors.New("SSH host key unavailable")
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(key)), "\n"), ssh.FingerprintSHA256(key), nil
}

// publishOrigin writes the panel origin and reports whether it changed.
func publishOrigin(origin string) (bool, error) {
	content := []byte("PANEL_ORIGIN=" + origin + "\n")
	if existing, err := os.ReadFile(serveEnvPath); err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(serveEnvPath), 0700); err != nil {
		return false, errors.New("panel configuration directory unavailable")
	}
	return true, writeFileAtomic(serveEnvPath, content, 0600)
}

func awaitPanelHTTPS(parent context.Context, self selfOrigin) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	for {
		if panelHealth(ctx, self) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("panel did not answer over private HTTPS; inspect journalctl -u panel.service")
		case <-time.After(time.Second):
		}
	}
}

// panelHealth dials only the verified Tailscale addresses with certificate
// verification for the origin; neither DNS nor redirects choose the peer.
func panelHealth(ctx context.Context, self selfOrigin) error {
	hostname := strings.TrimPrefix(self.origin, "https://")
	failure := errors.New("panel health check failed")
	clientTransport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 8 << 10, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != net.JoinHostPort(hostname, "443") {
			return nil, failure
		}
		for _, ip := range self.addresses {
			connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
			if err == nil {
				return connection, nil
			}
		}
		return nil, failure
	}}
	defer clientTransport.CloseIdleConnections()
	client := &http.Client{Transport: clientTransport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return failure }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, self.origin+"/healthz", nil)
	if err != nil {
		return failure
	}
	response, err := client.Do(request)
	if err != nil {
		return failure
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 33))
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		return failure
	}
	return nil
}
