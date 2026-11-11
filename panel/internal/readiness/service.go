// Package readiness observes every registered host through its pinned management
// SSH path. Local setup or a provisioned Tunnel never substitutes for this proof.
package readiness

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/riverqueue/river"
	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const MaxAge = 5 * time.Minute
const cycleTimeout = 45 * time.Second
const hostTimeout = 20 * time.Second

// Fresh is admission evidence only while its observation remains recent. The
// eventual executor must independently verify the target before remote effects.
func Fresh(server store.Server, now time.Time) bool {
	age := now.UnixMilli() - server.ObservedAt
	return server.Readiness == "ready" && server.ObservedAt > 0 && age >= 0 && age <= MaxAge.Milliseconds()
}

type helperBoundary interface {
	Helper(context.Context, remote.Target, []byte, []byte) (remote.Result, error)
}

type Service struct {
	database *sql.DB
	vault    *securestate.Vault
	remote   helperBoundary
}

func New(database *sql.DB, cipher *securestate.Cipher, client *remote.Client) *Service {
	service := &Service{database: database, vault: securestate.NewVault(database, cipher)}
	if client != nil {
		service.remote = client
	}
	return service
}

type sweepArgs struct{}

func (sweepArgs) Kind() string { return "server_readiness_v1" }

type sweepWorker struct {
	river.WorkerDefaults[sweepArgs]
	service *Service
}

func (*sweepWorker) Timeout(*river.Job[sweepArgs]) time.Duration { return cycleTimeout }
func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[sweepArgs]) error {
	return w.service.Sweep(ctx)
}

func (s *Service) Register(workers *river.Workers) ([]*river.PeriodicJob, error) {
	if err := river.AddWorkerSafely(workers, &sweepWorker{service: s}); err != nil {
		return nil, err
	}
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) {
		return sweepArgs{}, &river.InsertOpts{Queue: "readiness", MaxAttempts: 1, UniqueOpts: operations.DiscoveryUnique()}
	}, &river.PeriodicJobOpts{ID: "server-readiness", RunOnStart: true})}, nil
}

// Sweep rotates by observed_at, including failed observations, so an unreachable
// target cannot permanently starve other targets. SQL limits each batch to four;
// two workers share the existing SSH client's total connection bound.
func (s *Service) Sweep(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, cycleTimeout)
	defer cancel()
	targets, err := store.New(s.database).ListReadinessTargets(ctx)
	if err != nil {
		return err
	}
	if len(targets) == 0 || s.remote == nil {
		return nil
	}
	work := make(chan store.Server, len(targets))
	for _, server := range targets {
		work <- server
	}
	close(work)
	failures := make(chan error, len(targets))
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			for server := range work {
				if err := s.observe(ctx, server); err != nil {
					failures <- err
				}
			}
		})
	}
	workers.Wait()
	close(failures)
	var result error
	for failure := range failures {
		result = errors.Join(result, failure)
	}
	return result
}

type binding struct {
	server       store.Server
	management   store.ServerManagement
	installation store.Installation
}

func (s *Service) observe(parent context.Context, server store.Server) error {
	ctx, cancel := context.WithTimeout(parent, hostTimeout)
	defer cancel()
	queries := store.New(s.database)
	management, err := queries.GetServerManagement(ctx, server.ID)
	if err != nil {
		return errors.New("server readiness credential binding unavailable")
	}
	installation, err := queries.GetInstallation(ctx)
	if err != nil {
		return errors.New("server readiness installation binding unavailable")
	}
	bound := binding{server, management, installation}
	status := "needs_attention"
	if installation.PrivateProofAt <= 0 {
		status = "setup_pending"
	} else if response, err := s.exchange(ctx, bound); err == nil {
		status = response.Status
	} else if errors.Is(err, errUnreachable) {
		status = "unreachable"
	}
	// Cancellation/SSH timeout cannot leave a previously ready row indefinitely
	// fresh. Persist only this safe observation, with bounded shutdown allowance.
	persist, cancelPersist := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancelPersist()
	return s.persist(persist, bound, status)
}

var errUnreachable = errors.New("server management connection unavailable")
var errEvidence = errors.New("server readiness evidence rejected")

func (s *Service) exchange(ctx context.Context, bound binding) (hosthelper.ReadinessResponse, error) {
	management, installed := bound.management, bound.installation
	if management.CredentialKind != "credential" || management.CredentialOwnerID != "ssh:"+installed.InstallationID+":"+bound.server.ID || management.CredentialVersion < 1 {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	target, err := remote.NewTarget(bound.server.TailscaleAddress, "panel-runner", []byte(management.HostPublicKey), bound.server.SshFingerprint)
	if err != nil {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	private, err := s.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: management.CredentialOwnerID, Version: management.CredentialVersion})
	if err != nil {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	defer clear(private)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != management.ManagementFingerprint {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	request := hosthelper.ReadinessRequest{SchemaVersion: 1, Action: "readiness", InstallationID: installed.InstallationID, ServerID: bound.server.ID, Challenge: hex.EncodeToString(nonce)}
	payload, err := json.Marshal(request)
	if err != nil {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	result, err := s.remote.Helper(ctx, target, private, payload)
	defer clear(result.Output)
	if result.Outcome == remote.CommandExited && result.ExitCode != 0 {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	if err != nil || result.Outcome != remote.CommandExited || result.ExitCode != 0 {
		return hosthelper.ReadinessResponse{}, errUnreachable
	}
	response, err := hosthelper.ParseReadinessResponse(result.Output, request)
	role := "server"
	if bound.server.PanelHost {
		role = "panel"
	}
	if err != nil || response.Role != role {
		return hosthelper.ReadinessResponse{}, errEvidence
	}
	return response, nil
}

func (s *Service) persist(ctx context.Context, bound binding, status string) error {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("persist server readiness unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	server, err := queries.GetServer(ctx, bound.server.ID)
	if err != nil {
		return errors.New("persist server readiness target unavailable")
	}
	management, err := queries.GetServerManagement(ctx, server.ID)
	if err != nil {
		return errors.New("persist server readiness binding unavailable")
	}
	installed, err := queries.GetInstallation(ctx)
	if err != nil {
		return errors.New("persist server readiness installation unavailable")
	}
	if server.TailscaleAddress != bound.server.TailscaleAddress || server.SshFingerprint != bound.server.SshFingerprint || server.PanelHost != bound.server.PanelHost || server.ObservedAt != bound.server.ObservedAt || management != bound.management || installed.InstallationID != bound.installation.InstallationID || installed.PrivateProofAt != bound.installation.PrivateProofAt {
		return nil
	}
	if err := queries.ObserveServer(ctx, store.ObserveServerParams{ID: server.ID, Readiness: status, ObservedAt: time.Now().UnixMilli()}); err != nil {
		return errors.New("persist server readiness failed")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("commit server readiness failed")
	}
	return nil
}
