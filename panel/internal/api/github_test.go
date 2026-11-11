package api_test

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestGitHubCallbacksRequireOwningLiveSessionAndRejectDuplicateParameters(t *testing.T) {
	installation := setup(t)
	ctx := context.Background()
	token := strings.Repeat("a", 64)
	stateHash := sha256.Sum256([]byte(token))
	code := "nonreflecting-callback-code"
	check := func(client *http.Client, path string, want int) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, installation.origin+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, 2048))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("callback status %d, want%d", response.StatusCode, want)
		}
		if strings.Contains(string(body), token) || strings.Contains(string(body), code) {
			t.Fatal("callback reflected sensitive query")
		}
	}
	path := "/github/manifest?state=" + token + "&code=" + code
	check(installation.client, path, http.StatusForbidden)
	if _, err := login(installation, "initial-private-password", installation.origin); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(installation.origin)
	if err != nil {
		t.Fatal(err)
	}
	var bearer string
	for _, cookie := range installation.client.Jar.Cookies(parsed) {
		if cookie.Name == "__Host-panel" {
			bearer = cookie.Value
		}
	}
	if bearer == "" {
		t.Fatal("missing session cookie")
	}
	sessionHash := sha256.Sum256([]byte(bearer))
	if err := store.New(installation.database).CreateGitHubSetupState(ctx, store.CreateGitHubSetupStateParams{StateHash: stateHash[:], Phase: "manifest", OwnerID: 1, OwnerLogin: "operator", SessionHash: sessionHash[:], Generation: 1, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	check(installation.client, path+"&state="+token, http.StatusBadRequest)
	check(installation.client, path+"&code="+code, http.StatusBadRequest)
	foreign := *installation
	foreign.client = installation.server.Client()
	foreign.client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := login(&foreign, "initial-private-password", foreign.origin); err != nil {
		t.Fatal(err)
	}
	check(foreign.client, path, http.StatusBadRequest)
	state, err := store.New(installation.database).GetGitHubSetupState(ctx, stateHash[:])
	if err != nil || state.Consumed {
		t.Fatalf("foreign callback consumed owner state: %v", err)
	}
	if err := installation.authentication.Reset(ctx, []byte("replacement-private-password")); err != nil {
		t.Fatal(err)
	}
	check(installation.client, path, http.StatusForbidden)
	check(foreign.client, "/github/installation?state="+token+"&installation_id=10", http.StatusForbidden)
}
