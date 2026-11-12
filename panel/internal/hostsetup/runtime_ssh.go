package hostsetup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
)

var ErrRuntimeSSH = errors.New("SSH access on the running generation does not match the expected configuration")

const (
	maxSSHFileBytes      = 16 << 10
	maxSSHAuthorityBytes = 64 << 10
	maxSSHRecordBytes    = 96 << 10
)

type sshAuthorityFile struct {
	User    string `json:"user"`
	Source  string `json:"source"`
	Path    string `json:"path"`
	Active  bool   `json:"active"`
	Present bool   `json:"present"`
	UID     uint32 `json:"uid"`
	Mode    uint32 `json:"mode"`
	Bytes   []byte `json:"bytes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Keys    int    `json:"keys"`
}

type sshObservation struct {
	ConfigSHA256    string             `json:"config_sha256"`
	EffectiveSHA256 string             `json:"effective_sha256"`
	Files           []sshAuthorityFile `json:"files"`
}

type sshEffective map[string][]string

func (settings sshEffective) scalar(name string) string {
	values := settings[name]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func parseSSHEffective(data []byte) (sshEffective, error) {
	if len(data) == 0 || len(data) > 64<<10 {
		return nil, ErrRuntimeSSH
	}
	result := make(sshEffective)
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || name == "" || len(value) > 16<<10 {
			return nil, ErrRuntimeSSH
		}
		result[strings.ToLower(name)] = append(result[strings.ToLower(name)], strings.TrimSpace(value))
	}
	return result, nil
}

// admitSSHAuthority bounds and fingerprints authorized-key files. Their keys are
// not restricted: fresh images carry only provider-added keys, and the final
// system drops root and password access regardless.
func admitSSHAuthority(input foundation.Input, files []sshAuthorityFile) ([]sshAuthorityFile, error) {
	selected, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(input.Operator.PublicKey))
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrRuntimeSSH
	}
	files = append([]sshAuthorityFile(nil), files...)
	total, keys := 0, 0
	for index := range files {
		file := &files[index]
		file.Keys = 0
		if !file.Present && len(file.Bytes) != 0 {
			return nil, ErrRuntimeSSH
		}
		total += len(file.Bytes)
		if len(file.Bytes) > maxSSHFileBytes || total > maxSSHAuthorityBytes {
			return nil, ErrRuntimeSSH
		}
		for _, line := range bytes.Split(file.Bytes, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 || line[0] == '#' {
				continue
			}
			key, _, options, _, err := ssh.ParseAuthorizedKey(line)
			if err != nil {
				continue
			}
			// A restricted copy of the operator key would block operator access.
			if len(options) > 0 && bytes.Equal(key.Marshal(), selected.Marshal()) {
				return nil, ErrRuntimeSSH
			}
			file.Keys++
			keys++
			if keys > 128 {
				return nil, ErrRuntimeSSH
			}
		}
		if file.Present {
			digest := sha256.Sum256(file.Bytes)
			file.SHA256 = hex.EncodeToString(digest[:])
		}
	}
	return files, nil
}
