package cloudflareflow

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/mutations"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var ErrZoneInUse = errors.New("selected Cloudflare zone owns retained route claims")

const connectProcedure = "panel.v1.CloudflareService/ConnectAccount"

type Service struct {
	database  *sql.DB
	vault     *securestate.Vault
	mutations *mutations.Service
	transport http.RoundTripper
	permits   chan struct{}
}

func New(database *sql.DB, cipher *securestate.Cipher, transport http.RoundTripper) *Service {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Service{database: database, vault: securestate.NewVault(database, cipher), mutations: mutations.New(database, cipher), transport: transport, permits: make(chan struct{}, 2)}
}

func (s *Service) GetIntegration(ctx context.Context) (*panelv1.GetCloudflareIntegrationResponse, error) {
	// One read transaction keeps connection version and selected zones coherent
	// with a concurrent credential replacement.
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := integration(ctx, store.New(tx))
	if err != nil {
		return nil, err
	}
	return &panelv1.GetCloudflareIntegrationResponse{Integration: value}, nil
}

func integration(ctx context.Context, queries *store.Queries) (*panelv1.CloudflareIntegration, error) {
	row, err := queries.GetCloudflareIntegration(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return new(panelv1.CloudflareIntegration), nil
	}
	if err != nil {
		return nil, err
	}
	if row.Version < 1 {
		return nil, errors.New("invalid stored Cloudflare credential version")
	}
	zones, err := queries.ListCloudflareZones(ctx)
	if err != nil {
		return nil, err
	}
	result := &panelv1.CloudflareIntegration{Configured: true, Version: uint64(row.Version), AccountId: row.AccountID, TokenId: row.TokenID, VerifiedAt: timestamppb.New(time.UnixMilli(row.VerifiedAt))}
	if row.ExpiresAt.Valid {
		result.ExpiresAt = timestamppb.New(time.UnixMilli(row.ExpiresAt.Int64))
	}
	for _, zone := range zones {
		result.Zones = append(result.Zones, &panelv1.CloudflareZone{Id: zone.ID, Name: zone.Name})
	}
	return result, nil
}

func (s *Service) ConnectAccount(ctx context.Context, request *panelv1.ConnectAccountRequest) (*panelv1.ConnectAccountResponse, error) {
	version := request.GetExpectedVersion()
	if request == nil || version > math.MaxInt64-1 || protovalidate.Validate(request) != nil {
		return nil, ErrInvalid
	}
	canonical := proto.Clone(request).(*panelv1.ConnectAccountRequest)
	sort.Strings(canonical.ZoneIds)
	value, err := s.mutations.Begin(ctx, connectProcedure, canonical.GetMutation().GetRequestId(), canonical)
	if err != nil {
		return nil, err
	}
	if value.Response != nil {
		defer func() { _ = value.Tx.Rollback() }()
		return replay(value.Response)
	}
	if err := checkConnectionChange(ctx, store.New(value.Tx), int64(version), canonical.AccountId, canonical.ZoneIds); err != nil {
		_ = value.Tx.Rollback()
		return nil, err
	}
	// Never retain the sole SQLite connection while waiting on provider I/O.
	if err := value.Tx.Rollback(); err != nil {
		return nil, err
	}
	select {
	case s.permits <- struct{}{}:
		defer func() { <-s.permits }()
	default:
		return nil, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	verified, err := s.verify(ctx, canonical.AccountId, canonical.Token, canonical.ZoneIds)
	if err != nil {
		return nil, err
	}
	value, err = s.mutations.Begin(ctx, connectProcedure, canonical.GetMutation().GetRequestId(), canonical)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		return replay(value.Response)
	}
	if err := checkConnectionChange(ctx, store.New(value.Tx), int64(version), canonical.AccountId, canonical.ZoneIds); err != nil {
		return nil, err
	}
	queries := store.New(value.Tx)
	previousZones, err := queries.ListCloudflareZones(ctx)
	if err != nil {
		return nil, err
	}
	claimedZones, err := queries.ListClaimedCloudflareZones(ctx)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]string, len(verified.zones))
	for _, zone := range verified.zones {
		selected[zone.id] = zone.name
	}
	for _, zone := range claimedZones {
		if selected[zone.ZoneID] != zone.ZoneName {
			return nil, ErrZoneInUse
		}
	}
	ref, err := s.vault.SaveCredentialTx(ctx, value.Tx, "cloudflare-api", int64(version), []byte(canonical.Token))
	if err != nil {
		return nil, err
	}
	expires := sql.NullInt64{}
	if !verified.expiresAt.IsZero() {
		expires = sql.NullInt64{Int64: verified.expiresAt.UnixMilli(), Valid: true}
	}
	if err := queries.SetCloudflareIntegration(ctx, store.SetCloudflareIntegrationParams{AccountID: canonical.AccountId, TokenID: verified.tokenID, Version: ref.Version, ExpiresAt: expires, VerifiedAt: time.Now().UnixMilli()}); err != nil {
		return nil, err
	}
	for _, zone := range previousZones {
		if _, retained := selected[zone.ID]; !retained {
			if err := queries.DeleteCloudflareZone(ctx, zone.ID); err != nil {
				return nil, err
			}
		}
	}
	for _, zone := range verified.zones {
		if err := queries.UpsertCloudflareZone(ctx, store.UpsertCloudflareZoneParams{ID: zone.id, Name: zone.name}); err != nil {
			return nil, err
		}
	}
	metadata, err := integration(ctx, queries)
	if err != nil {
		return nil, err
	}
	response := &panelv1.ConnectAccountResponse{Integration: metadata}
	if err := s.mutations.Commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}

func checkVersion(ctx context.Context, queries *store.Queries, expected int64) error {
	row, err := queries.GetCloudflareIntegration(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		if expected == 0 {
			return nil
		}
		return securestate.ErrStaleVersion
	}
	if err != nil {
		return err
	}
	if row.Version != expected {
		return securestate.ErrStaleVersion
	}
	return nil
}

func replay(value []byte) (*panelv1.ConnectAccountResponse, error) {
	response := new(panelv1.ConnectAccountResponse)
	if err := proto.Unmarshal(value, response); err != nil {
		return nil, err
	}
	return response, nil
}

func checkConnectionChange(ctx context.Context, queries *store.Queries, version int64, account string, zoneIDs []string) error {
	if err := checkVersion(ctx, queries, version); err != nil {
		return err
	}
	current, err := queries.GetCloudflareIntegration(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(zoneIDs))
	for _, id := range zoneIDs {
		selected[id] = true
	}
	claimedZones, err := queries.ListClaimedCloudflareZones(ctx)
	if err != nil {
		return err
	}
	for _, zone := range claimedZones {
		if !selected[zone.ZoneID] {
			return ErrZoneInUse
		}
	}
	if current.AccountID != account {
		count, err := queries.CloudflareAccountTunnelCount(ctx, current.AccountID)
		if err != nil {
			return err
		}
		if count > 0 {
			return ErrAccountInUse
		}
	}
	return nil
}

// ListCandidateZones lists zones an unsaved token can read so the operator can
// pick by name. The token is used for this request only and never retained.
func (s *Service) ListCandidateZones(ctx context.Context, request *panelv1.ListCandidateZonesRequest) (*panelv1.ListCandidateZonesResponse, error) {
	if request == nil || protovalidate.Validate(request) != nil {
		return nil, ErrInvalid
	}
	select {
	case s.permits <- struct{}{}:
		defer func() { <-s.permits }()
	default:
		return nil, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	listed, truncated, err := s.listCandidateZones(ctx, request.GetAccountId(), request.GetToken())
	if err != nil {
		return nil, err
	}
	response := &panelv1.ListCandidateZonesResponse{Truncated: truncated}
	for _, zone := range listed {
		response.Zones = append(response.Zones, &panelv1.CandidateZone{Id: zone.ID, Name: zone.Name, Status: zone.Status, Type: zone.Type, Paused: zone.Paused, Eligible: zone.Eligible})
	}
	return response, nil
}
