// Package deployment owns immutable private release material and bounded native
// deployment execution. Readiness requires direct native observations.
package deployment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
)

const MaxStageBytes = 8 << 20
const MaxControlBytes = 4096
const MaxStatusBytes = 64 << 10

var ErrInvalid = errors.New("invalid deployment staging input")
var ErrState = errors.New("deployment staging state is partial or belongs to another intent")
var ErrBusy = errors.New("another deployment owns this environment")
var ErrDeadline = errors.New("deployment execution deadline is expired or invalid")

type Identity struct {
	InstallationID           string `json:"installation_id"`
	ServerID                 string `json:"server_id"`
	EnvironmentID            string `json:"environment_id"`
	OperationID              string `json:"operation_id"`
	ReleaseID                string `json:"release_id"`
	BindingVersion           int64  `json:"binding_version"`
	ExpectedCurrentReleaseID string `json:"expected_current_release_id,omitempty"`
	ExecutionDeadline        int64  `json:"execution_deadline"`
}

type Manifest struct {
	Identity            Identity                `json:"identity"`
	EnvironmentVersion  int64                   `json:"environment_version"`
	SecretVersion       int64                   `json:"secret_version"`
	ReleaseSHA256       string                  `json:"release_sha256"`
	RouteSnapshotSHA256 string                  `json:"route_snapshot_sha256"`
	RouteSnapshot       routes.Snapshot         `json:"route_snapshot"`
	BridgeName          string                  `json:"bridge_name"`
	Artifact            releases.Artifact       `json:"artifact"`
	Config              json.RawMessage         `json:"config"`
	Domains             []*panelv1.Domain       `json:"domains"`
	Bindings            []compose.SecretBinding `json:"bindings"`
}

type StageRequest struct {
	SchemaVersion int                 `json:"schema_version"`
	Action        string              `json:"action"`
	Challenge     string              `json:"challenge"`
	Manifest      Manifest            `json:"manifest"`
	SecretValues  map[string][]byte   `json:"secret_values"`
	Registry      *RegistryCredential `json:"registry,omitempty"`
}

type RegistryCredential struct {
	Username string `json:"username"`
	Token    []byte `json:"token"`
}

func (request *StageRequest) Clear() {
	if request.Registry != nil {
		clear(request.Registry.Token)
		request.Registry = nil
	}
	for key, value := range request.SecretValues {
		clear(value)
		delete(request.SecretValues, key)
	}
}

type StatusRequest struct {
	SchemaVersion  int      `json:"schema_version"`
	Action         string   `json:"action"`
	Challenge      string   `json:"challenge"`
	Identity       Identity `json:"identity"`
	ManifestSHA256 string   `json:"manifest_sha256"`
}

type Response struct {
	SchemaVersion  int      `json:"schema_version"`
	Action         string   `json:"action"`
	Challenge      string   `json:"challenge"`
	Identity       Identity `json:"identity"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	Phase          string   `json:"phase"`
	Reason         string   `json:"reason,omitempty"`
	FinalSystem    string   `json:"final_system"`
	Ready          bool     `json:"ready"`
}

func validID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}
func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

var finalSystemPath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-nixos-system-[A-Za-z0-9.+_-]+$`)

func ValidateIdentity(value Identity) error {
	for _, id := range []string{value.InstallationID, value.ServerID, value.EnvironmentID, value.OperationID, value.ReleaseID} {
		if !validID(id) {
			return ErrInvalid
		}
	}
	if value.BindingVersion < 1 || value.ExecutionDeadline < 1 || value.ExpectedCurrentReleaseID != "" && !validID(value.ExpectedCurrentReleaseID) {
		return ErrInvalid
	}
	return nil
}

func ParseStage(data []byte) (request StageRequest, err error) {
	defer func() {
		if err != nil {
			request.Clear()
		}
	}()
	if len(data) == 0 || len(data) > MaxStageBytes || json.Unmarshal(data, &request) != nil || request.SchemaVersion != 1 || request.Action != "deploy_stage" || !validHash(request.Challenge) || ValidateIdentity(request.Manifest.Identity) != nil {
		return request, ErrInvalid
	}
	// #nosec G117 -- Canonical validation of transient SSH secret bytes; this buffer and decoded values are cleared on rejection and by the caller after use.
	canonical, encodeErr := json.Marshal(request)
	defer clear(canonical)
	if encodeErr != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) {
		return request, ErrInvalid
	}
	return request, nil
}

func ParseStatus(data []byte) (StatusRequest, error) {
	var request StatusRequest
	if len(data) == 0 || len(data) > MaxControlBytes || json.Unmarshal(data, &request) != nil || request.SchemaVersion != 1 || request.Action != "deploy_status" || !validHash(request.Challenge) || !validHash(request.ManifestSHA256) || ValidateIdentity(request.Identity) != nil {
		return StatusRequest{}, ErrInvalid
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) {
		return StatusRequest{}, ErrInvalid
	}
	return request, nil
}

func ManifestSHA256(manifest Manifest) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", ErrInvalid
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func ParseResponse(data []byte, action, challenge string, identity Identity, manifestSHA256 string) (Response, error) {
	if action != "deploy_stage" && action != "deploy_status" || !validHash(challenge) || !validHash(manifestSHA256) || ValidateIdentity(identity) != nil {
		return Response{}, ErrInvalid
	}
	var response Response
	if len(data) == 0 || len(data) > MaxStatusBytes || json.Unmarshal(data, &response) != nil {
		return Response{}, ErrInvalid
	}
	canonical, err := json.Marshal(response)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) || response.SchemaVersion != 1 || response.Action != action || response.Challenge != challenge || response.Identity != identity || response.ManifestSHA256 != manifestSHA256 || response.Ready || !finalSystemPath.MatchString(response.FinalSystem) || (response.Phase != "staged" && response.Phase != "needs_attention") {
		return Response{}, ErrInvalid
	}
	if response.Phase == "staged" && response.Reason != "" || response.Phase == "needs_attention" && response.Reason != "staging_incomplete" && response.Reason != "execution_deadline_exceeded" {
		return Response{}, ErrInvalid
	}
	return response, nil
}
