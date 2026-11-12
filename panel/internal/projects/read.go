package projects

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/readiness"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type cursor struct {
	Kind     string `json:"k"`
	Scope    string `json:"s"`
	Snapshot string `json:"r,omitempty"`
	At       int64  `json:"t"`
	ID       string `json:"i"`
}

func page(request *panelv1.PageRequest, kind, scope string) (cursor, int64, error) {
	size := int64(request.GetPageSize())
	if size == 0 {
		size = 50
	}
	if size > 100 {
		return cursor{}, 0, ErrInvalid
	}
	value := cursor{Kind: kind, Scope: scope, At: math.MaxInt64}
	if request.GetPageToken() != "" {
		if len(request.GetPageToken()) > 512 {
			return cursor{}, 0, ErrInvalid
		}
		encoded, err := base64.RawURLEncoding.DecodeString(request.GetPageToken())
		if err != nil || json.Unmarshal(encoded, &value) != nil || value.Kind != kind || value.Scope != scope || value.At < 0 || len(value.ID) > 128 {
			return cursor{}, 0, ErrInvalid
		}
	}
	return value, size, nil
}
func next(kind, scope, snapshot string, at int64, id string) string {
	encoded, _ := json.Marshal(cursor{kind, scope, snapshot, at, id})
	return base64.RawURLEncoding.EncodeToString(encoded)
}
func projectMessage(value store.Project) *panelv1.Project {
	return &panelv1.Project{Id: value.ID, Name: value.Name, Repository: &panelv1.Repository{GithubRepositoryId: value.RepositoryID, Owner: value.RepositoryOwner, Name: value.RepositoryName}, CreatedAt: timestamppb.New(time.UnixMilli(value.CreatedAt))}
}

var errCorruptMetadata = errors.New("stored metadata is invalid")

func storeVersion(value uint64) (int64, error) {
	if value < 1 || value > math.MaxInt64 {
		return 0, ErrInvalid
	}
	return int64(value), nil
}
func apiVersion(value int64) (uint64, error) {
	if value < 1 {
		return 0, errCorruptMetadata
	}
	return uint64(value), nil
}
func environmentMessage(value store.Environment) (*panelv1.Environment, error) {
	var domains []*panelv1.Domain
	if json.Unmarshal(value.DomainsJson, &domains) != nil {
		return nil, errCorruptMetadata
	}
	var names []string
	if json.Unmarshal(value.SecretNamesJson, &names) != nil {
		return nil, errCorruptMetadata
	}
	version, err := apiVersion(value.Version)
	if err != nil {
		return nil, err
	}
	binding, err := apiVersion(value.BindingVersion)
	if err != nil {
		return nil, err
	}
	return &panelv1.Environment{Id: value.ID, ProjectId: value.ProjectID, Settings: &panelv1.EnvironmentSettings{Name: value.Name, ServerId: value.ServerID, Branch: value.Branch, ComposeOverridePath: value.OverridePath, Domains: domains, AutoDeploy: value.AutoDeploy}, Version: version, BindingVersion: binding, LatestSecretVersionId: strconv.FormatInt(value.SecretVersion, 10), SecretNames: names, CurrentReleaseId: value.CurrentReleaseID.String, PreviousReleaseId: value.PreviousReleaseID.String, PendingChanges: value.Version > value.AppliedVersion}, nil
}
func serverMessage(value store.Server) *panelv1.Server {
	if value.Readiness == "ready" && !readiness.Fresh(value, time.Now()) {
		value.Readiness = ""
	}
	states := map[string]panelv1.ServerReadiness{"setup_pending": panelv1.ServerReadiness_SERVER_READINESS_SETUP_PENDING, "ready": panelv1.ServerReadiness_SERVER_READINESS_READY, "unreachable": panelv1.ServerReadiness_SERVER_READINESS_UNREACHABLE, "needs_attention": panelv1.ServerReadiness_SERVER_READINESS_NEEDS_ATTENTION}
	result := &panelv1.Server{Id: value.ID, Name: value.Name, TailscaleAddress: value.TailscaleAddress, SshHostKeyFingerprint: value.SshFingerprint, PanelHost: value.PanelHost, Readiness: states[value.Readiness]}
	if value.ObservedAt > 0 {
		result.ObservedAt = timestamppb.New(time.UnixMilli(value.ObservedAt))
	}
	return result
}
func (s *Service) GetProject(ctx context.Context, id string) (*panelv1.Project, error) {
	canonical, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrInvalid
	}
	value, err := store.New(s.database).GetProject(ctx, canonical.String())
	if err != nil {
		return nil, readError(err)
	}
	return projectMessage(value), nil
}
func (s *Service) GetEnvironment(ctx context.Context, id string) (*panelv1.Environment, error) {
	canonical, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrInvalid
	}
	value, err := store.New(s.database).GetEnvironment(ctx, canonical.String())
	if err != nil {
		return nil, readError(err)
	}
	return environmentMessage(value)
}
func (s *Service) GetServer(ctx context.Context, id string) (*panelv1.Server, error) {
	canonical, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrInvalid
	}
	value, err := store.New(s.database).GetServer(ctx, canonical.String())
	if err != nil {
		return nil, readError(err)
	}
	return serverMessage(value), nil
}
func (s *Service) ListProjects(ctx context.Context, request *panelv1.PageRequest) (*panelv1.ListProjectsResponse, error) {
	position, size, err := page(request, "projects", "")
	if err != nil {
		return nil, err
	}
	values, err := store.New(s.database).ListProjects(ctx, store.ListProjectsParams{CursorTime: position.At, CursorID: position.ID, PageLimit: size + 1})
	if err != nil {
		return nil, err
	}
	response := &panelv1.ListProjectsResponse{Page: &panelv1.PageResponse{}}
	if int64(len(values)) > size {
		last := values[size-1]
		response.Page.NextPageToken = next("projects", "", "", last.CreatedAt, last.ID)
		values = values[:size]
	}
	for _, value := range values {
		response.Projects = append(response.Projects, projectMessage(value))
	}
	return response, nil
}
func (s *Service) ListEnvironments(ctx context.Context, projectID string, request *panelv1.PageRequest) (*panelv1.ListEnvironmentsResponse, error) {
	canonical, err := uuid.Parse(projectID)
	if err != nil {
		return nil, ErrInvalid
	}
	position, size, err := page(request, "environments", canonical.String())
	if err != nil {
		return nil, err
	}
	if size > 50 {
		size = 50
	} // Keep maximum legal domain/secret-name metadata within the RPC budget.
	values, err := store.New(s.database).ListEnvironments(ctx, store.ListEnvironmentsParams{ProjectID: canonical.String(), CursorTime: position.At, CursorID: position.ID, PageLimit: size + 1})
	if err != nil {
		return nil, err
	}
	response := &panelv1.ListEnvironmentsResponse{Page: &panelv1.PageResponse{}}
	if int64(len(values)) > size {
		last := values[size-1]
		response.Page.NextPageToken = next("environments", canonical.String(), "", last.CreatedAt, last.ID)
		values = values[:size]
	}
	for _, value := range values {
		message, err := environmentMessage(value)
		if err != nil {
			return nil, err
		}
		response.Environments = append(response.Environments, message)
	}
	return response, nil
}
func (s *Service) ListServers(ctx context.Context, request *panelv1.PageRequest) (*panelv1.ListServersResponse, error) {
	position, size, err := page(request, "servers", "")
	if err != nil {
		return nil, err
	}
	values, err := store.New(s.database).ListServers(ctx, store.ListServersParams{CursorTime: position.At, CursorID: position.ID, PageLimit: size + 1})
	if err != nil {
		return nil, err
	}
	response := &panelv1.ListServersResponse{Page: &panelv1.PageResponse{}}
	if int64(len(values)) > size {
		last := values[size-1]
		response.Page.NextPageToken = next("servers", "", "", last.CreatedAt, last.ID)
		values = values[:size]
	}
	for _, value := range values {
		response.Servers = append(response.Servers, serverMessage(value))
	}
	return response, nil
}
