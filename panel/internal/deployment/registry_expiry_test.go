package deployment

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCredentialPublicationRequiresExpiryAndCleansWithoutStageReceipt(t *testing.T) {
	request, _ := stageFixture(t)
	hash, err := ManifestSHA256(request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	expected := claim{request.Manifest.Identity, hash, fixtureFinal}
	credential := RegistryCredential{Username: "fixture", Token: []byte("synthetic-expiring-token")}
	defer clear(credential.Token)
	_, data, err := registryProof(&credential)
	defer clear(data)
	if err != nil {
		t.Fatal(err)
	}
	root := stateRoot(t)
	refused := func(context.Context, registryExpiry) error { return ErrState }
	if err := publishRegistry(t.Context(), root, expected, data, refused); !errors.Is(err, ErrState) {
		t.Fatal("failed timer admission published token")
	}
	directory, err := child(root, expected.Identity.OperationID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Lstat("credential.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plaintext existed before expiry admission")
	}
	_ = directory.Close()
	if err := cleanupExpiredRegistry(t.Context(), root, expected.Identity.OperationID, time.UnixMilli(expected.Identity.ExecutionDeadline)); err != nil {
		t.Fatal(err)
	}
	expected.Identity.OperationID = uuid.NewString()
	armed := false
	arm := func(_ context.Context, expiry registryExpiry) error {
		directory, err := child(root, expiry.Claim.Identity.OperationID, false)
		if err != nil {
			return err
		}
		defer func() { _ = directory.Close() }()
		if _, err := readRegistryExpiry(directory, expiry.Claim.Identity.OperationID); err != nil {
			return err
		}
		if _, err := directory.Lstat("credential.json"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("token existed before native timer verification")
		}
		armed = true
		return nil
	}
	if err := publishRegistry(t.Context(), root, expected, data, arm); err != nil || !armed {
		t.Fatalf("expiry-bound token admission: %v", err)
	}
	// No active.json/stage.json or runner was ever published for this token.
	if err := cleanupExpiredRegistry(t.Context(), root, expected.Identity.OperationID, time.UnixMilli(expected.Identity.ExecutionDeadline-1)); !errors.Is(err, ErrDeadline) {
		t.Fatal("credential removed before immutable deadline")
	}
	if err := cleanupExpiredRegistry(t.Context(), root, expected.Identity.OperationID, time.UnixMilli(expected.Identity.ExecutionDeadline)); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(expected.Identity.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphaned staged token survived expiry")
	}
	if err := cleanupExpiredRegistry(t.Context(), root, expected.Identity.OperationID, time.UnixMilli(expected.Identity.ExecutionDeadline)); err != nil {
		t.Fatal("completed expiry was not idempotent")
	}
	expected.Identity.OperationID = uuid.NewString()
	if publishRegistry(t.Context(), root, expected, data, arm) != nil {
		t.Fatal("foreign material fixture")
	}
	directory, err = child(root, expected.Identity.OperationID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if directory.WriteFile("credential.json", []byte(`{"username":"foreign","token":"Y2hhbmdlZA=="}`), 0600) != nil {
		t.Fatal("foreign credential fixture")
	}
	if err := cleanupExpiredRegistry(t.Context(), root, expected.Identity.OperationID, time.UnixMilli(expected.Identity.ExecutionDeadline)); !errors.Is(err, ErrState) {
		t.Fatal("foreign credential was deleted by expiry")
	}
	if _, err := directory.Lstat("credential.json"); err != nil {
		t.Fatal("expiry removed mismatched credential")
	}
}
