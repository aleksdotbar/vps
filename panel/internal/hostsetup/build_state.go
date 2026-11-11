// Package hostsetup builds, inspects and activates this host's own panel installation.
package hostsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

const (
	hostStateDirectory = "/var/lib/panel-host"
	maxStateBytes      = 64 << 10
)

var (
	ErrHandoff = errors.New("invalid or unprotected installer state")
	ErrBusy    = errors.New("another setup process owns the installer state")

	gitSHA    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	storePath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]{1,211}$`)
)

// localBuild identifies the panel and host systems this host built for itself
// from one source revision and its reviewed input.
type localBuild struct {
	InstallationID   string          `json:"installation_id"`
	ServerID         string          `json:"server_id"`
	Role             foundation.Role `json:"role"`
	SourceSHA        string          `json:"source_sha"`
	InputSHA256      string          `json:"input_sha256"`
	MachineSHA256    string          `json:"machine_sha256"`
	PanelStorePath   string          `json:"panel_store_path"`
	EnrollmentSystem string          `json:"enrollment_system"`
	FinalSystem      string          `json:"final_system"`
}

func (build localBuild) valid() bool {
	return canonicalOperation(build.InstallationID) && canonicalOperation(build.ServerID) && (build.Role == foundation.Panel || build.Role == foundation.Server) &&
		gitSHA.MatchString(build.SourceSHA) && sha256Hex.MatchString(build.InputSHA256) && sha256Hex.MatchString(build.MachineSHA256) &&
		storeRoot(build.PanelStorePath) && validEnrollmentSystem(build.EnrollmentSystem) && validEnrollmentSystem(build.FinalSystem) && build.EnrollmentSystem != build.FinalSystem
}

func storeRoot(path string) bool {
	return storePath.MatchString(path) && !strings.HasSuffix(path, ".drv")
}

// buildPin binds every later stage to the exact recorded build.
func buildPin(build localBuild) (adoptionPins, error) {
	data, err := json.Marshal(build)
	if err != nil {
		return adoptionPins{}, ErrIdentity
	}
	digest := sha256.Sum256(data)
	return adoptionPins{BuildSHA256: hex.EncodeToString(digest[:])}, nil
}

// Every later phase reads the same persisted identity and its pin.
func readImportIntent(root *os.Root, operationID string, pins adoptionPins) (importIntent, error) {
	var intent importIntent
	if err := readState(root, "intent.json", &intent); err != nil {
		return importIntent{}, err
	}
	expected, err := buildPin(intent.Build)
	if err != nil || intent.OperationID != operationID || intent.Pins != pins || expected != pins || !intent.Build.valid() {
		return importIntent{}, ErrIdentity
	}
	identity := hostIdentity{intent.Build.InstallationID, intent.Build.ServerID, intent.Build.Role}
	var actualIdentity hostIdentity
	if readState(root, "identity.json", &actualIdentity) != nil || actualIdentity != identity {
		return importIntent{}, ErrIdentity
	}
	return intent, nil
}

// checkBuildInput binds the exact canonical input bytes to the recorded build.
func checkBuildInput(build localBuild, data []byte) (foundation.Input, error) {
	input, machineSHA, err := parseCanonicalInput(data)
	if err != nil || !matchesPin(data, build.InputSHA256) || machineSHA != build.MachineSHA256 ||
		input.InstallationID != build.InstallationID || input.ServerID != build.ServerID || input.Role != build.Role {
		return foundation.Input{}, ErrIdentity
	}
	return input, nil
}

// parseCanonicalInput admits only the exact bytes foundation.Canonical emits.
func parseCanonicalInput(data []byte) (foundation.Input, string, error) {
	if len(data) == 0 || len(data) > foundation.MaxInputBytes || !utf8.Valid(data) {
		return foundation.Input{}, "", ErrIdentity
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return foundation.Input{}, "", ErrIdentity
	}
	var machineSHA string
	if json.Unmarshal(fields["machine_sha256"], &machineSHA) != nil {
		return foundation.Input{}, "", ErrIdentity
	}
	delete(fields, "machine_sha256")
	unsigned, err := json.Marshal(fields)
	if err != nil {
		return foundation.Input{}, "", ErrIdentity
	}
	input, err := foundation.Parse(unsigned)
	if err != nil {
		return foundation.Input{}, "", ErrIdentity
	}
	canonical, err := foundation.Canonical(input)
	if err != nil || !bytes.Equal(data, canonical) {
		return foundation.Input{}, "", ErrIdentity
	}
	return input, machineSHA, nil
}

func openPrepared(root *os.Root, name string, maximum int64, owner int64) (*os.File, error) {
	file, err := privatefile.OpenNoFollow(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, ErrHandoff
	}
	info, err := file.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && int64(stat.Uid) == owner && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && info.Size() > 0 && info.Size() <= maximum {
			return file, nil
		}
	}
	_ = file.Close()
	return nil, ErrHandoff
}

func copyPrepared(ctx context.Context, destination *os.Root, name string, source io.Reader, size int64) error {
	file, err := destination.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrHandoff
	}
	defer func() { _ = file.Close() }()
	count, err := io.Copy(file, io.LimitReader(setupReader{ctx, source}, size+1))
	if err != nil || count != size || file.Sync() != nil {
		return ErrHandoff
	}
	if err := file.Close(); err != nil {
		return ErrHandoff
	}
	return nil
}

type setupReader struct {
	context context.Context
	reader  io.Reader
}

func (reader setupReader) Read(data []byte) (int, error) {
	if err := reader.context.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(data)
}

func matchesPin(data []byte, expected string) bool {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:]) == expected
}
