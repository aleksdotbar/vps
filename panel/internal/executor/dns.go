package executor

import (
	"context"
	"database/sql"
	"errors"

	"github.com/aleksdotbar/vps/panel/internal/cloudflareflow"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

// AuthorizeDNS is invoked by the provider boundary before any DNS mutation. It
// re-observes exact candidate health and owned native routes over pinned SSH.
func (s *Service) AuthorizeDNS(ctx context.Context, operation store.Operation, snapshot routes.Snapshot, hostname string) error {
	input, err := loadInput(ctx, s.database, operation)
	if err != nil {
		return err
	}
	if input.manifest.RouteSnapshotSHA256 != snapshot.SHA256 {
		return errExecutionState
	}
	found := false
	for _, binding := range input.manifest.RouteSnapshot.Bindings {
		found = found || binding.Hostname == hostname
	}
	if !found {
		return errExecutionState
	}
	connection, err := s.connect(ctx, input)
	if err != nil {
		return err
	}
	defer clear(connection.private)
	system, err := s.ready(ctx, connection, input)
	if err != nil {
		return err
	}
	observed, err := s.control(ctx, connection, input, system, "deploy_observe")
	if err != nil {
		return err
	}
	if observed.Phase != "awaiting_dns" {
		return errObservation
	}
	return s.verifyServices(ctx, input, observed, input.operation.ReleaseID)
}

// At most four provider checks per attempt keep DNS work below the worker's
// timeout. Permanent claims belong to one environment; its execution exclusion
// means a later confirmed observation cannot come from another deployment.
// Captured creation time is stable across retries, unlike operation.updated_at.
func (s *Service) ensureDNS(ctx context.Context, input executionInput) (bool, error) {
	checked := 0
	for _, binding := range input.manifest.RouteSnapshot.Bindings {
		record, err := store.New(s.database).GetDNSRecord(ctx, binding.ClaimID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if err == nil && record.RecordID.Valid && record.ObservedAt > input.capture.CreatedAt {
			continue
		}
		if checked == 4 {
			return false, nil
		}
		if _, err := s.dns.Ensure(ctx, cloudflareflow.DNSRequest{OperationID: input.operation.ID, Fence: input.operation.Fence, RouteSnapshotSHA256: input.manifest.RouteSnapshotSHA256, Hostname: binding.Hostname}); err != nil {
			return false, err
		}
		checked++
	}
	return true, nil
}

func dnsReason(err error) string {
	switch {
	case errors.Is(err, cloudflareflow.ErrDNSConflict):
		return "record_conflict"
	case errors.Is(err, cloudflareflow.ErrDNSUnknown):
		return "create_outcome_unknown"
	case errors.Is(err, cloudflareflow.ErrDNSPrecondition):
		return "native_prerequisites_changed"
	default:
		return "provider_unavailable"
	}
}
