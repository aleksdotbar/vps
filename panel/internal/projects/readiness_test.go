package projects

import (
	"errors"
	"testing"
	"time"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/readiness"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestStaleServerEvidenceCannotAdmitNewOrCapturedRelease(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	ctx := t.Context()
	queries := store.New(database)
	if err := queries.ObserveServer(ctx, store.ObserveServerParams{ID: input.Selection.Environment.ServerID, Readiness: "ready", ObservedAt: time.Now().Add(-readiness.MaxAge - time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, captureErr := service.CaptureSelection(ctx, tx, input.Intent.EnvironmentID, input.Intent.ExpectedVersion)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(captureErr, ErrServer) {
		t.Fatalf("stale readiness admitted selection: %v", captureErr)
	}
	if _, _, err := service.QueueRelease(ctx, runtime, resolver, input); !errors.Is(err, ErrServer) {
		t.Fatalf("captured release bypassed stale readiness: %v", err)
	}
	server, err := queries.GetServer(ctx, input.Selection.Environment.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if view := serverMessage(server); view.Readiness != panelv1.ServerReadiness_SERVER_READINESS_UNSPECIFIED || view.ObservedAt == nil {
		t.Fatal("stale observation must retain its timestamp without claiming readiness or a failed connection")
	}
	var releases int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM releases").Scan(&releases); err != nil || releases != 0 {
		t.Fatalf("stale admission left a release: count=%d error=%v", releases, err)
	}
}
