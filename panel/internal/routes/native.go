package routes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

var ErrTunnelRuntime = errors.New("managed Tunnel runtime could not be verified")

const RuntimeDirectory = "/var/lib/panel-cloudflared"

// TunnelObservation binds readiness to the exact native process incarnation.
// A changed config cannot be considered loaded merely because an older process
// still serves /ready while its on-disk configuration has already changed.
type TunnelObservation struct {
	Ready   bool
	Process string
}

func nativeTunnelTools(finalSystem string) tunnelTools {
	return tunnelTools{finalSystem + "/sw/bin/systemctl", finalSystem + "/sw/bin/cloudflared"}
}

func ValidateTunnelConfiguration(ctx context.Context, runtime *os.Root, configuration []byte, cloudflared string) error {
	return validateTunnelConfiguration(ctx, runtime, configuration, tunnelTools{cloudflared: cloudflared})
}

func InspectTunnelRuntimeFiles(runtime *os.Root, credentials, configuration []byte) (bool, error) {
	return inspectTunnelRuntimeFiles(runtime, credentials, configuration)
}

func ObserveTunnelRuntime(ctx context.Context, finalSystem string, runtime *os.Root, credentials, configuration []byte) (TunnelObservation, error) {
	return observeTunnelRuntime(ctx, nativeTunnelTools(finalSystem), finalSystem, runtime, credentials, configuration)
}

func VerifyTunnelUnit(ctx context.Context, finalSystem string) error {
	tools := nativeTunnelTools(finalSystem)
	unit, err := readTunnelUnit(ctx, tools)
	if err != nil || !boundTunnelUnit(unit, finalSystem) {
		return ErrTunnelRuntime
	}
	return nil
}

func StartTunnel(ctx context.Context, finalSystem string) error {
	if err := VerifyTunnelUnit(ctx, finalSystem); err != nil {
		return err
	}
	_, err := tunnelCommand(ctx, nativeTunnelTools(finalSystem).systemctl, nil, "start", "--no-block", "--", "panel-cloudflared.service")
	return err
}

func restartTunnel(ctx context.Context, finalSystem string) error {
	if err := VerifyTunnelUnit(ctx, finalSystem); err != nil {
		return err
	}
	_, err := tunnelCommand(ctx, nativeTunnelTools(finalSystem).systemctl, nil, "restart", "--no-block", "--", "panel-cloudflared.service")
	return err
}

type tunnelTools struct{ systemctl, cloudflared string }

type tunnelCommandOutput struct{ data bytes.Buffer }

func (output *tunnelCommandOutput) Write(data []byte) (int, error) {
	if len(data) > 4096-output.data.Len() {
		return 0, ErrTunnelRuntime
	}
	return output.data.Write(data)
}

func tunnelCommand(ctx context.Context, executable string, files []*os.File, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// #nosec G204,G702 -- Private callers use only verified final-system tools and fixed native arguments; no shell or request-selected command/path.
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = []string{"HOME=/var/empty", "LANG=C", "LC_ALL=C", "GOMEMLIMIT=128MiB"}
	command.Dir = "/"
	command.ExtraFiles = files
	command.Stderr = io.Discard
	output := tunnelCommandOutput{}
	command.Stdout = &output
	command.WaitDelay = 100 * time.Millisecond
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
	if command.Run() != nil || ctx.Err() != nil {
		return nil, ErrTunnelRuntime
	}
	return output.data.Bytes(), nil
}

func validateTunnelConfiguration(ctx context.Context, root *os.Root, data []byte, tools tunnelTools) error {
	if len(data) > MaxConfigBytes {
		return ErrRuntimeState
	}
	directory, err := root.Open(".")
	if err != nil {
		return ErrRuntimeState
	}
	defer func() { _ = directory.Close() }()
	// A readable anonymous descriptor gives native validation the exact staged
	// bytes before config.json can make the service startable.
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDWR|unix.O_CLOEXEC|unix.O_TMPFILE, 0600)
	if err != nil {
		return ErrRuntimeState
	}
	file := os.NewFile(uintptr(fd), "tunnel-validation")
	if file == nil {
		_ = syscall.Close(fd)
		return ErrRuntimeState
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		return ErrRuntimeState
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ErrRuntimeState
	}
	_, err = tunnelCommand(ctx, tools.cloudflared, []*os.File{file}, "tunnel", "--config", "/proc/self/fd/3", "ingress", "validate")
	return err
}

type tunnelUnit struct{ load, active, sub, pid, fragment, dropins, reload, invocation string }

func readTunnelUnit(ctx context.Context, tools tunnelTools) (tunnelUnit, error) {
	data, err := tunnelCommand(ctx, tools.systemctl, nil, "show", "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID,FragmentPath,DropInPaths,NeedDaemonReload,InvocationID", "--", "panel-cloudflared.service")
	if err != nil {
		return tunnelUnit{}, err
	}
	fields := make(map[string]string)
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if _, exists := fields[key]; exists {
			return tunnelUnit{}, ErrTunnelRuntime
		}
		fields[key] = value
	}
	return tunnelUnit{fields["LoadState"], fields["ActiveState"], fields["SubState"], fields["MainPID"], fields["FragmentPath"], fields["DropInPaths"], fields["NeedDaemonReload"], fields["InvocationID"]}, nil
}

func tunnelSameFile(actual, expected string) bool {
	if actual == "" {
		return false
	}
	left, err := os.Stat(actual)
	if err != nil || left == nil || !left.Mode().IsRegular() {
		return false
	}
	right, err := os.Stat(expected)
	return err == nil && right != nil && right.Mode().IsRegular() && os.SameFile(left, right)
}

func boundTunnelUnit(unit tunnelUnit, finalSystem string) bool {
	if unit.load != "loaded" || unit.reload != "no" || !tunnelSameFile(unit.fragment, finalSystem+"/etc/systemd/system/panel-cloudflared.service") {
		return false
	}
	// This product generates one native override file only when the package unit
	// is reused. Compare its inode; runtime/global foreign drop-ins cannot match.
	expected := finalSystem + "/etc/systemd/system/panel-cloudflared.service.d/overrides.conf"
	_, err := os.Stat(expected)
	actual := strings.Fields(unit.dropins)
	if errors.Is(err, os.ErrNotExist) {
		return len(actual) == 0
	}
	return err == nil && len(actual) == 1 && tunnelSameFile(actual[0], expected)
}

func inspectTunnelRuntimeFiles(runtime *os.Root, credentials, configuration []byte) (bool, error) {
	if runtimeDirectoryEntries(runtime, "credentials.json", "config.json") != nil {
		return false, ErrRuntimeState
	}
	complete := true
	for _, item := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{"credentials.json", credentials, 0600}, {"config.json", configuration, 0644}} {
		maximum := 4096
		if item.name == "config.json" {
			maximum = MaxConfigBytes
		}
		data, err := readRuntimeFile(runtime, item.name, item.mode, maximum)
		matches := bytes.Equal(data, item.data)
		clear(data)
		if errors.Is(err, os.ErrNotExist) {
			complete = false
			continue
		}
		if err != nil || !matches {
			return false, ErrRuntimeState
		}
	}
	return complete, nil
}

func observeTunnelRuntime(ctx context.Context, tools tunnelTools, finalSystem string, runtime *os.Root, credentials, configuration []byte) (TunnelObservation, error) {
	complete, err := inspectTunnelRuntimeFiles(runtime, credentials, configuration)
	if err != nil || !complete {
		return TunnelObservation{}, err
	}
	unit, err := readTunnelUnit(ctx, tools)
	if err != nil || !boundTunnelUnit(unit, finalSystem) {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	if unit.active != "active" || unit.sub != "running" {
		return TunnelObservation{}, nil
	}
	pid, err := strconv.ParseUint(unit.pid, 10, 31)
	if err != nil || pid == 0 || strconv.FormatUint(pid, 10) != unit.pid {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	process := "/proc/" + unit.pid
	if !tunnelSameFile(process+"/exe", tools.cloudflared) {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	command, err := readTunnelPublic(process+"/cmdline", 4096)
	if err != nil || !tunnelProcessCommand(command, tools.cloudflared) {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	invocation, err := uuid.Parse(unit.invocation)
	if err != nil || len(unit.invocation) != 32 || invocation == uuid.Nil {
		return TunnelObservation{}, ErrTunnelRuntime
	}

	snapshot, err := os.OpenRoot("/run/credentials/panel-cloudflared.service")
	if err != nil {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	defer func() { _ = snapshot.Close() }()
	// systemd owns this read-only credential snapshot; its file permissions are
	// intentionally independent of the protected source's fixed 0600 mode.
	file, err := snapshot.Open("credentials.json")
	if err != nil {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	matches := bytes.Equal(data, credentials)
	clear(data)
	if readErr != nil || closeErr != nil || !matches {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	ready := tunnelReady(ctx)
	after, err := readTunnelUnit(ctx, tools)
	if err != nil || after != unit {
		return TunnelObservation{}, ErrTunnelRuntime
	}
	return TunnelObservation{Ready: ready, Process: unit.invocation}, nil
}

func readTunnelPublic(name string, maximum int64) ([]byte, error) {
	// #nosec G304 -- Private production callers read fixed public /proc/<native MainPID>/cmdline only.
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrTunnelRuntime
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return nil, ErrTunnelRuntime
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrTunnelRuntime
	}
	return data, nil
}

func tunnelProcessCommand(data []byte, executable string) bool {
	parts := strings.Split(string(data), "\x00")
	expected := []string{"tunnel", "--config", RuntimeDirectory + "/config.json", "--no-autoupdate", "--metrics", "127.0.0.1:20241", "--loglevel", "warn", "--management-diagnostics=false", "--grace-period", "20s", "--retries", "5", "run", "--credentials-file", "/run/credentials/panel-cloudflared.service/credentials.json", ""}
	return len(parts) == len(expected)+1 && tunnelSameFile(parts[0], executable) && slices.Equal(parts[1:], expected)
}

func tunnelReady(ctx context.Context) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:20241/ready", nil)
	if err != nil {
		return false
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 4096}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
	return err == nil && n <= 4096 && response.StatusCode == http.StatusOK
}
