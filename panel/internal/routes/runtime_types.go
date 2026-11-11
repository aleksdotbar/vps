package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"buf.build/go/protovalidate"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
)

var (
	ErrRuntimeState    = errors.New("native route state is incomplete or belongs to another intent")
	ErrRuntimeConflict = errors.New("native route revision or current release changed")
	ErrRuntimeBusy     = errors.New("native route configuration is busy")
	ErrRuntimeDeadline = errors.New("native route execution deadline is expired or invalid")
)

// RuntimeOperation is mapped from the target's authoritative staged deployment.
// InstallationID also binds empty-domain snapshots, which have no provider IDs.
// The deployment runner must prove exact native candidate publications and
// health before ApplyRuntime, and previous-runtime health before RestoreRuntime.
type RuntimeOperation struct {
	InstallationID           string
	Snapshot                 Snapshot
	ManifestSHA256           string
	ExpectedCurrentReleaseID string
	ExecutionDeadline        int64
}

type RuntimePreparation struct {
	Origins      []Origin
	Revision     uint64
	ConfigSHA256 string
}

type RuntimePhase string

const (
	RuntimeApplying RuntimePhase = "applying"
	RuntimeReady    RuntimePhase = "ready"
	RuntimeRestored RuntimePhase = "restored"
	RuntimeNeutral  RuntimePhase = "neutral"
)

// A ready/restored receipt proves route configuration and connector observation,
// not application health, DNS ownership or whole-deployment success.
// Neutral carries no release/config authority. The runner may accept it only
// for a domainless candidate and domainless retained previous release, with
// independently verified matching application and terminal/recovery state.
type RuntimeReceipt struct {
	OperationID         string
	ReleaseID           string
	EnvironmentID       string
	RouteSnapshotSHA256 string
	ActiveReleaseID     string
	Revision            uint64
	ConfigSHA256        string
	Phase               RuntimePhase
}

func runtimeHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

// SnapshotSHA256 validates nonsecret immutable route input at the native staging
// boundary. The omitted SHA256 field is intentionally not trusted or hashed.
func SnapshotSHA256(snapshot Snapshot) (string, error) {
	if snapshot.SchemaVersion != 1 || !canonicalID(snapshot.OperationID) || !canonicalID(snapshot.ReleaseID) || !canonicalID(snapshot.EnvironmentID) || !canonicalID(snapshot.ServerID) || len(snapshot.Bindings) > maxDomains || !slices.IsSorted(snapshot.Services) {
		return "", ErrInvalid
	}
	if len(snapshot.Bindings) == 0 {
		if snapshot.InstallationID != "" || snapshot.AccountID != "" || snapshot.TunnelID != "" {
			return "", ErrInvalid
		}
	} else if !canonicalID(snapshot.InstallationID) || !cloudflareID(snapshot.AccountID) || !canonicalID(snapshot.TunnelID) {
		return "", ErrInvalid
	}
	contribution := Contribution{EnvironmentID: snapshot.EnvironmentID, Services: snapshot.Services, Domains: make([]*panelv1.Domain, 0, len(snapshot.Bindings))}
	claims := make(map[string]bool, len(snapshot.Bindings))
	last := ""
	for _, binding := range snapshot.Bindings {
		if !canonicalID(binding.ClaimID) || claims[binding.ClaimID] || binding.Hostname <= last || !cloudflareID(binding.ZoneID) || binding.CredentialVersion < 1 || !withinZone(binding.Hostname, binding.ZoneName) {
			return "", ErrInvalid
		}
		zone := &panelv1.Domain{Hostname: binding.ZoneName, Service: binding.Service, Port: binding.ContainerPort}
		if protovalidate.Validate(zone) != nil {
			return "", ErrInvalid
		}
		claims[binding.ClaimID], last = true, binding.Hostname
		contribution.Domains = append(contribution.Domains, &panelv1.Domain{Hostname: binding.Hostname, Service: binding.Service, Port: binding.ContainerPort})
	}
	if _, err := checkContributions([]Contribution{contribution}); err != nil {
		return "", err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", ErrInvalid
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func ValidateRuntimeOperation(operation RuntimeOperation) error {
	if !canonicalID(operation.InstallationID) || !runtimeHash(operation.ManifestSHA256) || operation.ExecutionDeadline < 1 || operation.ExpectedCurrentReleaseID != "" && !canonicalID(operation.ExpectedCurrentReleaseID) {
		return ErrInvalid
	}
	if operation.Snapshot.InstallationID != "" && operation.Snapshot.InstallationID != operation.InstallationID {
		return ErrOwnership
	}
	hash, err := SnapshotSHA256(operation.Snapshot)
	if err != nil {
		return err
	}
	if hash != operation.Snapshot.SHA256 {
		return ErrInvalid
	}
	return nil
}
