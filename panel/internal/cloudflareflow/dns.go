package cloudflareflow

import (
	"context"
	"database/sql"
	"errors"
	"github.com/cloudflare/cloudflare-go/v7/dns"
	"time"

	"github.com/google/uuid"

	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var (
	ErrDNSPrecondition = errors.New("DNS operation requires matching active native route prerequisites")
	ErrDNSConflict     = errors.New("DNS record conflicts with retained hostname ownership")
	ErrDNSUnknown      = errors.New("DNS create outcome requires read-only reconciliation")
	errDNSAbsent       = errors.New("no DNS record observed")
)

// DNSAuthorizer must verify fresh native candidate/route prerequisites over
// pinned SSH for these exact stored identities. It must not trust a caller's
// healthy flag or interpret DNS observation as deployment success. The P09
// coordinator owns that implementation; there is no production caller yet.
type DNSAuthorizer interface {
	AuthorizeDNS(context.Context, store.Operation, routes.Snapshot, string) error
}

type DNSRecords struct {
	connection *Service
	authorizer DNSAuthorizer
}

func NewDNSRecords(connection *Service, authorizer DNSAuthorizer) *DNSRecords {
	return &DNSRecords{connection: connection, authorizer: authorizer}
}

type DNSRequest struct {
	OperationID         string
	Fence               int64
	RouteSnapshotSHA256 string
	Hostname            string
}

// DNSObservation proves only a matching provider record was read. Native route
// application and deployment health require their own subsequent observations.
type DNSObservation struct {
	ClaimID    string
	RecordID   string
	ObservedAt time.Time
}

type dnsInput struct {
	operation         store.Operation
	snapshot          routes.Snapshot
	binding           routes.Binding
	credentialVersion int64
}

func readDNSInput(ctx context.Context, queries *store.Queries, request DNSRequest) (dnsInput, *store.DnsRecord, error) {
	parsed, err := uuid.Parse(request.OperationID)
	if err != nil || parsed == uuid.Nil || parsed.String() != request.OperationID || request.Fence < 1 {
		return dnsInput{}, nil, ErrDNSPrecondition
	}
	operation, err := queries.GetOperation(ctx, request.OperationID)
	if err != nil {
		return dnsInput{}, nil, err
	}
	if !operation.Started || operation.Fence != request.Fence || (operation.State != "running" && operation.State != "recovering") || operation.ExecutionDeadline <= time.Now().UnixMilli() {
		return dnsInput{}, nil, ErrDNSPrecondition
	}
	snapshot, err := routes.OperationSnapshot(ctx, queries, operation.ID)
	if err != nil {
		return dnsInput{}, nil, err
	}
	if snapshot.SHA256 != request.RouteSnapshotSHA256 {
		return dnsInput{}, nil, ErrDNSPrecondition
	}
	var binding routes.Binding
	for _, value := range snapshot.Bindings {
		if value.Hostname == request.Hostname {
			binding = value
			break
		}
	}
	if binding.ClaimID == "" {
		return dnsInput{}, nil, ErrDNSPrecondition
	}
	current, err := queries.GetCloudflareIntegration(ctx)
	if err != nil {
		return dnsInput{}, nil, err
	}
	if current.AccountID != snapshot.AccountID || current.Version < 1 || (current.ExpiresAt.Valid && current.ExpiresAt.Int64 <= time.Now().UnixMilli()) {
		return dnsInput{}, nil, ErrDNSPrecondition
	}
	tunnel, err := queries.GetServerTunnel(ctx, snapshot.ServerID)
	if err != nil {
		return dnsInput{}, nil, err
	}
	if tunnel.Phase != "ready" || !tunnel.TunnelID.Valid || tunnel.TunnelID.String != snapshot.TunnelID || tunnel.AccountID != snapshot.AccountID || tunnel.InstallationID != snapshot.InstallationID {
		return dnsInput{}, nil, ErrDNSPrecondition
	}
	zones, err := queries.ListCloudflareZones(ctx)
	if err != nil {
		return dnsInput{}, nil, err
	}
	found := false
	for _, zone := range zones {
		if zone.ID == binding.ZoneID && zone.Name == binding.ZoneName {
			found = true
			break
		}
	}
	if !found {
		return dnsInput{}, nil, ErrDNSPrecondition
	}
	input := dnsInput{operation: operation, snapshot: snapshot, binding: binding, credentialVersion: current.Version}
	attempt, err := queries.GetDNSRecord(ctx, binding.ClaimID)
	if errors.Is(err, sql.ErrNoRows) {
		return input, nil, nil
	}
	if err != nil {
		return dnsInput{}, nil, err
	}
	// Reconciliation preserves the original POST's evidence even under a newer
	// operation/fence or rotated token. It cannot authorize another POST.
	original, err := routes.OperationSnapshot(ctx, queries, attempt.OperationID)
	if err != nil {
		return dnsInput{}, nil, err
	}
	if original.SHA256 != attempt.RouteSnapshotSha256 || original.InstallationID != snapshot.InstallationID || original.AccountID != snapshot.AccountID || original.TunnelID != snapshot.TunnelID || (attempt.RecordID.Valid && !idPattern.MatchString(attempt.RecordID.String)) {
		return dnsInput{}, nil, ErrDNSConflict
	}
	for _, value := range original.Bindings {
		// Service/container port may change between admitted releases; provider
		// identity remains bound to the same permanent hostname claim.
		if value.ClaimID == binding.ClaimID && value.Hostname == binding.Hostname && value.ZoneID == binding.ZoneID && value.ZoneName == binding.ZoneName && value.CredentialVersion == binding.CredentialVersion {
			return input, &attempt, nil
		}
	}
	return dnsInput{}, nil, ErrDNSConflict
}

func (d *DNSRecords) read(ctx context.Context, request DNSRequest) (dnsInput, *store.DnsRecord, error) {
	tx, err := d.connection.database.BeginTx(ctx, nil)
	if err != nil {
		return dnsInput{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return readDNSInput(ctx, store.New(tx), request)
}

func (d *DNSRecords) token(ctx context.Context, version int64) ([]byte, error) {
	token, err := d.connection.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: "cloudflare-api", Version: version})
	if err != nil {
		return nil, ErrProvider
	}
	return token, nil
}

// claim rechecks state/fence/deadline and immutable input after native/provider
// reads. Only its insert winner may issue POST, after this transaction commits.
func (d *DNSRecords) claim(ctx context.Context, request DNSRequest) (dnsInput, store.DnsRecord, bool, error) {
	tx, err := d.connection.database.BeginTx(ctx, nil)
	if err != nil {
		return dnsInput{}, store.DnsRecord{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	input, attempt, err := readDNSInput(ctx, queries, request)
	if err != nil {
		return dnsInput{}, store.DnsRecord{}, false, err
	}
	if attempt != nil {
		return input, *attempt, false, nil
	}
	changed, err := queries.ClaimDNSCreate(ctx, store.ClaimDNSCreateParams{ClaimID: input.binding.ClaimID, OperationID: input.operation.ID, OperationFence: input.operation.Fence, RouteSnapshotSha256: input.snapshot.SHA256, CredentialVersion: input.credentialVersion, CreateAttemptedAt: time.Now().UnixMilli()})
	if err != nil {
		return dnsInput{}, store.DnsRecord{}, false, err
	}
	row, err := queries.GetDNSRecord(ctx, input.binding.ClaimID)
	if err != nil {
		return dnsInput{}, store.DnsRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return dnsInput{}, store.DnsRecord{}, false, err
	}
	return input, row, changed == 1, nil
}

func (d *DNSRecords) bind(ctx context.Context, request DNSRequest, recordID string, observedAt int64) (DNSObservation, error) {
	tx, err := d.connection.database.BeginTx(ctx, nil)
	if err != nil {
		return DNSObservation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	input, attempt, err := readDNSInput(ctx, queries, request)
	if err != nil {
		return DNSObservation{}, err
	}
	if attempt == nil || !idPattern.MatchString(recordID) {
		return DNSObservation{}, ErrDNSConflict
	}
	id := sql.NullString{String: recordID, Valid: true}
	changed, err := queries.BindDNSRecord(ctx, store.BindDNSRecordParams{RecordID: id, RecordID_2: id, ClaimID: input.binding.ClaimID, ObservedAt: max(observedAt, attempt.ObservedAt)})
	if err != nil {
		return DNSObservation{}, err
	}
	if changed != 1 {
		return DNSObservation{}, ErrDNSConflict
	}
	if err := tx.Commit(); err != nil {
		return DNSObservation{}, err
	}
	return DNSObservation{ClaimID: input.binding.ClaimID, RecordID: recordID, ObservedAt: time.UnixMilli(observedAt)}, nil
}

// Ensure consumes only stored admitted routes. Every invocation is bounded by
// the caller, the operation's durable deadline and a 12-second local limit.
// A retained attempt with no matching observation never becomes another POST.
func (d *DNSRecords) Ensure(ctx context.Context, request DNSRequest) (observation DNSObservation, returnErr error) {
	defer func() {
		if recover() != nil {
			observation, returnErr = DNSObservation{}, ErrProvider
		}
	}()
	if d.connection == nil || d.authorizer == nil {
		return DNSObservation{}, ErrDNSPrecondition
	}
	select {
	case d.connection.permits <- struct{}{}:
		defer func() { <-d.connection.permits }()
	default:
		return DNSObservation{}, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	input, attempt, err := d.read(ctx, request)
	if err != nil {
		return DNSObservation{}, err
	}
	ctx, deadlineCancel := context.WithDeadline(ctx, time.UnixMilli(input.operation.ExecutionDeadline))
	defer deadlineCancel()
	if d.authorizer.AuthorizeDNS(ctx, input.operation, input.snapshot, input.binding.Hostname) != nil {
		return DNSObservation{}, ErrDNSPrecondition
	}
	token, err := d.token(ctx, input.credentialVersion)
	if err != nil {
		return DNSObservation{}, err
	}
	defer clear(token)
	record, err := d.find(ctx, dns.NewRecordService(d.connection.options(string(token))...), input.snapshot, input.binding, attempt)
	if err != nil && !errors.Is(err, errDNSAbsent) {
		return DNSObservation{}, err
	}
	if record != nil {
		return d.bind(ctx, request, record.ID, time.Now().UnixMilli())
	}
	if attempt != nil {
		return DNSObservation{}, ErrDNSUnknown
	}
	input, row, won, err := d.claim(ctx, request)
	if err != nil {
		return DNSObservation{}, err
	}
	// The current same-account version selected inside the claim transaction is
	// separate from the route's admitted version and immutable POST audit version.
	currentToken, err := d.token(ctx, input.credentialVersion)
	if err != nil {
		return DNSObservation{}, err
	}
	defer clear(currentToken)
	if won {
		record, err = d.create(ctx, dns.NewRecordService(d.connection.options(string(currentToken))...), input.snapshot, input.binding, row)
		if err != nil {
			return DNSObservation{}, err
		}
		if _, err := d.bind(ctx, request, record.ID, 0); err != nil {
			return DNSObservation{}, err
		}
		row.RecordID = sql.NullString{String: record.ID, Valid: true}
	}
	record, err = d.find(ctx, dns.NewRecordService(d.connection.options(string(currentToken))...), input.snapshot, input.binding, &row)
	if errors.Is(err, errDNSAbsent) {
		return DNSObservation{}, ErrDNSUnknown
	}
	if err != nil {
		return DNSObservation{}, err
	}
	return d.bind(ctx, request, record.ID, time.Now().UnixMilli())
}
