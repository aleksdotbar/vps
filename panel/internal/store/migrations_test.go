package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// Upgrading to image tags must treat the running release's commit as handled,
// so the first discovery does not redeploy the current branch head.
func TestImageTagMigrationSeedsHandledHeadFromCurrentRelease(t *testing.T) {
	ctx := context.Background()
	database, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "panel.sqlite")+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 15); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	for _, statement := range []string{
		`INSERT INTO environments(id,project_id,name,server_id,branch,override_path,domains_json,auto_deploy,version,binding_version,secret_version,secret_names_json,bridge_name,current_release_id,created_at)
		 VALUES('env-1','p','production','s','main','compose.production.yaml','[]',1,1,3,1,'[]','vps-1','rel-1',1)`,
		`INSERT INTO environments(id,project_id,name,server_id,branch,override_path,domains_json,auto_deploy,version,binding_version,secret_version,secret_names_json,bridge_name,created_at)
		 VALUES('env-2','p','staging','s','main','compose.staging.yaml','[]',1,1,1,1,'[]','vps-2',1)`,
		`INSERT INTO releases(id,environment_id,server_id,repository_id,workflow_id,run_id,run_attempt,branch,source_sha,environment_version,binding_version,images_json,domains_json,secret_version,compose_config,secret_bindings_json,config_sha256,created_at)
		 VALUES('rel-1','env-1','s',1,1,1,1,'main','` + sha + `',1,3,'{}','[]',1,'{}','[]','c',1)`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var binding int64
	var handled string
	if err := database.QueryRowContext(ctx, "SELECT binding_version,handled_sha FROM github_branch_progress WHERE environment_id='env-1'").Scan(&binding, &handled); err != nil {
		t.Fatal(err)
	}
	if binding != 3 || handled != sha {
		t.Fatalf("current release head not seeded: binding %d handled %q", binding, handled)
	}
	if err := database.QueryRowContext(ctx, "SELECT handled_sha FROM github_branch_progress WHERE environment_id='env-2'").Scan(&handled); err != sql.ErrNoRows {
		t.Fatalf("environment without a release seeded: %v", err)
	}
}
