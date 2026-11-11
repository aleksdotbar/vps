package securestate

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

var (
	ErrDecrypt      = errors.New("encrypted state cannot be authenticated")
	ErrStaleVersion = errors.New("encrypted state version changed")
	ErrInvalidInput = errors.New("invalid secret change")
)

type Kind string

const (
	CredentialKind    Kind = "credential"
	InstallationKind  Kind = "installation"
	EnvironmentKind   Kind = "environment"
	maxPlaintextBytes      = 2 << 20
)

type RecordRef struct {
	Kind    Kind   `json:"kind"`
	OwnerID string `json:"owner_id"`
	Version int64  `json:"version"`
}

type Cipher struct {
	aead        cipher.AEAD
	identityKey []byte
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("master key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize master cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("initialize authenticated encryption: %w", err)
	}
	identityKey, err := hkdf.Key(sha256.New, key, nil, "panel-mutation-identity-v1", 32)
	if err != nil {
		return nil, fmt.Errorf("derive mutation identity key: %w", err)
	}
	return &Cipher{aead: aead, identityKey: identityKey}, nil
}

func (c *Cipher) Seal(ref RecordRef, plaintext []byte) ([]byte, error) {
	if len(plaintext) > maxPlaintextBytes {
		return nil, ErrInvalidInput
	}
	data, err := associatedData(ref)
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- NewGCMWithRandomNonce generates/prepends the nonce and requires nil here.
	return c.aead.Seal(nil, nil, plaintext, data), nil
}

func (c *Cipher) Open(ref RecordRef, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) > maxPlaintextBytes+c.aead.Overhead() {
		return nil, ErrDecrypt
	}
	data, err := associatedData(ref)
	if err != nil {
		return nil, err
	}
	plaintext, err := c.aead.Open(nil, nil, ciphertext, data)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

func associatedData(ref RecordRef) ([]byte, error) {
	if (ref.Kind != CredentialKind && ref.Kind != EnvironmentKind && ref.Kind != InstallationKind) || ref.OwnerID == "" || !utf8.ValidString(ref.OwnerID) || len(ref.OwnerID) > 128 || ref.Version < 1 {
		return nil, ErrInvalidInput
	}
	// Fixed format version and structured identity prevent cross-record swaps.
	return json.Marshal(struct {
		Format int `json:"format"`
		RecordRef
	}{1, ref})
}

type Vault struct {
	database *sql.DB
	cipher   *Cipher
}

func NewVault(database *sql.DB, cipher *Cipher) *Vault {
	return &Vault{database: database, cipher: cipher}
}

func (v *Vault) Credential(ctx context.Context, ref RecordRef) ([]byte, error) {
	if ref.Kind != CredentialKind {
		return nil, ErrInvalidInput
	}
	return v.read(ctx, store.New(v.database), ref)
}
func (v *Vault) SaveCredential(ctx context.Context, owner string, expectedVersion int64, value []byte) (RecordRef, error) {
	if len(value) > 64<<10 {
		return RecordRef{}, ErrInvalidInput
	}
	tx, err := v.database.BeginTx(ctx, nil)
	if err != nil {
		return RecordRef{}, fmt.Errorf("begin credential change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ref, err := v.SaveCredentialTx(ctx, tx, owner, expectedVersion, value)
	if err != nil {
		return RecordRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return RecordRef{}, fmt.Errorf("commit credential change: %w", err)
	}
	return ref, nil
}
func (v *Vault) Environment(ctx context.Context, ref RecordRef) (map[string]string, error) {
	if ref.Kind != EnvironmentKind {
		return nil, ErrInvalidInput
	}
	return v.environment(ctx, store.New(v.database), ref)
}
func (v *Vault) environment(ctx context.Context, queries *store.Queries, ref RecordRef) (map[string]string, error) {
	plaintext, err := v.read(ctx, queries, ref)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	if err := json.Unmarshal(plaintext, &values); err != nil {
		return nil, ErrDecrypt
	}
	return values, nil
}

var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (v *Vault) UpdateEnvironment(ctx context.Context, owner string, expectedVersion int64, upserts map[string]string, removals []string) (RecordRef, error) {
	tx, err := v.database.BeginTx(ctx, nil)
	if err != nil {
		return RecordRef{}, fmt.Errorf("begin secret change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ref, err := v.UpdateEnvironmentTx(ctx, tx, owner, expectedVersion, upserts, removals)
	if err != nil {
		return RecordRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return RecordRef{}, fmt.Errorf("commit secret change: %w", err)
	}
	return ref, nil
}

// UpdateEnvironmentTx composes immutable secret versions with environment edits
// and their protected replay result in one caller-owned transaction.
func (v *Vault) UpdateEnvironmentTx(ctx context.Context, tx *sql.Tx, owner string, expectedVersion int64, upserts map[string]string, removals []string) (RecordRef, error) {
	if len(upserts) > 64 || len(removals) > 64 {
		return RecordRef{}, ErrInvalidInput
	}
	removed := make(map[string]bool, len(removals))
	for _, name := range removals {
		if !secretName.MatchString(name) || removed[name] {
			return RecordRef{}, ErrInvalidInput
		}
		if _, overlap := upserts[name]; overlap {
			return RecordRef{}, ErrInvalidInput
		}
		removed[name] = true
	}
	for name, value := range upserts {
		if !secretName.MatchString(name) || len(value) > 16384 {
			return RecordRef{}, ErrInvalidInput
		}
	}
	queries := store.New(tx)
	values := make(map[string]string)
	if expectedVersion > 0 {
		var err error
		values, err = v.environment(ctx, queries, RecordRef{EnvironmentKind, owner, expectedVersion})
		if err != nil {
			return RecordRef{}, err
		}
	}
	for _, name := range removals {
		delete(values, name)
	}
	for name, value := range upserts {
		values[name] = value
	}
	if len(values) > 64 {
		return RecordRef{}, ErrInvalidInput
	}
	plaintext, err := json.Marshal(values)
	if err != nil {
		return RecordRef{}, ErrInvalidInput
	}
	ref, err := v.put(ctx, queries, EnvironmentKind, owner, expectedVersion, plaintext)
	if err != nil {
		return RecordRef{}, err
	}
	return ref, nil
}
func (v *Vault) read(ctx context.Context, queries *store.Queries, ref RecordRef) ([]byte, error) {
	row, err := queries.GetEncryptedVersion(ctx, store.GetEncryptedVersionParams{Kind: string(ref.Kind), OwnerID: ref.OwnerID, Version: ref.Version})
	if err != nil {
		return nil, fmt.Errorf("read encrypted version: %w", err)
	}
	return v.cipher.Open(ref, row.Ciphertext)
}
func (v *Vault) put(ctx context.Context, queries *store.Queries, kind Kind, owner string, expectedVersion int64, value []byte) (RecordRef, error) {
	if expectedVersion < 0 {
		return RecordRef{}, ErrInvalidInput
	}
	latest, err := queries.GetLatestEncryptedVersion(ctx, store.GetLatestEncryptedVersionParams{Kind: string(kind), OwnerID: owner})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RecordRef{}, fmt.Errorf("read latest encrypted version: %w", err)
	}
	if latest.Version != expectedVersion {
		return RecordRef{}, ErrStaleVersion
	}
	ref := RecordRef{kind, owner, expectedVersion + 1}
	ciphertext, err := v.cipher.Seal(ref, value)
	if err != nil {
		return RecordRef{}, err
	}
	if err := queries.CreateEncryptedVersion(ctx, store.CreateEncryptedVersionParams{Kind: string(kind), OwnerID: owner, Version: ref.Version, Ciphertext: ciphertext}); err != nil {
		return RecordRef{}, fmt.Errorf("write encrypted version: %w", err)
	}
	return ref, nil
}

// Identity binds secret-bearing mutation payloads without a dictionary-checkable
// plaintext hash in SQLite. Scope includes the concrete RPC procedure.
func (c *Cipher) Identity(scope string, payload []byte) ([]byte, error) {
	if len(scope) == 0 || len(scope) > 128 || len(payload) > maxPlaintextBytes {
		return nil, ErrInvalidInput
	}
	hash := hmac.New(sha256.New, c.identityKey)
	_, _ = hash.Write([]byte(scope))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hash.Sum(nil), nil
}
func (v *Vault) EnvironmentTx(ctx context.Context, tx *sql.Tx, ref RecordRef) (map[string]string, error) {
	if ref.Kind != EnvironmentKind {
		return nil, ErrInvalidInput
	}
	return v.environment(ctx, store.New(tx), ref)
}

// SaveCredentialTx binds encrypted credential versions to the caller's metadata transaction.
func (v *Vault) SaveCredentialTx(ctx context.Context, tx *sql.Tx, owner string, expectedVersion int64, value []byte) (RecordRef, error) {
	if len(value) > 64<<10 {
		return RecordRef{}, ErrInvalidInput
	}
	return v.put(ctx, store.New(tx), CredentialKind, owner, expectedVersion, value)
}
