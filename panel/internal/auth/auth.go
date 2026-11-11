package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var (
	ErrUnauthorized = errors.New("authentication required")
	ErrLimited      = errors.New("login temporarily limited")
	ErrInitialized  = errors.New("administrator already initialized")
	ErrPassword     = errors.New("password must be valid UTF-8 and contain 12 to 1024 bytes")
)
var hashParams = argon2id.Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}
var keyCheckRef = securestate.RecordRef{Kind: securestate.InstallationKind, OwnerID: "installation-key-check", Version: 1}

type Service struct {
	database *sql.DB
	hashing  chan struct{}
}

func New(ctx context.Context, database *sql.DB, cipher *securestate.Cipher) (*Service, error) {
	administrator, err := store.New(database).GetAdministrator(ctx)
	if err != nil {
		return nil, fmt.Errorf("read administrator: %w", err)
	}
	value, err := cipher.Open(keyCheckRef, administrator.KeyCheck)
	if err != nil || string(value) != "panel-master-key-v1" {
		return nil, securestate.ErrDecrypt
	}
	return &Service{database: database, hashing: make(chan struct{}, 1)}, nil
}

func Bootstrap(ctx context.Context, directory, keyPath string, password []byte) (returnErr error) {
	if !utf8.Valid(password) || len(password) < 12 || len(password) > 1024 {
		return ErrPassword
	}
	if !filepath.IsAbs(keyPath) {
		return errors.New("master key path must be absolute")
	}
	if err := privatefile.PrivateDirectory(directory); err != nil {
		return err
	}
	// Never generate a replacement key for an existing database.
	_, databaseErr := os.Lstat(filepath.Join(directory, "panel.sqlite"))
	_, keyErr := os.Lstat(keyPath)
	if errors.Is(keyErr, os.ErrNotExist) && errors.Is(databaseErr, os.ErrNotExist) {
		if err := privatefile.CreateKey(keyPath); err != nil {
			return err
		}
	}
	key, err := privatefile.ReadKey(keyPath)
	if err != nil {
		return err
	}
	defer clear(key)
	cipher, err := securestate.NewCipher(key)
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, directory, true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	if _, err := store.New(database).GetAdministrator(ctx); err == nil {
		return ErrInitialized
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect administrator: %w", err)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	check, err := cipher.Seal(keyCheckRef, []byte("panel-master-key-v1"))
	if err != nil {
		return err
	}
	if err := store.New(database).CreateAdministrator(ctx, store.CreateAdministratorParams{PasswordHash: hash, KeyCheck: check}); err != nil {
		return fmt.Errorf("initialize administrator: %w", err)
	}
	return nil
}

func hashPassword(password []byte) (string, error) {
	if !utf8.Valid(password) || len(password) < 12 || len(password) > 1024 {
		return "", ErrPassword
	}
	hash, err := argon2id.CreateHash(string(password), &hashParams)
	if err != nil {
		return "", errors.New("password hashing failed")
	}
	return hash, nil
}

func (s *Service) Login(ctx context.Context, password string) (int64, error) {
	if len(password) == 0 || len(password) > 1024 {
		return 0, ErrUnauthorized
	}
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	default:
		return 0, ErrLimited
	}
	queries := store.New(s.database)
	if _, err := queries.ClaimLoginAttempt(ctx, time.Now().Unix()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrLimited
		}
		return 0, fmt.Errorf("claim login budget: %w", err)
	}
	administrator, err := queries.GetAdministrator(ctx)
	if err != nil {
		return 0, fmt.Errorf("read administrator: %w", err)
	}
	params, _, _, err := argon2id.DecodeHash(administrator.PasswordHash)
	if err != nil || params == nil || *params != hashParams {
		return 0, errors.New("administrator password state is invalid")
	}
	match, err := argon2id.ComparePasswordAndHash(password, administrator.PasswordHash)
	if err != nil {
		return 0, errors.New("password verification failed")
	}
	if !match {
		return 0, ErrUnauthorized
	}
	current, err := queries.GetAdministrator(ctx)
	if err != nil {
		return 0, fmt.Errorf("check administrator generation: %w", err)
	}
	if current.Generation != administrator.Generation {
		return 0, ErrUnauthorized
	}
	return current.Generation, nil
}

func (s *Service) Authenticated(ctx context.Context, generation int64) (bool, error) {
	if generation < 1 {
		return false, nil
	}
	administrator, err := store.New(s.database).GetAdministrator(ctx)
	if err != nil {
		return false, fmt.Errorf("check session generation: %w", err)
	}
	return generation == administrator.Generation, nil
}

func (s *Service) Reset(ctx context.Context, password []byte) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	if err := queries.ResetAdministrator(ctx, hash); err != nil {
		return fmt.Errorf("reset administrator: %w", err)
	}
	if err := queries.DeleteSessions(ctx); err != nil {
		return fmt.Errorf("invalidate sessions: %w", err)
	}
	if err := queries.ClearLoginBudget(ctx); err != nil {
		return fmt.Errorf("reset login budget: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit password reset: %w", err)
	}
	return nil
}
