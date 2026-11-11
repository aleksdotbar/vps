package hosthelper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/aleksdotbar/vps/panel/internal/routes"
)

var ErrTunnelRuntime = routes.ErrTunnelRuntime

// FinalLease holds setup exclusion until Release. The CLI supplies the settled
// final-generation authority without coupling this protocol to setup storage.
type FinalLease struct {
	InstallationID string
	ServerID       string
	FinalSystem    string
	Release        func() error
}

type FinalAcquirer func(context.Context) (FinalLease, error)

func handleTunnel(ctx context.Context, request TunnelRequest, acquire FinalAcquirer) (response TunnelResponse, returnErr error) {
	if acquire == nil {
		return TunnelResponse{}, ErrIdentity
	}
	lease, err := acquire(ctx)
	if err != nil || lease.Release == nil {
		return TunnelResponse{}, ErrIdentity
	}
	defer func() {
		if lease.Release() != nil {
			clear(response.Secret)
			response = TunnelResponse{}
			returnErr = ErrIdentity
		}
	}()
	if verifyTunnelLease(request, lease) != nil {
		return TunnelResponse{}, ErrIdentity
	}
	response = tunnelResponse(request)
	if request.Action == "tunnel_prepare" {
		if err := createTunnelStateDirectory(); err != nil {
			return TunnelResponse{}, err
		}
	}
	private, err := openTunnelDirectory(tunnelPrivateDirectory, 0700)
	if err != nil {
		return TunnelResponse{}, err
	}
	defer func() { _ = private.Close() }()
	runtime, err := openTunnelDirectory(tunnelRuntimeDirectory, 0755)
	if err != nil {
		return TunnelResponse{}, err
	}
	defer func() { _ = runtime.Close() }()
	binding := bindTunnel(request, lease)
	var intent tunnelIntent
	if request.Action == "tunnel_prepare" {
		intent, err = prepareTunnelIntent(ctx, private, runtime, binding)
	} else {
		intent, err = readTunnelIntent(private, binding)
	}
	if err != nil {
		return TunnelResponse{}, err
	}
	defer clear(intent.Secret)
	final, err := readTunnelFinal(private, binding)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return TunnelResponse{}, ErrTunnelState
	}
	if request.Action == "tunnel_prepare" && errors.Is(err, os.ErrNotExist) {
		if tunnelDirectoryEntries(runtime) != nil || verifyTunnelLease(request, lease) != nil {
			return TunnelResponse{}, ErrTunnelState
		}
		response.Phase = "prepared"
		response.Secret = slices.Clone(intent.Secret)
		return response, nil
	}
	if request.Action == "tunnel_status" && errors.Is(err, os.ErrNotExist) {
		response.Phase = "prepared"
		return response, nil
	}
	if request.Action == "tunnel_finalize" {
		if err == nil && final.TunnelID != request.TunnelID {
			return TunnelResponse{}, ErrTunnelState
		}
		final = tunnelFinal{binding, request.TunnelID}
	}
	response.TunnelID = final.TunnelID
	response.Phase = "finalizing"
	initialConfiguration, err := routes.Compile(final.TunnelID, nil, nil)
	if err != nil {
		return TunnelResponse{}, ErrTunnelState
	}
	configuration, err := routes.OwnedConfiguration(request.InstallationID, request.ServerID, request.AccountID, final.TunnelID)
	if err != nil {
		return TunnelResponse{}, tunnelRuntimeError(err)
	}
	hash := sha256.Sum256(configuration)
	response.ConfigSHA256 = hex.EncodeToString(hash[:])
	credentials, err := json.Marshal(tunnelCredentials{request.AccountID, intent.Secret, final.TunnelID})
	defer clear(credentials)
	if err != nil {
		return TunnelResponse{}, ErrTunnelState
	}
	tools := tunnelTools{lease.FinalSystem + "/sw/bin/systemctl", lease.FinalSystem + "/sw/bin/cloudflared"}
	if request.Action == "tunnel_finalize" {
		if verifyTunnelLease(request, lease) != nil {
			return TunnelResponse{}, ErrIdentity
		}
		if bytes.Equal(configuration, initialConfiguration) {
			if err := finalizeTunnelFiles(ctx, private, runtime, final, credentials, configuration, tools); err != nil {
				return TunnelResponse{}, err
			}
		} else {
			// Application routes are published only by the route CAS boundary.
			// Provisioning replay must neither replace nor repair their config.
			complete, err := inspectTunnelRuntimeFiles(runtime, credentials, configuration)
			if err != nil || !complete {
				return TunnelResponse{}, ErrTunnelState
			}
		}
		if err := routes.VerifyTunnelUnit(ctx, lease.FinalSystem); err != nil {
			return TunnelResponse{}, ErrTunnelRuntime
		}
		if err := routes.StartTunnel(ctx, lease.FinalSystem); err != nil {
			return response, nil
		}
	}
	ready, err := observeTunnelRuntime(ctx, tools, lease.FinalSystem, runtime, credentials, configuration)
	if errors.Is(err, ErrTunnelRuntime) {
		// An unavailable or changing process snapshot does not prove foreign
		// ownership. Leave the durable intent for bounded caller reconciliation.
		return TunnelResponse{}, err
	}
	if err != nil {
		response.Phase = "needs_attention"
	} else if ready {
		response.Phase = "ready"
		response.CredentialProof = true
	}
	if verifyTunnelLease(request, lease) != nil {
		return TunnelResponse{}, ErrIdentity
	}
	return response, nil
}

func tunnelResponse(request TunnelRequest) TunnelResponse {
	return TunnelResponse{SchemaVersion: 1, Action: request.Action, InstallationID: request.InstallationID, ServerID: request.ServerID, OperationID: request.OperationID, Challenge: request.Challenge, AccountID: request.AccountID, Name: request.Name}
}

func verifyTunnelLease(request TunnelRequest, lease FinalLease) error {
	if lease.InstallationID != request.InstallationID || lease.ServerID != request.ServerID || !systemPath.MatchString(lease.FinalSystem) || lease.Release == nil {
		return ErrIdentity
	}
	response, err := probe(ProbeRequest{1, "probe", request.InstallationID, request.ServerID, request.Challenge})
	if err != nil || response.Stage != "final" || response.CurrentSystem != lease.FinalSystem || response.SystemProfile != lease.FinalSystem {
		return ErrIdentity
	}
	return nil
}

func createTunnelStateDirectory() error {
	parent, err := os.OpenRoot("/var/lib")
	if err != nil {
		return ErrTunnelState
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Mkdir("panel-tunnel", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrTunnelState
	}
	return syncTunnelDirectory(parent)
}

func finalizeTunnelFiles(ctx context.Context, private, runtime *os.Root, final tunnelFinal, credentials, configuration []byte, tools tunnelTools) error {
	// Refuse conflicting files before recording final authority. Each later
	// no-replace publication is independently recoverable under this receipt.
	if _, err := inspectTunnelRuntimeFiles(runtime, credentials, configuration); err != nil {
		return err
	}
	if err := validateTunnelConfiguration(ctx, runtime, configuration, tools); err != nil {
		return err
	}
	data, err := json.Marshal(final)
	if err != nil || publishTunnelFile(ctx, private, "final.json", data, 0600) != nil {
		return ErrTunnelState
	}
	if publishTunnelFile(ctx, runtime, "credentials.json", credentials, 0600) != nil {
		return ErrTunnelState
	}
	return publishTunnelFile(ctx, runtime, "config.json", configuration, 0644)
}

type tunnelTools struct{ systemctl, cloudflared string }

func tunnelRuntimeError(err error) error {
	if errors.Is(err, routes.ErrRuntimeState) {
		return ErrTunnelState
	}
	return err
}
func validateTunnelConfiguration(ctx context.Context, root *os.Root, data []byte, tools tunnelTools) error {
	return tunnelRuntimeError(routes.ValidateTunnelConfiguration(ctx, root, data, tools.cloudflared))
}
func inspectTunnelRuntimeFiles(runtime *os.Root, credentials, configuration []byte) (bool, error) {
	complete, err := routes.InspectTunnelRuntimeFiles(runtime, credentials, configuration)
	return complete, tunnelRuntimeError(err)
}
func observeTunnelRuntime(ctx context.Context, _ tunnelTools, finalSystem string, runtime *os.Root, credentials, configuration []byte) (bool, error) {
	observed, err := routes.ObserveTunnelRuntime(ctx, finalSystem, runtime, credentials, configuration)
	return observed.Ready, tunnelRuntimeError(err)
}
