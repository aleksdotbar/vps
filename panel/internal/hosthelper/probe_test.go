package hosthelper

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func probeFixture() ProbeRequest {
	return ProbeRequest{1, "probe", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", strings.Repeat("ab", 32)}
}

func TestProbeRejectsAmbiguousOrExecutableRequests(t *testing.T) {
	request := probeFixture()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(encoded)
	if got, err := parseRequest(append(encoded, '\n')); err != nil || got != request {
		t.Fatalf("canonical probe rejected: %v", err)
	}
	for name, data := range map[string]string{
		"command":         strings.Replace(valid, `"probe"`, `"sh"`, 1),
		"extra argument":  strings.TrimSuffix(valid, "}") + `,"command":"id"}`,
		"duplicate":       strings.Replace(valid, `"action":"probe"`, `"action":"sh","action":"probe"`, 1),
		"wrong schema":    strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"missing server":  strings.Replace(valid, request.ServerID, "", 1),
		"nil identity":    strings.Replace(valid, request.InstallationID, "00000000-0000-0000-0000-000000000000", 1),
		"short challenge": strings.Replace(valid, request.Challenge, "ab", 1),
		"second object":   valid + valid,
		"oversized":       valid + strings.Repeat(" ", maxRequestBytes),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRequest([]byte(data)); !errors.Is(err, ErrRequest) {
				t.Fatalf("accepted unsafe request %s: %v", name, err)
			}
		})
	}
}

func TestProbeBindsRetainedAndRunningIdentityWithoutClaimingReadiness(t *testing.T) {
	request := probeFixture()
	retained := identity{request.InstallationID, request.ServerID, "server"}
	installed := metadata{retained, 1, "enrollment"}
	observed := systems{"current", "previous-boot", "current"}
	response, err := bindResponse(request, retained, installed, observed)
	if err != nil || response.Ready || response.BootedSystem != observed.booted || response.Challenge != request.Challenge {
		t.Fatalf("probe misreported observation or readiness: %+v %v", response, err)
	}
	for _, stage := range []string{"enrollment", "final"} {
		installed.Stage = stage
		response, err = bindResponse(request, retained, installed, observed)
		if err != nil || response.Ready {
			t.Fatalf("stage %s must not establish readiness: %+v %v", stage, response, err)
		}
	}
	for name, change := range map[string]func(*metadata){
		"other installation": func(value *metadata) { value.InstallationID = request.ServerID },
		"other server":       func(value *metadata) { value.ServerID = request.InstallationID },
		"other role":         func(value *metadata) { value.Role = "panel" },
		"invalid stage":      func(value *metadata) { value.Stage = "ready" },
		"invalid schema":     func(value *metadata) { value.SchemaVersion = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := installed
			change(&changed)
			if _, err := bindResponse(request, retained, changed, observed); !errors.Is(err, ErrIdentity) {
				t.Fatalf("accepted mismatched installed authority %s: %v", name, err)
			}
		})
	}
	request.ServerID = request.InstallationID
	if _, err := bindResponse(request, retained, installed, observed); !errors.Is(err, ErrIdentity) {
		t.Fatal("accepted a request for a different retained server")
	}
}

func TestHelperRejectsNonManagementSudoCaller(t *testing.T) {
	t.Setenv("SUDO_USER", "operator")
	if err := authorize(); !errors.Is(err, ErrPrivilege) {
		t.Fatalf("ordinary administrator admitted to management protocol: %v", err)
	}
}

func TestInheritedBlockingPipeCannotKeepHelperReadingPastDeadline(t *testing.T) {
	// Pipe2 without O_NONBLOCK plus NewFile reproduces inherited SSH stdin;
	// os.Pipe would already be pollable and hide this integration failure.
	var descriptors [2]int
	if err := unix.Pipe2(descriptors[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	input := os.NewFile(uintptr(descriptors[0]), "inherited-stdin")
	writer := os.NewFile(uintptr(descriptors[1]), "peer-writer")
	if input == nil || writer == nil {
		t.Fatal("native pipe descriptors unavailable")
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = writer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	wrapped, err := pollableFile(input, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wrapped.Close() }()
	completed := make(chan error, 1)
	go func() {
		var data [1]byte
		_, err := wrapped.Read(data[:])
		completed <- err
	}()
	select {
	case err := <-completed:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("held-open peer returned %v instead of timing out", err)
		}
	case <-time.After(3 * time.Second):
		// Release the actual writer before failure so a broken implementation
		// cannot leave the test process blocked in a native read.
		_ = writer.Close()
		t.Fatal("inherited SSH pipe exceeded the helper deadline")
	}
}
