package auth_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/auth"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestExistingDatabaseNeverReceivesReplacementMasterKey(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	keyPath := filepath.Join(t.TempDir(), "keys", "master")
	if err := auth.Bootstrap(ctx, directory, keyPath, []byte("initial-private-password")); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, directory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	before, err := store.New(database).GetAdministrator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := auth.Bootstrap(ctx, directory, keyPath, []byte("replacement-private-password")); err == nil {
		t.Fatal("bootstrap accepted existing database with missing key")
	}
	if _, err := os.Lstat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap generated a replacement key: %v", err)
	}
	after, err := store.New(database).GetAdministrator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash != before.PasswordHash || after.Generation != before.Generation {
		t.Fatal("failed bootstrap changed administrator")
	}
}
