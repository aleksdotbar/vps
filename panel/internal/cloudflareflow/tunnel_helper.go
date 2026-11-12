package cloudflareflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func (t *Tunnels) helper(ctx context.Context, row store.ServerTunnel, action string) (hosthelper.TunnelResponse, error) {
	queries := store.New(t.connection.database)
	installed, err := queries.GetInstallation(ctx)
	if err != nil || installed.InstallationID != row.InstallationID || installed.PrivateProofAt <= 0 {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	server, err := queries.GetServer(ctx, row.ServerID)
	if err != nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	management, err := queries.GetServerManagement(ctx, row.ServerID)
	if err != nil || management.CredentialKind != "credential" || management.CredentialOwnerID != "ssh:"+row.InstallationID+":"+row.ServerID {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	target, err := remote.NewTarget(server.TailscaleAddress, "panel-runner", []byte(management.HostPublicKey), server.SshFingerprint)
	if err != nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	private, err := t.connection.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: management.CredentialOwnerID, Version: management.CredentialVersion})
	if err != nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	defer clear(private)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != management.ManagementFingerprint {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	request := hosthelper.TunnelRequest{SchemaVersion: 1, Action: action, InstallationID: row.InstallationID, ServerID: row.ServerID, OperationID: row.RequestID, Challenge: hex.EncodeToString(nonce), AccountID: row.AccountID, Name: row.Name}
	if action == "tunnel_finalize" {
		request.TunnelID = row.TunnelID.String
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	if _, err := hosthelper.ParseTunnelRequest(payload); err != nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	if t.remote == nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	result, err := t.remote.Helper(ctx, target, private, payload)
	defer clear(result.Output)
	if err != nil || result.Outcome != remote.CommandExited || result.ExitCode != 0 {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	response, err := hosthelper.ParseTunnelResponse(result.Output, request)
	if err != nil {
		return hosthelper.TunnelResponse{}, ErrHostSetup
	}
	// The concrete helper independently takes the host lock and binds the
	// running generation before any prepare/finalize effect.
	return response, nil
}
