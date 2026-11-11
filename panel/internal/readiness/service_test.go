package readiness

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type fixtureHelper func(context.Context, remote.Target, []byte, []byte) (remote.Result, error)

func (f fixtureHelper) Helper(ctx context.Context, target remote.Target, key, payload []byte) (remote.Result, error) {
	return f(ctx, target, key, payload)
}

type fixture struct {
	service              *Service
	database             *sql.DB
	cipher               *securestate.Cipher
	installation, server string
	private              []byte
	sequence             int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, err := securestate.NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	result := &fixture{database: database, cipher: cipher, installation: uuid.NewString(), private: pem.EncodeToMemory(block)}
	result.service = New(database, cipher, nil)
	result.server = result.addServer(t, true)
	if err := store.New(database).CreateInstallation(t.Context(), store.CreateInstallationParams{InstallationID: result.installation, PanelServerID: result.server, OperationID: uuid.NewString(), InputSha256: strings.Repeat("a", 64), SourceSystem: "fixture", EnrollmentSystem: "fixture", BootstrapIdentity: bytes.Repeat([]byte{1}, 32), CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.New(database).RecordPanelPrivateProof(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *fixture) addServer(t *testing.T, panel bool) string {
	t.Helper()
	id := uuid.NewString()
	f.sequence++
	_, host, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(host)
	if err != nil {
		t.Fatal(err)
	}
	managementSigner, err := ssh.ParsePrivateKey(f.private)
	if err != nil {
		t.Fatal(err)
	}
	queries := store.New(f.database)
	if err := queries.CreateServer(t.Context(), store.CreateServerParams{ID: id, Name: "fixture", TailscaleAddress: fmt.Sprintf("100.64.0.%d", f.sequence), SshFingerprint: ssh.FingerprintSHA256(hostSigner.PublicKey()), PanelHost: panel, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	owner := "ssh:" + f.installation + ":" + id
	ref, err := securestate.NewVault(f.database, f.cipher).SaveCredential(t.Context(), owner, 0, f.private)
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateServerManagement(t.Context(), store.CreateServerManagementParams{ServerID: id, CredentialKind: "credential", CredentialOwnerID: owner, CredentialVersion: ref.Version, HostPublicKey: string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())), ManagementFingerprint: ssh.FingerprintSHA256(managementSigner.PublicKey())}); err != nil {
		t.Fatal(err)
	}
	return id
}

func readyReply(t *testing.T, request hosthelper.ReadinessRequest, role string) remote.Result {
	t.Helper()
	path := "/nix/store/" + strings.Repeat("a", 32) + "-nixos-system-fixture"
	response := hosthelper.ReadinessResponse{SchemaVersion: 1, Action: "readiness", InstallationID: request.InstallationID, ServerID: request.ServerID, Challenge: request.Challenge, Role: role, Stage: "final", CurrentSystem: path, BootedSystem: path, SystemProfile: path, Status: "ready", DockerVersion: hosthelper.DockerVersion, ComposeVersion: hosthelper.ComposeVersion}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return remote.Result{Outcome: remote.CommandExited, ExitCode: 0, Output: data}
}

func TestReadinessPersistsOnlyFreshPinnedEvidenceAndSeparatesUnavailable(t *testing.T) {
	f := setup(t)
	queries := store.New(f.database)
	var previous string
	for _, mode := range []string{"ready", "challenge replay", "unreachable", "unsupported", "binding changed"} {
		t.Run(mode, func(t *testing.T) {
			f.service.remote = fixtureHelper(func(ctx context.Context, target remote.Target, key, payload []byte) (remote.Result, error) {
				if !bytes.Equal(key, f.private) {
					t.Error("wrong encrypted credential selected")
				}
				request, err := hosthelper.ParseReadinessRequest(payload)
				if err != nil {
					t.Fatal(err)
				}
				if request.Challenge == previous {
					t.Error("reused readiness challenge")
				}
				current := request.Challenge
				if mode == "challenge replay" {
					request.Challenge = previous
				}
				previous = current
				if mode == "unreachable" {
					return remote.Result{}, remote.ErrTransport
				}
				result := readyReply(t, request, "panel")
				if mode == "unsupported" {
					result.Output = bytes.Replace(result.Output, []byte(hosthelper.DockerVersion), []byte("0.0.0"), 1)
				}
				if mode == "binding changed" {
					if _, err := f.database.ExecContext(ctx, `UPDATE servers SET tailscale_address='100.64.0.2',readiness='setup_pending',observed_at=0 WHERE id=?`, f.server); err != nil {
						t.Fatal(err)
					}
				}
				return result, nil
			})
			if err := f.service.Sweep(t.Context()); err != nil {
				t.Fatal(err)
			}
			server, err := queries.GetServer(t.Context(), f.server)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"ready": "ready", "challenge replay": "needs_attention", "unreachable": "unreachable", "unsupported": "needs_attention", "binding changed": "setup_pending"}[mode]
			if server.Readiness != want {
				t.Fatalf("got %s, want %s", server.Readiness, want)
			}
			if mode == "ready" && !Fresh(server, time.Now()) {
				t.Fatal("confirmed observation not fresh")
			}
			if mode != "binding changed" && server.ObservedAt <= 0 {
				t.Fatal("failure observation did not rotate scheduling")
			}
		})
	}
	server := store.Server{Readiness: "ready", ObservedAt: time.Now().Add(-MaxAge - time.Second).UnixMilli()}
	if Fresh(server, time.Now()) {
		t.Fatal("stale ready flag authorized deployment")
	}
	server.ObservedAt = time.Now().Add(time.Minute).UnixMilli()
	if Fresh(server, time.Now()) {
		t.Fatal("future observation authorized deployment")
	}
}

func TestReadinessSweepBoundsParallelismAndRotatesFailedTargets(t *testing.T) {
	f := setup(t)
	for range 4 {
		f.addServer(t, false)
	}
	var mutex sync.Mutex
	seen := make(map[string]bool)
	active, peak := 0, 0
	f.service.remote = fixtureHelper(func(ctx context.Context, _ remote.Target, _ []byte, payload []byte) (remote.Result, error) {
		request, err := hosthelper.ParseReadinessRequest(payload)
		if err != nil {
			t.Fatal(err)
		}
		mutex.Lock()
		active++
		if active > peak {
			peak = active
		}
		seen[request.ServerID] = true
		mutex.Unlock()
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
		}
		mutex.Lock()
		active--
		mutex.Unlock()
		return remote.Result{}, remote.ErrTransport
	})
	if err := f.service.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 || peak > 2 {
		t.Fatalf("unbounded first sweep seen%d peak%d", len(seen), peak)
	}
	if err := f.service.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 5 || peak > 2 {
		t.Fatalf("old failed host starved another target seen%d peak%d", len(seen), peak)
	}
	rows, err := f.database.QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT s.* FROM servers s WHERE EXISTS(SELECT 1 FROM server_management m WHERE m.server_id=s.id) ORDER BY s.observed_at,s.id LIMIT 4`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || strings.Contains(detail, "servers_readiness_order")
		if strings.Contains(detail, "USE TEMP B-TREE") {
			t.Fatal("sweep sorts unbounded inventory")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("readiness sweep lost oldest-first index")
	}
}
