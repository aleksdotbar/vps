package cloudflareflow

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/riverqueue/river"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const tunnelPollInterval = 15 * time.Second

type tunnelWorker struct {
	river.WorkerDefaults[operations.TunnelArgs]
	tunnels *Tunnels
}

func (*tunnelWorker) Timeout(*river.Job[operations.TunnelArgs]) time.Duration { return 2 * time.Minute }
func (w *tunnelWorker) Work(ctx context.Context, job *river.Job[operations.TunnelArgs]) (returnErr error) {
	// Provider/helper panics can contain credentials. The durable create claim
	// forces reconciliation; River receives only this fixed, redacted failure.
	defer func() {
		if recover() != nil {
			returnErr = errors.New("interrupted Tunnel provisioning")
		}
	}()
	delay, err := w.tunnels.advance(ctx, job.Args.ServerID)
	if err != nil {
		return errors.New("update Tunnel provisioning state")
	}
	if delay > 0 {
		return river.JobSnooze(delay)
	}
	return nil
}
func (t *Tunnels) attention(ctx context.Context, row store.ServerTunnel, reason string) (time.Duration, error) {
	return 0, store.New(t.connection.database).AttendServerTunnel(ctx, store.AttendServerTunnelParams{Reason: reason, UpdatedAt: time.Now().UnixMilli(), ServerID: row.ServerID})
}
func (t *Tunnels) advance(ctx context.Context, serverID string) (time.Duration, error) {
	queries := store.New(t.connection.database)
	row, err := queries.GetServerTunnel(ctx, serverID)
	if err != nil {
		return 0, err
	}
	if row.Phase == "ready" || row.Phase == "needs_attention" {
		return 0, nil
	}
	if row.Polls >= 120 {
		return t.attention(ctx, row, "observation_exhausted")
	}
	if _, err := queries.AdvanceTunnelPoll(ctx, store.AdvanceTunnelPollParams{UpdatedAt: time.Now().UnixMilli(), ServerID: serverID}); err != nil {
		return 0, err
	}
	if !row.CreateAttempted {
		prepared, err := t.helper(ctx, row, "tunnel_prepare")
		if err != nil {
			return t.attention(ctx, row, "host_setup_required")
		}
		defer clear(prepared.Secret)
		if prepared.Phase != "prepared" {
			return t.attention(ctx, row, "target_conflict")
		}
		// A preexisting exact-name match is never adopted by a fresh intent.
		existing, err := t.find(ctx, row)
		if errors.Is(err, ErrScope) || existing != nil {
			return t.attention(ctx, row, "provider_identity")
		}
		if err != nil && !errors.Is(err, errTunnelAbsent) {
			return tunnelPollInterval, nil
		}
		if err := queries.PrepareServerTunnel(ctx, store.PrepareServerTunnelParams{UpdatedAt: time.Now().UnixMilli(), ServerID: serverID}); err != nil {
			return 0, err
		}
		now := time.Now().UnixMilli()
		claimed, err := queries.ClaimTunnelCreate(ctx, store.ClaimTunnelCreateParams{CreateAttemptedAt: now, UpdatedAt: now, ServerID: serverID})
		if err != nil {
			return 0, err
		}
		if claimed != 1 {
			return tunnelPollInterval, nil
		}
		row, err = queries.GetServerTunnel(ctx, serverID)
		if err != nil {
			return 0, err
		}
		// A crash or any uncertain response after this claim can only reconcile.
		// Never reset the claim, including after timeout, throttling or rejection.
		created, err := t.create(ctx, row, prepared.Secret)
		clear(prepared.Secret)
		if err != nil {
			return tunnelPollInterval, nil
		}
		if err := t.bind(ctx, row, created.ID); err != nil {
			return 0, err
		}
		row.TunnelID = sql.NullString{String: created.ID, Valid: true}
	} else {
		candidate, err := t.find(ctx, row)
		if errors.Is(err, ErrScope) {
			return t.attention(ctx, row, "provider_identity")
		}
		if err != nil || candidate == nil {
			return tunnelPollInterval, nil
		}
		if !row.TunnelID.Valid {
			if err := t.bind(ctx, row, candidate.ID); err != nil {
				return 0, err
			}
			row.TunnelID = sql.NullString{String: candidate.ID, Valid: true}
		}
	}
	observed, err := t.helper(ctx, row, "tunnel_status")
	if err != nil {
		return tunnelPollInterval, nil
	}
	if observed.TunnelID != "" && observed.TunnelID != row.TunnelID.String {
		return t.attention(ctx, row, "target_conflict")
	}
	if observed.Phase == "prepared" || observed.Phase == "finalizing" {
		// Finalization publishes owned files before starting the native service.
		// Replay that exact idempotent action after interruption; the one-way
		// provider create claim is never cleared or repeated.
		observed, err = t.helper(ctx, row, "tunnel_finalize")
		if err != nil {
			return tunnelPollInterval, nil
		}
	}
	if observed.Phase == "needs_attention" {
		return t.attention(ctx, row, "target_conflict")
	}
	return t.observe(ctx, row, observed)
}
func (t *Tunnels) bind(ctx context.Context, row store.ServerTunnel, tunnelID string) error {
	changed, err := store.New(t.connection.database).BindCreatedTunnel(ctx, store.BindCreatedTunnelParams{TunnelID: sql.NullString{String: tunnelID, Valid: true}, UpdatedAt: time.Now().UnixMilli(), ServerID: row.ServerID, TunnelID_2: sql.NullString{String: tunnelID, Valid: true}})
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrTunnelConflict
	}
	return nil
}
func (t *Tunnels) observe(ctx context.Context, row store.ServerTunnel, observed hosthelper.TunnelResponse) (time.Duration, error) {
	if observed.TunnelID != row.TunnelID.String {
		return t.attention(ctx, row, "target_conflict")
	}
	// Parser already binds challenge/account/intent. A name-only provider match
	// remains a candidate until the native connector proves the retained secret.
	if observed.Phase == "ready" && observed.CredentialProof {
		err := store.New(t.connection.database).ObserveServerTunnel(ctx, store.ObserveServerTunnelParams{Phase: "ready", ConfigSha256: observed.ConfigSHA256, ObservedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(), ServerID: row.ServerID})
		return 0, err
	}
	return tunnelPollInterval, nil
}
