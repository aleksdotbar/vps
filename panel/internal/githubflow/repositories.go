package githubflow

import (
	"context"

	"github.com/google/go-github/v93/github"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func (s *Service) selectedClient(ctx context.Context, id int64) (*github.Client, store.GithubRepository, error) {
	repository, err := store.New(s.database).GetGitHubRepository(ctx, id)
	if err != nil || !repository.Selected {
		return nil, repository, ErrOwner
	}
	client, err := s.installationClient(ctx, []int64{id})
	if err != nil {
		return nil, repository, err
	}
	return client, repository, nil
}
func (s *Service) VerifyProject(ctx context.Context, request *panelv1.CreateProjectRequest) error {
	client, repository, err := s.selectedClient(ctx, request.GetRepository().GetGithubRepositoryId())
	if err != nil {
		return err
	}
	observed, response, err := client.Repositories.GetByID(ctx, repository.ID)
	if err != nil || observed == nil {
		return s.failure(ctx, response, err)
	}
	if observed.GetID() != repository.ID || observed.Owner.GetLogin() != repository.Owner || observed.GetName() != repository.Name {
		return ErrOwner
	}
	if repository.Owner != request.GetRepository().GetOwner() || repository.Name != request.GetRepository().GetName() {
		return ErrOwner
	}
	return nil
}
