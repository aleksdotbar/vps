package projects

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/aleksdotbar/vps/panel/internal/mutations"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
)

var (
	ErrInvalid  = mutations.ErrInvalid
	ErrNotFound = errors.New("requested resource does not exist")
	ErrStale    = errors.New("environment configuration changed")
	ErrReused   = mutations.ErrReused
	ErrConflict = errors.New("resource is already in use")
	ErrCapacity = mutations.ErrCapacity
	ErrServer   = errors.New("server is not ready for deployment")
	ErrBusy     = errors.New("release preparation is busy")
	ErrBinding  = errors.New("deployed environment cannot move to another server")
)

type Service struct {
	database  *sql.DB
	cipher    *securestate.Cipher
	vault     *securestate.Vault
	preparing chan struct{}
}

func New(database *sql.DB, cipher *securestate.Cipher) *Service {
	return &Service{database: database, cipher: cipher, vault: securestate.NewVault(database, cipher), preparing: make(chan struct{}, 2)}
}

type mutation = mutations.Transaction

func (s *Service) begin(ctx context.Context, procedure, id string, input proto.Message) (mutation, error) {
	return mutations.New(s.database, s.cipher).Begin(ctx, "panel.v1.PanelService/"+procedure, id, input)
}
func (s *Service) commit(ctx context.Context, value mutation, response proto.Message) error {
	return mutations.New(s.database, s.cipher).Commit(ctx, value, response)
}
