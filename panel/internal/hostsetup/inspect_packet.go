package hostsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/machine"
)

var ErrSourceMismatch = errors.New("existing system or evaluated source does not match reviewed setup input")

type sourcePacket struct {
	SchemaVersion       int             `json:"schema_version"`
	SystemPath          string          `json:"system_path"`
	BootFiles           bootFiles       `json:"boot_files"`
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
func confirmSourcePacket(data []byte, input foundation.Input, expectedInputSHA256, currentSystem string) error {
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
	for _, present := range fields {
		if !present {
			return ErrSourceMismatch
		}
	}
	var packet sourcePacket
	if json.Unmarshal(data, &packet) != nil || packet.SchemaVersion != 1 || !runsEvaluatedSource(packet, input, currentSystem) ||
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

type bootFiles struct {
	Kernel string `json:"kernel"`
	Initrd string `json:"initrd"`
}

// runsEvaluatedSource requires the running generation to be the evaluated one.
// Hostinger's image assembles generated files (bashrc, dbus configuration)
// differently from a rebuild of its own configuration, so for that template the
// running kernel and initrd must match instead; the template comparison and
// runtime checks cover the remaining configuration.
func runsEvaluatedSource(packet sourcePacket, input foundation.Input, currentSystem string) bool {
	if input.Machine.Template != machine.HostingerKVM {
		return packet.SystemPath == currentSystem
	}
	kernel, kernelErr := os.Readlink(currentSystem + "/kernel")
	initrd, initrdErr := os.Readlink(currentSystem + "/initrd")
	return kernelErr == nil && initrdErr == nil && packet.BootFiles.Kernel != "" && packet.BootFiles.Initrd != "" &&
		kernel == packet.BootFiles.Kernel && initrd == packet.BootFiles.Initrd
}
