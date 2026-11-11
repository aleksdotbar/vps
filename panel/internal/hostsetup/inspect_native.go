package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxInspectionBytes = 256 << 10

var ErrInspection = errors.New("source inspection did not complete in its protected sandbox")

type inspectionTools struct {
	systemdRun     string
	systemctl      string
	userManager    bool   // Only native devbox fixtures use the unprivileged manager.
	fixtureSlice   string // Same bounded slice as the Go fixture process.
	memoryMaxBytes int64
}

type inspectionOutput struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (output *inspectionOutput) Write(data []byte) (int, error) {
	if len(data) > maxInspectionBytes-output.buffer.Len() {
		if output.cancel != nil {
			output.cancel()
		}
		return 0, ErrInspection
	}
	return output.buffer.Write(data)
}

func runSourceInspection(ctx context.Context, tools inspectionTools, operationID string, command []string) ([]byte, error) {
	if tools.memoryMaxBytes < (1280<<20) || tools.memoryMaxBytes > (2<<30) {
		return nil, ErrResources
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	unit := "panel-inspect-" + operationID + ".service"
	// The fixed operation unit bounds crash overlap. A unique description lets
	// cleanup distinguish this attempt from a previously running same-name unit.
	description := "Panel source inspection " + operationID + " " + uuid.NewString()
	arguments := []string{"--unit=" + unit, "--description=" + description, "--wait", "--pipe", "--collect", "--quiet", "--service-type=exec", "--expand-environment=no", "--no-ask-password",
		"--property=MemoryMax=" + strconv.FormatInt(tools.memoryMaxBytes, 10), "--property=CPUQuota=200%", "--property=TasksMax=64", "--property=RuntimeMaxSec=300", "--property=TimeoutStopSec=15", "--property=KillMode=control-group",
		"--property=PrivateNetwork=yes", "--property=RestrictAddressFamilies=AF_UNIX", "--property=InaccessiblePaths=-/nix/var/nix/daemon-socket",
		"--property=ProtectSystem=strict", "--property=ProtectHome=yes", "--property=PrivateTmp=yes", "--property=NoNewPrivileges=yes", "--property=UMask=0077", "--"}
	if tools.userManager {
		if tools.fixtureSlice == "" {
			return nil, ErrInspection
		}
		arguments = append([]string{"--user", "--slice=" + tools.fixtureSlice}, arguments...)
	}
	process := nativeCommand(ctx, tools.systemdRun, append(arguments, command...)...)
	if tools.userManager {
		process.Env = append(process.Env, "XDG_RUNTIME_DIR="+userManagerRuntimeDirectory())
	}
	output := inspectionOutput{cancel: cancel}
	process.Stdout = &output
	process.Stdin = nil
	if process.Run() != nil || ctx.Err() != nil {
		stopOwnedInspection(ctx, tools, unit, description)
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(ErrInspection, err)
		}
		return nil, ErrInspection
	}
	return output.buffer.Bytes(), nil
}

func inspectionMemoryLimit(availableKiB int64) (int64, bool) {
	if availableKiB < 1536<<10 {
		return 0, false
	}
	if availableKiB >= 2304<<10 {
		return 2 << 30, true
	}
	return (availableKiB - (256 << 10)) * 1024, true
}

func stopOwnedInspection(ctx context.Context, tools inspectionTools, unit, description string) {
	// Killing the launcher does not kill a transient service. Cleanup has its
	// own short context; an unknown/foreign description never authorizes a stop.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	arguments := []string{"show", "--property=Description", "--value", "--", unit}
	if tools.userManager {
		arguments = append([]string{"--user"}, arguments...)
	}
	query := nativeCommand(cleanup, tools.systemctl, arguments...)
	if tools.userManager {
		query.Env = append(query.Env, "XDG_RUNTIME_DIR="+userManagerRuntimeDirectory())
	}
	var output inspectionOutput
	query.Stdout = &output
	if query.Run() != nil || strings.TrimSpace(output.buffer.String()) != description {
		return
	}
	arguments = []string{"stop", "--no-block", "--", unit}
	if tools.userManager {
		arguments = append([]string{"--user"}, arguments...)
	}
	stop := nativeCommand(cleanup, tools.systemctl, arguments...)
	if tools.userManager {
		stop.Env = append(stop.Env, "XDG_RUNTIME_DIR="+userManagerRuntimeDirectory())
	}
	stop.Stdout = io.Discard
	_ = stop.Run()
}

func userManagerRuntimeDirectory() string { return "/run/user/" + strconv.Itoa(os.Geteuid()) }
