package githubflow

import (
	"context"
	"encoding/base64"
	"strconv"

	"github.com/google/go-github/v93/github"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/mutations"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func (s *Service) selectedClient(ctx context.Context, id int64, write bool) (*github.Client, store.GithubRepository, error) {
	repository, err := store.New(s.database).GetGitHubRepository(ctx, id)
	if err != nil || !repository.Selected {
		return nil, repository, ErrOwner
	}
	client, err := s.installationClient(ctx, []int64{id}, write)
	if err != nil {
		return nil, repository, err
	}
	return client, repository, nil
}
func (s *Service) ListWorkflows(ctx context.Context, id int64, pageRequest *panelv1.PageRequest) (*panelv1.ListWorkflowsResponse, error) {
	page, size, err := providerPage(pageRequest, "workflows:"+strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	client, repository, err := s.selectedClient(ctx, id, false)
	if err != nil {
		return nil, err
	}
	workflows, response, err := client.Actions.ListWorkflows(ctx, repository.Owner, repository.Name, &github.ListOptions{Page: page, PerPage: size})
	if err != nil || workflows == nil {
		return nil, s.failure(ctx, response, err)
	}
	result := &panelv1.ListWorkflowsResponse{Page: &panelv1.PageResponse{}}
	for _, workflow := range workflows.Workflows {
		if len(workflow.GetPath()) > 256 || len(workflow.GetName()) > 256 || workflow.GetID() < 1 {
			return nil, ErrProvider
		}
		result.Workflows = append(result.Workflows, &panelv1.GitHubWorkflow{Id: workflow.GetID(), Name: workflow.GetName(), Path: workflow.GetPath(), State: workflow.GetState()})
	}
	if response.NextPage > 100 {
		return nil, mutations.ErrCapacity
	}
	if response.NextPage != 0 {
		result.Page.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte("workflows:" + strconv.FormatInt(id, 10) + ":" + strconv.Itoa(response.NextPage)))
	}
	return result, nil
}
func (s *Service) VerifyProject(ctx context.Context, request *panelv1.CreateProjectRequest) error {
	client, repository, err := s.selectedClient(ctx, request.GetRepository().GetGithubRepositoryId(), false)
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
	workflow, response, err := client.Actions.GetWorkflowByID(ctx, repository.Owner, repository.Name, request.GetGithubWorkflowId())
	if err != nil || workflow == nil {
		return s.failure(ctx, response, err)
	}
	if workflow.GetID() != request.GetGithubWorkflowId() || workflow.GetPath() != ".github/workflows/panel-build.yaml" || workflow.GetState() != "active" {
		return ErrSetup
	}
	return nil
}
