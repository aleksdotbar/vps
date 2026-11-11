// Package hosthelper implements the fixed, privileged SSH management protocol.
// The initial probe is read-only; it does not establish deployment readiness.
package hosthelper

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/deployment"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

var (
	ErrPrivilege = errors.New("host helper requires the restricted management account through sudo")
	ErrRequest   = errors.New("invalid host helper request")
	ErrIdentity  = errors.New("host helper installation identity or system observation does not match")
	ErrIO        = errors.New("host helper input or output failed")
)

const maxRequestBytes = 4096
const maxMetadataBytes = 256 << 10

// ProbeRequest uses canonical encoding/json field order and values. The caller
// supplies a new 32-byte random hexadecimal challenge for each SSH observation.
type ProbeRequest struct {
	SchemaVersion  int    `json:"schema_version"`
	Action         string `json:"action"`
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	Challenge      string `json:"challenge"`
}

type ProbeResponse struct {
	SchemaVersion  int    `json:"schema_version"`
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	Challenge      string `json:"challenge"`
	Role           string `json:"role"`
	Stage          string `json:"stage"`
	CurrentSystem  string `json:"current_system"`
	BootedSystem   string `json:"booted_system"`
	SystemProfile  string `json:"system_profile"`
	Ready          bool   `json:"ready"`
}

type identity struct {
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	Role           string `json:"role"`
}

type metadata struct {
	identity
	SchemaVersion int    `json:"schema_version"`
	Stage         string `json:"stage"`
}

type systems struct {
	current, booted, profile string
}

// Serve accepts one bounded request. SSH_ORIGINAL_COMMAND and all caller paths
// are deliberately ignored: sudo authorizes this command, not a shell executor.
func Serve(parent context.Context, input, output *os.File, acquire ...FinalAcquirer) error {
	if err := authorize(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	input, err := pollableFile(input, ctx)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err = pollableFile(output, ctx)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	// Rewrapped SSH descriptors participate in Go's poller. Closing inherited
	// blocking os.Stdin directly would not interrupt its pending syscall.
	stop := context.AfterFunc(ctx, func() {
		_ = input.Close()
		_ = output.Close()
	})
	defer stop()
	data, err := io.ReadAll(io.LimitReader(input, deployment.MaxStageBytes+1))
	defer clear(data)
	if err != nil {
		return ErrIO
	}
	var final FinalAcquirer
	if len(acquire) > 1 {
		return ErrRequest
	}
	if len(acquire) == 1 {
		final = acquire[0]
	}
	encoded, err := dispatchHelper(ctx, data, final)
	defer clear(encoded)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ErrIO
	}
	line := append(encoded, '\n')
	defer clear(line)
	if _, err := output.Write(line); err != nil {
		return ErrIO
	}
	return nil
}

func dispatchHelper(ctx context.Context, data []byte, acquire FinalAcquirer) ([]byte, error) {
	if request, err := ParseReadinessRequest(data); err == nil {
		response, err := HandleReadiness(ctx, request, acquire)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(response)
		if err != nil || len(encoded) > maxRequestBytes {
			return nil, ErrIO
		}
		return encoded, nil
	}
	if request, err := deployment.ParseExecution(data); err == nil {
		response, err := HandleDeploymentExecution(ctx, request, acquire)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(response)
		if err != nil || len(encoded) > deployment.MaxStatusBytes {
			return nil, ErrIO
		}
		return encoded, nil
	}
	if request, err := deployment.ParseStage(data); err == nil {
		defer request.Clear()
		response, err := handleDeploymentStage(ctx, request, acquire)
		if err != nil {
			return nil, err
		}
		return encodeDeploymentResponse(response)
	}
	if request, err := deployment.ParseStatus(data); err == nil {
		response, err := handleDeploymentStatus(request)
		if err != nil {
			return nil, err
		}
		return encodeDeploymentResponse(response)
	}
	if request, err := parseRequest(data); err == nil {
		response, err := probe(request)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(response)
		if err != nil || len(encoded) > maxRequestBytes {
			return nil, ErrIO
		}
		return encoded, nil
	}
	request, err := ParseTunnelRequest(data)
	if err != nil {
		return nil, err
	}
	response, err := handleTunnel(ctx, request, acquire)
	defer clear(response.Secret)
	if err != nil {
		return nil, err
	}
	// #nosec G117 -- Preparation returns its target-owned secret only over authenticated management SSH; response and output buffers are cleared, never logged or persisted in panel state.
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > maxRequestBytes {
		clear(encoded)
		return nil, ErrIO
	}
	return encoded, nil
}

func pollableFile(file *os.File, ctx context.Context) (*os.File, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, ErrIO
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, ErrIO
	}
	if unix.SetNonblock(fd, true) != nil {
		_ = unix.Close(fd)
		return nil, ErrIO
	}
	wrapped := os.NewFile(uintptr(fd), "host-helper-stream")
	if wrapped == nil {
		_ = unix.Close(fd)
		return nil, ErrIO
	}
	if wrapped.SetDeadline(deadline) != nil {
		_ = wrapped.Close()
		return nil, ErrIO
	}
	return wrapped, nil
}

func authorize() error {
	if os.Geteuid() != 0 || os.Getenv("SUDO_USER") != "panel-runner" {
		return ErrPrivilege
	}
	runner, err := user.Lookup("panel-runner")
	if err != nil || runner == nil || runner.Uid == "0" || runner.Uid != os.Getenv("SUDO_UID") || runner.Gid != os.Getenv("SUDO_GID") {
		return ErrPrivilege
	}
	uid, err := strconv.ParseUint(runner.Uid, 10, 32)
	if err != nil || uid == 0 {
		return ErrPrivilege
	}
	return nil
}

func parseRequest(data []byte) (ProbeRequest, error) {
	var request ProbeRequest
	if len(data) > maxRequestBytes || json.Unmarshal(data, &request) != nil {
		return request, ErrRequest
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) || request.SchemaVersion != 1 || request.Action != "probe" ||
		!validID(request.InstallationID) || !validID(request.ServerID) {
		return ProbeRequest{}, ErrRequest
	}
	challenge, err := hex.DecodeString(request.Challenge)
	if err != nil || len(challenge) != 32 || hex.EncodeToString(challenge) != request.Challenge {
		return ProbeRequest{}, ErrRequest
	}
	return request, nil
}

func validID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func probe(request ProbeRequest) (ProbeResponse, error) {
	before, err := observeSystems()
	if err != nil {
		return ProbeResponse{}, err
	}
	file, err := privatefile.OpenPrivate("/var/lib/panel-host/identity.json", os.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return ProbeResponse{}, ErrIdentity
	}
	defer func() { _ = file.Close() }()
	var retained identity
	if readJSON(file, &retained) != nil {
		return ProbeResponse{}, ErrIdentity
	}
	// The system root is validated and root-controlled. This public Nix metadata
	// may itself be a store symlink; no payload value selects a filesystem path.
	public, err := os.OpenFile(before.current+"/etc/panel-host.json", os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- observed root-controlled canonical Nix system, fixed suffix.
	if err != nil {
		return ProbeResponse{}, ErrIdentity
	}
	defer func() { _ = public.Close() }()
	var installed metadata
	if readJSON(public, &installed) != nil {
		return ProbeResponse{}, ErrIdentity
	}
	after, err := observeSystems()
	if err != nil || after != before {
		return ProbeResponse{}, ErrIdentity
	}
	return bindResponse(request, retained, installed, before)
}

func bindResponse(request ProbeRequest, retained identity, installed metadata, observed systems) (ProbeResponse, error) {
	if installed.identity != retained || retained.InstallationID != request.InstallationID || retained.ServerID != request.ServerID ||
		(installed.Role != "panel" && installed.Role != "server") || installed.SchemaVersion != 1 ||
		(installed.Stage != "enrollment" && installed.Stage != "final") {
		return ProbeResponse{}, ErrIdentity
	}
	return ProbeResponse{
		SchemaVersion: 1, InstallationID: retained.InstallationID, ServerID: retained.ServerID,
		Challenge: request.Challenge, Role: installed.Role, Stage: installed.Stage,
		CurrentSystem: observed.current, BootedSystem: observed.booted, SystemProfile: observed.profile,
		Ready: false,
	}, nil
}

var systemPath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-nixos-system-[A-Za-z0-9.+_-]+$`)

func observeSystems() (systems, error) {
	var result systems
	for _, link := range []struct {
		path  string
		value *string
	}{{"/run/current-system", &result.current}, {"/run/booted-system", &result.booted}, {"/nix/var/nix/profiles/system", &result.profile}} {
		resolved, err := filepath.EvalSymlinks(link.path)
		if err != nil || !systemPath.MatchString(resolved) {
			return systems{}, ErrIdentity
		}
		*link.value = resolved
	}
	return result, nil
}

func readJSON(file *os.File, value any) error {
	info, err := file.Stat()
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode().Perm()&022 != 0 || info.Size() > maxMetadataBytes {
		return ErrIdentity
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return ErrIdentity
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMetadataBytes+1))
	if err != nil || len(data) > maxMetadataBytes || json.Unmarshal(data, value) != nil {
		return ErrIdentity
	}
	return nil
}
