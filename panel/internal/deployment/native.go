package deployment

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type validationTool struct {
	executable string
	prefix     []string
}

func nativeCommand(ctx context.Context, tool validationTool, arguments ...string) *exec.Cmd {
	// #nosec G204,G702 -- Production selects only the verified final-system Docker executable with a literal Compose prefix; private test substitutions invoke pinned native Compose.
	command := exec.CommandContext(ctx, tool.executable, append(append([]string{}, tool.prefix...), arguments...)...)
	command.Env = []string{"HOME=/var/empty", "PATH=/nonexistent", "LANG=C", "LC_ALL=C", "DOCKER_CONFIG=/var/empty", "DOCKER_HOST=unix:///var/empty/panel-staging-no-daemon.sock", "COMPOSE_DISABLE_ENV_FILE=1"}
	command.Dir = "/"
	command.Stderr = io.Discard
	command.Stdout = io.Discard
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
	return command
}

type versionOutput struct{ data bytes.Buffer }

func (output *versionOutput) Write(data []byte) (int, error) {
	if len(data) > 128-output.data.Len() {
		return 0, ErrInvalid
	}
	return output.data.Write(data)
}

func validateNative(ctx context.Context, data []byte, tool validationTool) error {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version := versionOutput{}
	check := nativeCommand(bounded, tool, "version", "--short")
	check.Stdout = &version
	if check.Run() != nil || string(bytes.TrimSpace(version.data.Bytes())) != "5.4.0" {
		return ErrInvalid
	}
	// Stdin survives the Docker CLI's plugin boundary without inheriting private
	// extra descriptors. Native Compose reads only these admitted bytes.
	command := nativeCommand(bounded, tool, "--env-file", "/dev/null", "--project-directory", "/var/empty", "-f", "-", "config", "--quiet")
	command.Stdin = bytes.NewReader(data)
	if command.Run() != nil {
		return ErrInvalid
	}
	return nil
}
