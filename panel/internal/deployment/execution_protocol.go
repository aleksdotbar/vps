package deployment

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// ExecutionRequest selects retained immutable target material. Commands, ports,
// paths, registry secrets and health assertions are never control input.
type ExecutionRequest struct {
	SchemaVersion  int      `json:"schema_version"`
	Action         string   `json:"action"`
	Challenge      string   `json:"challenge"`
	Identity       Identity `json:"identity"`
	ManifestSHA256 string   `json:"manifest_sha256"`
}

type ExecutionResponse struct {
	SchemaVersion     int                  `json:"schema_version"`
	Action            string               `json:"action"`
	Challenge         string               `json:"challenge"`
	Identity          Identity             `json:"identity"`
	ManifestSHA256    string               `json:"manifest_sha256"`
	FinalSystem       string               `json:"final_system"`
	Phase             string               `json:"phase"`
	Reason            string               `json:"reason,omitempty"`
	ActiveReleaseID   string               `json:"active_release_id,omitempty"`
	RouteRevision     uint64               `json:"route_revision,omitempty"`
	RouteConfigSHA256 string               `json:"route_config_sha256,omitempty"`
	Services          []ServiceObservation `json:"services,omitempty"`
}

type ServiceObservation struct {
	Service     string `json:"service"`
	Image       string `json:"image"`
	ContainerID string `json:"container_id"`
	State       string `json:"state"`
	Health      string `json:"health"`
}

var observedServiceName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func safeExecutionReason(reason string) bool {
	switch reason {
	case "", "execution_deadline_exceeded", "preparation_failed", "preparation_interrupted", "runtime_ownership_unknown", "native_execution_unknown", "retained_material_unavailable", "candidate_failed", "recovery_interrupted", "first_release_cleanup_failed", "retained_image_unavailable", "recovery_health_failed", "recovery_routes_unknown", "first_release_failed":
		return true
	default:
		return false
	}
}

func ParseExecution(data []byte) (ExecutionRequest, error) {
	var request ExecutionRequest
	if len(data) == 0 || len(data) > MaxControlBytes || json.Unmarshal(data, &request) != nil || request.SchemaVersion != 1 || !executionAction(request.Action) || !validHash(request.Challenge) || !validHash(request.ManifestSHA256) || ValidateIdentity(request.Identity) != nil {
		return ExecutionRequest{}, ErrInvalid
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) {
		return ExecutionRequest{}, ErrInvalid
	}
	return request, nil
}

func executionAction(action string) bool {
	return action == "deploy_start" || action == "deploy_observe" || action == "deploy_confirm"
}

func ParseExecutionResponse(data []byte, expected ExecutionRequest) (ExecutionResponse, error) {
	if !executionAction(expected.Action) || ValidateIdentity(expected.Identity) != nil || !validHash(expected.ManifestSHA256) || !validHash(expected.Challenge) {
		return ExecutionResponse{}, ErrInvalid
	}
	var result ExecutionResponse
	if len(data) == 0 || len(data) > MaxStatusBytes || json.Unmarshal(data, &result) != nil {
		return ExecutionResponse{}, ErrInvalid
	}
	canonical, err := json.Marshal(result)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) || result.SchemaVersion != 1 || result.Action != expected.Action || result.Challenge != expected.Challenge || result.Identity != expected.Identity || result.ManifestSHA256 != expected.ManifestSHA256 || !finalSystemPath.MatchString(result.FinalSystem) || !executionPhase(result.Phase) || len(result.Services) > 64 || result.ActiveReleaseID != "" && !validID(result.ActiveReleaseID) || result.RouteConfigSHA256 != "" && !validHash(result.RouteConfigSHA256) {
		return ExecutionResponse{}, ErrInvalid
	}
	if !safeExecutionReason(result.Reason) {
		return ExecutionResponse{}, ErrInvalid
	}
	seen := make(map[string]bool, len(result.Services))
	for _, service := range result.Services {
		_, digest, found := strings.Cut(service.Image, "@sha256:")
		if !observedServiceName.MatchString(service.Service) || seen[service.Service] || len(service.Image) > 512 || !strings.HasPrefix(service.Image, "ghcr.io/") || !found || !validHash(digest) || !validHash(service.ContainerID) || service.State != "running" || service.Health != "healthy" {
			return ExecutionResponse{}, ErrInvalid
		}
		seen[service.Service] = true
	}
	if (result.Phase == "awaiting_dns" || result.Phase == "succeeded" || result.Phase == "recovered") && len(result.Services) == 0 {
		return ExecutionResponse{}, ErrInvalid
	}
	if result.Phase == "succeeded" && result.ActiveReleaseID != expected.Identity.ReleaseID || result.Phase == "recovered" && result.ActiveReleaseID != expected.Identity.ExpectedCurrentReleaseID {
		return ExecutionResponse{}, ErrInvalid
	}
	return result, nil
}

func executionPhase(phase string) bool {
	switch phase {
	case "starting", "preparing", "replacing", "checking", "applying_routes", "awaiting_dns", "succeeded", "recovering", "recovered", "failed", "needs_attention":
		return true
	default:
		return false
	}
}

func terminalPhase(phase string) bool {
	return phase == "succeeded" || phase == "recovered" || phase == "failed" || phase == "needs_attention"
}
