package hostsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/machine"
)

var ErrSourceMismatch = errors.New("existing system or evaluated source does not match reviewed setup input")

type sourcePacket struct {
	SchemaVersion int `json:"schema_version"`
	// SystemPath and BootFiles stay in the packet shape but are not compared:
	// provider images are not byte-identical rebuilds of their own configuration.
	SystemPath          string          `json:"system_path"`
	BootFiles           json.RawMessage `json:"boot_files,omitempty"`
	ReviewedInputSHA256 string          `json:"reviewed_input_sha256"`
	Unsupported         []string        `json:"unsupported"`
	Machine             json.RawMessage `json:"machine"`
	MutableUsers        *bool           `json:"mutable_users"`
	Accounts            json.RawMessage `json:"accounts"`
	Groups              json.RawMessage `json:"groups"`
	SSH                 json.RawMessage `json:"ssh"`
}

// Only source consistency is admitted here. Account/key details remain opaque
// root-local evidence until the later native runtime/access comparison.
func confirmSourcePacket(data []byte, input foundation.Input, expectedInputSHA256 string) error {
	if len(data) == 0 || len(data) > maxInspectionBytes {
		return ErrSourceMismatch
	}
	fields := map[string]bool{"schema_version": false, "system_path": false, "boot_files": false, "reviewed_input_sha256": false, "unsupported": false,
		"machine": false, "mutable_users": false, "accounts": false, "groups": false, "ssh": false}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ErrSourceMismatch
	}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		seen, known := fields[name]
		if err != nil || !ok || !known || seen {
			return ErrSourceMismatch
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrSourceMismatch
		}
		fields[name] = true
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return ErrSourceMismatch
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrSourceMismatch
	}
	for name, present := range fields {
		if !present && name != "boot_files" {
			return ErrSourceMismatch
		}
	}
	var packet sourcePacket
	if json.Unmarshal(data, &packet) != nil || packet.SchemaVersion != 1 ||
		packet.ReviewedInputSHA256 != expectedInputSHA256 || len(packet.Unsupported) != 0 || packet.MutableUsers == nil || !*packet.MutableUsers {
		return ErrSourceMismatch
	}
	if _, err := machine.Parse(packet.Machine); err != nil {
		return ErrSourceMismatch
	}
	canonical, err := foundation.Canonical(input)
	if err != nil || !matchesPin(canonical, expectedInputSHA256) {
		return ErrSourceMismatch
	}
	// Native policy comparison uses the exact reviewed machine as its baseline.
	// Effective defaults/order can expand the observed machine, so its canonical
	// hash is not interchangeable with the reviewed machine's authority hash.
	for _, record := range []struct {
		data    json.RawMessage
		opening byte
	}{{packet.Accounts, '['}, {packet.Groups, '['}, {packet.SSH, '{'}} {
		trimmed := bytes.TrimSpace(record.data)
		if len(trimmed) == 0 || trimmed[0] != record.opening {
			return ErrSourceMismatch
		}
	}
	return nil
}
