package hosthelper

import (
	"encoding/json"
	"strings"
	"testing"
)

func tunnelRequestFixture() TunnelRequest {
	request := TunnelRequest{SchemaVersion: 1, Action: "tunnel_prepare",
		InstallationID: "11111111-1111-4111-8111-111111111111",
		ServerID:       "22222222-2222-4222-8222-222222222222",
		OperationID:    "33333333-3333-4333-8333-333333333333",
		Challenge:      strings.Repeat("a", 64), AccountID: strings.Repeat("b", 32)}
	request.Name = "panel-" + request.ServerID + "-" + request.OperationID
	return request
}

func TestTunnelRequestsCannotSelectForeignResourcesOrCommands(t *testing.T) {
	request := tunnelRequestFixture()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := ParseTunnelRequest(encoded); err != nil || actual != request {
		t.Fatalf("bound preparation rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*TunnelRequest)
	}{
		{"foreign name", func(r *TunnelRequest) { r.Name = "unmanaged" }},
		{"shell command", func(r *TunnelRequest) { r.Action = "sh" }},
		{"missing challenge", func(r *TunnelRequest) { r.Challenge = "" }},
		{"noncanonical account", func(r *TunnelRequest) { r.AccountID = strings.Repeat("B", 32) }},
		{"prepare with tunnel", func(r *TunnelRequest) { r.TunnelID = r.OperationID }},
		{"finalize without tunnel", func(r *TunnelRequest) { r.Action = "tunnel_finalize" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.change(&changed)
			data, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseTunnelRequest(data); err == nil {
				t.Fatal("unsafe tunnel authority accepted")
			}
		})
	}
	for _, added := range []string{`,"action":"tunnel_prepare"}`, `,"path":"/etc/shadow"}`} {
		if _, err := ParseTunnelRequest([]byte(strings.TrimSuffix(string(encoded), "}") + added)); err == nil {
			t.Fatal("ambiguous or caller-selected authority accepted")
		}
	}
}

func TestTunnelObservationsBindChallengeAndKeepSecretsInPreparationOnly(t *testing.T) {
	request := tunnelRequestFixture()
	response := TunnelResponse{SchemaVersion: 1, Action: request.Action,
		InstallationID: request.InstallationID, ServerID: request.ServerID,
		OperationID: request.OperationID, Challenge: request.Challenge,
		AccountID: request.AccountID, Name: request.Name, Phase: "prepared", Secret: make([]byte, 32)}
	for _, test := range []struct {
		name   string
		change func(*TunnelRequest, *TunnelResponse)
		valid  bool
	}{
		{"prepared", func(*TunnelRequest, *TunnelResponse) {}, true},
		{"foreign challenge", func(_ *TunnelRequest, r *TunnelResponse) { r.Challenge = strings.Repeat("c", 64) }, false},
		{"foreign operation", func(_ *TunnelRequest, r *TunnelResponse) { r.OperationID = request.ServerID }, false},
		{"missing prepare secret", func(_ *TunnelRequest, r *TunnelResponse) { r.Secret = nil }, false},
		{"secret in status", func(q *TunnelRequest, r *TunnelResponse) { q.Action, r.Action = "tunnel_status", "tunnel_status" }, false},
		{"status without secret", func(q *TunnelRequest, r *TunnelResponse) {
			q.Action, r.Action, r.Secret = "tunnel_status", "tunnel_status", nil
		}, true},
		{"unproven ready", func(_ *TunnelRequest, r *TunnelResponse) {
			r.Phase, r.TunnelID, r.ConfigSHA256, r.Secret = "ready", request.OperationID, strings.Repeat("d", 64), nil
		}, false},
		{"owned ready", func(_ *TunnelRequest, r *TunnelResponse) {
			r.Phase, r.TunnelID, r.ConfigSHA256, r.Secret = "ready", request.OperationID, strings.Repeat("d", 64), nil
			r.CredentialProof = true
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, observed := request, response
			test.change(&selected, &observed)
			// #nosec G117 -- This parser fixture contains only a synthetic all-zero secret, never operational credentials.
			data, err := json.Marshal(observed)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := ParseTunnelResponse(data, selected)
			defer clear(actual.Secret)
			if (err == nil) != test.valid {
				t.Fatalf("observation acceptance=%t, want %t", err == nil, test.valid)
			}
		})
	}
}
