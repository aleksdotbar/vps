// Package foundation admits nonsecret inputs for a fixed existing-host build.
// Admission does not discover a target or authorize changing its current policy.
package foundation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/machine"
)

const MaxInputBytes = 64 << 10

var ErrInvalidInput = errors.New("invalid or unsupported foundation input")

type Role string

const (
	Panel  Role = "panel"
	Server Role = "server"
)

type Input struct {
	SchemaVersion  int            `json:"schema_version"`
	InstallationID string         `json:"installation_id"`
	ServerID       string         `json:"server_id"`
	Role           Role           `json:"role"`
	Operator       Operator       `json:"operator"`
	Bootstrap      Bootstrap      `json:"bootstrap"`
	Machine        machine.Config `json:"machine"`
}

type Operator struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

type Bootstrap struct {
	PublicSSH22                       bool   `json:"public_ssh22"`
	RootLogin                         string `json:"root_login"`
	PasswordAuthentication            bool   `json:"password_authentication"`
	KeyboardInteractiveAuthentication bool   `json:"keyboard_interactive_authentication"`
}

// Parse requires every envelope/account/bootstrap field. The embedded machine
// retains its own nullable settings and admission rules, not another schema here.
func Parse(data []byte) (Input, error) {
	if len(data) == 0 || len(data) > MaxInputBytes || !utf8.Valid(data) {
		return Input{}, ErrInvalidInput
	}
	fields, err := exactObject(data, "schema_version", "installation_id", "server_id", "role", "operator", "bootstrap", "machine")
	if err != nil {
		return Input{}, err
	}
	if _, err := exactObject(fields["operator"], "name", "public_key"); err != nil {
		return Input{}, err
	}
	if _, err := exactObject(fields["bootstrap"], "public_ssh22", "root_login", "password_authentication", "keyboard_interactive_authentication"); err != nil {
		return Input{}, err
	}
	var input Input
	if json.Unmarshal(data, &input) != nil {
		return Input{}, ErrInvalidInput
	}
	input.Machine, err = machine.Parse(fields["machine"])
	if err != nil {
		return Input{}, ErrInvalidInput
	}
	return normalize(input)
}

// Canonical includes the hash of machine.Canonical's exact bytes. The caller may
// hash the complete returned bytes separately; input_sha256 is not self-embedded.
// No trailing newline is emitted, so artifact hashing has one exact byte string.
func Canonical(input Input) ([]byte, error) {
	input, err := normalize(input)
	if err != nil {
		return nil, err
	}
	machineData, machineSHA, err := machine.Canonical(input.Machine)
	if err != nil {
		return nil, ErrInvalidInput
	}
	// RawMessage preserves the canonical machine bytes rather than independently
	// choosing an encoding for the hash and the embedded object.
	output := struct {
		SchemaVersion  int             `json:"schema_version"`
		InstallationID string          `json:"installation_id"`
		ServerID       string          `json:"server_id"`
		Role           Role            `json:"role"`
		Operator       Operator        `json:"operator"`
		Bootstrap      Bootstrap       `json:"bootstrap"`
		Machine        json.RawMessage `json:"machine"`
		MachineSHA256  string          `json:"machine_sha256"`
	}{input.SchemaVersion, input.InstallationID, input.ServerID, input.Role, input.Operator, input.Bootstrap, machineData, machineSHA}
	data, err := json.Marshal(output)
	if err != nil || len(data) > MaxInputBytes {
		return nil, ErrInvalidInput
	}
	return data, nil
}

var userName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func normalize(input Input) (Input, error) {
	if input.SchemaVersion != 1 || (input.Role != Panel && input.Role != Server) || !canonicalID(input.InstallationID) || !canonicalID(input.ServerID) {
		return Input{}, ErrInvalidInput
	}
	if !userName.MatchString(input.Operator.Name) || reservedOperator(input.Operator.Name) {
		return Input{}, ErrInvalidInput
	}
	keyText := strings.TrimSuffix(input.Operator.PublicKey, "\n")
	if len(input.Operator.PublicKey) > 1024 || strings.ContainsAny(keyText, "\r\n") {
		return Input{}, ErrInvalidInput
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(keyText))
	if err != nil || len(options) != 0 || len(rest) != 0 || key.Type() != ssh.KeyAlgoED25519 {
		return Input{}, ErrInvalidInput
	}
	input.Operator.PublicKey = strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(key)), "\n")
	if input.Bootstrap.RootLogin != "no" && input.Bootstrap.RootLogin != "prohibit-password" && input.Bootstrap.RootLogin != "yes" {
		return Input{}, ErrInvalidInput
	}
	if machine.Validate(input.Machine) != nil {
		return Input{}, ErrInvalidInput
	}
	return input, nil
}

func canonicalID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func reservedOperator(name string) bool {
	if strings.HasPrefix(name, "nixbld") || strings.HasPrefix(name, "systemd-") {
		return true
	}
	switch name {
	case "root", "nobody", "panel", "panel-runner", "panel-cloudflared", "tailscale", "tailscaled", "cloudflared", "docker",
		"sshd", "nix", "daemon", "bin", "sys", "sync", "halt", "shutdown", "messagebus",
		"polkituser", "polkitd", "dhcpcd", "nscd", "nginx", "caddy", "postgres", "mysql",
		"redis", "ntp", "chrony", "avahi", "ftp", "mail", "www-data":
		return true
	default:
		return false
	}
}

// Three fixed records need exact required keys and duplicate/null rejection.
// Nested machine structure is deliberately left to machine.Parse.
func exactObject(data []byte, names ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, ErrInvalidInput
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrInvalidInput
		}
		name, ok := token.(string)
		if !ok || !slices.Contains(names, name) || fields[name] != nil {
			return nil, ErrInvalidInput
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(raw, []byte("null")) {
			return nil, ErrInvalidInput
		}
		fields[name] = raw
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != len(names) {
		return nil, ErrInvalidInput
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidInput
	}
	return fields, nil
}
