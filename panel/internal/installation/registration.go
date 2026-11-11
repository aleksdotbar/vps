package installation

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/auth"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type Result struct {
	Status                string `json:"status"`
	InstallationID        string `json:"installation_id"`
	ServerID              string `json:"server_id"`
	OperationID           string `json:"operation_id"`
	InputSHA256           string `json:"input_sha256"`
	ManagementFingerprint string `json:"management_fingerprint"`
	ObservedAt            string `json:"observed_at"`
	Ready                 bool   `json:"ready"`
}

func Bootstrap(ctx context.Context, input Handoff) (Result, error) {
	if CheckRuntimeUser() != nil {
		return Result{}, ErrIdentity
	}
	input, err := validate(input)
	if err != nil {
		return Result{}, err
	}
	if confirmInstalled(input) != nil {
		return Result{}, ErrIdentity
	}
	lock, err := privatefile.LockProcess(StateDirectory)
	if err != nil {
		return Result{}, ErrIdentity
	}
	defer func() { _ = lock.Close() }()
	if err := auth.Bootstrap(ctx, StateDirectory, MasterKeyFile, []byte(input.AdministratorPassword)); err != nil && !errors.Is(err, auth.ErrInitialized) {
		return Result{}, err
	}
	database, cipher, err := openPanel(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = database.Close() }()
	authentication, err := auth.New(ctx, database, cipher)
	if err != nil {
		return Result{}, ErrIdentity
	}
	if _, err := authentication.Login(ctx, input.AdministratorPassword); err != nil {
		return Result{}, err
	}
	// Registration must bind our native private address before its immutable row
	// commits, rather than retain a wrong peer and discover that only during proof.
	if confirmInstalled(input) != nil {
		return Result{}, ErrIdentity
	}
	if _, err := selfAddress(ctx, input.TailscaleAddress); err != nil {
		return Result{}, err
	}
	return register(ctx, database, cipher, input)
}

func openPanel(ctx context.Context) (*sql.DB, *securestate.Cipher, error) {
	key, err := privatefile.ReadKey(MasterKeyFile)
	if err != nil {
		return nil, nil, ErrIdentity
	}
	defer clear(key)
	cipher, err := securestate.NewCipher(key)
	if err != nil {
		return nil, nil, ErrIdentity
	}
	database, err := store.Open(ctx, StateDirectory, false)
	if err != nil {
		return nil, nil, ErrIdentity
	}
	if _, err := auth.New(ctx, database, cipher); err != nil {
		_ = database.Close()
		return nil, nil, ErrIdentity
	}
	return database, cipher, nil
}

func registrationIdentity(cipher *securestate.Cipher, input Handoff) ([]byte, error) {
	// Password retries authenticate through Argon2, independently of the immutable
	// management credential. Never persist an unkeyed hash of secret-bearing input.
	input.AdministratorPassword = ""
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, ErrInput
	}
	defer clear(encoded)
	return cipher.Identity("panel-bootstrap-v1", encoded)
}

func register(ctx context.Context, database *sql.DB, cipher *securestate.Cipher, input Handoff) (Result, error) {
	identity, err := registrationIdentity(cipher, input)
	if err != nil {
		return Result{}, err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, ErrIdentity
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	existing, err := queries.GetInstallation(ctx)
	if err == nil {
		if existing.InstallationID != input.InstallationID || existing.PanelServerID != input.ServerID || existing.OperationID != input.OperationID || existing.InputSha256 != input.InputSHA256 || existing.SourceSystem != input.SourceSystem || existing.EnrollmentSystem != input.EnrollmentSystem || !hmac.Equal(existing.BootstrapIdentity, identity) {
			return Result{}, ErrIdentity
		}
		server, err := queries.GetServer(ctx, existing.PanelServerID)
		if err != nil || !server.PanelHost || server.ID != input.ServerID || server.TailscaleAddress != input.TailscaleAddress || server.SshFingerprint != input.HostFingerprint {
			return Result{}, ErrIdentity
		}
		management, err := queries.GetServerManagement(ctx, server.ID)
		if err != nil || management.HostPublicKey != input.HostPublicKey || management.ManagementFingerprint != input.ManagementFingerprint || management.CredentialKind != "credential" || management.CredentialOwnerID != credentialOwner(input.InstallationID, input.ServerID) || management.CredentialVersion != 1 {
			return Result{}, ErrIdentity
		}
		if tx.Commit() != nil {
			return Result{}, ErrIdentity
		}
		return registrationResult(input), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrIdentity
	}
	now := time.Now().UnixMilli()
	if queries.CreateServer(ctx, store.CreateServerParams{ID: input.ServerID, Name: "Panel host", TailscaleAddress: input.TailscaleAddress, SshFingerprint: input.HostFingerprint, PanelHost: true, CreatedAt: now}) != nil {
		return Result{}, ErrIdentity
	}
	secret := []byte(input.ManagementPrivateKey)
	defer clear(secret)
	ref, err := securestate.NewVault(database, cipher).SaveCredentialTx(ctx, tx, credentialOwner(input.InstallationID, input.ServerID), 0, secret)
	if err != nil {
		return Result{}, ErrIdentity
	}
	if queries.CreateServerManagement(ctx, store.CreateServerManagementParams{ServerID: input.ServerID, CredentialKind: string(ref.Kind), CredentialOwnerID: ref.OwnerID, CredentialVersion: ref.Version, HostPublicKey: input.HostPublicKey, ManagementFingerprint: input.ManagementFingerprint}) != nil {
		return Result{}, ErrIdentity
	}
	if queries.CreateInstallation(ctx, store.CreateInstallationParams{InstallationID: input.InstallationID, PanelServerID: input.ServerID, OperationID: input.OperationID, InputSha256: input.InputSHA256, SourceSystem: input.SourceSystem, EnrollmentSystem: input.EnrollmentSystem, BootstrapIdentity: identity, CreatedAt: now}) != nil {
		return Result{}, ErrIdentity
	}
	if tx.Commit() != nil {
		return Result{}, ErrIdentity
	}
	return registrationResult(input), nil
}

func credentialOwner(installationID, serverID string) string {
	return "ssh:" + installationID + ":" + serverID
}
func registrationResult(input Handoff) Result {
	return Result{Status: "registered", InstallationID: input.InstallationID, ServerID: input.ServerID, OperationID: input.OperationID, InputSHA256: input.InputSHA256, ManagementFingerprint: input.ManagementFingerprint, Ready: false}
}
