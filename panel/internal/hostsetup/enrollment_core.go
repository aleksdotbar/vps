package hostsetup

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"golang.org/x/crypto/ssh"
)

var ErrEnrollment = errors.New("enrollment could not be admitted; retained setup requires inspection")

type enrollmentKeyFile struct {
	User  string `json:"user"`
	Bytes []byte `json:"bytes"`
}

type enrollmentIntent struct {
	OperationID           string              `json:"operation_id"`
	Pins                  adoptionPins        `json:"pins"`
	InspectionSHA256      string              `json:"inspection_sha256"`
	SourceSystem          string              `json:"source_system"`
	EnrollmentSystem      string              `json:"enrollment_system"`
	ManagementKey         string              `json:"management_key"`
	ManagementFingerprint string              `json:"management_fingerprint"`
	LaunchID              string              `json:"launch_id"`
	RecoveryDeadline      int64               `json:"recovery_deadline"`
	Files                 []enrollmentKeyFile `json:"files"`
}

type enrollmentReceipt struct {
	OperationID string `json:"operation_id"`
	Phase       string `json:"phase"`
}

type enrollmentLinks struct{ current, profile, booted string }

func enrollmentAccessPlan(public []byte, input foundation.Input, observation sshObservation) ([]enrollmentKeyFile, string, string, error) {
	if len(public) == 0 || len(public) > 1024 {
		return nil, "", "", ErrEnrollment
	}
	selected, _, options, rest, err := ssh.ParseAuthorizedKey(public)
	if err != nil || selected.Type() != ssh.KeyAlgoED25519 || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, "", "", ErrEnrollment
	}
	operator, _, _, _, operatorErr := ssh.ParseAuthorizedKey([]byte(input.Operator.PublicKey))
	if operatorErr != nil || bytes.Equal(selected.Marshal(), operator.Marshal()) {
		return nil, "", "", ErrEnrollment
	}
	files := []enrollmentKeyFile{}
	for _, file := range observation.Files {
		if file.User != "root" && file.User != input.Operator.Name {
			return nil, "", "", ErrEnrollment
		}
		if file.Source != "rendered" && file.Source != "home" && file.Source != "runtime" {
			return nil, "", "", ErrEnrollment
		}
		if len(file.Bytes) > maxSSHFileBytes || (!file.Active && len(file.Bytes) > 0) {
			return nil, "", "", ErrEnrollment
		}
		if len(file.Bytes) > 0 {
			for line := range bytes.SplitSeq(file.Bytes, []byte{'\n'}) {
				line = bytes.TrimSpace(line)
				if len(line) == 0 || line[0] == '#' {
					continue
				}
				key, _, _, remaining, err := ssh.ParseAuthorizedKey(line)
				if err != nil || len(bytes.TrimSpace(remaining)) != 0 || bytes.Equal(key.Marshal(), selected.Marshal()) {
					return nil, "", "", ErrEnrollment
				}
			}
		}
		// Never adopt or append to preexisting runtime authority without an owned
		// publication receipt. Home files remain where native OpenSSH already reads.
		if file.Source == "runtime" && file.Present {
			return nil, "", "", ErrEnrollment
		}
		if file.Source == "rendered" && file.Active && file.Present && len(file.Bytes) > 0 && (file.User != "root" || input.Bootstrap.RootLogin != "no") {
			if slices.ContainsFunc(files, func(existing enrollmentKeyFile) bool { return existing.User == file.User }) {
				return nil, "", "", ErrEnrollment
			}
			files = append(files, enrollmentKeyFile{file.User, bytes.Clone(file.Bytes)})
		}
	}
	canonical := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(selected)), "\n")
	files = append(files, enrollmentKeyFile{"panel-runner", []byte("restrict " + canonical + "\n")})
	slices.SortFunc(files, func(a, b enrollmentKeyFile) int { return strings.Compare(a.User, b.User) })
	return files, canonical, ssh.FingerprintSHA256(selected), nil
}

// Only our two named generations may authorize a recovery effect. Booting a
// third generation or an external profile change never becomes permission to
// overwrite it, even when the setup service reports failure.
func enrollmentOwnsLinks(intent enrollmentIntent, links enrollmentLinks) bool {
	owned := func(path string) bool { return path == intent.SourceSystem || path == intent.EnrollmentSystem }
	return owned(links.current) && owned(links.profile) && owned(links.booted)
}

func enrollmentAt(intent enrollmentIntent, links enrollmentLinks, system string) bool {
	return links.current == system && links.profile == system && (links.booted == intent.SourceSystem || links.booted == intent.EnrollmentSystem)
}

func confirmEnrollmentLoginUsers(settings sshEffective, input foundation.Input, accounts accountSnapshot) error {
	allowed := func(user string) bool {
		if len(settings["allowusers"]) == 0 {
			return true
		}
		for _, row := range settings["allowusers"] {
			if slices.Contains(strings.Fields(row), user) {
				return true
			}
		}
		return false
	}
	if input.Bootstrap.RootLogin != "no" && !allowed("root") {
		return ErrEnrollment
	}
	if _, exists := accounts.users[input.Operator.Name]; exists && !allowed(input.Operator.Name) {
		return ErrEnrollment
	}
	return nil
}

// The absolute recovery guard must outlive a late-started primary's complete
// 600-second runtime, 30-second stop and a further 30-second scheduling margin.
func enrollmentMayStart(intent enrollmentIntent, phase string, now time.Time) bool {
	return phase == "starting" && now.Add(11*time.Minute).Unix() <= intent.RecoveryDeadline
}
