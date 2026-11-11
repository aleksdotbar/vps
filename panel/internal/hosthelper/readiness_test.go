package hosthelper

import (
	"encoding/json"
	"strings"
	"testing"
)

func readinessFixture() (ReadinessRequest, ReadinessResponse) {
	request := ReadinessRequest{1, "readiness", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", strings.Repeat("ab", 32)}
	path := "/nix/store/" + strings.Repeat("a", 32) + "-nixos-system-fixture"
	return request, ReadinessResponse{1, "readiness", request.InstallationID, request.ServerID, request.Challenge, "panel", "final", path, path, path, "ready", DockerVersion, ComposeVersion}
}

func TestReadinessEvidenceRequiresFreshFinalBootAndExactIdentity(t *testing.T) {
	request, response := readinessFixture()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseReadinessResponse(data, request); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ReadinessResponse){
		"challenge":    func(p *ReadinessResponse) { p.Challenge = strings.Repeat("cd", 32) },
		"installation": func(p *ReadinessResponse) { p.InstallationID = request.ServerID },
		"server":       func(p *ReadinessResponse) { p.ServerID = request.InstallationID },
		"enrollment":   func(p *ReadinessResponse) { p.Stage = "enrollment" },
		"old boot": func(p *ReadinessResponse) {
			p.BootedSystem = strings.Replace(p.CurrentSystem, "fixture", "previous", 1)
		},
		"profile":             func(p *ReadinessResponse) { p.SystemProfile = p.CurrentSystem + "-external" },
		"unsupported engine":  func(p *ReadinessResponse) { p.DockerVersion = "0.0.0" },
		"unsupported compose": func(p *ReadinessResponse) { p.ComposeVersion = "0.0.0" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := response
			change(&changed)
			encoded, _ := json.Marshal(changed)
			if _, err := ParseReadinessResponse(encoded, request); err == nil {
				t.Fatal("accepted mismatched readiness")
			}
		})
	}
	requestData, _ := json.Marshal(request)
	for _, data := range []string{strings.Replace(string(requestData), `"action":"readiness"`, `"action":"readiness","action":"probe"`, 1), strings.TrimSuffix(string(requestData), "}") + `,"command":"id"}`, string(requestData) + string(requestData), strings.Repeat(" ", maxRequestBytes+1)} {
		if _, err := ParseReadinessRequest([]byte(data)); err == nil {
			t.Fatal("accepted executable/ambiguous request")
		}
	}
	lease := FinalLease{request.InstallationID, request.ServerID, response.CurrentSystem, func() error { return nil }}
	probe := ProbeResponse{1, request.InstallationID, request.ServerID, request.Challenge, "panel", "final", response.CurrentSystem, response.BootedSystem, response.SystemProfile, false}
	if !readinessFinalMatches(request, probe, lease) {
		t.Fatal("settled booted final refused")
	}
	probe.BootedSystem += "-old"
	if !readinessSetupMatches(request, probe, lease) {
		t.Fatal("settled pre-reboot setup was not recognized")
	}
	if readinessFinalMatches(request, probe, lease) {
		t.Fatal("settled pre-reboot setup authorized deployment")
	}
}

func TestReadinessRejectsUnsupportedDaemonAndIncompleteNativeUnits(t *testing.T) {
	info := readinessDocker{DockerVersion, "linux", "x86_64", "2", "systemd", "local", true, true, true, true, true, []string{"name=seccomp,profile=builtin", "name=cgroupns"}}
	data, _ := json.Marshal(info)
	if !supportedReadinessDocker(data) {
		t.Fatal("admitted native daemon refused")
	}
	for name, change := range map[string]func(*readinessDocker){
		"memory":       func(p *readinessDocker) { p.MemoryLimit = false },
		"cpu":          func(p *readinessDocker) { p.CPUQuota = false },
		"pids":         func(p *readinessDocker) { p.PidsLimit = false },
		"security":     func(p *readinessDocker) { p.SecurityOptions = nil },
		"rootless":     func(p *readinessDocker) { p.SecurityOptions = append(p.SecurityOptions, "name=rootless") },
		"cgroups":      func(p *readinessDocker) { p.CgroupVersion = "1" },
		"logging":      func(p *readinessDocker) { p.LoggingDriver = "json-file" },
		"live restore": func(p *readinessDocker) { p.LiveRestore = false },
		"engine":       func(p *readinessDocker) { p.Version = "0.0.0" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := info
			changed.SecurityOptions = append([]string(nil), info.SecurityOptions...)
			change(&changed)
			data, _ := json.Marshal(changed)
			if supportedReadinessDocker(data) {
				t.Fatal("unsupported daemon admitted")
			}
		})
	}
	unit := "Id=docker.service\nLoadState=loaded\nActiveState=active\nSubState=running\nMainPID=100\nFragmentPath=/nix/store/fixture/docker.service\nDropInPaths=\nNeedDaemonReload=no\n\nId=nftables.service\nLoadState=loaded\nActiveState=active\nSubState=exited\nMainPID=0\nFragmentPath=/nix/store/fixture/nftables.service\nDropInPaths=\nNeedDaemonReload=no\n"
	if _, err := parseReadinessUnits([]byte(unit)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Split(unit, "\n\n")[0], unit + "\nId=docker.service\n", strings.Replace(unit, "Id=nftables.service", "Id=foreign.service", 1)} {
		if _, err := parseReadinessUnits([]byte(invalid)); err == nil {
			t.Fatal("incomplete or ambiguous native service observation admitted")
		}
	}
}

func TestPendingRebootRequiresSettledGenerationWithoutClaimingReadiness(t *testing.T) {
	request, response := readinessFixture()
	response.Status = "setup_pending"
	response.DockerVersion, response.ComposeVersion = "", ""
	response.BootedSystem += "-previous"
	for name, change := range map[string]func(*ReadinessResponse){
		"pending reboot":    func(*ReadinessResponse) {},
		"divergent profile": func(p *ReadinessResponse) { p.SystemProfile += "-other" },
		"already booted":    func(p *ReadinessResponse) { p.BootedSystem = p.CurrentSystem },
		"invalid boot":      func(p *ReadinessResponse) { p.BootedSystem = "unknown" },
		"claims runtime":    func(p *ReadinessResponse) { p.DockerVersion = DockerVersion },
	} {
		t.Run(name, func(t *testing.T) {
			value := response
			change(&value)
			data, _ := json.Marshal(value)
			_, err := ParseReadinessResponse(data, request)
			if (err == nil) != (name == "pending reboot") {
				t.Fatalf("pending readiness accepted incorrectly: %v", err)
			}
		})
	}
	lease := FinalLease{request.InstallationID, request.ServerID, response.CurrentSystem, func() error { return nil }}
	observed := ProbeResponse{1, request.InstallationID, request.ServerID, request.Challenge, "server", "final", response.CurrentSystem, response.BootedSystem, response.SystemProfile, false}
	if !readinessSetupMatches(request, observed, lease) || readinessFinalMatches(request, observed, lease) {
		t.Fatal("pending reboot did not preserve deployment refusal")
	}
	lease.InstallationID = request.ServerID
	if readinessSetupMatches(request, observed, lease) {
		t.Fatal("foreign setup authority admitted")
	}
}
