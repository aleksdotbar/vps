package hostsetup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
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

func TestRuntimeSSHFingerprintsProviderKeysAndKeepsOperatorUnrestricted(t *testing.T) {
	input, _, _ := sshFixture(t)
	provider := "environment=\"x=1\" " + otherAuthorityKey(t) + " provider key\nnot a key\n"
	file := sshAuthorityFile{User: "root", Source: "home", Active: false, Present: true, Bytes: []byte("# provider\n" + provider)}
	files, err := admitSSHAuthority(input, []sshAuthorityFile{file})
	if err != nil || len(files) != 1 || files[0].Keys != 1 || !bytes.Equal(files[0].Bytes, file.Bytes) || files[0].SHA256 == "" || file.Keys != 0 {
		t.Fatalf("provider-added keys refused or source mutated: %v", err)
	}
	for _, prefix := range []string{"restrict ", "command=\"/usr/bin/true\" "} {
		file.Bytes = []byte(prefix + input.Operator.PublicKey + "\n")
		if _, err := admitSSHAuthority(input, []sshAuthorityFile{file}); !errors.Is(err, ErrRuntimeSSH) {
			t.Fatalf("restricted operator key accepted: %v", err)
		}
	}
}
