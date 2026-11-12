package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestRunnerKeyIsRestrictedPlainEd25519(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	plain := ssh.MarshalAuthorizedKey(key)
	withComment := strings.TrimSuffix(string(plain), "\n") + " panel-management"
	content, err := runnerAuthorizedKeys([]byte(withComment))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, options, rest, err := ssh.ParseAuthorizedKey(content)
	if err != nil || len(options) != 1 || options[0] != "restrict" || len(rest) != 0 || ssh.FingerprintSHA256(parsed) != ssh.FingerprintSHA256(key) {
		t.Fatalf("panel-runner key is not exactly the restricted management key: %q", content)
	}
	for name, input := range map[string]string{
		"options":  `command="sh" ` + string(plain),
		"two keys": string(plain) + string(plain),
		"empty":    "",
	} {
		if _, err := runnerAuthorizedKeys([]byte(input)); err == nil {
			t.Fatalf("%s accepted as management key", name)
		}
	}
}

func TestHostIdentityRequiresDistinctCanonicalPanelIDs(t *testing.T) {
	valid := hostIdentity{InstallationID: "11111111-1111-4111-8111-111111111111", ServerID: "aaaaaaaa-2222-4222-8222-222222222222", Role: "panel"}
	if !validIdentity(valid) {
		t.Fatal("valid panel identity refused")
	}
	for name, change := range map[string]func(*hostIdentity){
		"server role":   func(value *hostIdentity) { value.Role = "server" },
		"same ids":      func(value *hostIdentity) { value.ServerID = value.InstallationID },
		"uppercase":     func(value *hostIdentity) { value.ServerID = strings.ToUpper(value.ServerID) },
		"nil uuid":      func(value *hostIdentity) { value.InstallationID = "00000000-0000-0000-0000-000000000000" },
		"missing value": func(value *hostIdentity) { value.ServerID = "" },
	} {
		changed := valid
		change(&changed)
		if validIdentity(changed) {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestRegisteredTailscaleAddressMustNotChange(t *testing.T) {
	current := netip.MustParseAddr("100.64.0.2")
	for marker, want := range map[string]bool{"": true, "100.64.0.2": true, "100.64.0.2\n": true, "100.64.0.9": false} {
		err := sameRegisteredAddress([]byte(marker), current)
		if (err == nil) != want {
			t.Fatalf("marker %q: %v", marker, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "100.64.0.9") || !strings.Contains(err.Error(), "100.64.0.2")) {
			t.Fatalf("mismatch must name both addresses: %v", err)
		}
	}
}
