package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/hostsetup"
	"github.com/aleksdotbar/vps/panel/internal/installation"
)

const (
	hostState          = "/var/lib/panel-host"
	installState       = "/var/lib/panel-install"
	managementKeyPath  = installState + "/management_ed25519"
	systemBin          = "/run/current-system/sw/bin/"
	observationTimeout = 13 * time.Minute
)

// installer carries one invocation's identity; every run re-observes host state.
type installer struct {
	ctx      context.Context
	built    hostsetup.Built
	operator string
	yes      bool
	stdin    *bufio.Reader
	log      io.Writer
}

func installRun(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("panel install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "store path of the installing repository source")
	sourceSHA := flags.String("source-sha", "", "exact repository revision of the source")
	operator := flags.String("operator", "", "human administrator account name")
	key := flags.String("operator-key", "", "administrator Ed25519 public key")
	keyFile := flags.String("operator-key-file", "", "administrator Ed25519 public key file")
	yes := flags.Bool("yes", false, "accept the discovered host configuration without prompting")
	if flags.Parse(arguments) != nil || flags.NArg() != 0 || (*key != "" && *keyFile != "") {
		return errors.New("usage: panel install [--source PATH --source-sha SHA --operator NAME --operator-key KEY|--operator-key-file FILE] [--yes]")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root: sudo panel install")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	run := &installer{ctx: ctx, yes: *yes, stdin: bufio.NewReader(os.Stdin), log: os.Stderr}

	built, operatorName, err := hostsetup.RecordedBuild(ctx)
	if errors.Is(err, os.ErrNotExist) {
		publicKey := *key
		if *keyFile != "" {
			// #nosec G304 -- The operator names their own public key file.
			data, err := os.ReadFile(*keyFile)
			if err != nil || len(data) > 1024 {
				return errors.New("operator public key file is unreadable or too large")
			}
			publicKey = string(data)
		}
		if *source == "" || *sourceSHA == "" || *operator == "" || publicKey == "" {
			return errors.New("first run needs --source, --source-sha, --operator and --operator-key; use the documented nix run command")
		}
		built, err = hostsetup.Build(ctx, hostsetup.BuildRequest{Source: *source, SourceSHA: *sourceSHA,
			Operator: foundation.Operator{Name: *operator, PublicKey: strings.TrimSpace(publicKey)}, Confirm: run.confirmInput, Log: os.Stderr})
		if err != nil {
			return err
		}
		operatorName = *operator
	} else if err != nil {
		return err
	}
	run.built, run.operator = built, operatorName
	return run.proceed(output)
}

func (run *installer) proceed(output io.Writer) error {
	enrolled, panelStarted, finalized := false, false, false
	deadline := time.Now().Add(observationTimeout)
	for time.Now().Before(deadline) {
		status, err := hostsetup.ObserveSetup(run.ctx, run.built.OperationID, run.built.BuildSHA256)
		if errors.Is(err, hostsetup.ErrBusy) {
			if err := run.pause(); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		switch nextStep(status) {
		case stepEnroll:
			if enrolled {
				return errors.New("enrollment did not start; inspect /var/lib/panel-host before retrying")
			}
			enrolled = true
			if err := run.enroll(); err != nil {
				return err
			}
		case stepWaitEnrollment, stepWaitFinal:
			if err := run.pause(); err != nil {
				return err
			}
		case stepConfirmEnrollment:
			done, err := run.confirmEnrollment(status.Enrollment)
			if err != nil || !done {
				return err
			}
		case stepPanel:
			if panelStarted {
				return errors.New("panel setup did not complete; inspect /var/lib/panel-host before retrying")
			}
			panelStarted = true
			if err := run.startPanel(status.Enrollment); err != nil {
				return err
			}
		case stepFinal:
			if finalized {
				return errors.New("final activation did not start; inspect /var/lib/panel-host before retrying")
			}
			finalized = true
			done, err := run.activateFinal()
			if err != nil || !done {
				return err
			}
		case stepConfirmFinal:
			done, err := run.confirmFinal(status.Final)
			if err != nil || !done {
				return err
			}
		case stepDone:
			return run.report(status, output)
		default:
			return errors.New("setup needs attention: the host restored or could not prove its configuration; keep provider console access and inspect /var/lib/panel-host")
		}
	}
	return errors.New("setup observation timed out; rerun sudo panel install to observe it again (no step is repeated)")
}

func (run *installer) pause() error {
	select {
	case <-run.ctx.Done():
		return run.ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

func (run *installer) confirmInput(summary string) bool {
	_, _ = fmt.Fprint(run.log, summary)
	return run.yes || run.ask("Build and install with this host configuration?")
}

func (run *installer) ask(question string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		_, _ = fmt.Fprintf(run.log, "%s\nThis question needs an interactive terminal; connect with ssh -t.\n", question)
		return false
	}
	_, _ = fmt.Fprintf(run.log, "%s [y/N] ", question)
	answer, _ := run.stdin.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}

func (run *installer) enroll() error {
	public, err := managementPublicKey(run.ctx)
	if err != nil {
		return err
	}
	inspected, err := hostsetup.Inspect(run.ctx, run.built.OperationID, run.built.BuildSHA256)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(run.log, "Host configuration verified. Switching to the enrollment configuration; bootstrap SSH stays open.")
	// A lost response may follow a started switch: observe, never repeat it.
	if _, err := hostsetup.Enroll(run.ctx, run.built.OperationID, run.built.BuildSHA256, inspected.InspectionSHA256, public); err != nil {
		if !unresolved(run.ctx, err) {
			return err
		}
		_, _ = fmt.Fprintln(run.log, "Enrollment response unavailable; observing without repeating activation.")
	}
	return nil
}

func (run *installer) confirmEnrollment(result *hostsetup.EnrollmentResult) (bool, error) {
	if fresh, err := run.freshSession("enrollment-receipt.json"); err != nil || !fresh {
		_, _ = fmt.Fprintf(run.log, "Enrollment is active. Before %s, open a NEW SSH session to this host and run:\n  sudo panel install\nOtherwise the host restores its previous configuration.\n", result.RecoveryDeadline)
		return false, nil
	}
	_, err := hostsetup.ConfirmEnrollment(run.ctx, run.built.OperationID, run.built.BuildSHA256)
	return err == nil, err
}

// freshSession reports whether this SSH login began after the named phase receipt was written.
func (run *installer) freshSession(receipt string) (bool, error) {
	info, err := os.Stat(filepath.Join(hostState, receipt))
	if err != nil {
		return false, errors.New("setup phase time unavailable")
	}
	session, err := currentLoginSession()
	if err != nil {
		return false, err
	}
	return session.startedAfter(info.ModTime()), nil
}

func (run *installer) startPanel(enrolled *hostsetup.EnrollmentResult) error {
	address, err := tailscaleAddress(run.ctx, run.log)
	if err != nil {
		return err
	}
	if err := run.bootstrapPanel(enrolled, address); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(run.log, "Starting the panel on its private HTTPS address.")
	result, err := hostsetup.StartPanelHTTPS(run.ctx, run.built.OperationID, run.built.BuildSHA256)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(run.log, "Panel is reachable at %s over Tailscale.\n", result.Origin)
	return nil
}

func (run *installer) bootstrapPanel(enrolled *hostsetup.EnrollmentResult, address netip.Addr) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("administrator setup needs an interactive terminal")
	}
	_, _ = fmt.Fprintln(run.log, "Set the panel administrator password (reuse it if already set).")
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
	hostKey, fingerprint, err := hostPublicKey()
	if err != nil {
		return err
	}
	handoff := installation.Handoff{SchemaVersion: 1, OperationID: enrolled.OperationID, InstallationID: enrolled.InstallationID,
		ServerID: enrolled.ServerID, InputSHA256: enrolled.InputSHA256, SourceSystem: enrolled.SourceSystem, EnrollmentSystem: enrolled.EnrollmentSystem,
		TailscaleAddress: address.String(), HostPublicKey: hostKey, HostFingerprint: fingerprint, ManagementFingerprint: enrolled.ManagementFingerprint,
		// The handoff's string fields keep unwiped copies until this short-lived process exits.
		AdministratorPassword: string(password), ManagementPrivateKey: string(private)}
	encoded, err := json.Marshal(handoff)
	if err != nil {
		return errors.New("panel setup input unavailable")
	}
	defer clear(encoded)
	response, err := runPanelBootstrap(run.ctx, enrolled.EnrollmentSystem, encoded)
	if err != nil {
		return err
	}
	var registered struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(response, &registered) != nil || registered.Status != "registered" {
		return errors.New("panel registration did not succeed; inspect /var/lib/panel before retrying")
	}
	return nil
}

// Fixed paths only; never adopt foreign directories or recursively chown
// existing state. The panel CLI takes its own process lock before opening DB.
const panelBootstrapScript = `set -euo pipefail
umask 077
ulimit -c 0
candidate=$1
export PATH="$candidate/sw/bin:/run/wrappers/bin"
test "$(id -u)" = 0
test "$(readlink -f /run/current-system)" = "$candidate"
test "$(readlink -f /nix/var/nix/profiles/system)" = "$candidate"
test "$(systemctl show panel.service --property=ActiveState --value)" = inactive
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
exec "$candidate/sw/bin/runuser" --user panel --group panel -- \
  "$candidate/sw/bin/env" -i HOME=/var/lib/panel USER=panel LOGNAME=panel \
  PATH="$candidate/sw/bin:/run/wrappers/bin" \
  "$candidate/sw/bin/panel" install panel-bootstrap
`

func runPanelBootstrap(parent context.Context, candidate string, handoff []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	// #nosec G204 -- The candidate is the recorded enrollment system path.
	command := exec.CommandContext(ctx, candidate+"/sw/bin/flock", "--nonblock", hostState+"/setup.lock",
		candidate+"/sw/bin/bash", "-c", panelBootstrapScript, "--", candidate)
	command.Env = []string{"LANG=C", "LC_ALL=C"}
	command.Stdin = bytes.NewReader(handoff)
	var output bytes.Buffer
	command.Stdout = &limitedWriter{buffer: &output, remaining: 8 << 10}
	if command.Run() != nil {
		return nil, errors.New("panel registration did not succeed; inspect retained setup before retrying")
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

// unresolved reports an outcome that may have started effects: observe it, never repeat.
func unresolved(ctx context.Context, err error) bool {
	return errors.Is(err, hostsetup.ErrUnresolved) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil
}

// managementPublicKey creates the panel's machine-management key once. It
// stays on this host; the panel stores an encrypted copy during registration.
func managementPublicKey(ctx context.Context) ([]byte, error) {
	if err := os.Mkdir(installState, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errors.New("management key directory unavailable")
	}
	info, err := os.Lstat(installState)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("management key directory must be a private root directory")
	}
	if _, err := os.Lstat(managementKeyPath); errors.Is(err, os.ErrNotExist) {
		// #nosec G204 -- Fixed native tool and paths.
		command := exec.CommandContext(ctx, systemBin+"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "panel-management", "-f", managementKeyPath)
		if command.Run() != nil {
			return nil, errors.New("management key generation failed")
		}
	}
	key, err := os.Lstat(managementKeyPath)
	if err != nil || !key.Mode().IsRegular() || key.Mode().Perm() != 0600 {
		return nil, errors.New("management key must be a private regular file")
	}
	// Derive from the private key: a crash may have left no .pub file.
	// #nosec G204 -- Fixed native tool and path.
	public, err := exec.CommandContext(ctx, systemBin+"ssh-keygen", "-y", "-f", managementKeyPath).Output()
	if err != nil || len(public) > 1024 {
		return nil, errors.New("management public key unavailable")
	}
	return bytes.TrimSpace(public), nil
}

// tailscaleAddress logs the host into the tailnet when needed and returns its IPv4 address.
func tailscaleAddress(ctx context.Context, log io.Writer) (netip.Addr, error) {
	if address, ok := tailscaleIPv4(ctx); ok {
		return address, nil
	}
	_, _ = fmt.Fprintln(log, "Log this host into your tailnet with the URL below.")
	// #nosec G204 -- Fixed native tool and arguments.
	command := exec.CommandContext(ctx, systemBin+"tailscale", "up", "--timeout=15m")
	command.Stdout, command.Stderr = log, log
	if command.Run() != nil {
		return netip.Addr{}, errors.New("tailscale login did not complete; rerun sudo panel install")
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

// operatorSession requires the recorded operator's sudo session over Tailscale,
// which proves private human access before bootstrap access closes.
func (run *installer) operatorSession() (bool, error) {
	session, err := currentLoginSession()
	if err != nil {
		return false, nil
	}
	return os.Getenv("SUDO_USER") == run.operator && session.overTailnet(), nil
}

func (run *installer) privateInstruction(ctx context.Context, prefix string) {
	target := "<tailscale-address>"
	if address, ok := tailscaleIPv4(ctx); ok {
		target = address.String()
	}
	_, _ = fmt.Fprintf(run.log, "%sFrom your workstation run:\n  ssh -t %s@%s sudo panel install\n", prefix, run.operator, target)
}

func (run *installer) activateFinal() (bool, error) {
	if ok, _ := run.operatorSession(); !ok {
		run.privateInstruction(run.ctx, "Final setup closes public SSH and needs your private operator login. ")
		return false, nil
	}
	if !run.ask("Final setup closes bootstrap SSH. Have you confirmed you can reach this host through your provider's console?") {
		return false, errors.New("final setup needs confirmed provider console access in an interactive terminal; nothing was changed")
	}
	// The retained panel start may not have proven readiness; repeating it is idempotent.
	if _, err := hostsetup.StartPanelHTTPS(run.ctx, run.built.OperationID, run.built.BuildSHA256); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintln(run.log, "Switching to the final configuration.")
	_, err := hostsetup.ActivateFinal(run.ctx, run.built.OperationID, run.built.BuildSHA256, []byte(`{"schema_version":1,"independent_recovery_verified":true}`))
	if err != nil {
		if !unresolved(run.ctx, err) {
			return false, err
		}
		_, _ = fmt.Fprintln(run.log, "Final activation response unavailable; observing without repeating activation.")
	}
	return true, nil
}

func (run *installer) confirmFinal(result *hostsetup.FinalResult) (bool, error) {
	fresh, err := run.freshSession("final-receipt.json")
	if ok, _ := run.operatorSession(); err != nil || !fresh || !ok {
		run.privateInstruction(run.ctx, fmt.Sprintf("Final configuration is active. Before %s, open a NEW private session. ", result.RecoveryDeadline))
		return false, nil
	}
	address, ok := tailscaleIPv4(run.ctx)
	if !ok {
		return false, errors.New("tailscale address unavailable")
	}
	proof, err := managementProof(run.ctx, address, result)
	if err != nil {
		return false, err
	}
	_, err = hostsetup.ConfirmFinal(run.ctx, run.built.OperationID, run.built.BuildSHA256, proof)
	return err == nil, err
}

// managementProof asks the host helper, over the panel's own private SSH path,
// to bind the final generation to this operation's challenge.
func managementProof(parent context.Context, address netip.Addr, result *hostsetup.FinalResult) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	hostKey, _, err := hostPublicKey()
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(installState, "proof-")
	if err != nil {
		return nil, errors.New("management proof workspace unavailable")
	}
	defer func() { _ = os.RemoveAll(directory) }()
	knownHosts := filepath.Join(directory, "known_hosts")
	if os.WriteFile(knownHosts, []byte(address.String()+" "+hostKey+"\n"), 0600) != nil {
		return nil, errors.New("management proof workspace unavailable")
	}
	request, err := json.Marshal(hosthelper.ProbeRequest{SchemaVersion: 1, Action: "probe", InstallationID: result.InstallationID,
		ServerID: result.ServerID, Challenge: result.Challenge})
	if err != nil {
		return nil, errors.New("management proof request unavailable")
	}
	// #nosec G204 -- Fixed native tool; the address is this host's validated tailnet IP.
	command := exec.CommandContext(ctx, systemBin+"ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "IdentityFile="+managementKeyPath, "-o", "UserKnownHostsFile="+knownHosts, "-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes", "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "ConnectTimeout=15",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes",
		"-p", "22", "panel-runner@"+address.String(), "exec /run/wrappers/bin/sudo -n -- /run/current-system/sw/bin/panel host-helper")
	command.Stdin = bytes.NewReader(request)
	var output bytes.Buffer
	command.Stdout = &limitedWriter{buffer: &output, remaining: 8 << 10}
	if command.Run() != nil {
		return nil, errors.New("private management proof failed; final recovery remains armed")
	}
	var response hosthelper.ProbeResponse
	if json.Unmarshal(output.Bytes(), &response) != nil {
		return nil, errors.New("private management proof is invalid")
	}
	// Confirmation compares this exact canonical encoding.
	return json.Marshal(response)
}

func (run *installer) report(status hostsetup.SetupStatus, output io.Writer) error {
	final := status.Final
	if final.BootedSystem != final.FinalSystem {
		_, _ = fmt.Fprintf(run.log, "Installation complete. Panel: %s\nReboot when ready, then run sudo panel install to verify the booted configuration.\n", final.Origin)
	} else {
		_, _ = fmt.Fprintf(run.log, "Installation complete and verified after boot. Panel: %s\n", final.Origin)
	}
	if json.NewEncoder(output).Encode(final) != nil {
		return errors.New("installation status could not be written")
	}
	return nil
}
