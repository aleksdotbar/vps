package cloudflareflow

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"google.golang.org/protobuf/types/known/timestamppb"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var (
	ErrTunnelConflict = errors.New("server Tunnel binding already exists")
	ErrHostSetup      = errors.New("verified final host management is required")
	ErrAccountInUse   = errors.New("account owns server Tunnel state")
)

// helperBoundary is the existing pinned SSH exchange, not a command runner.
type helperBoundary interface {
	Helper(context.Context, remote.Target, []byte, []byte) (remote.Result, error)
}

type Tunnels struct {
	connection *Service
	remote     helperBoundary
	runtime    *operations.Runtime
}

func NewTunnels(connection *Service, client *remote.Client, runtime *operations.Runtime) *Tunnels {
	value := &Tunnels{connection: connection, runtime: runtime}
	if client != nil {
		value.remote = client
	}
	return value
}
func (t *Tunnels) Register(workers *river.Workers) error {
	return river.AddWorkerSafely(workers, &tunnelWorker{tunnels: t})
}
func (t *Tunnels) Provision(ctx context.Context, request *panelv1.ProvisionServerTunnelRequest) (*panelv1.ProvisionServerTunnelResponse, error) {
	if request == nil || protovalidate.Validate(request) != nil {
		return nil, ErrInvalid
	}
	if t.runtime == nil || t.remote == nil {
		return nil, ErrHostSetup
	}
	id, err := uuid.Parse(request.ServerId)
	if err != nil || id == uuid.Nil || id.String() != request.ServerId {
		return nil, ErrInvalid
	}
	// Observe River outside our sole SQLite transaction; the matching job ID is
	// rechecked under the write transaction before any explicit resume.
	var resumableJob int64
	prior, readErr := store.New(t.connection.database).GetServerTunnel(ctx, request.ServerId)
	if readErr == nil && prior.Phase != "ready" && prior.RiverJobID > 0 {
		terminal, err := t.runtime.BuildJobsTerminal(ctx, []int64{prior.RiverJobID})
		if err != nil {
			return nil, err
		}
		if terminal[prior.RiverJobID] {
			resumableJob = prior.RiverJobID
		}
	} else if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return nil, readErr
	}
	value, err := t.connection.mutations.Begin(ctx, "panel.v1.CloudflareService/ProvisionServerTunnel", request.GetMutation().GetRequestId(), request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	queries := store.New(value.Tx)
	if value.Response != nil {
		row, err := queries.GetServerTunnel(ctx, request.ServerId)
		if err != nil {
			return nil, err
		}
		if resumableJob > 0 && row.RiverJobID == resumableJob && row.Phase != "ready" {
			changed, err := queries.ResumeServerTunnel(ctx, store.ResumeServerTunnelParams{UpdatedAt: time.Now().UnixMilli(), ServerID: row.ServerID, RiverJobID: row.RiverJobID})
			if err != nil || changed != 1 {
				return nil, ErrTunnelConflict
			}
			jobID, err := t.runtime.EnqueueTunnel(ctx, value.Tx, row.ServerID)
			if err != nil {
				return nil, err
			}
			if err := queries.SetTunnelJob(ctx, store.SetTunnelJobParams{RiverJobID: jobID, ServerID: row.ServerID}); err != nil {
				return nil, err
			}
			row, err = queries.GetServerTunnel(ctx, row.ServerID)
			if err != nil {
				return nil, err
			}
			if err := value.Tx.Commit(); err != nil {
				return nil, err
			}
		}
		return &panelv1.ProvisionServerTunnelResponse{Tunnel: tunnelMessage(row)}, nil
	}
	if _, err := queries.GetServerTunnel(ctx, request.ServerId); err == nil {
		return nil, ErrTunnelConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	integration, err := queries.GetCloudflareIntegration(ctx)
	if err != nil {
		return nil, ErrHostSetup
	}
	if integration.Version < 1 || uint64(integration.Version) != request.ExpectedConnectionVersion {
		return nil, securestate.ErrStaleVersion
	}
	installed, err := queries.GetInstallation(ctx)
	if err != nil || installed.PrivateProofAt <= 0 {
		return nil, ErrHostSetup
	}
	if _, err := queries.GetServerManagement(ctx, request.ServerId); err != nil {
		return nil, ErrHostSetup
	}
	capacity, err := queries.CloudflareTunnelCapacity(ctx)
	if err != nil {
		return nil, err
	}
	if capacity.Total >= 10000 || capacity.Unresolved >= 128 {
		return nil, operations.ErrCapacity
	}
	now := time.Now().UnixMilli()
	requestID := request.GetMutation().GetRequestId()
	canonicalID, err := uuid.Parse(requestID)
	if err != nil {
		return nil, ErrInvalid
	}
	requestID = canonicalID.String()
	err = queries.CreateServerTunnel(ctx, store.CreateServerTunnelParams{ServerID: request.ServerId, RequestID: requestID, InstallationID: installed.InstallationID, AccountID: integration.AccountID, CredentialVersion: integration.Version, Name: "panel-" + request.ServerId + "-" + requestID, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return nil, err
	}
	jobID, err := t.runtime.EnqueueTunnel(ctx, value.Tx, request.ServerId)
	if err != nil {
		return nil, err
	}
	if err := queries.SetTunnelJob(ctx, store.SetTunnelJobParams{RiverJobID: jobID, ServerID: request.ServerId}); err != nil {
		return nil, err
	}
	row, err := queries.GetServerTunnel(ctx, request.ServerId)
	if err != nil {
		return nil, err
	}
	response := &panelv1.ProvisionServerTunnelResponse{Tunnel: tunnelMessage(row)}
	if err := t.connection.mutations.Commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
func (t *Tunnels) Status(ctx context.Context, serverID string) (*panelv1.GetServerTunnelResponse, error) {
	row, err := store.New(t.connection.database).GetServerTunnel(ctx, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return &panelv1.GetServerTunnelResponse{Tunnel: &panelv1.ServerTunnel{ServerId: serverID}}, nil
	}
	if err != nil {
		return nil, err
	}
	if t.runtime != nil && row.Phase != "ready" && row.Phase != "needs_attention" && row.RiverJobID > 0 {
		terminal, err := t.runtime.BuildJobsTerminal(ctx, []int64{row.RiverJobID})
		if err != nil {
			return nil, err
		}
		if terminal[row.RiverJobID] {
			current, err := store.New(t.connection.database).GetServerTunnel(ctx, serverID)
			if err != nil {
				return nil, err
			}
			if current.RiverJobID == row.RiverJobID && current.Phase != "ready" {
				row = current
				row.Phase = "needs_attention"
				row.Reason = "observation_exhausted"
			} else {
				row = current
			}
		}
	}
	return &panelv1.GetServerTunnelResponse{Tunnel: tunnelMessage(row)}, nil
}
func tunnelMessage(row store.ServerTunnel) *panelv1.ServerTunnel {
	phases := map[string]panelv1.ServerTunnelPhase{"queued": panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_QUEUED, "prepared": panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_PREPARED, "creating": panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_CREATING, "finalizing": panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_FINALIZING, "ready": panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_READY, "needs_attention": panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_NEEDS_ATTENTION}
	messages := map[string]string{"host_setup_required": "Host final setup or connector verification requires attention.", "provider_unknown": "Tunnel creation outcome is unknown; no second create was sent.", "provider_identity": "Cloudflare Tunnel identity conflicts with the recorded intent.", "observation_exhausted": "Bounded Tunnel reconciliation stopped; inspect the provider and target.", "target_conflict": "Target Tunnel ownership or credential proof was rejected."}
	value := &panelv1.ServerTunnel{ServerId: row.ServerID, RequestId: row.RequestID, AccountId: row.AccountID, TunnelId: row.TunnelID.String, Phase: phases[row.Phase], ConfigSha256: row.ConfigSha256, Message: messages[row.Reason]}
	if row.CredentialVersion > 0 {
		value.ExpectedConnectionVersion = uint64(row.CredentialVersion)
	}
	if row.ObservedAt > 0 {
		value.ObservedAt = timestamppb.New(time.UnixMilli(row.ObservedAt))
	}
	return value
}
