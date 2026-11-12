package cloudflareflow

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/dns"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

var (
	ErrInvalid  = errors.New("invalid Cloudflare connection input")
	ErrProvider = errors.New("verify Cloudflare connection")
	ErrScope    = errors.New("reject Cloudflare token or selected zone")
	ErrBusy     = errors.New("busy Cloudflare connection")
	idPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	zoneName    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// RejectedError is a definite provider rejection with fixed operator guidance.
// It matches ErrScope so existing callers keep treating it as a refusal.
type RejectedError struct{ Guidance string }

func (e RejectedError) Error() string        { return e.Guidance }
func (e RejectedError) Is(target error) bool { return target == ErrScope }

// rejection turns a definite client-side refusal into guidance; transport
// failures, timeouts, rate limits and server errors stay ErrProvider (retryable).
func rejection(err error, guidance string) error {
	var provider *cloudflare.Error
	if errors.As(err, &provider) && provider.StatusCode >= 400 && provider.StatusCode < 500 && provider.StatusCode != http.StatusTooManyRequests && provider.StatusCode != http.StatusRequestTimeout {
		return RejectedError{guidance}
	}
	return ErrProvider
}

const (
	guidanceAccountToken = "Cloudflare rejected this token for the account. Use an account-owned API token (Manage Account → Account API Tokens) created in this account."
	guidanceInactive     = "This Cloudflare token is not active, has expired or is not valid yet."
	guidanceTunnel       = "The token needs the account permission Cloudflare One Connector: cloudflared → Edit."
)

func guidanceZoneRead(id string) string {
	return "The token cannot read zone " + id + ". Give it Zone → Read and DNS → Edit on that zone, and check the zone belongs to this account."
}

func guidanceDNS(name string) string {
	return "The token cannot manage DNS records in " + name + ". Give it DNS → Edit on that zone."
}

// Bound fixed-origin SDK responses and redact transport errors.
type apiTransport struct{ base http.RoundTripper }

func (t apiTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "api.cloudflare.com" || request.URL.User != nil {
		return nil, ErrProvider
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, ErrProvider
	}
	response.Body = http.MaxBytesReader(nil, response.Body, 1<<20)
	return response, nil
}

func (s *Service) options(token string) []option.RequestOption {
	return []option.RequestOption{
		option.WithBaseURL("https://api.cloudflare.com/client/v4/"),
		option.WithAPIToken(token),
		option.WithMaxRetries(0),
		option.WithHTTPClient(&http.Client{
			Transport: apiTransport{s.transport}, Timeout: 12 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
	}
}

type verifiedAccount struct {
	tokenID   string
	expiresAt time.Time
	zones     []verifiedZone
}

type verifiedZone struct{ id, name string }

func (s *Service) verify(ctx context.Context, account, token string, zoneIDs []string) (verifiedAccount, error) {
	options := s.options(token)
	// Service constructors deliberately avoid NewClient's CLOUDFLARE_* env
	// credentials, base URL and custom headers. Only explicit options apply.
	tokenService := accounts.NewTokenService(options...)
	var tokenParams accounts.TokenVerifyParams
	tokenParams.AccountID.Value, tokenParams.AccountID.Present = account, true
	verified, err := tokenService.Verify(ctx, tokenParams)
	if err != nil || verified == nil {
		return verifiedAccount{}, rejection(err, guidanceAccountToken)
	}
	now := time.Now()
	if !idPattern.MatchString(verified.ID) || verified.Status != accounts.TokenVerifyResponseStatusActive || verified.JSON.ExpiresOn.IsInvalid() || verified.JSON.NotBefore.IsInvalid() || (!verified.ExpiresOn.IsZero() && !verified.ExpiresOn.After(now)) || (!verified.NotBefore.IsZero() && verified.NotBefore.After(now)) {
		return verifiedAccount{}, RejectedError{guidanceInactive}
	}
	result := verifiedAccount{tokenID: verified.ID, expiresAt: verified.ExpiresOn}
	// Sample one bounded page to establish usable read access, without requiring
	// token-policy/list-all permissions or pretending these reads prove writes.
	var tunnelParams zero_trust.TunnelCloudflaredListParams
	tunnelParams.AccountID.Value, tunnelParams.AccountID.Present = account, true
	tunnelParams.PerPage.Value, tunnelParams.PerPage.Present = 1, true
	tunnels, err := zero_trust.NewTunnelCloudflaredService(options...).List(ctx, tunnelParams)
	if err != nil || tunnels == nil {
		return verifiedAccount{}, rejection(err, guidanceTunnel)
	}
	zoneService := zones.NewZoneService(options...)
	recordService := dns.NewRecordService(options...)
	seenNames := make(map[string]bool, len(zoneIDs))
	for _, id := range zoneIDs {
		var params zones.ZoneGetParams
		params.ZoneID.Value, params.ZoneID.Present = id, true
		zone, err := zoneService.Get(ctx, params)
		if err != nil || zone == nil {
			return verifiedAccount{}, rejection(err, guidanceZoneRead(id))
		}
		if guidance := zoneProblem(zone, id, account); guidance != "" || seenNames[zone.Name] {
			if guidance == "" {
				guidance = "Each selected zone must be different."
			}
			return verifiedAccount{}, RejectedError{guidance}
		}
		seenNames[zone.Name] = true
		var recordParams dns.RecordListParams
		recordParams.ZoneID.Value, recordParams.ZoneID.Present = id, true
		recordParams.PerPage.Value, recordParams.PerPage.Present = 1, true
		records, err := recordService.List(ctx, recordParams)
		if err != nil || records == nil {
			return verifiedAccount{}, rejection(err, guidanceDNS(zone.Name))
		}
		result.zones = append(result.zones, verifiedZone{id: id, name: zone.Name})
	}
	if !result.expiresAt.IsZero() && !result.expiresAt.After(time.Now()) {
		return verifiedAccount{}, RejectedError{guidanceInactive}
	}
	return result, nil
}

// zoneProblem explains why a zone cannot hold panel routes, or returns "".
func zoneProblem(zone *zones.Zone, id, account string) string {
	switch {
	case zone.ID != id || !zoneName.MatchString(zone.Name) || len(zone.Name) > 253:
		return "Cloudflare returned an unexpected zone for " + id + "."
	case zone.Account.ID != account:
		return "Zone " + zone.Name + " belongs to a different Cloudflare account."
	case zone.Status != zones.ZoneStatusActive:
		return "Zone " + zone.Name + " is not active yet; finish its nameserver setup in Cloudflare."
	case zone.Type != zones.TypeFull:
		return "Zone " + zone.Name + " must use a full DNS setup, not partial or secondary."
	case zone.Paused || zone.JSON.Paused.IsInvalid():
		return "Zone " + zone.Name + " is paused in Cloudflare."
	}
	return ""
}

// CandidateZone is a zone a candidate token can read, before it is saved.
type CandidateZone struct {
	ID, Name, Status, Type string
	Paused, Eligible       bool
}

const candidatePageSize, candidatePages = 50, 2

// listCandidateZones reads at most two pages of the account's zones.
func (s *Service) listCandidateZones(ctx context.Context, account, token string) ([]CandidateZone, bool, error) {
	service := zones.NewZoneService(s.options(token)...)
	result := []CandidateZone{}
	for page := 1; page <= candidatePages; page++ {
		var params zones.ZoneListParams
		params.Account.Value, params.Account.Present = zones.ZoneListParamsAccount{ID: cloudflare.F(account)}, true
		params.Page.Value, params.Page.Present = float64(page), true
		params.PerPage.Value, params.PerPage.Present = candidatePageSize, true
		listed, err := service.List(ctx, params)
		if err != nil || listed == nil {
			return nil, false, rejection(err, guidanceAccountToken)
		}
		for _, zone := range listed.Result {
			if !idPattern.MatchString(zone.ID) || !zoneName.MatchString(zone.Name) || len(zone.Name) > 253 || zone.Account.ID != account {
				continue
			}
			value := zone
			result = append(result, CandidateZone{ID: zone.ID, Name: zone.Name, Status: string(zone.Status), Type: string(zone.Type), Paused: zone.Paused, Eligible: zoneProblem(&value, zone.ID, account) == ""})
		}
		if len(listed.Result) < candidatePageSize {
			return result, false, nil
		}
	}
	return result, true, nil
}
