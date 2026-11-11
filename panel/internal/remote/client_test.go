package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "disposable-fixture")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), key
}

func TestTargetRequiresPrivateAddressAndIndependentlyMatchingPlainHostKey(t *testing.T) {
	_, key := testKey(t)
	_, other := testKey(t)
	authorized := ssh.MarshalAuthorizedKey(key)
	fingerprint := ssh.FingerprintSHA256(key)
	for _, address := range []string{"100.64.0.1", "100.127.255.254", "fd7a:115c:a1e0::1"} {
		if _, err := NewTarget(address, "panel-host", authorized, fingerprint); err != nil {
			t.Fatalf("valid target %s rejected: %v", address, err)
		}
	}
	for _, test := range []struct {
		name        string
		address     string
		user        string
		key         []byte
		fingerprint string
	}{
		{"public address", "203.0.113.1", "panel", authorized, fingerprint},
		{"loopback", "127.0.0.1", "panel", authorized, fingerprint},
		{"DNS destination", "host.example.ts.net", "panel", authorized, fingerprint},
		{"IPv6 zone", "fd7a:115c:a1e0::1%eth0", "panel", authorized, fingerprint},
		{"address option", "-oProxyCommand=evil", "panel", authorized, fingerprint},
		{"user injection", "100.64.0.1", "panel;id", authorized, fingerprint},
		{"wrong fingerprint", "100.64.0.1", "panel", authorized, ssh.FingerprintSHA256(other)},
		{"missing fingerprint", "100.64.0.1", "panel", authorized, ""},
		{"multiple keys", "100.64.0.1", "panel", append(bytes.Clone(authorized), authorized...), fingerprint},
		{"key options", "100.64.0.1", "panel", append([]byte("command=\"id\" "), authorized...), fingerprint},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewTarget(test.address, test.user, test.key, test.fingerprint); !errors.Is(err, ErrTarget) {
				t.Fatalf("unsafe target admitted: %v", err)
			}
		})
	}
}

func TestNativeSSHIdentityAndProcessFailureBoundaries(t *testing.T) {
	sshCommand, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("native OpenSSH client unavailable")
	}
	sshdCommand, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("native OpenSSH server unavailable")
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	privateKey, identity := testKey(t)
	defer clear(privateKey)
	hostPrivate, hostKey := testKey(t)
	defer clear(hostPrivate)
	otherPrivate, otherKey := testKey(t)
	defer clear(otherPrivate)
	for name, data := range map[string][]byte{
		"host_key":        hostPrivate,
		"authorized_keys": ssh.MarshalAuthorizedKey(identity),
		"helper":          []byte("#!/bin/sh\nIFS= read -r mode\ncase \"$mode\" in\necho) exec /bin/cat ;;\nstdout) exec /usr/bin/head -c 2097153 /dev/zero ;;\nstderr) /usr/bin/head -c 32769 /dev/zero >&2 ;;\nwait) exec /bin/sleep 1 ;;\nexit) exit 7 ;;\nlost) exit 255 ;;\n*) exit 9 ;;\nesac\n"),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(directory, "helper"), 0700); err != nil { // #nosec G302 -- executable disposable fixture, accessible only to its owner.
		t.Fatal(err)
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	portText := strconv.Itoa(port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %s\nPidFile none\nHostKey %s/host_key\nAuthorizedKeysFile %s/authorized_keys\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nAuthenticationMethods publickey\nAllowUsers %s\nPermitRootLogin prohibit-password\nDisableForwarding yes\nPermitTTY no\nLogLevel QUIET\nForceCommand %s/helper\n", portText, directory, directory, currentUser.Username, directory)
	configPath := filepath.Join(directory, "sshd_config")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureContext, stopFixture := context.WithTimeout(t.Context(), 15*time.Second)
	defer stopFixture()
	server := exec.CommandContext(fixtureContext, sshdCommand, "-D", "-e", "-f", configPath) // #nosec G204 -- disposable native fixture with owned configuration.
	server.Stdout = io.Discard
	diagnostics := boundedOutput{limit: maxStderrBytes, cancel: stopFixture, retain: true}
	server.Stderr = &diagnostics
	server.WaitDelay = time.Second
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopFixture()
		_ = server.Wait()
	})
	endpoint := net.JoinHostPort("127.0.0.1", portText)
	deadline := time.Now().Add(3 * time.Second)
	for {
		dialer := net.Dialer{Timeout: 50 * time.Millisecond}
		connection, err := dialer.DialContext(t.Context(), "tcp4", endpoint)
		if err == nil {
			_ = connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disposable SSH server did not listen: %s", diagnostics.buffer.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	client, err := New(sshCommand, filepath.Join(directory, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewTarget("100.64.0.1", currentUser.Username, ssh.MarshalAuthorizedKey(hostKey), ssh.FingerprintSHA256(hostKey))
	if err != nil {
		t.Fatal(err)
	}
	// Only this disposable native fixture bypasses the public Tailscale-only
	// address boundary and uses an ephemeral port. Production Helper fixes 22.
	target.address = netip.MustParseAddr("127.0.0.1")
	for _, test := range []struct {
		name    string
		mode    string
		key     []byte
		target  Target
		outcome Outcome
		err     error
		code    int
		timeout time.Duration
	}{
		{"verified identity", "echo\nsensitive-fixture-payload", privateKey, target, CommandExited, nil, 0, 3 * time.Second},
		{"changed host key", "echo\nsensitive-fixture-payload", privateKey, Target{target.address, target.user, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherKey))), ssh.KeyAlgoED25519}, Unknown, ErrTransport, -1, 3 * time.Second},
		{"wrong client identity", "echo\nsensitive-fixture-payload", otherPrivate, target, Unknown, ErrTransport, -1, 3 * time.Second},
		{"remote nonzero is not transport loss", "exit\n", privateKey, target, CommandExited, ErrRemoteExit, 7, 3 * time.Second},
		{"reserved exit remains unknown", "lost\n", privateKey, target, Unknown, ErrTransport, -1, 3 * time.Second},
		{"stdout bounded", "stdout\n", privateKey, target, Unknown, ErrOutputLimit, -1, 3 * time.Second},
		{"stderr bounded and hidden", "stderr\n", privateKey, target, Unknown, ErrOutputLimit, -1, 3 * time.Second},
		{"cancellation remains unknown", "wait\n", privateKey, target, Unknown, context.DeadlineExceeded, -1, 200 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), test.timeout)
			defer cancel()
			result, err := client.run(ctx, test.target, test.key, []byte(test.mode), port)
			if !errors.Is(err, test.err) || result.Outcome != test.outcome || result.ExitCode != test.code {
				t.Fatalf("outcome=%v exit=%d error=%v; want outcome=%v exit=%d error=%v", result.Outcome, result.ExitCode, err, test.outcome, test.code, test.err)
			}
			if err == nil {
				if string(result.Output) != "sensitive-fixture-payload" {
					t.Fatal("stdin payload did not reach the verified endpoint intact")
				}
			} else if len(result.Output) != 0 || strings.Contains(err.Error(), "sensitive-fixture-payload") {
				t.Fatal("failed transport leaked partial output or diagnostic data")
			}
			entries, err := os.ReadDir(client.directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("temporary SSH credentials survived the completed call")
			}
		})
	}
}
