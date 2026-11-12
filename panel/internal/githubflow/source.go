package githubflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/go-github/v93/github"

	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

// ErrSource means the commit's Compose files are missing or not regular files.
var ErrSource = errors.New("commit Compose files are missing or unsupported")

// source copies compose.yaml and the environment override at sha into a
// private temporary tree for native Compose resolution.
func (b *Builds) source(ctx context.Context, client *github.Client, repository store.GithubRepository, selection projects.Selection, sha string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "panel-compose-source-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	root, err := os.OpenRoot(directory)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer func() { _ = root.Close() }()
	for _, path := range []string{"compose.yaml", selection.Environment.OverridePath} {
		file, entries, response, err := client.Repositories.GetContents(ctx, repository.Owner, repository.Name, path, &github.RepositoryContentGetOptions{Ref: sha})
		if err != nil {
			cleanup()
			return "", nil, b.integration.failure(ctx, response, err)
		}
		if file == nil || len(entries) > 0 || file.GetType() != "file" || file.GetSubmoduleGitURL() != "" || file.GetTarget() != "" || file.GetSize() < 0 || file.GetSize() > 256<<10 {
			cleanup()
			return "", nil, ErrSource
		}
		value, err := file.GetContent()
		if err != nil || len(value) > 256<<10 {
			cleanup()
			return "", nil, ErrSource
		}
		if err := root.MkdirAll(filepath.Dir(path), 0700); err != nil {
			cleanup()
			return "", nil, ErrSource
		}
		if err := root.WriteFile(path, []byte(value), 0600); err != nil {
			cleanup()
			return "", nil, ErrSource
		}
	}
	return directory, cleanup, nil
}
