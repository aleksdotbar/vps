package hostsetup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupSnapshotRefusesPartialStateWithoutChangingIt(t *testing.T) {
	for _, name := range []string{"empty", "lock only", "orphan artifact", "orphan identity"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if name != "empty" {
				if err := os.WriteFile(filepath.Join(path, "setup.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "orphan artifact" {
				if err := os.Mkdir(filepath.Join(path, "artifact"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if name == "orphan identity" {
				if err := os.WriteFile(filepath.Join(path, "identity.json"), []byte("prior evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadDir(path)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			observed, err := observeSetupAt(t.Context(), root, "11111111-1111-4111-8111-111111111111", adoptionPins{}, importTools{})
			if name == "empty" || name == "lock only" {
				if err != nil || observed.ImportPhase != "not_started" || observed.Binding != nil {
					t.Fatalf("clean absence not observed: %v", err)
				}
			} else if !errors.Is(err, ErrUnresolved) {
				t.Fatalf("partial state adopted: %v", err)
			}
			after, readErr := os.ReadDir(path)
			if readErr != nil || len(before) != len(after) {
				t.Fatal("read-only snapshot changed state")
			}
		})
	}
}

func TestSetupSnapshotRechecksNativeImportWithoutRepairingIt(t *testing.T) {
	state, operation, pins, build, tools := nativeBuildFixture(t)
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	before, err := root.ReadFile("receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	observed, err := observeSetupAt(t.Context(), root, operation, pins, tools)
	if err != nil || observed.ImportPhase != "imported" || observed.Binding == nil || observed.Binding.InputSHA256 != build.InputSHA256 || observed.Enrollment != nil {
		t.Fatalf("verified import snapshot failed: %v", err)
	}
	link := filepath.Join(rootsDirectory(tools, build), "enrollment")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if _, err := observeSetupAt(t.Context(), root, operation, pins, tools); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("missing native root accepted: %v", err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only snapshot repaired native state")
	}
	after, err := root.ReadFile("receipt.json")
	if err != nil || string(before) != string(after) {
		t.Fatal("read-only snapshot rewrote receipt")
	}
}
