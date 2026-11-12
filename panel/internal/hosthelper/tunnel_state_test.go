package hosthelper

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/routes"
)

func tunnelStateFixture(t *testing.T) (*os.Root, *os.Root, tunnelBinding) {
	t.Helper()
	parent := t.TempDir()
	for name, mode := range map[string]os.FileMode{"private": 0700, "runtime": 0755} {
		if err := os.Mkdir(filepath.Join(parent, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	private, err := openTunnelDirectory(filepath.Join(parent, "private"), 0700)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := openTunnelDirectory(filepath.Join(parent, "runtime"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = private.Close(); _ = runtime.Close() })
	return private, runtime, bindTunnel(tunnelRequestFixture())
}

func TestTunnelSecretPreparationReusesExactIntentAndRefusesForeignState(t *testing.T) {
	private, runtime, binding := tunnelStateFixture(t)
	first, err := prepareTunnelIntent(t.Context(), private, runtime, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(first.Secret)
	second, err := prepareTunnelIntent(t.Context(), private, runtime, binding)
	defer clear(second.Secret)
	if err != nil || !bytes.Equal(first.Secret, second.Secret) || len(first.Secret) != 32 {
		t.Fatal("retry replaced its retained secret")
	}
	for _, change := range []func(*tunnelBinding){func(value *tunnelBinding) { value.OperationID = value.ServerID }, func(value *tunnelBinding) { value.AccountID = "cccccccccccccccccccccccccccccccc" }} {
		foreign := binding
		change(&foreign)
		actual, err := prepareTunnelIntent(t.Context(), private, runtime, foreign)
		clear(actual.Secret)
		if !errors.Is(err, ErrTunnelState) {
			t.Fatal("foreign operation adopted the retained secret")
		}
	}
	if err := private.Chmod("intent.json", 0644); err != nil {
		t.Fatal(err)
	}
	actual, err := prepareTunnelIntent(t.Context(), private, runtime, binding)
	clear(actual.Secret)
	if !errors.Is(err, ErrTunnelState) {
		t.Fatal("publicly readable secret accepted")
	}
}

func TestTunnelPreparationDoesNotAdoptExistingRuntimeOrSymlink(t *testing.T) {
	for _, test := range []string{"runtime", "symlink", "unrelated private state"} {
		t.Run(test, func(t *testing.T) {
			private, runtime, binding := tunnelStateFixture(t)
			var err error
			switch test {
			case "runtime":
				err = runtime.WriteFile("credentials.json", []byte("foreign"), 0600)
			case "symlink":
				err = private.Symlink("final.json", "intent.json")
			case "unrelated private state":
				err = private.WriteFile("foreign", nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			intent, err := prepareTunnelIntent(t.Context(), private, runtime, binding)
			clear(intent.Secret)
			if !errors.Is(err, ErrTunnelState) {
				t.Fatal("foreign or ambiguous state adopted")
			}
		})
	}
}

func TestTunnelPublicationRecoversPartialFinalizationWithoutReplacement(t *testing.T) {
	private, runtime, binding := tunnelStateFixture(t)
	intent, err := prepareTunnelIntent(t.Context(), private, runtime, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(intent.Secret)
	final := tunnelFinal{binding, "44444444-4444-4444-8444-444444444444"}
	data, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishTunnelFile(t.Context(), private, "final.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	credential, err := json.Marshal(tunnelCredentials{binding.AccountID, intent.Secret, final.TunnelID})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(credential)
	if err := publishTunnelFile(t.Context(), runtime, "credentials.json", credential, 0600); err != nil {
		t.Fatal(err)
	}
	// An interrupted helper may have published credentials but never config.
	retained, err := readTunnelFinal(private, binding)
	if err != nil || retained != final {
		t.Fatal("final identity did not survive partial publication")
	}
	configuration, err := routes.Compile(final.TunnelID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishTunnelFile(t.Context(), runtime, "credentials.json", credential, 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishTunnelFile(t.Context(), runtime, "config.json", configuration, 0644); err != nil {
		t.Fatal(err)
	}
	if err := publishTunnelFile(t.Context(), runtime, "config.json", []byte("foreign"), 0644); !errors.Is(err, ErrTunnelState) {
		t.Fatal("conflicting config overwritten")
	}
	actual, err := tunnelRead(runtime, "config.json", 0644)
	if err != nil || !bytes.Equal(actual, configuration) {
		t.Fatal("bound config changed")
	}
}

func TestTunnelValidationFailureLeavesConnectorUnpublished(t *testing.T) {
	private, runtime, binding := tunnelStateFixture(t)
	intent, err := prepareTunnelIntent(t.Context(), private, runtime, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(intent.Secret)
	final := tunnelFinal{binding, "44444444-4444-4444-8444-444444444444"}
	configuration, err := routes.Compile(final.TunnelID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(tunnelCredentials{binding.AccountID, intent.Secret, final.TunnelID})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(credentials)
	err = finalizeTunnelFiles(t.Context(), private, runtime, final, credentials, configuration, tunnelTools{cloudflared: "/does-not-exist"})
	if err == nil {
		t.Fatal("failed validator allowed publication")
	}
	if tunnelDirectoryEntries(private, "intent.json") != nil || tunnelDirectoryEntries(runtime) != nil {
		t.Fatal("validation failure published final authority or startable runtime")
	}
}

func TestPinnedTunnelInterruptedFinalizationResumesOnlyBoundFiles(t *testing.T) {
	command := os.Getenv("PANEL_TEST_CLOUDFLARED")
	if command == "" {
		t.Skip("PANEL_TEST_CLOUDFLARED must name pinned cloudflared2026.10.3")
	}
	version, err := tunnelCommand(t.Context(), command, nil, "--version")
	if err != nil || !bytes.HasPrefix(version, []byte("cloudflared version 2026.10.3 ")) {
		t.Fatal("wrong native validation tool")
	}
	for _, stage := range []string{"final receipt", "credentials published", "complete before service start", "missing credentials with foreign config"} {
		t.Run(stage, func(t *testing.T) {
			private, runtime, binding := tunnelStateFixture(t)
			intent, err := prepareTunnelIntent(t.Context(), private, runtime, binding)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(intent.Secret)
			final := tunnelFinal{binding, "44444444-4444-4444-8444-444444444444"}
			receipt, err := json.Marshal(final)
			if err != nil {
				t.Fatal(err)
			}
			if err := publishTunnelFile(t.Context(), private, "final.json", receipt, 0600); err != nil {
				t.Fatal(err)
			}
			configuration, err := routes.Compile(final.TunnelID, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			credentials, err := json.Marshal(tunnelCredentials{binding.AccountID, intent.Secret, final.TunnelID})
			if err != nil {
				t.Fatal(err)
			}
			defer clear(credentials)
			if stage == "credentials published" || stage == "complete before service start" {
				if err := publishTunnelFile(t.Context(), runtime, "credentials.json", credentials, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "complete before service start" {
				if err := publishTunnelFile(t.Context(), runtime, "config.json", configuration, 0644); err != nil {
					t.Fatal(err)
				}
			}
			tools := tunnelTools{cloudflared: command}
			if stage == "missing credentials with foreign config" {
				if err := runtime.WriteFile("config.json", []byte("foreign"), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := observeTunnelRuntime(t.Context(), tools, "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-nixos-system-fixture", runtime, credentials, configuration); !errors.Is(err, ErrTunnelState) {
					t.Fatal("missing credentials hid foreign config")
				}
				if err := finalizeTunnelFiles(t.Context(), private, runtime, final, credentials, configuration, tools); !errors.Is(err, ErrTunnelState) {
					t.Fatal("replay adopted foreign config")
				}
				return
			}
			if stage != "complete before service start" {
				ready, err := observeTunnelRuntime(t.Context(), tools, "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-nixos-system-fixture", runtime, credentials, configuration)
				if err != nil || ready {
					t.Fatal("own incomplete publication did not remain recoverable finalizing")
				}
			}
			for range 2 {
				if err := finalizeTunnelFiles(t.Context(), private, runtime, final, credentials, configuration, tools); err != nil {
					t.Fatal(err)
				}
			}
			complete, err := inspectTunnelRuntimeFiles(runtime, credentials, configuration)
			if err != nil || !complete {
				t.Fatal("exact native-validated replay did not complete runtime files")
			}
			retained, err := readTunnelFinal(private, binding)
			if err != nil || retained != final {
				t.Fatal("replay changed bound Tunnel identity")
			}
			after, err := readTunnelIntent(private, binding)
			defer clear(after.Secret)
			if err != nil || !bytes.Equal(after.Secret, intent.Secret) {
				t.Fatal("replay replaced retained secret")
			}
		})
	}
}
