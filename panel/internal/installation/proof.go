package installation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/tsaddr"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
	"golang.org/x/crypto/ssh"
)

func confirmSelfAddress(status *ipnstate.Status, address string) error {
	selected, err := netip.ParseAddr(address)
	if err != nil || selected.String() != address || selected.Is4In6() || selected.Zone() != "" || !tsaddr.IsTailscaleIP(selected) || status == nil || status.BackendState != "Running" || !status.TUN || status.Self == nil || status.Self.ID == "" || !slices.Contains(status.TailscaleIPs, selected) || !slices.Contains(status.Self.TailscaleIPs, selected) {
		return ErrProof
	}
	return nil
}
func selfAddress(ctx context.Context, address string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := local.Client{UseSocketOnly: true}
	status, err := client.StatusWithoutPeers(bounded)
	if err != nil || confirmSelfAddress(status, address) != nil {
		return "", ErrProof
	}
	return string(status.Self.ID), nil
}

// VerifyProbe accepts only the canonical fixed helper response. A fresh nonce is
// bound to this host's identity and running profile, never merely SSH exit zero.
func VerifyProbe(data []byte, request hosthelper.ProbeRequest) (hosthelper.ProbeResponse, error) {
	var response hosthelper.ProbeResponse
	if len(data) == 0 || len(data) > 8<<10 || json.Unmarshal(data, &response) != nil {
		return response, ErrProof
	}
	canonical, err := json.Marshal(response)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) || response.SchemaVersion != 1 || response.InstallationID != request.InstallationID || response.ServerID != request.ServerID || response.Challenge != request.Challenge || response.Role != "panel" || response.CurrentSystem != response.SystemProfile || response.Ready {
		return hosthelper.ProbeResponse{}, ErrProof
	}
	return response, nil
}

func Prove(ctx context.Context) (Result, error) {
	if CheckRuntimeUser() != nil {
		return Result{}, ErrIdentity
	}
	lock, err := privatefile.LockProcess(StateDirectory)
	if errors.Is(err, privatefile.ErrProcessBusy) {
		return Result{}, ErrRunning
	}
	if err != nil {
		return Result{}, ErrIdentity
	}
	defer func() { _ = lock.Close() }()
	database, cipher, err := openPanel(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = database.Close() }()
	queries := store.New(database)
	installed, err := queries.GetInstallation(ctx)
	if err != nil {
		return Result{}, ErrIdentity
	}
	management, err := queries.GetServerManagement(ctx, installed.PanelServerID)
	if err != nil {
		return Result{}, ErrIdentity
	}
	server, err := queries.GetServer(ctx, installed.PanelServerID)
	if err != nil || !server.PanelHost {
		return Result{}, ErrIdentity
	}
	input := Handoff{SchemaVersion: 1, InstallationID: installed.InstallationID, ServerID: installed.PanelServerID, TailscaleAddress: server.TailscaleAddress, HostPublicKey: management.HostPublicKey, HostFingerprint: server.SshFingerprint, ManagementFingerprint: management.ManagementFingerprint}
	if confirmInstalled(input) != nil {
		return Result{}, ErrIdentity
	}
	before, err := selfAddress(ctx, input.TailscaleAddress)
	if err != nil {
		return Result{}, err
	}
	target, err := remote.NewTarget(input.TailscaleAddress, "panel-runner", []byte(input.HostPublicKey), input.HostFingerprint)
	if err != nil {
		return Result{}, ErrProof
	}
	client, err := remote.New("/run/current-system/sw/bin/ssh", runtimeDirectory)
	if err != nil {
		return Result{}, ErrProof
	}
	ref := securestate.RecordRef{Kind: securestate.Kind(management.CredentialKind), OwnerID: management.CredentialOwnerID, Version: management.CredentialVersion}
	if ref.Kind != securestate.CredentialKind || ref.OwnerID != credentialOwner(input.InstallationID, input.ServerID) || ref.Version != 1 {
		return Result{}, ErrIdentity
	}
	private, err := securestate.NewVault(database, cipher).Credential(ctx, ref)
	if err != nil {
		return Result{}, ErrIdentity
	}
	defer clear(private)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != input.ManagementFingerprint {
		return Result{}, ErrIdentity
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return Result{}, ErrProof
	}
	request := hosthelper.ProbeRequest{SchemaVersion: 1, Action: "probe", InstallationID: input.InstallationID, ServerID: input.ServerID, Challenge: hex.EncodeToString(nonce)}
	payload, err := json.Marshal(request)
	if err != nil {
		return Result{}, ErrProof
	}
	result, err := client.Helper(ctx, target, private, payload)
	if err != nil || result.Outcome != remote.CommandExited || result.ExitCode != 0 {
		return Result{}, ErrProof
	}
	if _, err := VerifyProbe(result.Output, request); err != nil {
		return Result{}, err
	}
	after, err := selfAddress(ctx, input.TailscaleAddress)
	if err != nil || after != before || confirmInstalled(input) != nil {
		return Result{}, ErrProof
	}
	observed := time.Now()
	if recordProof(ctx, database, installed, management, server, observed.UnixMilli()) != nil {
		return Result{}, ErrIdentity
	}
	response := registrationResult(input)
	response.Status = "private_proven"
	response.ObservedAt = observed.UTC().Format(time.RFC3339)
	return response, nil
}

func recordProof(ctx context.Context, database *sql.DB, expected store.Installation, management store.ServerManagement, server store.Server, observed int64) error {
	// SSH and LocalAPI have finished before this short transaction begins. Reject
	// metadata/credential changes instead of attaching old evidence to new intent.
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return ErrIdentity
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	current, err := queries.GetInstallation(ctx)
	if err != nil || current.InstallationID != expected.InstallationID || current.PanelServerID != expected.PanelServerID || !hmac.Equal(current.BootstrapIdentity, expected.BootstrapIdentity) {
		return ErrIdentity
	}
	selected, err := queries.GetServerManagement(ctx, expected.PanelServerID)
	if err != nil || selected != management {
		return ErrIdentity
	}
	host, err := queries.GetServer(ctx, expected.PanelServerID)
	if err != nil || !host.PanelHost || host.TailscaleAddress != server.TailscaleAddress || host.SshFingerprint != server.SshFingerprint {
		return ErrIdentity
	}
	if queries.RecordPanelPrivateProof(ctx, observed) != nil || tx.Commit() != nil {
		return ErrIdentity
	}
	return nil
}
