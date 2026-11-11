package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/deployment"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

const (
	maxPayloadBytes = deployment.MaxStageBytes
	maxOutputBytes  = 2 << 20
	maxStderrBytes  = 32 << 10
	maxKeyBytes     = 64 << 10
	commandTimeout  = 30 * time.Second
	helperCommand   = "/run/wrappers/bin/sudo -n -- /run/current-system/sw/bin/panel host-helper"
)

var (
	ErrInput       = errors.New("invalid SSH helper input")
	ErrUnavailable = errors.New("SSH transport is unavailable")
	ErrTransport   = errors.New("SSH connection ended without a confirmed command exit")
	ErrRemoteExit  = errors.New("SSH helper command exited unsuccessfully")
	ErrOutputLimit = errors.New("SSH process output exceeded its limit")
	ErrCleanup     = errors.New("SSH temporary credential cleanup failed")
)

type Outcome uint8

const (
	// Unknown is deliberately the zero value. Killing the local SSH process does
	// not stop remote work. Reconcile remote state before retrying any mutation.
	Unknown Outcome = iota
	NotStarted
	// CommandExited means only that SSH reported an exit status other than 255.
	// Neither zero nor nonzero status proves a remote operation's final state;
	// that requires the future operation receipt and reconciliation protocol.
	CommandExited
)

type Result struct {
	Outcome  Outcome
	ExitCode int
	// Output is populated only after a clean command exit with status zero. It
	// is untrusted helper data, may contain secrets, and must not be logged.
	Output []byte
}

type Client struct {
	command   string
	directory string
	slots     chan struct{}
}

// New accepts installation-owned configuration, never request parameters. The
// runtime directory should be /run/panel/ssh under the service's private /run/panel
// RuntimeDirectory, so service shutdown also removes interrupted temporary keys.
func New(sshCommand, runtimeDirectory string) (*Client, error) {
	if !filepath.IsAbs(sshCommand) || filepath.Clean(sshCommand) != sshCommand || !filepath.IsAbs(runtimeDirectory) || filepath.Clean(runtimeDirectory) != runtimeDirectory || strings.ContainsAny(runtimeDirectory, " \t\r\n%$~\"'\\") {
		return nil, ErrUnavailable
	}
	info, err := os.Stat(sshCommand)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, ErrUnavailable
	}
	if err := privatefile.PrivateDirectory(runtimeDirectory); err != nil {
		return nil, ErrUnavailable
	}
	return &Client{command: sshCommand, directory: runtimeDirectory, slots: make(chan struct{}, 2)}, nil
}

// Helper invokes one fixed product command. Payload is already bounded in memory,
// passed only on stdin, and never interpreted as a command or command arguments.
// The caller owns and clears decrypted privateKey after this call. The client
// performs no retries and does not implement readiness, enrollment or deployment.
func (client *Client) Helper(ctx context.Context, target Target, privateKey, payload []byte) (result Result, returnErr error) {
	return client.run(ctx, target, privateKey, payload, 22)
}

func (client *Client) run(ctx context.Context, target Target, privateKey, payload []byte, port int) (result Result, returnErr error) {
	result = Result{Outcome: NotStarted, ExitCode: -1}
	if client == nil || client.command == "" || client.directory == "" || client.slots == nil {
		return result, ErrUnavailable
	}
	if !target.valid() {
		return result, ErrTarget
	}
	if len(privateKey) == 0 || len(privateKey) > maxKeyBytes || len(payload) > maxPayloadBytes {
		return result, ErrInput
	}
	bounded, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	select {
	case client.slots <- struct{}{}:
		defer func() { <-client.slots }()
	case <-bounded.Done():
		return result, bounded.Err()
	}
	if err := bounded.Err(); err != nil {
		return result, err
	}
	if _, err := ssh.ParseRawPrivateKey(privateKey); err != nil {
		return result, ErrInput
	}
	if err := privatefile.PrivateDirectory(client.directory); err != nil {
		return result, ErrUnavailable
	}
	directory, err := os.MkdirTemp(client.directory, "ssh-")
	if err != nil {
		return result, ErrUnavailable
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			returnErr = errors.Join(returnErr, ErrCleanup)
		}
	}()
	keyPath := filepath.Join(directory, "identity")
	hostsPath := filepath.Join(directory, "known_hosts")
	for _, file := range []struct {
		path string
		data []byte
	}{{keyPath, privateKey}, {hostsPath, []byte("panel-target " + target.hostKey + "\n")}} {
		if err := os.WriteFile(file.path, file.data, 0600); err != nil {
			return result, ErrUnavailable
		}
	}

	// SAFETY: Only installation configuration chooses the executable. No local
	// shell is invoked; the remote shell receives this fixed product command.
	process := exec.CommandContext(bounded, client.command, arguments(target, keyPath, hostsPath, port)...) // #nosec G204 -- trusted executable, validated target, fixed command and options.
	process.Dir = directory
	process.Env = []string{"PATH=/nonexistent", "HOME=" + directory, "LANG=C", "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never"}
	process.Stdin = bytes.NewReader(payload)
	process.WaitDelay = time.Second
	output := boundedOutput{limit: maxOutputBytes, cancel: cancel, retain: true}
	diagnostics := boundedOutput{limit: maxStderrBytes, cancel: cancel}
	process.Stdout = &output
	process.Stderr = &diagnostics
	if err := process.Start(); err != nil {
		if bounded.Err() != nil {
			return result, bounded.Err()
		}
		return result, ErrUnavailable
	}
	result.Outcome = Unknown
	err = process.Wait()
	if output.exceeded || diagnostics.exceeded {
		return result, ErrOutputLimit
	}
	if bounded.Err() != nil {
		return result, bounded.Err()
	}
	if err == nil {
		result.Outcome = CommandExited
		result.ExitCode = 0
		result.Output = output.buffer.Bytes()
		return result, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 && exit.ExitCode() != 255 {
		result.Outcome = CommandExited
		result.ExitCode = exit.ExitCode()
		return result, ErrRemoteExit
	}
	return result, ErrTransport
}

func arguments(target Target, keyPath, hostsPath string, port int) []string {
	arguments := []string{"-F", "none", "-T", "-a", "-x", "-p", strconv.Itoa(port), "-l", target.user, "-i", keyPath}
	for _, option := range []string{
		"BatchMode=yes", "NumberOfPasswordPrompts=0", "PreferredAuthentications=publickey", "PubkeyAuthentication=yes",
		"PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "GSSAPIAuthentication=no", "HostbasedAuthentication=no",
		"IdentitiesOnly=yes", "IdentityAgent=none", "CertificateFile=none",
		"StrictHostKeyChecking=yes", "HostKeyAlias=panel-target", "HostKeyAlgorithms=" + target.hostAlgorithm,
		"UserKnownHostsFile=" + hostsPath, "GlobalKnownHostsFile=/dev/null", "KnownHostsCommand=none",
		"VerifyHostKeyDNS=no", "UpdateHostKeys=no", "CheckHostIP=no", "CanonicalizeHostname=no",
		"ProxyCommand=none", "ProxyJump=none", "PermitLocalCommand=no",
		"ForwardAgent=no", "ForwardX11=no", "ClearAllForwardings=yes", "Tunnel=no",
		"ControlMaster=no", "ControlPath=none", "ControlPersist=no", "RequestTTY=no",
		"ConnectTimeout=10", "ConnectionAttempts=1", "ServerAliveInterval=5", "ServerAliveCountMax=2", "LogLevel=ERROR",
	} {
		arguments = append(arguments, "-o", option)
	}
	return append(arguments, "--", target.address.String(), helperCommand)
}

type boundedOutput struct {
	// Embedding bytes.Buffer would promote ReadFrom and bypass Write in io.Copy.
	buffer   bytes.Buffer
	limit    int
	written  int
	exceeded bool
	retain   bool
	cancel   context.CancelFunc
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > output.limit-output.written {
		output.exceeded = true
		output.cancel()
		return 0, ErrOutputLimit
	}
	output.written += len(data)
	if output.retain {
		return output.buffer.Write(data)
	}
	return len(data), nil
}
