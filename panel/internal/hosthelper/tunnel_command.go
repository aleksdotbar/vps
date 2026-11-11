package hosthelper

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

// Private native readiness probes retain their existing bounded command runner.
// Caller-selected executable paths and arguments never cross the SSH protocol.
type tunnelUnit struct{ load, active, sub, pid, fragment, dropins, reload string }

type tunnelCommandOutput struct{ data bytes.Buffer }

func (output *tunnelCommandOutput) Write(data []byte) (int, error) {
	if len(data) > 4096-output.data.Len() {
		return 0, ErrTunnelRuntime
	}
	return output.data.Write(data)
}

func tunnelCommand(ctx context.Context, executable string, files []*os.File, arguments ...string) ([]byte, error) {
	// Cold Docker CLI startup must fit on small hosts; callers also bound the whole request.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
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
