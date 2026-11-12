// Package installation binds the panel host to its local administrator, encrypted
// management credential and actual private SSH proof. It never marks it ready.
package installation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/remote"
)

const MaxHandoffBytes = 96 << 10

var (
	ErrInput    = errors.New("invalid panel installation handoff")
	ErrIdentity = errors.New("panel installation identity or credential conflicts with retained setup")
	ErrProof    = errors.New("private management access could not be proved; server remains setup pending")
	ErrRunning  = errors.New("panel.service is running; stop it or rerun panel setup")
)

type Handoff struct {
	SchemaVersion         int    `json:"schema_version"`
	InstallationID        string `json:"installation_id"`
	ServerID              string `json:"server_id"`
	TailscaleAddress      string `json:"tailscale_address"`
	HostPublicKey         string `json:"ssh_host_public_key"`
	HostFingerprint       string `json:"ssh_host_key_fingerprint"`
	ManagementFingerprint string `json:"management_fingerprint"`
	AdministratorPassword string `json:"administrator_password"`
	ManagementPrivateKey  string `json:"management_private_key"`
}

func Parse(data []byte) (Handoff, error) {
	var result Handoff
	if len(data) == 0 || len(data) > MaxHandoffBytes || !utf8.Valid(data) {
		return result, ErrInput
	}
	allowed := map[string]bool{"schema_version": false, "installation_id": false, "server_id": false, "tailscale_address": false, "ssh_host_public_key": false, "ssh_host_key_fingerprint": false, "management_fingerprint": false, "administrator_password": false, "management_private_key": false}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return result, ErrInput
	}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		seen, known := allowed[name]
		if err != nil || !ok || !known || seen {
			return result, ErrInput
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return result, ErrInput
		}
		allowed[name] = true
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return result, ErrInput
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return result, ErrInput
	}
	for _, seen := range allowed {
		if !seen {
			return result, ErrInput
		}
	}
	if json.Unmarshal(data, &result) != nil {
		return Handoff{}, ErrInput
	}
	return validate(result)
}

func validID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}
func validate(value Handoff) (Handoff, error) {
	if value.SchemaVersion != 1 || !validID(value.InstallationID) || !validID(value.ServerID) || len(value.AdministratorPassword) < 12 || len(value.AdministratorPassword) > 1024 || !utf8.ValidString(value.AdministratorPassword) || len(value.ManagementPrivateKey) == 0 || len(value.ManagementPrivateKey) > 64<<10 {
		return Handoff{}, ErrInput
	}
	if value.HostFingerprint == value.ManagementFingerprint {
		return Handoff{}, ErrInput
	}
	if _, err := remote.NewTarget(value.TailscaleAddress, "panel-runner", []byte(value.HostPublicKey), value.HostFingerprint); err != nil {
		return Handoff{}, ErrInput
	}
	host, _, _, _, err := ssh.ParseAuthorizedKey([]byte(value.HostPublicKey))
	if err != nil {
		return Handoff{}, ErrInput
	}
	value.HostPublicKey = strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(host)), "\n")
	signer, err := ssh.ParsePrivateKey([]byte(value.ManagementPrivateKey))
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != value.ManagementFingerprint {
		return Handoff{}, ErrInput
	}
	return value, nil
}
