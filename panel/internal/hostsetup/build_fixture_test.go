package hostsetup

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
)

// buildFixture returns the canonical reviewed input and a matching build record.
func buildFixture(t *testing.T) (string, localBuild, []byte) {
	t.Helper()
	data, err := os.ReadFile("../foundation/testdata/input.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := foundation.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := foundation.Canonical(input)
	if err != nil {
		t.Fatal(err)
	}
	_, machineSHA, err := machineCanonical(input)
	if err != nil {
		t.Fatal(err)
	}
	sourceSHA := strings.Repeat("a", 40)
	build := localBuild{InstallationID: input.InstallationID, ServerID: input.ServerID, Role: input.Role, SourceSHA: sourceSHA,
		InputSHA256: sha256Text(canonical), MachineSHA256: machineSHA,
		PanelStorePath:   "/nix/store/" + strings.Repeat("1", 32) + "-panel-" + sourceSHA,
		EnrollmentSystem: "/nix/store/" + strings.Repeat("2", 32) + "-nixos-system-fixture-enrollment",
		FinalSystem:      "/nix/store/" + strings.Repeat("3", 32) + "-nixos-system-fixture-final"}
	return uuid.NewString(), build, canonical
}

func nativeTool(t *testing.T, environment, name string) string {
	t.Helper()
	path := os.Getenv(environment)
	if path == "" {
		path = name
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		t.Fatalf("native setup fixture requires %s or %s on PATH", environment, name)
	}
	return resolved
}

func fixtureCommand(t *testing.T, tools importTools, input []byte, arguments ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	process := nativeCommand(ctx, tools.nixStore, append([]string{"--store", tools.store}, arguments...)...)
	process.Stdin = bytes.NewReader(input)
	var output importOutput
	process.Stdout = &output
	if err := process.Run(); err != nil {
		t.Fatalf("isolated native fixture %s: %v", arguments[0], err)
	}
	return output.data.Bytes()
}

// nativeBuildFixture registers the build's roots in an isolated store and records
// the intent, receipt and identity a completed Build stage leaves behind.
func nativeBuildFixture(t *testing.T) (string, string, adoptionPins, localBuild, importTools) {
	t.Helper()
	operation, build, input := buildFixture(t)
	nixStore := nativeTool(t, "PANEL_TEST_NIX_STORE", "nix-store")
	storeRoot := filepath.Join(t.TempDir(), "store")
	writableFixtureCleanup(t, storeRoot)
	tools := importTools{nixStore: nixStore, store: "local?root=" + storeRoot}
	fixtureCommand(t, tools, nil, "--init")
	var registration strings.Builder
	for _, path := range []string{build.PanelStorePath, build.EnrollmentSystem, build.FinalSystem} {
		directory := storeRoot + path
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		registration.WriteString(path + "\n\n")
		if path == build.PanelStorePath {
			if err := os.Mkdir(filepath.Join(directory, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "bin/panel"), []byte("fixture product binary; never executed"), 0700); err != nil {
				t.Fatal(err)
			}
			registration.WriteString("0\n")
		} else {
			registration.WriteString("1\n" + build.PanelStorePath + "\n")
			if err := os.WriteFile(filepath.Join(directory, "reference"), []byte(build.PanelStorePath), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixtureCommand(t, tools, []byte(registration.String()), "--register-validity")
	if err := os.MkdirAll(storeRoot+"/nix/var/nix/gcroots", 0755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	lock, err := stateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	reviewed, err := checkBuildInput(build, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBuildInput(t.Context(), root, reviewed, input); err != nil {
		t.Fatal(err)
	}
	if observed, err := observeRootedImport(t.Context(), tools, build); err != nil || !observed {
		t.Fatalf("fixture build not rooted: %v", err)
	}
	pins, err := writeBuildIntent(t.Context(), root, operation, build)
	if err != nil {
		t.Fatal(err)
	}
	if writeState(t.Context(), root, "receipt.json", importReceipt{operation, "imported"}, false) != nil {
		t.Fatal("fixture receipt")
	}
	return state, operation, pins, build, tools
}

func writableFixtureCleanup(t *testing.T, path string) {
	t.Helper()
	// Native Nix makes store objects read-only. Restore only directory write
	// permission inside this test-owned isolated store so TempDir can remove it.
	t.Cleanup(func() {
		root, err := os.OpenRoot(path)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = root.Close() }()
		err = filepath.WalkDir(path, func(child string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				relative, err := filepath.Rel(path, child)
				if err != nil {
					return err
				}
				// #nosec G302 -- Only directories in this test-owned temporary store receive directory permissions.
				return root.Chmod(relative, 0700)
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	})
}
