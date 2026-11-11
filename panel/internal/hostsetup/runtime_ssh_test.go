package hostsetup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
)

func sshFixture(t *testing.T) (foundation.Input, sshEffective, []string) {
	t.Helper()
	_, input, _ := sourcePacketFixture(t)
	sources := []string{"%h/.ssh/authorized_keys", "/etc/ssh/authorized_keys.d/%u", "/var/lib/panel-access/%u/authorized_keys"}
	data := []byte("Port 22\nPermitRootLogin prohibit-password\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM yes\nStrictModes yes\nPermitUserEnvironment no\nAuthenticationMethods publickey\nHostbasedAuthentication no\nAuthorizedKeysCommand none\nAuthorizedPrincipalsCommand none\nAuthorizedPrincipalsFile none\nTrustedUserCAKeys none\nForceCommand none\nChrootDirectory none\nAuthorizedKeysFile %h/.ssh/authorized_keys\nAuthorizedKeysFile /etc/ssh/authorized_keys.d/%u\nAuthorizedKeysFile /var/lib/panel-access/%u/authorized_keys\nPubkeyAcceptedAlgorithms ssh-ed25519,rsa-sha2-512,rsa-sha2-256\nRequiredRSASize 1024\nAllowUsers root\nAllowUsers " + input.Operator.Name + "\n")
	settings, err := parseSSHEffective(data)
	if err != nil {
		t.Fatal(err)
	}
	return input, settings, sources
}

func otherAuthorityKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestRuntimeSSHRefusesUnpreservedLoginUsersWithoutAllowUsers(t *testing.T) {
	input, settings, _ := sshFixture(t)
	accounts := accountSnapshot{users: map[string]runtimeAccount{
		"root":              {shell: "/bin/bash"},
		input.Operator.Name: {shell: "/bin/bash"},
		"service":           {shell: "/nix/store/00000000000000000000000000000000-shadow-4.19/bin/nologin"},
	}}
	delete(settings, "allowusers")
	if err := confirmSSHLoginUsers(settings, input.Operator.Name, accounts); err != nil {
		t.Fatalf("root/operator and native non-login services refused: %v", err)
	}
	accounts.users["existing-human"] = runtimeAccount{shell: "/bin/bash"}
	if err := confirmSSHLoginUsers(settings, input.Operator.Name, accounts); !errors.Is(err, ErrRuntimeSSH) {
		t.Fatalf("unpreserved login authority admitted: %v", err)
	}
	settings["allowusers"] = []string{"root " + input.Operator.Name}
	if err := confirmSSHLoginUsers(settings, input.Operator.Name, accounts); err != nil {
		t.Fatalf("explicitly excluded unrelated account refused: %v", err)
	}
}

func TestRuntimeSSHPreservesExactRestrictedAuthorityAndRefusesPlainCollision(t *testing.T) {
	input, settings, _ := sshFixture(t)
	line := "from=\"192.0.2.0/24\",no-pty " + otherAuthorityKey(t) + " original comment\r\n"
	file := sshAuthorityFile{User: "root", Source: "runtime", Active: true, Present: true, Bytes: []byte("# retain comment\r\n" + line)}
	files, err := admitSSHAuthority(input, settings, []sshAuthorityFile{file})
	if err != nil || len(files) != 1 || files[0].Keys != 1 || !bytes.Equal(files[0].Bytes, file.Bytes) || files[0].SHA256 == "" || file.Keys != 0 {
		t.Fatalf("restricted bytes changed or source mutated: %v", err)
	}
	for _, prefix := range []string{"restrict ", "command=\"/usr/bin/true\" ", "from=\"192.0.2.0/24\" "} {
		file.Bytes = []byte(prefix + input.Operator.PublicKey + "\n")
		if _, err := admitSSHAuthority(input, settings, []sshAuthorityFile{file}); !errors.Is(err, ErrRuntimeSSH) {
			t.Fatalf("restricted credential became an unrestricted operator: %v", err)
		}
	}
	file.Bytes = []byte("environment=\"sensitive-option\" " + otherAuthorityKey(t) + "\n")
	if _, err := admitSSHAuthority(input, settings, []sshAuthorityFile{file}); !errors.Is(err, ErrRuntimeSSH) || strings.Contains(err.Error(), "sensitive-option") {
		t.Fatalf("unsupported authority accepted or reflected: %v", err)
	}
	file.Bytes = []byte(otherAuthorityKey(t) + "\n")
	file.Active = false
	if _, err := admitSSHAuthority(input, settings, []sshAuthorityFile{file}); !errors.Is(err, ErrRuntimeSSH) {
		t.Fatalf("inactive key source would be silently enabled: %v", err)
	}
}

func TestRuntimeSSHRefusesEffectiveAuthenticationAndSourceDrift(t *testing.T) {
	for _, name := range []string{"native defaults", "password drift", "root drift", "MFA", "hostbased", "GSSAPI", "key command", "CA", "unknown source", "user pattern", "duplicate scalar"} {
		t.Run(name, func(t *testing.T) {
			input, settings, sources := sshFixture(t)
			switch name {
			case "password drift":
				settings["passwordauthentication"] = []string{"yes"}
			case "root drift":
				settings["permitrootlogin"] = []string{"yes"}
			case "MFA":
				settings["authenticationmethods"] = []string{"publickey,password"}
			case "hostbased":
				settings["hostbasedauthentication"] = []string{"yes"}
			case "GSSAPI":
				settings["gssapiauthentication"] = []string{"yes"}
			case "key command":
				settings["authorizedkeyscommand"] = []string{"/unsupported/private-source"}
			case "CA":
				settings["trustedusercakeys"] = []string{"/unsupported/ca"}
			case "unknown source":
				settings["authorizedkeysfile"] = []string{"/unsupported/keys"}
			case "user pattern":
				settings["allowusers"] = []string{"root@192.0.2.0/24"}
			case "duplicate scalar":
				settings["port"] = []string{"22", "23"}
			}
			err := confirmSSHPolicy(settings, input, sources)
			if name == "native defaults" {
				if err != nil {
					t.Fatalf("capitalized/repeated native fields or absent unsupported GSSAPI refused: %v", err)
				}
			} else if !errors.Is(err, ErrRuntimeSSH) {
				t.Fatalf("effective authority drift accepted: %v", err)
			}
		})
	}
	for _, config := range []struct {
		body    string
		allowed bool
	}{
		{"Port 22\nMatch User panel-runner\nForceCommand /fixed/helper\nMatch all\n", true},
		{"Include /unsupported/private\n", false}, {"Include=/unsupported/private\n", false},
		{"Match User operator\nPasswordAuthentication yes\n", false}, {"Match Address 192.0.2.0/24\n", false},
	} {
		if admittedSSHConfig([]byte(config.body)) != config.allowed {
			t.Fatal("unsupported human Match/Include gate disagrees")
		}
	}
}

func TestRuntimeSSHAuthorityReadsRejectMissingRenderedSymlinkAndOversize(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	file := sshAuthorityFile{User: "operator", Source: "home", Active: true}
	effectiveUID, err := strconv.ParseUint(strconv.Itoa(os.Geteuid()), 10, 32)
	if err != nil {
		t.Fatal("unsupported fixture UID")
	}
	uid := uint32(effectiveUID)
	missing, err := readSSHAuthorityFile(t.Context(), root, "authorized_keys", file, uid)
	if err != nil || missing.Present {
		t.Fatalf("missing optional home source treated as authority: %v", err)
	}
	if err := root.WriteFile("private-sentinel", []byte("not an authorized public source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("private-sentinel", "authorized_keys"); err != nil {
		t.Fatal(err)
	}
	if value, err := readSSHAuthorityFile(t.Context(), root, "authorized_keys", file, uid); !errors.Is(err, ErrRuntimeSSH) || len(value.Bytes) != 0 {
		t.Fatalf("symlink public source followed: %v", err)
	}
	if err := root.Remove("authorized_keys"); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("authorized_keys", make([]byte, maxSSHFileBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSSHAuthorityFile(t.Context(), root, "authorized_keys", file, uid); !errors.Is(err, ErrRuntimeSSH) {
		t.Fatalf("oversized authority accepted: %v", err)
	}
	path := filepath.Join(t.TempDir(), "expected-rendered")
	if err := os.WriteFile(path, []byte("expected public source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRenderedSSHKeys(t.Context(), sshAuthorityFile{Path: path + "-missing"}, path); !errors.Is(err, ErrRuntimeSSH) {
		t.Fatalf("missing declared rendered authority silently ignored: %v", err)
	}
}

func TestRuntimeSSHNativeQualifiedDumpNeverLoadsHostPrivateKeys(t *testing.T) {
	directory := t.TempDir()
	key := filepath.Join(directory, "host-private-sentinel")
	if err := syscall.Mkfifo(key, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "sshd_config")
	config := "Port 22\nUsePAM yes\nPubkeyAuthentication yes\nPermitRootLogin prohibit-password\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nAuthenticationMethods publickey\nHostKey " + key + "\nAuthorizedKeysFile %h/.ssh/authorized_keys /etc/ssh/authorized_keys.d/%u /var/lib/panel-access/%u/authorized_keys\n"
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := runtimeProbe(t.Context(), nativeTool(t, "PANEL_TEST_SSHD", "sshd"), "-G", "-T", "-C", "user=root", "-f", path)
	if err != nil {
		t.Fatalf("qualified config print attempted a blocking private-key read or failed: %v", err)
	}
	settings, err := parseSSHEffective(data)
	input, _, sources := sshFixture(t)
	if err != nil || confirmSSHPolicy(settings, input, sources) != nil {
		t.Fatalf("actual native human policy shape incompatible: %v", err)
	}
}

func TestRuntimeSSHNativeReadabilityRejectsDormantAuthority(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires the explicit root-only temporary native fixture scope")
	}
	// Use the public temporary parent rather than the test cache's private
	// ancestors, whose permissions would mask the authority-specific checks.
	directory, err := os.MkdirTemp("/tmp", "panel-ssh-readable-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	// #nosec G302 -- Public temporary fixture must be traversable by its test UID; no credentials are stored here.
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(directory, "authority")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "authorized_keys")
	// #nosec G306 -- Public test bytes deliberately model a key source readable by the test UID.
	if err := os.WriteFile(path, []byte("public fixture only\n"), 0644); err != nil {
		t.Fatal(err)
	}
	accounts := accountSnapshot{users: map[string]runtimeAccount{"operator": {uid: 65534, gid: 65534}}, groups: map[string]runtimeAccountGroup{}}
	executable := nativeTool(t, "PANEL_TEST_TEST", "test")
	check := func(wanted bool) {
		t.Helper()
		err := confirmSSHReadable(t.Context(), executable, path, "operator", accounts)
		if (err == nil) != wanted {
			t.Fatalf("native readability got %v; readable wanted %v", err, wanted)
		}
	}
	check(false) // Root-only ancestor must not become usable during retention.
	// #nosec G302 -- Ancestor traversal is the permission boundary under test; the directory holds only public fixture bytes.
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	check(false) // Root can read this file, but the actual operator cannot.
	// #nosec G302 -- This deliberate public mode is the readable counterpart of the preceding root-only fixture.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := os.Chown(path, 0, 65533); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- Group-readable public fixture verifies that only explicitly captured groups grant access.
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	check(false)
	accounts.groups["fixture-access"] = runtimeAccountGroup{gid: 65533, members: map[string]bool{"operator": true}}
	check(true) // Only the explicitly captured supplementary group grants access.
}
