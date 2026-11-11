package hosthelper

import (
	"bytes"
	"encoding/json"
)

const DockerVersion = "29.9.2"
const ComposeVersion = "5.4.0"

// ReadinessRequest has no paths or executable arguments. Each observation uses
// a fresh challenge over the installation's pinned SSH management connection.
type ReadinessRequest struct {
	SchemaVersion  int    `json:"schema_version"`
	Action         string `json:"action"`
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	Challenge      string `json:"challenge"`
}

type ReadinessResponse struct {
	SchemaVersion  int    `json:"schema_version"`
	Action         string `json:"action"`
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	Challenge      string `json:"challenge"`
	Role           string `json:"role"`
	Stage          string `json:"stage"`
	CurrentSystem  string `json:"current_system"`
	BootedSystem   string `json:"booted_system"`
	SystemProfile  string `json:"system_profile"`
	Status         string `json:"status"`
	DockerVersion  string `json:"docker_version"`
	ComposeVersion string `json:"compose_version"`
}

func validReadinessRequest(request ReadinessRequest) bool {
	return request.SchemaVersion == 1 && request.Action == "readiness" && validID(request.InstallationID) && validID(request.ServerID) && exactHex(request.Challenge, 32)
}

func ParseReadinessRequest(data []byte) (ReadinessRequest, error) {
	var request ReadinessRequest
	if len(data) == 0 || len(data) > maxRequestBytes || json.Unmarshal(data, &request) != nil || !validReadinessRequest(request) {
		return ReadinessRequest{}, ErrRequest
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) {
		return ReadinessRequest{}, ErrRequest
	}
	return request, nil
}

func ParseReadinessResponse(data []byte, request ReadinessRequest) (ReadinessResponse, error) {
	var response ReadinessResponse
	if !validReadinessRequest(request) || len(data) == 0 || len(data) > maxRequestBytes || json.Unmarshal(data, &response) != nil {
		return ReadinessResponse{}, ErrIdentity
	}
	canonical, err := json.Marshal(response)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) || response.SchemaVersion != 1 || response.Action != request.Action || response.InstallationID != request.InstallationID || response.ServerID != request.ServerID || response.Challenge != request.Challenge || (response.Role != "panel" && response.Role != "server") || (response.Stage != "enrollment" && response.Stage != "final") || !systemPath.MatchString(response.CurrentSystem) || !systemPath.MatchString(response.BootedSystem) || !systemPath.MatchString(response.SystemProfile) {
		return ReadinessResponse{}, ErrIdentity
	}
	switch response.Status {
	case "ready":
		if response.Stage != "final" || response.CurrentSystem != response.SystemProfile || response.CurrentSystem != response.BootedSystem || response.DockerVersion != DockerVersion || response.ComposeVersion != ComposeVersion {
			return ReadinessResponse{}, ErrIdentity
		}
	case "setup_pending":
		pendingReboot := response.Stage == "final" && response.CurrentSystem == response.SystemProfile && response.BootedSystem != response.CurrentSystem
		if (response.Stage != "enrollment" && !pendingReboot) || response.DockerVersion != "" || response.ComposeVersion != "" {
			return ReadinessResponse{}, ErrIdentity
		}
	case "needs_attention":
		if response.DockerVersion != "" || response.ComposeVersion != "" {
			return ReadinessResponse{}, ErrIdentity
		}
	default:
		return ReadinessResponse{}, ErrIdentity
	}
	return response, nil
}
