package hosthelper

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
)

// TunnelRequest selects only fixed native connector operations. Runtime paths,
// service commands and credential contents are never caller-selected.
type TunnelRequest struct {
	SchemaVersion  int    `json:"schema_version"`
	Action         string `json:"action"`
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	OperationID    string `json:"operation_id"`
	Challenge      string `json:"challenge"`
	AccountID      string `json:"account_id"`
	Name           string `json:"name"`
	TunnelID       string `json:"tunnel_id,omitempty"`
}

// TunnelResponse's Secret is returned only from preparation over authenticated
// management SSH. Callers must not log this value or persist it in panel state.
// CredentialProof means the owned connector authenticated; it is not app health.
type TunnelResponse struct {
	SchemaVersion   int    `json:"schema_version"`
	Action          string `json:"action"`
	InstallationID  string `json:"installation_id"`
	ServerID        string `json:"server_id"`
	OperationID     string `json:"operation_id"`
	Challenge       string `json:"challenge"`
	AccountID       string `json:"account_id"`
	Name            string `json:"name"`
	Phase           string `json:"phase"`
	TunnelID        string `json:"tunnel_id,omitempty"`
	ConfigSHA256    string `json:"config_sha256,omitempty"`
	CredentialProof bool   `json:"credential_proof"`
	Secret          []byte `json:"secret,omitempty"`
}

func exactHex(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size && hex.EncodeToString(decoded) == value
}

func validTunnelRequest(request TunnelRequest) bool {
	if request.SchemaVersion != 1 || !validID(request.InstallationID) || !validID(request.ServerID) ||
		!validID(request.OperationID) || !exactHex(request.Challenge, 32) || !exactHex(request.AccountID, 16) ||
		request.Name != "panel-"+request.ServerID+"-"+request.OperationID {
		return false
	}
	switch request.Action {
	case "tunnel_prepare", "tunnel_status":
		return request.TunnelID == ""
	case "tunnel_finalize":
		return validID(request.TunnelID)
	default:
		return false
	}
}

func ParseTunnelRequest(data []byte) (TunnelRequest, error) {
	var request TunnelRequest
	if len(data) == 0 || len(data) > maxRequestBytes || json.Unmarshal(data, &request) != nil || !validTunnelRequest(request) {
		return TunnelRequest{}, ErrRequest
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) {
		return TunnelRequest{}, ErrRequest
	}
	return request, nil
}

// ParseTunnelResponse binds every observation to the fresh request, including
// its immutable account/intent and challenge. Invalid secret bytes are cleared.
func ParseTunnelResponse(data []byte, request TunnelRequest) (TunnelResponse, error) {
	var response TunnelResponse
	reject := func() (TunnelResponse, error) {
		clear(response.Secret)
		return TunnelResponse{}, ErrRequest
	}
	if !validTunnelRequest(request) || len(data) == 0 || len(data) > maxRequestBytes || json.Unmarshal(data, &response) != nil {
		return reject()
	}
	// #nosec G117 -- Secret canonicalization is transient protocol validation only; this buffer and rejected secret bytes are cleared, never logged or retained.
	canonical, err := json.Marshal(response)
	defer clear(canonical)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSpace(data)) || response.SchemaVersion != 1 ||
		response.Action != request.Action || response.InstallationID != request.InstallationID ||
		response.ServerID != request.ServerID || response.OperationID != request.OperationID ||
		response.Challenge != request.Challenge || response.AccountID != request.AccountID || response.Name != request.Name {
		return reject()
	}
	if response.TunnelID != "" && !validID(response.TunnelID) ||
		(request.TunnelID != "" && response.TunnelID != request.TunnelID) ||
		(response.ConfigSHA256 != "" && !exactHex(response.ConfigSHA256, 32)) {
		return reject()
	}
	if request.Action != "tunnel_prepare" && len(response.Secret) != 0 {
		return reject()
	}
	switch response.Phase {
	case "prepared":
		if request.Action == "tunnel_finalize" || response.TunnelID != "" || response.ConfigSHA256 != "" || response.CredentialProof ||
			(request.Action == "tunnel_prepare" && len(response.Secret) != 32) ||
			(request.Action == "tunnel_status" && len(response.Secret) != 0) {
			return reject()
		}
	case "finalizing":
		if response.TunnelID == "" || response.CredentialProof || len(response.Secret) != 0 {
			return reject()
		}
	case "ready":
		if response.TunnelID == "" || response.ConfigSHA256 == "" || !response.CredentialProof || len(response.Secret) != 0 {
			return reject()
		}
	case "needs_attention":
		if response.CredentialProof || len(response.Secret) != 0 {
			return reject()
		}
	default:
		return reject()
	}
	return response, nil
}
