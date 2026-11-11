package cloudflareflow

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

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
		return verifiedAccount{}, ErrProvider
	}
	now := time.Now()
	if !idPattern.MatchString(verified.ID) || verified.Status != accounts.TokenVerifyResponseStatusActive || verified.JSON.ExpiresOn.IsInvalid() || verified.JSON.NotBefore.IsInvalid() || (!verified.ExpiresOn.IsZero() && !verified.ExpiresOn.After(now)) || (!verified.NotBefore.IsZero() && verified.NotBefore.After(now)) {
		return verifiedAccount{}, ErrScope
	}
	result := verifiedAccount{tokenID: verified.ID, expiresAt: verified.ExpiresOn}
	// Sample one bounded page to establish usable read access, without requiring
	// token-policy/list-all permissions or pretending these reads prove writes.
	var tunnelParams zero_trust.TunnelCloudflaredListParams
	tunnelParams.AccountID.Value, tunnelParams.AccountID.Present = account, true
	tunnelParams.PerPage.Value, tunnelParams.PerPage.Present = 1, true
	tunnels, err := zero_trust.NewTunnelCloudflaredService(options...).List(ctx, tunnelParams)
	if err != nil || tunnels == nil {
		return verifiedAccount{}, ErrProvider
	}
	zoneService := zones.NewZoneService(options...)
	recordService := dns.NewRecordService(options...)
	seenNames := make(map[string]bool, len(zoneIDs))
	for _, id := range zoneIDs {
		var params zones.ZoneGetParams
		params.ZoneID.Value, params.ZoneID.Present = id, true
		zone, err := zoneService.Get(ctx, params)
		if err != nil || zone == nil {
			return verifiedAccount{}, ErrProvider
		}
		if zone.ID != id || zone.Account.ID != account || zone.Status != zones.ZoneStatusActive || zone.Type != zones.TypeFull || zone.Paused || zone.JSON.Paused.IsInvalid() || !zoneName.MatchString(zone.Name) || len(zone.Name) > 253 || seenNames[zone.Name] {
			return verifiedAccount{}, ErrScope
		}
		seenNames[zone.Name] = true
		var recordParams dns.RecordListParams
		recordParams.ZoneID.Value, recordParams.ZoneID.Present = id, true
		recordParams.PerPage.Value, recordParams.PerPage.Present = 1, true
		records, err := recordService.List(ctx, recordParams)
		if err != nil || records == nil {
			return verifiedAccount{}, ErrProvider
		}
		result.zones = append(result.zones, verifiedZone{id: id, name: zone.Name})
	}
	if !result.expiresAt.IsZero() && !result.expiresAt.After(time.Now()) {
		return verifiedAccount{}, ErrScope
	}
	return result, nil
}
