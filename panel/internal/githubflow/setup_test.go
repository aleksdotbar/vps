package githubflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
func setupService(t *testing.T) *Service {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, err := securestate.NewCipher(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.New(database).CreateAdministrator(context.Background(), store.CreateAdministratorParams{PasswordHash: "fixture-unused", KeyCheck: []byte("fixture-unused")}); err != nil {
		t.Fatal(err)
	}
	return New(database, cipher, "https://panel.example.ts.net", nil)
}
func TestSetupBindsOwnerSessionGenerationAndConsumesCallbacksBeforeConversion(t *testing.T) {
	service := setupService(t)
	ctx := context.Background()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyData := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	conversions := 0
	refresh := false
	resetDuringRead := false
	ownerID := int64(1)
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		var value any
		switch request.URL.Path {
		case "/users/operator":
			value = map[string]any{"id": 1, "login": "operator"}
		case "/app-manifests/generic-code/conversions":
			conversions++
			value = map[string]any{"id": 2, "slug": "owned-panel", "owner": map[string]any{"id": ownerID, "login": "operator"}, "client_id": "Iv1.generic", "pem": string(keyData)}
		case "/app/installations/10":
			value = map[string]any{"id": 10, "app_id": 2, "account": map[string]any{"id": 1, "login": "operator"}, "repository_selection": "selected", "permissions": map[string]string{"contents": "read", "actions": "write", "metadata": "read"}}
		case "/app/installations/10/access_tokens":
			if request.Header.Get("Authorization") == "" {
				t.Fatal("App JWT missing")
			}
			value = map[string]any{"token": uuid.NewString()}
		case "/installation/repositories":
			name := "generic"
			if refresh {
				name = "renamed"
			}
			if resetDuringRead {
				name = "revoked-change"
				if err := store.New(service.database).ResetAdministrator(ctx, "fixture-reset"); err != nil {
					t.Fatal(err)
				}
			}
			value = map[string]any{"total_count": 1, "repositories": []any{map[string]any{"id": 20, "name": name, "owner": map[string]any{"id": 1, "login": "operator"}}}}
		default:
			t.Fatalf("unexpected provider path %s", request.URL.Path)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
	})
	browser := Browser{SessionHash: bytes.Repeat([]byte{3}, 32), Generation: 1}
	begin := func() string {
		t.Helper()
		response, err := service.BeginSetup(ctx, &panelv1.BeginSetupRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Owner: "operator", Name: "owned-panel"}, browser)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(response.ManifestJson, "private_key") || !strings.Contains(response.ManifestJson, `"public":false`) {
			t.Fatal("unsafe manifest")
		}
		action, err := url.Parse(response.ActionUrl)
		if err != nil {
			t.Fatal(err)
		}
		return action.Query().Get("state")
	}
	token := begin()
	for _, wrong := range []Browser{{SessionHash: bytes.Repeat([]byte{4}, 32), Generation: 1}, {SessionHash: browser.SessionHash, Generation: 2}} {
		if _, err := service.CompleteManifest(ctx, token, "generic-code", wrong); !errors.Is(err, ErrState) {
			t.Fatalf("foreign browser accepted: %v", err)
		}
	}
	if conversions != 0 {
		t.Fatal("unauthorized callback reached conversion")
	}
	ownerID = 9
	if _, err := service.CompleteManifest(ctx, token, "generic-code", browser); !errors.Is(err, ErrOwner) {
		t.Fatalf("foreign owner accepted: %v", err)
	}
	if _, err := service.CompleteManifest(ctx, token, "generic-code", browser); !errors.Is(err, ErrState) || conversions != 1 {
		t.Fatal("consumed callback replay reached provider")
	}
	token = begin()
	ownerID = 1
	installationURL, err := service.CompleteManifest(ctx, token, "generic-code", browser)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(installationURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteInstallation(ctx, parsed.Query().Get("state"), 10, browser); err != nil {
		t.Fatal(err)
	}
	integration, err := service.GetIntegration(ctx)
	if err != nil || !integration.Installed || integration.AppId != 2 {
		t.Fatalf("integration not persisted: %v", err)
	}
	encrypted, err := store.New(service.database).GetEncryptedVersion(ctx, store.GetEncryptedVersionParams{Kind: "credential", OwnerID: "github-app", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Ciphertext, keyData) {
		t.Fatal("private key stored plaintext")
	}
	if err := store.New(service.database).ClearSelectedGitHubRepository(ctx, 20); err != nil {
		t.Fatal(err)
	}
	resume, err := service.BeginInstallation(ctx, &panelv1.BeginInstallationRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}}, browser)
	if err != nil {
		t.Fatal(err)
	}
	resumeURL, err := url.Parse(resume.InstallationUrl)
	if err != nil {
		t.Fatal(err)
	}
	refresh = true
	if err := service.CompleteInstallation(ctx, resumeURL.Query().Get("state"), 10, browser); err != nil {
		t.Fatal(err)
	}
	repository, err := store.New(service.database).GetGitHubRepository(ctx, 20)
	if err != nil || repository.Name != "renamed" || repository.Selected {
		t.Fatalf("refresh did not preserve deselection/canonical name: %v", err)
	}
	resume, err = service.BeginInstallation(ctx, &panelv1.BeginInstallationRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}}, browser)
	if err != nil {
		t.Fatal(err)
	}
	resumeURL, err = url.Parse(resume.InstallationUrl)
	if err != nil {
		t.Fatal(err)
	}
	resetDuringRead = true
	if err := service.CompleteInstallation(ctx, resumeURL.Query().Get("state"), 10, browser); !errors.Is(err, ErrState) {
		t.Fatalf("revoked browser committed callback: %v", err)
	}
	repository, err = store.New(service.database).GetGitHubRepository(ctx, 20)
	if err != nil || repository.Name != "renamed" {
		t.Fatalf("reset during provider read changed catalog: %v", err)
	}

}
func TestRegistryCredentialReplayIsKeyedAndNoPlaintextGetter(t *testing.T) {
	service := setupService(t)
	ctx := context.Background()
	request := &panelv1.SaveRegistryCredentialRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Username: "operator", Token: "ghp_" + strings.Repeat("x", 36)}
	first, err := service.SaveRegistryCredential(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.SaveRegistryCredential(ctx, request)
	if err != nil || replay.Version != first.Version {
		t.Fatalf("registry replay failed: %v", err)
	}
	request.Token = "ghp_" + strings.Repeat("y", 36)
	if _, err := service.SaveRegistryCredential(ctx, request); err == nil {
		t.Fatal("changed credential reused request identity")
	}
	integration, err := service.GetIntegration(ctx)
	if err != nil || !integration.RegistryCredentialConfigured || integration.RegistryVersion != 1 {
		t.Fatalf("credential metadata absent: %v", err)
	}
	var identity []byte
	if err := service.database.QueryRowContext(ctx, "SELECT identity FROM metadata_mutations WHERE request_id=?", request.Mutation.RequestId).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256([]byte(request.Token))
	if bytes.Equal(identity, plain[:]) {
		t.Fatal("credential identity exposes dictionary hash")
	}
}
