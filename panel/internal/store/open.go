package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open uses one bounded connection. DSN pragmas apply again on replacement
// connections; immediate transactions keep read-then-write updates serializable.
func Open(ctx context.Context, directory string, create bool) (*sql.DB, error) {
	if err := privatefile.PrivateDirectory(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "panel.sqlite")
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := privatefile.OpenPrivate(path, flags)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close database admission file: %w", err)
	}
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{"mode": {"rw"}, "_txlock": {"immediate"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)"}}
	uri.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	failed := true
	defer func() {
		if failed {
			_ = database.Close()
		}
	}()
	if err := database.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return nil, fmt.Errorf("prepare application migrations: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("check application schema version: %w", err)
	}
	sources := provider.ListSources()
	if len(sources) == 0 || version > sources[len(sources)-1].Version {
		return nil, fmt.Errorf("application schema is newer than this binary; downgrade is unsupported")
	}
	if _, err := provider.Up(ctx); err != nil {
		return nil, fmt.Errorf("migrate application schema: %w", err)
	}
	failed = false
	return database, nil
}
