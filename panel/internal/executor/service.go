package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/cloudflareflow"
	"github.com/aleksdotbar/vps/panel/internal/deployment"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var errRemoteUnknown = errors.New("deployment management outcome is unknown")
var errRemoteRejected = errors.New("deployment management command rejected")

type helperBoundary interface {
	Helper(context.Context, remote.Target, []byte, []byte) (remote.Result, error)
}

type Service struct {
	database *sql.DB
	vault    *securestate.Vault
	remote   helperBoundary
	dns      *cloudflareflow.DNSRecords
}

func New(database *sql.DB, cipher *securestate.Cipher, client *remote.Client, cloudflare *cloudflareflow.Service) *Service {
	service := &Service{database: database, vault: securestate.NewVault(database, cipher)}
	if client != nil {
		service.remote = client
	}
	service.dns = cloudflareflow.NewDNSRecords(cloudflare, service)
	return service
}

func (s *Service) Execute(ctx context.Context, operation store.Operation) (operations.Result, error) {
	return s.run(ctx, operation, false)
}
func (s *Service) Reconcile(ctx context.Context, operation store.Operation) (operations.Result, error) {
	return s.run(ctx, operation, true)
}

func (s *Service) run(parent context.Context, operation store.Operation, reconcile bool) (operations.Result, error) {
	if operation.Kind != "deploy" && operation.Kind != "rollback" || s.remote == nil {
		return operations.Unsupported, nil
	}
	if reconcile && (operation.State == "needs_attention" || time.Now().UnixMilli() >= operation.ExecutionDeadline) {
		return s.observeTerminal(parent, operation)
	}
	ctx, cancel := context.WithDeadline(parent, time.UnixMilli(operation.ExecutionDeadline))
	defer cancel()
	if _, err := capture(ctx, s.database, operation); err != nil {
		return operations.Unknown, err
	}
	input, err := loadInput(ctx, s.database, operation)
	if err != nil {
		return operations.Unknown, err
	}
	connection, err := s.connect(ctx, input)
	if err != nil {
		return operations.Unknown, err
	}
	defer clear(connection.private)
	system, err := s.ready(ctx, connection, input)
	if err != nil {
		return operations.Unknown, err
	}
	var observed deployment.ExecutionResponse
	if reconcile {
		observed, err = s.control(ctx, connection, input, system, "deploy_observe")
		if err == nil {
			return s.accept(ctx, connection, input, system, observed)
		}
		if !errors.Is(err, errRemoteRejected) {
			return operations.Unknown, err
		}
		// A completed stage with no launch can safely enter native compare-and-start.
		// Existing/partial launch intent never permits another native launch.
		staged, statusErr := s.staged(ctx, connection, input, system)
		if statusErr != nil {
			return operations.Unknown, statusErr
		}
		if !staged {
			if err := s.stage(ctx, connection, input, system); err != nil {
				return operations.Unknown, err
			}
		}
	} else if err := s.stage(ctx, connection, input, system); err != nil {
		return operations.Unknown, err
	}
	observed, err = s.control(ctx, connection, input, system, "deploy_start")
	if err != nil {
		return operations.Unknown, err
	}

	return s.accept(ctx, connection, input, system, observed)
}

// Expiry limits effects, not receipt collection after a disconnected panel.
func (s *Service) observeTerminal(parent context.Context, operation store.Operation) (operations.Result, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if _, err := store.New(s.database).GetDeploymentExecution(ctx, operation.ID); errors.Is(err, sql.ErrNoRows) {
		// Capture is committed before every target effect.
		return operations.Failed, nil
	} else if err != nil {
		return operations.Unknown, err
	}
	input, err := loadInput(ctx, s.database, operation)
	if err != nil {
		return operations.Unknown, err
	}
	connection, err := s.connect(ctx, input)
	if err != nil {
		return operations.Unknown, err
	}
	defer clear(connection.private)
	system, err := s.ready(ctx, connection, input)
	if err != nil {
		return operations.Unknown, err
	}
	observed, err := s.control(ctx, connection, input, system, "deploy_observe")
	if err != nil {
		return operations.Unknown, err
	}
	switch observed.Phase {
	case "succeeded", "recovered", "failed":
		return s.accept(ctx, connection, input, system, observed)
	default:
		return operations.Unknown, nil
	}
}

type connection struct {
	target  remote.Target
	private []byte
}

func (s *Service) connect(ctx context.Context, input executionInput) (connection, error) {
	target, err := remote.NewTarget(input.server.TailscaleAddress, "panel-runner", []byte(input.management.HostPublicKey), input.server.SshFingerprint)
	if err != nil {
		return connection{}, errExecutionState
	}
	private, err := s.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: input.management.CredentialOwnerID, Version: input.management.CredentialVersion})
	if err != nil {
		return connection{}, errors.New("retained management credential unavailable")
	}
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != input.management.ManagementFingerprint {
		clear(private)
		return connection{}, errExecutionState
	}
	return connection{target, private}, nil
}
func challenge() (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}
func (s *Service) exchange(parent context.Context, connection connection, payload []byte) ([]byte, error) {
	if s.remote == nil {
		return nil, errRemoteRejected
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	result, err := s.remote.Helper(ctx, connection.target, connection.private, payload)
	if result.Outcome == remote.CommandExited && result.ExitCode != 0 {
		clear(result.Output)
		return nil, errRemoteRejected
	}
	if err != nil || result.Outcome != remote.CommandExited || result.ExitCode != 0 {
		clear(result.Output)
		return nil, errRemoteUnknown
	}
	return result.Output, nil
}
func (s *Service) ready(ctx context.Context, connection connection, input executionInput) (string, error) {
	nonce, err := challenge()
	if err != nil {
		return "", err
	}
	request := hosthelper.ReadinessRequest{SchemaVersion: 1, Action: "readiness", InstallationID: input.installation.InstallationID, ServerID: input.server.ID, Challenge: nonce}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	data, err := s.exchange(ctx, connection, payload)
	defer clear(data)
	if err != nil {
		return "", err
	}
	response, err := hosthelper.ParseReadinessResponse(data, request)
	role := "server"
	if input.server.PanelHost {
		role = "panel"
	}
	if err != nil || response.Status != "ready" || response.Role != role {
		return "", errObservation
	}
	return response.CurrentSystem, nil
}
func (s *Service) stage(ctx context.Context, connection connection, input executionInput, system string) error {
	nonce, err := challenge()
	if err != nil {
		return err
	}
	request, err := stageRequest(ctx, s.vault, input, nonce)
	defer request.Clear()
	if err != nil {
		return err
	}
	// #nosec G117 -- Bounded transient SSH secret payload, cleared after exchange; never logged or persisted.
	payload, err := json.Marshal(request)
	defer clear(payload)
	if err != nil || len(payload) > deployment.MaxStageBytes {
		return errExecutionState
	}
	data, err := s.exchange(ctx, connection, payload)
	defer clear(data)
	if err != nil {
		return err
	}
	observed, err := deployment.ParseResponse(data, request.Action, nonce, input.manifest.Identity, input.manifestSHA256)
	if err != nil || observed.Phase != "staged" || observed.FinalSystem != system {
		return errObservation
	}
	return nil
}
func (s *Service) control(ctx context.Context, connection connection, input executionInput, system, action string) (deployment.ExecutionResponse, error) {
	nonce, err := challenge()
	if err != nil {
		return deployment.ExecutionResponse{}, err
	}
	request := deployment.ExecutionRequest{SchemaVersion: 1, Action: action, Challenge: nonce, Identity: input.manifest.Identity, ManifestSHA256: input.manifestSHA256}
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > deployment.MaxControlBytes {
		return deployment.ExecutionResponse{}, errExecutionState
	}
	data, err := s.exchange(ctx, connection, payload)
	defer clear(data)
	if err != nil {
		return deployment.ExecutionResponse{}, err
	}
	observed, err := deployment.ParseExecutionResponse(data, request)
	if err != nil || observed.FinalSystem != system {
		return deployment.ExecutionResponse{}, errObservation
	}
	return observed, nil
}

func (s *Service) accept(ctx context.Context, connection connection, input executionInput, system string, observed deployment.ExecutionResponse) (operations.Result, error) {
	switch observed.Phase {
	case "awaiting_dns":
		if err := s.verifyServices(ctx, input, observed, input.operation.ReleaseID); err != nil {
			return operations.Unknown, err
		}
		ready, err := s.ensureDNS(ctx, input)
		if err != nil {
			// Native awaiting_dns proof remains running; withheld acknowledgement lets
			// its independent deadline recover. Provider failures never become success.
			slog.WarnContext(ctx, "deployment DNS confirmation pending", "operation", input.operation.ID, "reason", dnsReason(err))
			return operations.Running, nil
		}
		if !ready {
			return operations.Running, nil
		}
		confirmed, err := s.control(ctx, connection, input, system, "deploy_confirm")
		if err != nil {
			return operations.Unknown, err
		}
		if confirmed.Phase == "succeeded" {
			return s.accept(ctx, connection, input, system, confirmed)
		}
		return operations.Running, nil
	case "succeeded", "recovered":
		result, active := operations.Succeeded, input.operation.ReleaseID
		if observed.Phase == "recovered" {
			if !input.capture.PreviousReleaseID.Valid {
				return operations.Unknown, errObservation
			}
			result, active = operations.Recovered, input.capture.PreviousReleaseID.String
		}
		if observed.ActiveReleaseID != active {
			return operations.Unknown, errObservation
		}
		if err := s.verifyServices(ctx, input, observed, active); err != nil {
			return operations.Unknown, err
		}
		services := make([]observedService, 0, len(observed.Services))
		for _, value := range observed.Services {
			services = append(services, observedService{Name: value.Service, Image: value.Image, ContainerID: value.ContainerID})
		}
		if err := complete(ctx, s.database, input.operation, completion{Result: result, ActiveReleaseID: active, Services: services}); err != nil {
			return operations.Unknown, err
		}
		return result, nil
	case "failed":
		return operations.Failed, nil
	case "needs_attention":
		return operations.Unknown, nil
	case "recovering":
		return operations.Recovering, nil
	case "starting", "preparing", "replacing", "checking", "applying_routes":
		return operations.Running, nil
	default:
		return operations.Unknown, errObservation
	}
}

func (s *Service) verifyServices(ctx context.Context, input executionInput, observed deployment.ExecutionResponse, releaseID string) error {
	services, err := store.New(s.database).ListReleaseServices(ctx, store.ListReleaseServicesParams{ReleaseID: releaseID, Name: "", Limit: 65})
	if err != nil {
		return err
	}
	values := make([]observedService, 0, len(observed.Services))
	for _, service := range observed.Services {
		values = append(values, observedService{Name: service.Service, Image: service.Image, ContainerID: service.ContainerID})
	}
	if !matchesServices(services, values) {
		return errObservation
	}
	if releaseID == input.operation.ReleaseID && len(input.manifest.RouteSnapshot.Bindings) > 0 && (observed.RouteRevision == 0 || observed.RouteConfigSHA256 == "") {
		return errObservation
	}
	return nil
}

func (s *Service) staged(ctx context.Context, connection connection, input executionInput, system string) (bool, error) {
	nonce, err := challenge()
	if err != nil {
		return false, err
	}
	request := deployment.StatusRequest{SchemaVersion: 1, Action: "deploy_status", Challenge: nonce, Identity: input.manifest.Identity, ManifestSHA256: input.manifestSHA256}
	payload, err := json.Marshal(request)
	if err != nil {
		return false, err
	}
	data, err := s.exchange(ctx, connection, payload)
	defer clear(data)
	if errors.Is(err, errRemoteRejected) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	observed, err := deployment.ParseResponse(data, request.Action, nonce, input.manifest.Identity, input.manifestSHA256)
	if err != nil || observed.Phase != "staged" || observed.FinalSystem != system {
		return false, errObservation
	}
	return true, nil
}
