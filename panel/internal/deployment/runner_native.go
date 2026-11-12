package deployment

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// system is the generation whose tools serve one operation; it may be newer than
// the generation that staged the release.
type runnerTools struct{ docker, systemctl, systemdRun, panel, env, system string }

func systemTools(system string) runnerTools {
	bin := system + "/sw/bin/"
	return runnerTools{bin + "docker", bin + "systemctl", bin + "systemd-run", bin + "panel", bin + "env", system}
}

type limitedOutput struct {
	data    bytes.Buffer
	maximum int
	cancel  context.CancelFunc
}

func (output *limitedOutput) Write(data []byte) (int, error) {
	if len(data) > output.maximum-output.data.Len() {
		output.cancel()
		return 0, ErrState
	}
	return output.data.Write(data)
}

func runnerCommand(ctx context.Context, executable string, arguments []string, input []byte, environment []string, maximum int) ([]byte, error) {
	bounded, cancel := context.WithCancel(ctx)
	defer cancel()
	// #nosec G204,G702 -- Private production callers choose only verified final-generation native tools and fixed argument arrays; no shell or caller-selected executable.
	command := exec.CommandContext(bounded, executable, arguments...)
	command.Dir = "/"
	command.Env = append([]string{"HOME=/var/empty", "PATH=/nonexistent", "LANG=C", "LC_ALL=C", "GOMEMLIMIT=256MiB"}, environment...)
	command.Stdin = bytes.NewReader(input)
	command.Stderr = io.Discard
	output := limitedOutput{maximum: maximum, cancel: cancel}
	command.Stdout = &output
	command.WaitDelay = time.Second
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if command.Run() != nil {
		clear(output.data.Bytes())
		return nil, ErrState
	}
	return output.data.Bytes(), nil
}

func runnerUnitName(value execution, recovery bool) string {
	if recovery {
		return "panel-deploy-recover-" + value.Claim.Identity.OperationID
	}
	return "panel-deploy-" + value.Claim.Identity.OperationID
}
func runnerDescription(value execution, recovery bool) string {
	kind := "execution"
	if recovery {
		kind = "recovery"
	}
	return "Panel deployment " + kind + " " + value.Claim.Identity.OperationID + " " + value.LaunchID
}

func runnerArguments(value execution, tools runnerTools, recovery bool, now time.Time) ([]string, error) {
	deadline := time.UnixMilli(value.Claim.Identity.ExecutionDeadline)
	limit := deadline.Add(-recoveryReserve)
	if recovery {
		limit = deadline
	}
	seconds := int64(limit.Sub(now)/time.Second) - 30
	if recovery {
		seconds = int64(recoveryReserve/time.Second) - 30
	}
	if seconds < 1 {
		return nil, ErrDeadline
	}
	arguments := []string{"--quiet", "--no-ask-password", "--collect", "--service-type=exec", "--expand-environment=no", "--unit=" + runnerUnitName(value, recovery), "--description=" + runnerDescription(value, recovery), "--property=MemoryMax=512M", "--property=CPUQuota=200%", "--property=TasksMax=128", "--property=RuntimeMaxSec=" + strconv.FormatInt(seconds, 10), "--property=TimeoutStopSec=15", "--property=KillMode=control-group", "--property=UMask=0077", "--property=StandardOutput=null", "--property=StandardError=journal"}
	if recovery {
		arguments = append(arguments, "--on-calendar=@"+strconv.FormatInt(limit.Add(-recoveryReserve).Unix(), 10), "--timer-property=AccuracySec=1s", "--timer-property=RemainAfterElapse=no", "--timer-property=Description="+runnerDescription(value, true))
	}
	arguments = append(arguments, "--", tools.env, "-i", "HOME=/var/empty", "PATH=/nonexistent", "LANG=C", "LC_ALL=C", "GOMEMLIMIT=256MiB", tools.panel, "deploy-runner", "--environment-id", value.Claim.Identity.EnvironmentID, "--operation-id", value.Claim.Identity.OperationID)
	if recovery {
		arguments = append(arguments, "--recover")
	}
	return arguments, nil
}

type runnerUnit struct{ LoadState, ActiveState, SubState, Description, MainPID, InvocationID, Transient string }

func observeRunnerUnit(ctx context.Context, tools runnerTools, value execution, recovery bool) (runnerUnit, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	data, err := runnerCommand(bounded, tools.systemctl, []string{"show", "--property=LoadState,ActiveState,SubState,Description,MainPID,InvocationID,Transient", "--", runnerUnitName(value, recovery) + ".service"}, nil, nil, 16384)
	if err != nil {
		return runnerUnit{}, err
	}
	var unit runnerUnit
	seen := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || seen[key] {
			return unit, ErrState
		}
		seen[key] = true
		switch key {
		case "LoadState":
			unit.LoadState = value
		case "ActiveState":
			unit.ActiveState = value
		case "SubState":
			unit.SubState = value
		case "Description":
			unit.Description = value
		case "MainPID":
			unit.MainPID = value
		case "InvocationID":
			unit.InvocationID = value
		case "Transient":
			unit.Transient = value
		default:
			return unit, ErrState
		}
	}
	if len(seen) != 7 {
		return unit, ErrState
	}
	return unit, nil
}

func runnerUnitOwned(unit runnerUnit, value execution, recovery bool) bool {
	return validNativeInvocation(unit.InvocationID) && unit.LoadState == "loaded" && unit.Transient == "yes" && unit.Description == runnerDescription(value, recovery)
}
func (unit runnerUnit) running() bool {
	return unit.ActiveState == "active" && unit.SubState == "running" || unit.ActiveState == "activating"
}
func (unit runnerUnit) absent() bool {
	return unit.LoadState == "not-found" && unit.ActiveState == "inactive"
}

func launchRunner(ctx context.Context, tools runnerTools, value execution, recovery bool) error {
	arguments, err := runnerArguments(value, tools, recovery, time.Now())
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = runnerCommand(bounded, tools.systemdRun, arguments, nil, nil, 4096)
	return err
}

func dockerEnvironment(directory string) []string {
	return []string{"DOCKER_HOST=unix:///run/docker.sock", "DOCKER_CONFIG=" + directory, "COMPOSE_DISABLE_ENV_FILE=1"}
}
func dockerCommand(ctx context.Context, tools runnerTools, directory string, arguments ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return runnerCommand(bounded, tools.docker, arguments, nil, dockerEnvironment(directory), MaxStatusBytes)
}
func composeCommand(ctx context.Context, tools runnerTools, config []byte, arguments ...string) error {
	fixed := []string{"compose", "--env-file", "/dev/null", "--project-directory", "/var/empty", "-f", "-"}
	_, err := runnerCommand(ctx, tools.docker, append(fixed, arguments...), config, dockerEnvironment("/var/empty"), 4096)
	return err
}

// The native image template intentionally excludes environment variables,
// entrypoints and other metadata that may contain application secrets.
const imageTemplate = `{"id":{{json .Id}},"volumes":{{json (index .Config "Volumes")}}}`

type imageObservation struct {
	ID      string                     `json:"id"`
	Volumes map[string]json.RawMessage `json:"volumes"`
}

func inspectImage(ctx context.Context, tools runnerTools, image string) (string, error) {
	data, err := dockerCommand(ctx, tools, "/var/empty", "image", "inspect", "--format", imageTemplate, "--", image)
	if err != nil {
		return "", err
	}
	var observed imageObservation
	if json.Unmarshal(data, &observed) != nil || !strings.HasPrefix(observed.ID, "sha256:") || !validHash(strings.TrimPrefix(observed.ID, "sha256:")) || len(observed.Volumes) != 0 {
		return "", ErrInvalid
	}
	return observed.ID, nil
}

func processPublic(pid int, name string, limit int) ([]byte, error) {
	if pid < 1 || (name != "cmdline" && name != "cgroup") {
		return nil, ErrState
	}
	// #nosec G304 -- Validated native PID, fixed public proc files only; bounded reads never select application data or secret environment files.
	file, err := os.Open("/proc/" + strconv.Itoa(pid) + "/" + name)
	if err != nil {
		return nil, ErrState
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit+1)))
	if err != nil || len(data) > limit {
		return nil, ErrState
	}
	return data, nil
}

func verifyRunnerProcess(unit runnerUnit, tools runnerTools, value execution, recovery bool) error {
	pid, err := strconv.Atoi(unit.MainPID)
	if err != nil || pid < 1 {
		return ErrState
	}
	data, err := processPublic(pid, "cmdline", 4096)
	if err != nil {
		return err
	}
	expected := []string{tools.panel, "deploy-runner", "--environment-id", value.Claim.Identity.EnvironmentID, "--operation-id", value.Claim.Identity.OperationID}
	if recovery {
		expected = append(expected, "--recover")
	}
	if !bytes.Equal(data, []byte(strings.Join(expected, "\x00")+"\x00")) {
		return ErrState
	}
	// #nosec G304 -- Native PID was parsed above; this fixed proc executable link is compared by inode with the verified final-generation binary.
	actual, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ErrState
	}
	// #nosec G304 -- Production tool path derives only from the verified final generation and fixed panel suffix.
	expectedExecutable, err := os.Stat(tools.panel)
	if err != nil || !os.SameFile(actual, expectedExecutable) {
		return ErrState
	}
	cgroup, err := processPublic(pid, "cgroup", 4096)
	if err != nil || !strings.Contains(string(cgroup), "/"+runnerUnitName(value, recovery)+".service\n") {
		return ErrState
	}
	return nil
}

func verifyRunnerSelf(ctx context.Context, tools runnerTools, value execution, recovery bool) error {
	unit, err := observeRunnerUnit(ctx, tools, value, recovery)
	if err != nil || !runnerUnitOwned(unit, value, recovery) || !unit.running() || unit.MainPID != strconv.Itoa(os.Getpid()) {
		return ErrState
	}
	boot, err := nativeBootID()
	if err != nil || boot != value.BootID {
		return ErrState
	}
	return verifyRunnerProcess(unit, tools, value, recovery)
}

func stopRecoveryTimer(ctx context.Context, tools runnerTools, value execution) error {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	name := runnerUnitName(value, true) + ".timer"
	data, err := runnerCommand(bounded, tools.systemctl, []string{"show", "--property=LoadState,Description,Transient", "--", name}, nil, nil, 4096)
	if err != nil {
		return err
	}
	properties := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ErrState
		}
		if _, exists := properties[key]; exists {
			return ErrState
		}
		properties[key] = value
	}
	if properties["LoadState"] == "not-found" {
		return nil
	}
	if len(properties) != 3 || properties["LoadState"] != "loaded" || properties["Description"] != runnerDescription(value, true) || properties["Transient"] != "yes" {
		return ErrState
	}
	_, err = runnerCommand(bounded, tools.systemctl, []string{"stop", "--no-block", "--", name}, nil, nil, 4096)
	return err
}

func validNativeInvocation(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}
