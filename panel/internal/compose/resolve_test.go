package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/releases"
)

func TestSubprocessOutputCannotBypassBufferLimit(t *testing.T) {
	for _, size := range []int{1024, 1025} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			process := exec.CommandContext(ctx, "/usr/bin/head", "-c", strconv.Itoa(size), "/dev/zero") // #nosec G204 -- fixed local fixture producing bounded data, no shell.
			output := boundedBuffer{limit: 1024}
			process.Stdout = &output
			process.Stderr = io.Discard
			process.WaitDelay = time.Second
			err := process.Run()
			if size <= output.limit {
				if err != nil || output.Len() != size {
					t.Fatalf("bounded output rejected: size=%d error=%v", output.Len(), err)
				}
			} else if err == nil || output.Len() > output.limit {
				t.Fatalf("subprocess output escaped its limit: size=%d error=%v", output.Len(), err)
			}
		})
	}
}

func fixture(t *testing.T) ([]byte, []byte, releases.Artifact) {
	t.Helper()
	base, err := os.ReadFile("../../../examples/stateless/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	override, err := os.ReadFile("../../../examples/stateless/compose.staging.yaml")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile("../../../examples/stateless/release.json")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := releases.ParseArtifact(metadata, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	return base, override, artifact
}
func native(t *testing.T) *Resolver {
	t.Helper()
	command := os.Getenv("PANEL_TEST_COMPOSE")
	if command == "" {
		command = "docker-compose"
	}
	resolver, err := New(command)
	if err != nil {
		t.Fatalf("pinned native Compose5.4 required: %v", err)
	}
	return resolver
}
func source(t *testing.T, base, override []byte) string {
	t.Helper()
	directory := t.TempDir()
	for name, data := range map[string][]byte{"compose.yaml": base, "compose.staging.yaml": override} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}
func TestNativeResolutionPreservesSecretIsolationAndSupportedEmptyValues(t *testing.T) {
	base, override, artifact := fixture(t)
	resolver := native(t)
	for _, test := range []struct {
		name     string
		base     []byte
		secrets  map[string]bool
		bindings int
	}{
		{"shared plus override", base, map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}, 2},
		{"empty direct secret", bytes.ReplaceAll(base, []byte("${API_TOKEN:?set in the panel}"), []byte("${API_TOKEN}")), map[string]bool{"API_TOKEN": false, "WORKER_TOKEN": true}, 2},
		{"empty question secret", bytes.ReplaceAll(base, []byte("${API_TOKEN:?set in the panel}"), []byte("${API_TOKEN?set in the panel}")), map[string]bool{"API_TOKEN": false, "WORKER_TOKEN": true}, 2},
		{"native runtime defaults", []byte(strings.ReplaceAll(strings.ReplaceAll(string(base), "      start_period: 10s\n", ""), "    logging:\n      driver: json-file\n      options:\n        max-size: 10m\n        max-file: \"3\"\n", "")), map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}, 2},
		{"no environment", []byte(strings.ReplaceAll(strings.ReplaceAll(string(base), "    environment:\n      API_TOKEN: ${API_TOKEN:?set in the panel}\n      LOG_LEVEL: info\n", ""), "    environment:\n      WORKER_TOKEN: ${WORKER_TOKEN:?set in the panel}\n      LOG_LEVEL: info\n", "")), map[string]bool{}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			selectedOverride := override
			if test.bindings == 0 {
				selectedOverride = []byte("services:\n  app: {}\n  worker: {}\n")
			}
			prepared, err := resolver.Resolve(context.Background(), source(t, test.base, selectedOverride), "compose.staging.yaml", artifact, test.secrets, Network{ProjectName: "vps-00000000000000000000000000000001", BridgeName: "vps-0000000001"})
			if err != nil {
				t.Fatal(err)
			}
			if len(prepared.Services) != 2 || len(prepared.Bindings) != test.bindings || bytes.Contains(prepared.Config, []byte("PANEL_SECRET_")) {
				t.Fatal("wrong service/secret-reference snapshot")
			}
			var snapshot struct {
				Services map[string]struct {
					Healthcheck struct {
						StartInterval string `json:"start_interval"`
						StartPeriod   string `json:"start_period"`
					} `json:"healthcheck"`
					Logging struct {
						Driver string `json:"driver"`
					} `json:"logging"`
				} `json:"services"`
			}
			if err := json.Unmarshal(prepared.Config, &snapshot); err != nil {
				t.Fatal(err)
			}
			for _, service := range snapshot.Services {
				if service.Healthcheck.StartInterval != "5s" || service.Healthcheck.StartPeriod != "10s" {
					t.Fatal("startup health timings can inherit image metadata")
				}
				if test.name == "native runtime defaults" && service.Logging.Driver != "local" {
					t.Fatal("missing bounded default log driver")
				}
			}
			for _, binding := range prepared.Bindings {
				if (binding.Service == "app" && binding.Secret != "API_TOKEN") || (binding.Service == "worker" && binding.Secret != "WORKER_TOKEN") {
					t.Fatal("secret crossed service boundary")
				}
			}
		})
	}
}
func TestUnsafeSourceIsRejectedBeforeNativeResolution(t *testing.T) {
	base, override, artifact := fixture(t)
	resolver := native(t)
	for _, test := range []struct {
		name    string
		base    []byte
		secrets map[string]bool
	}{
		{"host socket bind", bytes.Replace(base, []byte("    user:"), []byte("    volumes: [/var/run/docker.sock:/var/run/docker.sock]\n    user:"), 1), map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}},
		{"recursive include", append([]byte("include: [/etc/passwd]\n"), base...), map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}},
		{"env file", bytes.Replace(base, []byte("    user:"), []byte("    env_file: /etc/passwd\n    user:"), 1), map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}},
		{"secret in command", bytes.Replace(base, []byte("    user:"), []byte("    command: echo ${API_TOKEN}\n    user:"), 1), map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}},
		{"reserved control input", bytes.ReplaceAll(base, []byte("${API_TOKEN:?set in the panel}"), []byte("${HOME}")), map[string]bool{"HOME": true, "WORKER_TOKEN": true}},
		{"null environment inheritance", bytes.ReplaceAll(base, []byte("API_TOKEN: ${API_TOKEN:?set in the panel}"), []byte("HOME:")), map[string]bool{"WORKER_TOKEN": true}},
		{"missing declared secret", base, map[string]bool{"WORKER_TOKEN": true}},
		{"empty required secret", base, map[string]bool{"API_TOKEN": false, "WORKER_TOKEN": true}},
		{"rapid prolonged startup healthchecks", bytes.ReplaceAll(base, []byte("      start_period: 10s"), []byte("      start_period: 24h\n      start_interval: 1ms")), map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}},
		{"YAML alias", append([]byte("x-base: &base {}\n"), base...), map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolver.Resolve(context.Background(), source(t, test.base, override), "compose.staging.yaml", artifact, test.secrets, Network{ProjectName: "vps-00000000000000000000000000000001", BridgeName: "vps-0000000001"})
			if !errors.Is(err, ErrUnsafe) && !errors.Is(err, ErrMissingSecret) {
				t.Fatalf("unsafe source accepted: %v", err)
			}
		})
	}
}
func TestSourcePathsAndNonregularFilesAreBounded(t *testing.T) {
	base, override, artifact := fixture(t)
	resolver := native(t)
	directory := source(t, base, override)
	for _, path := range []string{"../compose.yaml", "/etc/passwd", "folder/../../compose.staging.yaml"} {
		if _, err := resolver.Resolve(context.Background(), directory, path, artifact, nil, Network{ProjectName: "vps-00000000000000000000000000000001", BridgeName: "vps-0000000001"}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("unsafe path accepted: %q %v", path, err)
		}
	}
	if err := os.Remove(filepath.Join(directory, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(directory, "compose.yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), directory, "compose.staging.yaml", artifact, nil, Network{ProjectName: "vps-00000000000000000000000000000001", BridgeName: "vps-0000000001"}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("FIFO input accepted: %v", err)
	}
}

func TestResolvedServiceNetworkOverridesAreRejected(t *testing.T) {
	base, override, artifact := fixture(t)
	network := Network{ProjectName: "vps-00000000000000000000000000000001", BridgeName: "vps-0000000001"}
	prepared, err := native(t).Resolve(context.Background(), source(t, base, override), "compose.staging.yaml", artifact, map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": true}, network)
	if err != nil {
		t.Fatal(err)
	}
	var model map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Config, &model); err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("missing native model")
	}
	var services map[string]map[string]json.RawMessage
	if err := json.Unmarshal(model["services"], &services); err != nil {
		t.Fatal(err)
	}
	if services == nil {
		t.Fatal("missing native services")
	}
	app := services["app"]
	if app == nil {
		t.Fatal("missing native app service")
	}
	app["networks"] = json.RawMessage(`{"external":null}`)
	model["services"], err = json.Marshal(services)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admitResolved(data, artifact, nil, network); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unmanaged service network accepted: %v", err)
	}
}
