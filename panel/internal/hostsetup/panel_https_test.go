package hostsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/installation"
)

func TestPanelHTTPSProofBindsEnrollmentIdentity(t *testing.T) {
	operation, build, _ := buildFixture(t)
	imported := importIntent{OperationID: operation, Build: build}
	enrolled := enrollmentIntent{ManagementFingerprint: "SHA256:fixture"}
	proof := installation.Result{Status: "private_proven", OperationID: imported.OperationID, InstallationID: build.InstallationID, ServerID: build.ServerID, InputSHA256: build.InputSHA256, ManagementFingerprint: enrolled.ManagementFingerprint, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	if !panelProofMatches(proof, imported, enrolled) {
		t.Fatal("exact private proof rejected")
	}
	for _, test := range []struct {
		name   string
		change func(*installation.Result)
	}{
		{"operation", func(p *installation.Result) { p.OperationID = "other" }},
		{"installation", func(p *installation.Result) { p.InstallationID = "other" }},
		{"server", func(p *installation.Result) { p.ServerID = "other" }},
		{"input", func(p *installation.Result) { p.InputSHA256 = "other" }},
		{"management key", func(p *installation.Result) { p.ManagementFingerprint = "other" }},
		{"registered only", func(p *installation.Result) { p.Status = "registered" }},
		{"readiness", func(p *installation.Result) { p.Ready = true }},
		{"missing observation", func(p *installation.Result) { p.ObservedAt = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := proof
			test.change(&changed)
			if panelProofMatches(changed, imported, enrolled) {
				t.Fatal("unbound private proof accepted")
			}
		})
	}
}

func TestPanelOriginPublicationRefusesForeignOrInterruptedState(t *testing.T) {
	const origin = "https://panel.fixture.ts.net"
	for _, name := range []string{"complete replay", "interrupted unpublished", "missing intent", "wrong origin", "changed file", "symlink", "extra content", "cancelled", "unsafe directory"} {
		t.Run(name, func(t *testing.T) {
			setupPath, parentPath := filepath.Join(t.TempDir(), "setup"), filepath.Join(t.TempDir(), "parent")
			for _, path := range []string{setupPath, parentPath} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			setup, err := os.OpenRoot(setupPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = setup.Close() }()
			parent, err := os.OpenRoot(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			bound := origin
			if name == "wrong origin" {
				bound = "https://other.fixture.ts.net"
			}
			if name != "missing intent" {
				if err := writeState(context.Background(), setup, "panel-https.json", panelHTTPSIntent{Origin: bound}, false); err != nil {
					t.Fatal(err)
				}
			}
			directory := filepath.Join(parentPath, "panel-config")
			if name == "interrupted unpublished" {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				// Simulate death before publication: incomplete anonymous data must
				// leave no named state that prevents the bound operation's retry.
				file, err := os.OpenFile(directory, os.O_RDWR|unix.O_TMPFILE, 0600) // #nosec G304 -- Anonymous file is created only inside this test's private temporary directory.
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("incomplete")); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if name == "changed file" || name == "extra content" || name == "unsafe directory" || name == "symlink" {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				switch name {
				case "changed file":
					if err := os.WriteFile(filepath.Join(directory, "serve.env"), []byte("PANEL_ORIGIN=https://other.fixture.ts.net\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "extra content":
					if err := os.WriteFile(filepath.Join(directory, "foreign"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				case "unsafe directory":
					if err := os.Chmod(directory, 0755); err != nil {
						t.Fatal(err)
					} // #nosec G302 -- Public fixture's unsafe mode is the refusal test subject.
				case "symlink":
					if err := os.Symlink("outside", filepath.Join(directory, "serve.env")); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancelled" {
				cancel()
			}
			err = publishPanelOriginAt(ctx, setup, parent, origin)
			if name != "complete replay" && name != "interrupted unpublished" {
				if err == nil {
					t.Fatal("unsafe publication admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := publishPanelOriginAt(ctx, setup, parent, origin); err != nil {
				t.Fatalf("exact replay refused: %v", err)
			}
			actual, err := os.ReadFile(filepath.Join(directory, "serve.env")) // #nosec G304 -- Reads only this test's private temporary publication output.
			if err != nil || string(actual) != "PANEL_ORIGIN="+origin+"\n" {
				t.Fatal("wrong published origin")
			}
			if err := publishPanelOriginAt(ctx, setup, parent, "https://other.fixture.ts.net"); !errors.Is(err, ErrPanelHTTPS) {
				t.Fatal("origin mutation admitted")
			}
		})
	}
}
