package cloudflareflow

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/cloudflare/cloudflare-go/v7/shared"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
	"github.com/google/uuid"

	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var errTunnelAbsent = errors.New("no Tunnel observed")

func (t *Tunnels) providerToken(ctx context.Context, row store.ServerTunnel, create bool) ([]byte, error) {
	version := row.CredentialVersion
	if !create {
		current, err := store.New(t.connection.database).GetCloudflareIntegration(ctx)
		if err != nil || current.AccountID != row.AccountID {
			return nil, ErrScope
		}
		version = current.Version
	}
	return t.connection.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: "cloudflare-api", Version: version})
}
func (t *Tunnels) find(ctx context.Context, row store.ServerTunnel) (*shared.CloudflareTunnel, error) {
	token, err := t.providerToken(ctx, row, false)
	if err != nil {
		return nil, ErrProvider
	}
	defer clear(token)
	client := zero_trust.NewTunnelCloudflaredService(t.connection.options(string(token))...)
	if row.TunnelID.Valid {
		var params zero_trust.TunnelCloudflaredGetParams
		params.AccountID.Value, params.AccountID.Present = row.AccountID, true
		candidate, err := client.Get(ctx, row.TunnelID.String, params)
		if err != nil || candidate == nil {
			return nil, ErrProvider
		}
		if !validTunnel(candidate, row) {
			return nil, ErrScope
		}
		return candidate, nil
	}
	var params zero_trust.TunnelCloudflaredListParams
	params.AccountID.Value, params.AccountID.Present = row.AccountID, true
	params.Name.Value, params.Name.Present = row.Name, true
	params.PerPage.Value, params.PerPage.Present = 2, true
	params.IsDeleted.Value, params.IsDeleted.Present = false, true
	result, err := client.List(ctx, params)
	if err != nil || result == nil {
		return nil, ErrProvider
	}
	if len(result.Result) == 0 {
		return nil, errTunnelAbsent
	}
	if len(result.Result) != 1 {
		return nil, ErrScope
	}
	candidate := &result.Result[0]
	if !validTunnel(candidate, row) {
		return nil, ErrScope
	}
	return candidate, nil
}
func validTunnel(candidate *shared.CloudflareTunnel, row store.ServerTunnel) bool {
	id, err := uuid.Parse(candidate.ID)
	if err != nil || id == uuid.Nil || id.String() != candidate.ID || (row.TunnelID.Valid && row.TunnelID.String != candidate.ID) || candidate.AccountTag != row.AccountID || candidate.Name != row.Name || candidate.ConfigSrc != shared.CloudflareTunnelConfigSrcLocal || candidate.TunType != shared.CloudflareTunnelTunTypeCfdTunnel || !candidate.DeletedAt.IsZero() || candidate.JSON.DeletedAt.IsInvalid() || candidate.JSON.CreatedAt.IsInvalid() || candidate.CreatedAt.IsZero() {
		return false
	}
	if row.CreateAttemptedAt > 0 {
		attempted := time.UnixMilli(row.CreateAttemptedAt)
		if candidate.CreatedAt.Before(attempted.Add(-5*time.Minute)) || candidate.CreatedAt.After(time.Now().Add(5*time.Minute)) {
			return false
		}
	}
	return true
}
func (t *Tunnels) create(ctx context.Context, row store.ServerTunnel, secret []byte) (*shared.CloudflareTunnel, error) {
	if !row.CreateAttempted || len(secret) != 32 {
		return nil, ErrScope
	}
	token, err := t.providerToken(ctx, row, true)
	if err != nil {
		return nil, ErrProvider
	}
	defer clear(token)
	var params zero_trust.TunnelCloudflaredNewParams
	params.AccountID.Value, params.AccountID.Present = row.AccountID, true
	params.Name.Value, params.Name.Present = row.Name, true
	params.ConfigSrc.Value, params.ConfigSrc.Present = zero_trust.TunnelCloudflaredNewParamsConfigSrcLocal, true
	params.TunnelSecret.Value, params.TunnelSecret.Present = base64.StdEncoding.EncodeToString(secret), true
	result, err := zero_trust.NewTunnelCloudflaredService(t.connection.options(string(token))...).New(ctx, params)
	if err != nil || result == nil {
		return nil, ErrProvider
	}
	if !validTunnel(result, row) {
		return nil, ErrScope
	}
	return result, nil
}
