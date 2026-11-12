package installation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func installationFixture(t *testing.T) (Handoff, *sql.DB, *securestate.Cipher) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	hostPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewPublicKey(hostPublic)
	if err != nil {
		t.Fatal(err)
	}
	input := Handoff{SchemaVersion: 1, InstallationID: "22222222-2222-4222-8222-222222222222", ServerID: "33333333-3333-4333-8333-333333333333", TailscaleAddress: "100.64.0.10", HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host))), HostFingerprint: ssh.FingerprintSHA256(host), ManagementFingerprint: ssh.FingerprintSHA256(signer.PublicKey()), AdministratorPassword: "fixture-password-only", ManagementPrivateKey: string(pem.EncodeToMemory(private))}
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, err := securestate.NewCipher(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return input, database, cipher
}

func TestRegistrationEncryptsAndReplaysWithoutDuplicatePanelHost(t *testing.T) {
	input, database, cipher := installationFixture(t)
	if _, err := validate(input); err != nil {
		t.Fatal(err)
	}
	first, err := register(t.Context(), database, cipher, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := register(t.Context(), database, cipher, input)
	if err != nil || second != first || first.Ready || first.Status != "registered" {
		t.Fatalf("registration replay changed result: %v", err)
	}
	queries := store.New(database)
	installed, err := queries.GetInstallation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	management, err := queries.GetServerManagement(t.Context(), input.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := queries.GetEncryptedVersion(t.Context(), store.GetEncryptedVersionParams{Kind: management.CredentialKind, OwnerID: management.CredentialOwnerID, Version: management.CredentialVersion})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Ciphertext, []byte("PRIVATE KEY")) || bytes.Contains(encrypted.Ciphertext, []byte(input.ManagementPrivateKey)) {
		t.Fatal("private material persisted without encryption")
	}
	copyInput := input
	copyInput.AdministratorPassword = ""
	canonical, err := json.Marshal(copyInput)
	if err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256(canonical)
	if bytes.Equal(installed.BootstrapIdentity, plain[:]) {
		t.Fatal("secret-bearing replay identity is dictionary-checkable")
	}
	raw, err := securestate.NewVault(database, cipher).Credential(t.Context(), securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: management.CredentialOwnerID, Version: management.CredentialVersion})
	if err != nil || string(raw) != input.ManagementPrivateKey {
		t.Fatal("encrypted credential binding lost")
	}
	clear(raw)
	changed := input
	changed.ServerID = "44444444-4444-4444-8444-444444444444"
	if _, err := register(t.Context(), database, cipher, changed); !errors.Is(err, ErrIdentity) {
		t.Fatal("conflicting prior installation adopted")
	}
	changed = input
	changed.ManagementPrivateKey += "\n"
	if _, err := register(t.Context(), database, cipher, changed); !errors.Is(err, ErrIdentity) {
		t.Fatal("request replay accepted different protected payload")
	}
	var count int
	if database.QueryRowContext(t.Context(), "SELECT count(*) FROM servers WHERE panel_host").Scan(&count) != nil || count != 1 {
		t.Fatal("duplicate panel host created")
	}
	if _, err := database.ExecContext(t.Context(), "DELETE FROM servers WHERE id=?", input.ServerID); err == nil {
		t.Fatal("panel host deletion allowed")
	}
	if _, err := database.ExecContext(t.Context(), "UPDATE servers SET panel_host=FALSE WHERE id=?", input.ServerID); err == nil {
		t.Fatal("panel host protection bypassed")
	}
	server, err := queries.GetServer(t.Context(), input.ServerID)
	if err != nil || server.Readiness != "setup_pending" {
		t.Fatal("registration claimed deployment readiness")
	}
	if _, err := database.ExecContext(t.Context(), "UPDATE installation SET installation_id=? WHERE id=1", "44444444-4444-4444-8444-444444444444"); err != nil {
		t.Fatal(err)
	}
	if _, err := register(t.Context(), database, cipher, input); !errors.Is(err, ErrIdentity) {
		t.Fatal("conflicting retained installation identity silently adopted")
	}
}

func TestRegistrationFailureDoesNotLeaveServerOrInstallation(t *testing.T) {
	input, database, cipher := installationFixture(t)
	if _, err := securestate.NewVault(database, cipher).SaveCredential(t.Context(), credentialOwner(input.InstallationID, input.ServerID), 0, []byte("prior credential")); err != nil {
		t.Fatal(err)
	}
	if _, err := register(t.Context(), database, cipher, input); !errors.Is(err, ErrIdentity) {
		t.Fatal("conflicting credential version accepted")
	}
	if _, err := store.New(database).GetServer(t.Context(), input.ServerID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed encrypted credential write leaked server row")
	}
	if _, err := store.New(database).GetInstallation(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed encrypted credential write leaked installation")
	}
}

func TestPrivateProbeRequiresFreshChallengeAndExactNativeIdentity(t *testing.T) {
	input, database, cipher := installationFixture(t)
	if _, err := register(t.Context(), database, cipher, input); err != nil {
		t.Fatal(err)
	}
	request := hosthelper.ProbeRequest{SchemaVersion: 1, Action: "probe", InstallationID: input.InstallationID, ServerID: input.ServerID, Challenge: strings.Repeat("ab", 32)}
	system := "/nix/store/" + strings.Repeat("2", 32) + "-nixos-system-host"
	previous := "/nix/store/" + strings.Repeat("1", 32) + "-nixos-system-host"
	response := hosthelper.ProbeResponse{SchemaVersion: 1, InstallationID: input.InstallationID, ServerID: input.ServerID, Challenge: request.Challenge, Role: "panel", CurrentSystem: system, SystemProfile: system, BootedSystem: previous, Ready: false}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProbe(encoded, request); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"challenge", "identity", "role", "pending switch", "ready", "duplicate", "unknown", "oversized"} {
		t.Run(name, func(t *testing.T) {
			changed := response
			switch name {
			case "challenge":
				changed.Challenge = strings.Repeat("cd", 32)
			case "identity":
				changed.ServerID = input.InstallationID
			case "role":
				changed.Role = "server"
			case "pending switch":
				changed.SystemProfile = previous
			case "ready":
				changed.Ready = true
			}
			data, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if name == "duplicate" {
				data = bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
			}
			if name == "unknown" {
				data = append(data[:len(data)-1], []byte(`,"token":"private-sentinel"}`)...)
			}
			if name == "oversized" {
				data = make([]byte, (8<<10)+1)
			}
			if _, err := VerifyProbe(data, request); !errors.Is(err, ErrProof) || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatal("unsafe probe evidence admitted or reflected")
			}
		})
	}
	queries := store.New(database)
	installed, err := queries.GetInstallation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	management, err := queries.GetServerManagement(t.Context(), input.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := queries.GetServer(t.Context(), input.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if recordProof(t.Context(), database, installed, management, server, 123) != nil {
		t.Fatal("exact proof could not be recorded")
	}
	if _, err := database.ExecContext(t.Context(), "UPDATE server_management SET credential_version=2 WHERE server_id=?", input.ServerID); err == nil {
		t.Fatal("invalid encrypted credential reference allowed")
	}
	if _, err := database.ExecContext(t.Context(), "UPDATE server_management SET management_fingerprint='changed' WHERE server_id=?", input.ServerID); err != nil {
		t.Fatal(err)
	}
	if err := recordProof(t.Context(), database, installed, management, server, 456); !errors.Is(err, ErrIdentity) {
		t.Fatal("old proof attached to changed credential")
	}
	current, err := queries.GetInstallation(t.Context())
	if err != nil || current.PrivateProofAt != 123 {
		t.Fatal("stale proof overwrote prior observation")
	}
}

func TestSensitiveHandoffRejectsAmbiguityAndBoundsBlockedInput(t *testing.T) {
	input, _, _ := installationFixture(t)
	valid, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(valid); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1), bytes.Replace(valid, []byte(`"schema_version"`), []byte(`"Schema_Version"`), 1), append(bytes.Clone(valid), []byte(` {}`)...), make([]byte, MaxHandoffBytes+1), bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":"1"`), 1)} {
		if _, err := Parse(data); !errors.Is(err, ErrInput) {
			t.Fatal("ambiguous or oversized sensitive handoff admitted")
		}
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := ReadHandoff(ctx, reader); !errors.Is(err, ErrInput) {
		t.Fatal("blocked secret input escaped deadline")
	}
}

func TestPanelPrivateProofRefusesPeerOrUserspaceAddress(t *testing.T) {
	address := netip.MustParseAddr("100.64.0.10")
	status := &ipnstate.Status{BackendState: "Running", TUN: true, TailscaleIPs: []netip.Addr{address}, Self: &ipnstate.PeerStatus{ID: tailcfg.StableNodeID("self-node"), TailscaleIPs: []netip.Addr{address}}}
	if err := confirmSelfAddress(status, address.String()); err != nil {
		t.Fatal("native Self address refused")
	}
	if err := confirmSelfAddress(status, "100.64.0.11"); !errors.Is(err, ErrProof) {
		t.Fatal("peer address accepted as panel host")
	}
	status.TUN = false
	if err := confirmSelfAddress(status, address.String()); !errors.Is(err, ErrProof) {
		t.Fatal("userspace-only node accepted")
	}
	status.TUN = true
	status.Self = nil
	if err := confirmSelfAddress(status, address.String()); !errors.Is(err, ErrProof) {
		t.Fatal("unbound self identity accepted")
	}
}
