package securestate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestSecretVersionsPreserveOmissionsAndIsolateOwners(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	cipher, err := NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault(database, cipher)
	first, err := vault.UpdateEnvironment(ctx, "environment-a", 0, map[string]string{"API_TOKEN": "first-private-value", "WORKER_TOKEN": "worker-value"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := vault.UpdateEnvironment(ctx, "environment-a", first.Version, map[string]string{"API_TOKEN": "second-private-value"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, err := vault.Environment(ctx, first)
	if err != nil || old["API_TOKEN"] != "first-private-value" {
		t.Fatalf("queued snapshot changed: %v", err)
	}
	current, err := vault.Environment(ctx, second)
	if err != nil || current["WORKER_TOKEN"] != "worker-value" {
		t.Fatalf("omitted secret lost: %v", err)
	}
	_, err = vault.UpdateEnvironment(ctx, "environment-a", first.Version, map[string]string{"API_TOKEN": "stale"}, nil)
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	_, err = vault.UpdateEnvironment(ctx, "environment-a", second.Version, map[string]string{"API_TOKEN": "ambiguous"}, []string{"API_TOKEN"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ambiguous change accepted: %v", err)
	}
	third, err := vault.UpdateEnvironment(ctx, "environment-a", second.Version, nil, []string{"API_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	values, err := vault.Environment(ctx, third)
	if err != nil || len(values) != 1 {
		t.Fatalf("explicit removal failed: %v", err)
	}
	var ciphertext []byte
	if err := database.QueryRowContext(ctx, "SELECT ciphertext FROM encrypted_versions WHERE kind='environment' AND owner_id=? AND version=1", "environment-a").Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("first-private-value")) {
		t.Fatal("plaintext stored")
	}
	for _, ref := range []RecordRef{{Kind: EnvironmentKind, OwnerID: "environment-b", Version: 1}, {Kind: EnvironmentKind, OwnerID: "environment-a", Version: 2}, {Kind: CredentialKind, OwnerID: "environment-a", Version: 1}} {
		if _, err := cipher.Open(ref, ciphertext); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("record binding not enforced for %+v: %v", ref, err)
		}
	}
}

func TestMasterKeyCreationRefusesReplacementAndUnsafePermissions(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "keys", "master")
	if err := privatefile.CreateKey(keyPath); err != nil {
		t.Fatal(err)
	}
	first, err := privatefile.ReadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := privatefile.CreateKey(keyPath); err == nil {
		t.Fatal("existing key replaced")
	}
	second, err := privatefile.ReadKey(keyPath)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("key changed")
	}
	// #nosec G302 -- Deliberately unsafe key mode; this regression case must be rejected.
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := privatefile.ReadKey(keyPath); !errors.Is(err, privatefile.ErrProtection) {
		t.Fatalf("unsafe key mode accepted: %v", err)
	}
	if _, err := privatefile.ReadKey(keyPath + "-missing"); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestCredentialVersionsAreImmutableAndAuthenticationFailuresAreSafe(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	cipher, err := NewCipher(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault(database, cipher)
	first, err := vault.SaveCredential(ctx, "github-installation", 0, []byte("first-private-credential"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := vault.SaveCredential(ctx, "github-installation", first.Version, []byte("new-private-credential"))
	if err != nil {
		t.Fatal(err)
	}
	firstValue, err := vault.Credential(ctx, first)
	if err != nil || string(firstValue) != "first-private-credential" {
		t.Fatalf("credential snapshot changed: %v", err)
	}
	if _, err := vault.SaveCredential(ctx, "github-installation", 0, []byte("stale")); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale credential accepted: %v", err)
	}
	wrong, err := NewCipher(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewVault(database, wrong).Credential(ctx, second); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong key accepted: %v", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE encrypted_versions SET ciphertext = X'00' WHERE kind='credential' AND owner_id=? AND version=?", second.OwnerID, second.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Credential(ctx, second); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("corrupt record accepted: %v", err)
	}
}
