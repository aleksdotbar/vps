package githubflow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestRateLimitHintsRemainSafeAndSharedAcrossClients(t *testing.T) {
	service := setupService(t)
	calls := 0
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"private-provider-diagnostic","documentation_url":"https://docs.github.com/rest/overview/resources-in-the-rest-api#secondary-rate-limits"}`)), Request: request}, nil
	})
	client, err := service.client("fixture")
	if err != nil {
		t.Fatal(err)
	}
	_, response, err := client.Actions.DownloadArtifact(context.Background(), "operator", "generic", 50, 0)
	failure := service.failure(context.Background(), response, err)
	var safe *Failure
	if !errors.As(failure, &safe) || !safe.RateLimited || safe.Permanent || safe.After < time.Minute || strings.Contains(safe.Error(), "private-provider-diagnostic") {
		t.Fatalf("unsafe/misclassified hintless artifact limit: %v", failure)
	}
	other, err := service.client("different-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.Users.Get(context.Background(), "operator"); err == nil {
		t.Fatal("shared provider cooldown ignored")
	}
	if calls != 1 {
		t.Fatalf("cooldown issued %d provider calls", calls)
	}
	headers := make(http.Header)
	headers.Set("Retry-After", "7200")
	long := service.failure(context.Background(), &github.Response{Response: &http.Response{StatusCode: http.StatusTooManyRequests, Header: headers}}, errors.New("private-provider-diagnostic"))
	if !errors.As(long, &safe) || safe.After != 2*time.Hour || service.providerDelay() < 119*time.Minute {
		t.Fatal("provider deadline truncated to job wake bound")
	}
}
func TestLongProviderDeadlineSurvivesRestartButRequestStillExpires(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	id := f.request(t)
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if wake, err := f.builds.retry(ctx, waiting, &Failure{After: 2 * time.Hour, RateLimited: true}); err != nil || wake != time.Hour {
		t.Fatalf("invalid bounded wake: %v", err)
	}
	waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.NextPollAt < time.Now().Add(119*time.Minute).UnixMilli() {
		t.Fatal("provider deadline not persisted")
	}
	restarted := New(f.service.database, f.service.cipher, f.service.origin, fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("provider contacted before persisted deadline")
		return nil, ErrProvider
	}))
	f.builds.integration = restarted
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != time.Hour {
		t.Fatalf("restart lost provider deadline: %v", err)
	}
	if _, err := f.service.database.ExecContext(ctx, "UPDATE waiting_builds SET expires_at=? WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != 0 {
		t.Fatalf("expired request kept snoozing: %v", err)
	}
	waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "failed" {
		t.Fatal("expired request not terminal")
	}
}
func TestTokenReuseSeparatesRepositoryPermissionAndAuthFailure(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	for range 2 {
		if _, err := f.service.installationClient(ctx, []int64{1}); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokenMints.Load() != 1 {
		t.Fatalf("repository token not reused: %d", f.tokenMints.Load())
	}
	if err := store.New(f.service.database).CreateGitHubRepository(ctx, store.CreateGitHubRepositoryParams{ID: 3, Owner: "operator", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.installationClient(ctx, []int64{3}); err != nil {
		t.Fatal(err)
	}
	if f.tokenMints.Load() != 2 {
		t.Fatal("repository token scope was reused across repositories")
	}
	f.service.clearTokensOnFailure(&Failure{AuthRejected: true})
	if _, err := f.service.installationClient(ctx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if f.tokenMints.Load() != 3 {
		t.Fatal("auth failure retained a stale token")
	}
	// Token values remain memory-only and never become encrypted credential rows.
	var rows int
	if err := f.service.database.QueryRowContext(ctx, "SELECT count(*) FROM encrypted_versions WHERE kind='credential'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("ephemeral token persisted: %v", err)
	}
}

func TestRouteAdmissionFailuresStopWaitingBuildWithoutProviderBackoff(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  error
		reason string
	}{
		{"ownership", routes.ErrOwnership, "route_ownership_conflict"},
		{"setup", routes.ErrSetup, "route_setup_required"},
		{"capacity", routes.ErrCapacity, "route_capacity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := buildSetup(t)
			id := fixture.request(t)
			waiting, err := store.New(fixture.service.database).GetWaitingBuild(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			before := fixture.sourceReads.Load()
			delay, err := fixture.builds.retry(t.Context(), waiting, test.cause)
			if err != nil || delay != 0 {
				t.Fatalf("route admission entered provider backoff: %v %v", delay, err)
			}
			waiting, err = store.New(fixture.service.database).GetWaitingBuild(t.Context(), id)
			if err != nil || waiting.State != "failed" || waiting.Reason != test.reason || fixture.sourceReads.Load() != before {
				t.Fatalf("route failure lost actionable state: %v", err)
			}
		})
	}
}
