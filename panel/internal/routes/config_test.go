package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
)

const (
	tunnel       = "11111111-1111-4111-8111-111111111111"
	environmentA = "22222222-2222-4222-8222-222222222222"
	environmentB = "33333333-3333-4333-8333-333333333333"
)

func routeFixture() ([]Contribution, []Origin) {
	return []Contribution{
		{EnvironmentID: environmentA, Services: []string{"app"}, Domains: []*panelv1.Domain{
			{Hostname: "app.example.com", Service: "app", Port: 3000},
			{Hostname: "www.example.com", Service: "app", Port: 3000},
		}},
		{EnvironmentID: environmentB, Services: []string{"worker"}, Domains: []*panelv1.Domain{
			{Hostname: "jobs.example.com", Service: "worker", Port: 8080},
		}},
	}, []Origin{
		{EnvironmentID: environmentA, Service: "app", Port: 3000, LoopbackPort: 23000},
		{EnvironmentID: environmentB, Service: "worker", Port: 8080, LoopbackPort: 23001},
	}
}

func TestCompileUsesOnlyOwnedOriginsAndRejectsAllUnmatchedHosts(t *testing.T) {
	contributions, origins := routeFixture()
	data, err := Compile(tunnel, contributions, origins)
	if err != nil {
		t.Fatal(err)
	}
	var config tunnelConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Tunnel != tunnel || config.CredentialsFile != CredentialsFile || len(config.Ingress) != 4 {
		t.Fatalf("incomplete native configuration: %s", data)
	}
	expected := map[string]string{"app.example.com": "http://127.0.0.1:23000", "www.example.com": "http://127.0.0.1:23000", "jobs.example.com": "http://127.0.0.1:23001"}
	for _, rule := range config.Ingress[:3] {
		if expected[rule.Hostname] != rule.Service {
			t.Fatalf("wrong origin for %s: %s", rule.Hostname, rule.Service)
		}
	}
	if config.Ingress[3] != (ingressRule{Service: "http_status:404"}) {
		t.Fatal("missing final rejecting catch-all")
	}
}

func TestCompileRefusesAmbiguousOwnershipAndUnassignedTargets(t *testing.T) {
	for _, name := range []string{"duplicate environment", "duplicate hostname within environment", "duplicate hostname across environments",
		"arbitrary URL", "missing service", "missing assignment", "foreign assignment", "duplicate assignment", "shared host port"} {
		t.Run(name, func(t *testing.T) {
			contributions, origins := routeFixture()
			expected := ErrOwnership
			switch name {
			case "duplicate environment":
				contributions = append(contributions, contributions[0])
			case "duplicate hostname within environment":
				contributions[0].Domains[1].Hostname = contributions[0].Domains[0].Hostname
			case "duplicate hostname across environments":
				contributions[1].Domains[0].Hostname = contributions[0].Domains[0].Hostname
			case "arbitrary URL":
				contributions[0].Domains[0].Hostname = "https://private.example.com/path"
				expected = ErrInvalid
			case "missing service":
				contributions[0].Domains[0].Service = "other"
				expected = ErrInvalid
			case "missing assignment":
				origins = origins[:1]
				expected = ErrOrigin
			case "foreign assignment":
				origins[0].EnvironmentID = environmentB
				expected = ErrOrigin
			case "duplicate assignment":
				origins[1] = origins[0]
			case "shared host port":
				origins[1].LoopbackPort = origins[0].LoopbackPort
			}
			data, err := Compile(tunnel, contributions, origins)
			if !errors.Is(err, expected) || data != nil {
				t.Fatalf("unsafe %s admitted: %v", name, err)
			}
		})
	}
}

func TestReplaceRecoversOnlyAffectedEnvironmentWithoutAliasingInputs(t *testing.T) {
	current, origins := routeFixture()
	replacement := Contribution{EnvironmentID: environmentA, Services: []string{"app"},
		Domains: []*panelv1.Domain{{Hostname: "restored.example.com", Service: "app", Port: 3000}}}
	updated, err := Replace(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if current[0].Domains[0].Hostname != "app.example.com" || updated[0].Domains[0].Hostname != "restored.example.com" ||
		updated[1].Domains[0].Hostname != "jobs.example.com" {
		t.Fatal("environment recovery changed unrelated route contribution")
	}
	if _, err := Compile(tunnel, updated, origins); err != nil {
		t.Fatal(err)
	}
	updated[1].Domains[0].Hostname = "different.example.com"
	updated[1].Services[0] = "different"
	if current[1].Domains[0].Hostname != "jobs.example.com" || current[1].Services[0] != "worker" {
		t.Fatal("returned contribution aliases current state")
	}
	replacement.Domains[0].Hostname = "jobs.example.com"
	if _, err := Replace(current, replacement); !errors.Is(err, ErrOwnership) {
		t.Fatalf("replacement stole another environment's hostname: %v", err)
	}
	removed, err := Replace(current, Contribution{EnvironmentID: environmentA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(tunnel, removed, origins[1:]); err != nil {
		t.Fatalf("empty route contribution must preserve other environment: %v", err)
	}
}

func TestCompileRejectsConfigLargerThanRemoteTransportAllowance(t *testing.T) {
	var contributions []Contribution
	var origins []Origin
	for environment := range 128 {
		id := uuid.NewString()
		contribution := Contribution{EnvironmentID: id, Services: []string{"app"}}
		for domain := range 32 {
			label := fmt.Sprintf("%03d-%02d-", environment, domain)
			hostname := label + strings.Repeat("a", 63-len(label)) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
			contribution.Domains = append(contribution.Domains, &panelv1.Domain{Hostname: hostname, Service: "app", Port: 3000})
		}
		contributions = append(contributions, contribution)
		origins = append(origins, Origin{EnvironmentID: id, Service: "app", Port: 3000, LoopbackPort: uint16(23000 + environment)})
	}
	if data, err := Compile(tunnel, contributions, origins); !errors.Is(err, ErrCapacity) || data != nil {
		t.Fatalf("oversized combined configuration admitted: %v", err)
	}
}

func TestPinnedCloudflaredAdmitsCombinedConfigAndSelectsExactHosts(t *testing.T) {
	command := os.Getenv("PANEL_TEST_CLOUDFLARED")
	if command == "" {
		t.Skip("PANEL_TEST_CLOUDFLARED must name the pinned cloudflared 2026.10.3 executable")
	}
	run := func(arguments ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		process := exec.CommandContext(ctx, command, arguments...) // #nosec G204,G702 -- explicit developer-selected pinned native test tool, fixed arguments; no shell or external input.
		process.Env = []string{"HOME=/nonexistent", "LANG=C", "LC_ALL=C"}
		process.WaitDelay = time.Second
		output, err := process.CombinedOutput()
		if err != nil {
			t.Fatalf("native config admission failed: %v; %s", err, output)
		}
		return output
	}
	if version := string(run("--version")); !strings.HasPrefix(version, "cloudflared version 2026.10.3 ") {
		t.Fatalf("unexpected native version: %s", version)
	}
	contributions, origins := routeFixture()
	data, err := Compile(tunnel, contributions, origins)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	run("tunnel", "--config", path, "ingress", "validate")
	for hostname, expected := range map[string]string{"app.example.com": "http://127.0.0.1:23000", "jobs.example.com": "http://127.0.0.1:23001", "unmanaged.example.com": "http_status:404"} {
		output := run("tunnel", "--config", path, "ingress", "rule", "https://"+hostname+"/fixture")
		if !strings.Contains(string(output), "service: "+expected) {
			t.Fatalf("wrong native rule for %s: %s", hostname, output)
		}
	}
}
