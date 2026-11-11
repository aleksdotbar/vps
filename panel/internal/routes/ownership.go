package routes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var ErrSetup = errors.New("public routes require a configured account, selected zone and provisioned Tunnel")

const maxRetainedClaims = 100000

// Binding reserves provider identity and a release's service/container port.
// Loopback publication is intentionally absent: only the executor can allocate
// and prove that origin under its target operation.
type Binding struct {
	ClaimID           string `json:"claim_id"`
	Hostname          string `json:"hostname"`
	ZoneID            string `json:"zone_id"`
	ZoneName          string `json:"zone_name"`
	CredentialVersion int64  `json:"credential_version"`
	Service           string `json:"service"`
	ContainerPort     uint32 `json:"container_port"`
}

// Snapshot is nonsecret, immutable input for one stored deployment/rollback.
// It is not authority to execute that operation or evidence of provider effects.
type Snapshot struct {
	SchemaVersion  int       `json:"schema_version"`
	OperationID    string    `json:"operation_id"`
	ReleaseID      string    `json:"release_id"`
	EnvironmentID  string    `json:"environment_id"`
	ServerID       string    `json:"server_id"`
	InstallationID string    `json:"installation_id"`
	AccountID      string    `json:"account_id"`
	TunnelID       string    `json:"tunnel_id"`
	Services       []string  `json:"services"`
	Bindings       []Binding `json:"bindings"`
	SHA256         string    `json:"-"`
}

type operationInput struct {
	operation store.Operation
	release   store.GetReleaseMetadataRow
	domains   []*panelv1.Domain
	services  []string
}

func readOperationInput(ctx context.Context, queries *store.Queries, operationID string) (operationInput, error) {
	if !canonicalID(operationID) {
		return operationInput{}, ErrInvalid
	}
	operation, err := queries.GetOperation(ctx, operationID)
	if err != nil {
		return operationInput{}, fmt.Errorf("read route operation: %w", err)
	}
	if operation.Kind != "deploy" && operation.Kind != "rollback" {
		return operationInput{}, ErrInvalid
	}
	release, err := queries.GetReleaseMetadata(ctx, operation.ReleaseID)
	if err != nil {
		return operationInput{}, fmt.Errorf("read route release: %w", err)
	}
	if operation.EnvironmentID != release.EnvironmentID || operation.ServerID != release.ServerID || (operation.Kind == "deploy" && operation.BindingVersion != release.BindingVersion) || operation.BindingVersion < 1 || !canonicalID(release.ID) || !canonicalID(release.ServerID) {
		return operationInput{}, ErrOwnership
	}
	var domains []*panelv1.Domain
	if json.Unmarshal(release.DomainsJson, &domains) != nil {
		return operationInput{}, ErrInvalid
	}
	rows, err := queries.ListReleaseServices(ctx, store.ListReleaseServicesParams{ReleaseID: release.ID, Name: "", Limit: 65})
	if err != nil {
		return operationInput{}, err
	}
	services := make([]string, 0, len(rows))
	for _, row := range rows {
		services = append(services, row.Name)
	}
	if _, err := checkContributions([]Contribution{{EnvironmentID: release.EnvironmentID, Services: services, Domains: domains}}); err != nil {
		return operationInput{}, err
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i].Hostname < domains[j].Hostname })
	return operationInput{operation: operation, release: release, domains: domains, services: services}, nil
}

// CheckDesiredOwnership preserves permanent admitted claims while settings are
// edited. Desired rows alone do not authorize a queued release or transfer.
func CheckDesiredOwnership(ctx context.Context, queries *store.Queries, environmentID, serverID string, domains []*panelv1.Domain) error {
	moved, err := queries.EnvironmentRouteClaimsOnOtherServer(ctx, store.EnvironmentRouteClaimsOnOtherServerParams{EnvironmentID: environmentID, ServerID: serverID})
	if err != nil {
		return err
	}
	if moved > 0 {
		return ErrOwnership
	}
	for _, domain := range domains {
		if domain == nil {
			return ErrInvalid
		}
		claim, err := queries.GetRouteClaim(ctx, domain.GetHostname())
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if claim.EnvironmentID != environmentID || claim.ServerID != serverID {
			return ErrOwnership
		}
	}
	return nil
}

// ReserveOperationTx belongs in the same transaction as release, operation and
// River admission. Inputs come only from those stored immutable records.
func ReserveOperationTx(ctx context.Context, tx *sql.Tx, operationID string) (Snapshot, error) {
	queries := store.New(tx)
	input, err := readOperationInput(ctx, queries, operationID)
	if err != nil {
		return Snapshot{}, err
	}
	links, err := queries.ListReleaseRoutes(ctx, input.release.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if len(links) > 0 || len(input.domains) == 0 {
		return OperationSnapshot(ctx, queries, operationID)
	}
	if err := CheckDesiredOwnership(ctx, queries, input.release.EnvironmentID, input.release.ServerID, input.domains); err != nil {
		return Snapshot{}, err
	}
	integration, err := queries.GetCloudflareIntegration(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrSetup
	}
	if err != nil {
		return Snapshot{}, err
	}
	tunnel, err := queries.GetServerTunnel(ctx, input.release.ServerID)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrSetup
	}
	if err != nil {
		return Snapshot{}, err
	}
	if tunnel.Phase != "ready" || !tunnel.TunnelID.Valid || tunnel.AccountID != integration.AccountID || !canonicalID(tunnel.TunnelID.String) || !cloudflareID(integration.AccountID) || integration.Version < 1 {
		return Snapshot{}, ErrSetup
	}
	zones, err := queries.ListCloudflareZones(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	count, err := queries.RouteClaimCount(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for _, domain := range input.domains {
		desiredOwner, err := queries.GetDesiredDomainOwner(ctx, domain.Hostname)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Snapshot{}, err
		}
		if err == nil && desiredOwner != input.release.EnvironmentID {
			return Snapshot{}, ErrOwnership
		}
		claim, err := queries.GetRouteClaim(ctx, domain.Hostname)
		if errors.Is(err, sql.ErrNoRows) {
			zone, found := selectZone(domain.Hostname, zones)
			if !found {
				return Snapshot{}, ErrSetup
			}
			if count >= maxRetainedClaims {
				return Snapshot{}, ErrCapacity
			}
			claim = store.RouteClaim{Hostname: domain.Hostname, ID: uuid.NewString(), EnvironmentID: input.release.EnvironmentID, ServerID: input.release.ServerID, InstallationID: tunnel.InstallationID, AccountID: integration.AccountID, ZoneID: zone.ID, ZoneName: zone.Name, TunnelID: tunnel.TunnelID.String, CredentialVersion: integration.Version, CreatedAt: time.Now().UnixMilli()}
			if err := queries.CreateRouteClaim(ctx, store.CreateRouteClaimParams{Hostname: claim.Hostname, ID: claim.ID, EnvironmentID: claim.EnvironmentID, ServerID: claim.ServerID, InstallationID: claim.InstallationID, AccountID: claim.AccountID, ZoneID: claim.ZoneID, ZoneName: claim.ZoneName, TunnelID: claim.TunnelID, CredentialVersion: claim.CredentialVersion, CreatedAt: claim.CreatedAt}); err != nil {
				return Snapshot{}, err
			}
			count++
		} else if err != nil {
			return Snapshot{}, err
		}
		// Existing claims keep their original zone and credential version even if a
		// new child zone or replacement token has since been selected.
		if claim.EnvironmentID != input.release.EnvironmentID || claim.ServerID != input.release.ServerID || claim.InstallationID != tunnel.InstallationID || claim.AccountID != integration.AccountID || claim.TunnelID != tunnel.TunnelID.String {
			return Snapshot{}, ErrOwnership
		}
		if err := queries.CreateReleaseRoute(ctx, store.CreateReleaseRouteParams{ReleaseID: input.release.ID, Hostname: claim.Hostname, Service: domain.Service, ContainerPort: int64(domain.Port)}); err != nil {
			return Snapshot{}, err
		}
	}
	return OperationSnapshot(ctx, queries, operationID)
}

// OperationSnapshot validates exact stored release/operation relationships and
// the complete captured route set. It never resolves current desired settings.
func OperationSnapshot(ctx context.Context, queries *store.Queries, operationID string) (Snapshot, error) {
	input, err := readOperationInput(ctx, queries, operationID)
	if err != nil {
		return Snapshot{}, err
	}
	links, err := queries.ListReleaseRoutes(ctx, input.release.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if len(links) != len(input.domains) {
		return Snapshot{}, ErrInvalid
	}
	snapshot := Snapshot{SchemaVersion: 1, OperationID: input.operation.ID, ReleaseID: input.release.ID, EnvironmentID: input.release.EnvironmentID, ServerID: input.release.ServerID, Services: input.services, Bindings: make([]Binding, 0, len(links))}
	for i, link := range links {
		domain := input.domains[i]
		if link.Hostname != domain.Hostname || link.Service != domain.Service || link.ContainerPort != int64(domain.Port) || link.ContainerPort < 1 || link.ContainerPort > 65535 || link.EnvironmentID != snapshot.EnvironmentID || link.ServerID != snapshot.ServerID || !canonicalID(link.ClaimID) || !canonicalID(link.InstallationID) || !canonicalID(link.TunnelID) || !cloudflareID(link.AccountID) || !cloudflareID(link.ZoneID) || !withinZone(link.Hostname, link.ZoneName) || link.CredentialVersion < 1 {
			return Snapshot{}, ErrOwnership
		}
		if i == 0 {
			snapshot.InstallationID = link.InstallationID
			snapshot.AccountID = link.AccountID
			snapshot.TunnelID = link.TunnelID
		}
		if snapshot.InstallationID != link.InstallationID || snapshot.AccountID != link.AccountID || snapshot.TunnelID != link.TunnelID {
			return Snapshot{}, ErrOwnership
		}
		snapshot.Bindings = append(snapshot.Bindings, Binding{ClaimID: link.ClaimID, Hostname: link.Hostname, ZoneID: link.ZoneID, ZoneName: link.ZoneName, CredentialVersion: link.CredentialVersion, Service: link.Service, ContainerPort: uint32(link.ContainerPort)})
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	hash := sha256.Sum256(encoded)
	snapshot.SHA256 = hex.EncodeToString(hash[:])
	return snapshot, nil
}
func cloudflareID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}
func withinZone(hostname, zone string) bool {
	return zone != "" && (hostname == zone || strings.HasSuffix(hostname, "."+zone))
}
func selectZone(hostname string, zones []store.ListCloudflareZonesRow) (store.ListCloudflareZonesRow, bool) {
	var selected store.ListCloudflareZonesRow
	for _, zone := range zones {
		if cloudflareID(zone.ID) && withinZone(hostname, zone.Name) && len(zone.Name) > len(selected.Name) {
			selected = zone
		}
	}
	return selected, selected.ID != ""
}
