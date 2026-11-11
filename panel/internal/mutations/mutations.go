package mutations

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var (
	ErrInvalid  = errors.New("invalid mutation")
	ErrReused   = errors.New("request identity reused for different mutation")
	ErrCapacity = errors.New("metadata capacity reached")
)

type Service struct {
	database *sql.DB
	cipher   *securestate.Cipher
}

func New(database *sql.DB, cipher *securestate.Cipher) *Service {
	return &Service{database: database, cipher: cipher}
}

type Transaction struct {
	Tx            *sql.Tx
	id, procedure string
	identity      []byte
	Response      []byte
}

func (s *Service) Begin(ctx context.Context, procedure, id string, input proto.Message) (Transaction, error) {
	canonical, err := uuid.Parse(id)
	if err != nil {
		return Transaction{}, ErrInvalid
	}
	if len(procedure) > 128 {
		return Transaction{}, ErrInvalid
	}
	canonicalInput := proto.Clone(input)
	if field := canonicalInput.ProtoReflect().Descriptor().Fields().ByName("mutation"); field != nil {
		canonicalInput.ProtoReflect().Clear(field)
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(canonicalInput)
	if err != nil {
		return Transaction{}, ErrInvalid
	}
	identity, err := s.cipher.Identity(procedure, payload)
	if err != nil {
		return Transaction{}, ErrInvalid
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Transaction{}, fmt.Errorf("begin metadata mutation: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = tx.Rollback()
		}
	}()
	value := Transaction{Tx: tx, id: canonical.String(), procedure: procedure, identity: identity}
	if _, err := store.New(tx).GetOperation(ctx, value.id); err == nil {
		return Transaction{}, ErrReused
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, fmt.Errorf("check operation identity: %w", err)
	}
	if _, err := store.New(tx).GetWaitingBuild(ctx, value.id); err == nil {
		return Transaction{}, ErrReused
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, err
	}
	existing, err := store.New(tx).GetMetadataMutation(ctx, value.id)
	if err == nil {
		if existing.Procedure != procedure || !hmac.Equal(existing.Identity, identity) {
			return Transaction{}, ErrReused
		}
		value.Response, err = s.cipher.Open(securestate.RecordRef{Kind: securestate.InstallationKind, OwnerID: "metadata-result:" + value.id, Version: 1}, existing.EncryptedResponse)
		if err != nil {
			return Transaction{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, fmt.Errorf("read metadata replay: %w", err)
	} else {
		capacity, err := store.New(tx).MetadataCapacity(ctx)
		if err != nil {
			return Transaction{}, fmt.Errorf("check metadata capacity: %w", err)
		}
		if capacity.Mutations >= 100000 {
			return Transaction{}, ErrCapacity
		}
	}
	failed = false
	return value, nil
}
func (s *Service) Commit(ctx context.Context, value Transaction, response proto.Message) error {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode metadata result: %w", err)
	}
	encrypted, err := s.cipher.Seal(securestate.RecordRef{Kind: securestate.InstallationKind, OwnerID: "metadata-result:" + value.id, Version: 1}, payload)
	if err != nil {
		return err
	}
	if err := store.New(value.Tx).CreateMetadataMutation(ctx, store.CreateMetadataMutationParams{RequestID: value.id, Procedure: value.procedure, Identity: value.identity, EncryptedResponse: encrypted, CreatedAt: time.Now().UnixMilli()}); err != nil {
		return fmt.Errorf("persist metadata replay: %w", err)
	}
	if err := value.Tx.Commit(); err != nil {
		return fmt.Errorf("commit metadata mutation: %w", err)
	}
	return nil
}
