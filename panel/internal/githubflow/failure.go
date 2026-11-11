package githubflow

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/go-github/v93/github"
)

// Failure retains only safe provider control data, never an SDK error/body/URL.
type Failure struct {
	After        time.Duration
	RateLimited  bool
	Permanent    bool
	AuthRejected bool
}

func (*Failure) Error() string { return "GitHub request could not be completed" }
func (*Failure) Unwrap() error { return ErrProvider }

// ProviderFailure retains safe retry/status information and discards SDK text.
func ProviderFailure(ctx context.Context, response *github.Response, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, ErrBusy) {
		return ErrBusy
	}
	var safe *Failure
	if errors.As(cause, &safe) {
		copy := *safe
		return &copy
	}
	result := &Failure{}
	limited := false
	var primary *github.RateLimitError
	if errors.As(cause, &primary) {
		limited = true
		result.After = time.Until(primary.Rate.Reset.Time)
	}
	var secondary *github.AbuseRateLimitError
	if errors.As(cause, &secondary) {
		limited = true
		result.After = time.Minute
		if secondary.RetryAfter != nil && *secondary.RetryAfter > result.After {
			result.After = *secondary.RetryAfter
		}
	}
	if response != nil && response.Response != nil {
		if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
			result.After = time.Duration(seconds) * time.Second
		}
		if reset, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && response.Header.Get("X-RateLimit-Remaining") == "0" {
			result.After = time.Until(time.Unix(reset, 0))
		}
		limited = limited || response.StatusCode == http.StatusTooManyRequests || result.After > 0
		result.AuthRejected = response.StatusCode == http.StatusUnauthorized || (response.StatusCode == http.StatusForbidden && !limited)
		result.Permanent = response.StatusCode >= 400 && response.StatusCode < 500 && !limited
	}
	if limited && result.After < time.Minute {
		result.After = time.Minute
	}
	if result.After < 0 {
		result.After = 0
	}
	result.RateLimited = limited
	return result
}

func (s *Service) clearTokensOnFailure(cause error) {
	var failure *Failure
	if !errors.As(cause, &failure) {
		return
	}
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if failure.AuthRejected {
		clear(s.tokens)
	}
	if failure.RateLimited && failure.After > 0 {
		until := time.Now().Add(failure.After)
		if until.After(s.cooldownUntil) {
			s.cooldownUntil = until
		}
	}
}
func (s *Service) providerDelay() time.Duration {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	delay := time.Until(s.cooldownUntil)
	if delay < 0 {
		return 0
	}
	return delay
}

func (s *Service) failure(ctx context.Context, response *github.Response, cause error) error {
	failure := ProviderFailure(ctx, response, cause)
	s.clearTokensOnFailure(failure)
	return failure
}
