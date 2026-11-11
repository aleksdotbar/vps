package hosthelper

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// HandleReadiness is read-only. Settled setup authority is necessary but not
// sufficient: the imported final generation must also have booted successfully.
func HandleReadiness(ctx context.Context, request ReadinessRequest, acquire FinalAcquirer) (response ReadinessResponse, returnErr error) {
	if !validReadinessRequest(request) {
		return response, ErrRequest
	}
	observed, err := probe(ProbeRequest{1, "probe", request.InstallationID, request.ServerID, request.Challenge})
	if err != nil {
		return response, err
	}
	response = readinessResponse(request, observed)
	if observed.Stage == "enrollment" {
		response.Status = "setup_pending"
		return response, nil
	}
	if acquire == nil {
		return response, nil
	}
	lease, err := acquire(ctx)
	if err != nil || lease.Release == nil {
		return response, nil
	}
	defer func() {
		if lease.Release() != nil {
			response = ReadinessResponse{}
			returnErr = ErrIdentity
		}
	}()
	if !readinessSetupMatches(request, observed, lease) {
		return response, nil
	}
	if observed.BootedSystem != lease.FinalSystem {
		response.Status = "setup_pending"
		return response, nil
	}
	if nativeReadiness(ctx, lease.FinalSystem) != nil {
		return response, nil
	}
	after, err := probe(ProbeRequest{1, "probe", request.InstallationID, request.ServerID, request.Challenge})
	if err != nil || after != observed || !readinessFinalMatches(request, after, lease) {
		return ReadinessResponse{}, ErrIdentity
	}
	response.Status = "ready"
	response.DockerVersion = DockerVersion
	response.ComposeVersion = ComposeVersion
	return response, nil
}

func readinessResponse(request ReadinessRequest, observed ProbeResponse) ReadinessResponse {
	return ReadinessResponse{SchemaVersion: 1, Action: "readiness", InstallationID: request.InstallationID, ServerID: request.ServerID, Challenge: request.Challenge, Role: observed.Role, Stage: observed.Stage, CurrentSystem: observed.CurrentSystem, BootedSystem: observed.BootedSystem, SystemProfile: observed.SystemProfile, Status: "needs_attention"}
}

func readinessFinalMatches(request ReadinessRequest, observed ProbeResponse, lease FinalLease) bool {
	return readinessSetupMatches(request, observed, lease) && observed.BootedSystem == lease.FinalSystem
}

func readinessSetupMatches(request ReadinessRequest, observed ProbeResponse, lease FinalLease) bool {
	return systemPath.MatchString(observed.BootedSystem) && lease.Release != nil && lease.InstallationID == request.InstallationID && lease.ServerID == request.ServerID && systemPath.MatchString(lease.FinalSystem) && observed.Stage == "final" && observed.CurrentSystem == lease.FinalSystem && observed.SystemProfile == lease.FinalSystem
}

type readinessDocker struct {
	Version         string   `json:"version"`
	OS              string   `json:"os"`
	Architecture    string   `json:"architecture"`
	CgroupVersion   string   `json:"cgroup_version"`
	CgroupDriver    string   `json:"cgroup_driver"`
	LoggingDriver   string   `json:"logging_driver"`
	LiveRestore     bool     `json:"live_restore"`
	MemoryLimit     bool     `json:"memory_limit"`
	CPUPeriod       bool     `json:"cpu_period"`
	CPUQuota        bool     `json:"cpu_quota"`
	PidsLimit       bool     `json:"pids_limit"`
	SecurityOptions []string `json:"security_options"`
}

const readinessInfoFormat = `{"version":{{json .ServerVersion}},"os":{{json .OSType}},"architecture":{{json .Architecture}},"cgroup_version":{{json .CgroupVersion}},"cgroup_driver":{{json .CgroupDriver}},"logging_driver":{{json .LoggingDriver}},"live_restore":{{json .LiveRestoreEnabled}},"memory_limit":{{json .MemoryLimit}},"cpu_period":{{json .CPUCfsPeriod}},"cpu_quota":{{json .CPUCfsQuota}},"pids_limit":{{json .PidsLimit}},"security_options":{{json .SecurityOptions}}}`

func supportedReadinessDocker(data []byte) bool {
	var info readinessDocker
	if len(data) > 4096 || json.Unmarshal(data, &info) != nil {
		return false
	}
	if info.Version != DockerVersion || info.OS != "linux" || info.Architecture != "x86_64" || info.CgroupVersion != "2" || info.CgroupDriver != "systemd" || info.LoggingDriver != "local" || !info.LiveRestore || !info.MemoryLimit || !info.CPUPeriod || !info.CPUQuota || !info.PidsLimit || len(info.SecurityOptions) > 16 {
		return false
	}
	if !slices.Contains(info.SecurityOptions, "name=seccomp,profile=builtin") {
		return false
	}
	for _, option := range info.SecurityOptions {
		if strings.HasPrefix(option, "name=rootless") {
			return false
		}
	}
	return true
}

func nativeReadiness(ctx context.Context, finalSystem string) error {
	tools := tunnelTools{systemctl: finalSystem + "/sw/bin/systemctl"}
	before, err := readReadinessUnits(ctx, tools)
	if err != nil || !boundReadinessUnits(before, finalSystem) {
		return ErrIdentity
	}
	info, err := os.Lstat("/run/docker.sock")
	if err != nil || info == nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0007 != 0 {
		return ErrIdentity
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return ErrIdentity
	}
	// Explicit Unix endpoint plus tunnelCommand's clean environment prevent
	// ambient Docker contexts, credentials or caller-controlled daemon endpoints.
	data, err := tunnelCommand(ctx, finalSystem+"/sw/bin/docker", nil, "--host=unix:///run/docker.sock", "info", "--format", readinessInfoFormat)
	if err != nil || !supportedReadinessDocker(data) {
		return ErrIdentity
	}
	// nixpkgs' Docker wrapper supplies the pinned plugin directory. It does not
	// put standalone docker-compose on the host PATH.
	data, err = tunnelCommand(ctx, finalSystem+"/sw/bin/docker", nil, "compose", "version", "--short")
	if err != nil || strings.TrimSpace(string(data)) != ComposeVersion {
		return ErrIdentity
	}
	after, err := readReadinessUnits(ctx, tools)
	if err != nil || after != before || !boundReadinessUnits(after, finalSystem) {
		return ErrIdentity
	}
	return nil
}

type readinessUnits struct{ docker, firewall tunnelUnit }

func readReadinessUnits(ctx context.Context, tools tunnelTools) (readinessUnits, error) {
	data, err := tunnelCommand(ctx, tools.systemctl, nil, "show", "--no-pager", "--property=Id,LoadState,ActiveState,SubState,MainPID,FragmentPath,DropInPaths,NeedDaemonReload", "--", "docker.service", "nftables.service")
	if err != nil {
		return readinessUnits{}, err
	}
	return parseReadinessUnits(data)
}

func parseReadinessUnits(data []byte) (readinessUnits, error) {
	result := readinessUnits{}
	seen := make(map[string]bool)
	for block := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n\n") {
		fields := make(map[string]string)
		for line := range strings.SplitSeq(block, "\n") {
			key, value, ok := strings.Cut(line, "=")
			_, exists := fields[key]
			if !ok || exists {
				return readinessUnits{}, ErrIdentity
			}
			fields[key] = value
		}
		id := fields["Id"]
		if seen[id] {
			return readinessUnits{}, ErrIdentity
		}
		seen[id] = true
		unit := tunnelUnit{fields["LoadState"], fields["ActiveState"], fields["SubState"], fields["MainPID"], fields["FragmentPath"], fields["DropInPaths"], fields["NeedDaemonReload"]}
		switch id {
		case "docker.service":
			result.docker = unit
		case "nftables.service":
			result.firewall = unit
		default:
			return readinessUnits{}, ErrIdentity
		}
	}
	if !seen["docker.service"] || !seen["nftables.service"] {
		return readinessUnits{}, ErrIdentity
	}
	return result, nil
}

func boundReadinessUnits(units readinessUnits, finalSystem string) bool {
	pid, err := strconv.ParseInt(units.docker.pid, 10, 32)
	return err == nil && pid > 0 && units.docker.active == "active" && units.docker.sub == "running" && units.firewall.active == "active" && units.firewall.sub == "exited" && boundReadinessUnit(units.docker, finalSystem, "docker.service") && boundReadinessUnit(units.firewall, finalSystem, "nftables.service")
}

func boundReadinessUnit(unit tunnelUnit, finalSystem, name string) bool {
	if unit.load != "loaded" || unit.reload != "no" || !tunnelSameFile(unit.fragment, finalSystem+"/etc/systemd/system/"+name) {
		return false
	}
	expected := finalSystem + "/etc/systemd/system/" + name + ".d/overrides.conf"
	_, err := os.Stat(expected)
	actual := strings.Fields(unit.dropins)
	if os.IsNotExist(err) {
		return len(actual) == 0
	}
	return err == nil && len(actual) == 1 && tunnelSameFile(actual[0], expected)
}
