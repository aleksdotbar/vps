package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

type EnrollmentResult struct {
	Status                string `json:"status"`
	OperationID           string `json:"operation_id"`
	InstallationID        string `json:"installation_id"`
	ServerID              string `json:"server_id"`
	InputSHA256           string `json:"input_sha256"`
	SourceSystem          string `json:"source_system"`
	EnrollmentSystem      string `json:"enrollment_system"`
	CurrentSystem         string `json:"current_system"`
	ProfileSystem         string `json:"profile_system"`
	BootedSystem          string `json:"booted_system"`
	ManagementFingerprint string `json:"management_fingerprint"`
	RecoveryDeadline      string `json:"recovery_deadline"`
	ActivationReady       bool   `json:"activation_ready"`
}

func enrollmentStateRoot() (*os.Root, error) {
	if os.Geteuid() != 0 {
		return nil, ErrPrivilege
	}
	info, err := os.Lstat(hostStateDirectory)
	if err != nil || info == nil || !info.IsDir() || info.Mode().Perm() != 0700 || !enrollmentRootInfo(info) {
		return nil, ErrIdentity
	}
	root, err := os.OpenRoot(hostStateDirectory)
	if err != nil {
		return nil, ErrIdentity
	}
	if !sameSSHDirectory(root, info, 0) {
		_ = root.Close()
		return nil, ErrIdentity
	}
	return root, nil
}

func enrollmentInspection(ctx context.Context, root *os.Root, expected string, imported importIntent) (sourceInspectionRecord, error) {
	return readEnrollmentInspection(ctx, root, "source-inspection.json", expected, imported)
}

func readEnrollmentInspection(ctx context.Context, root *os.Root, name, expected string, imported importIntent) (sourceInspectionRecord, error) {
	file, err := openPrepared(root, name, maxInspectionRecordBytes, 0)
	if err != nil {
		return sourceInspectionRecord{}, ErrEnrollment
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(setupReader{ctx, file}, maxInspectionRecordBytes+1))
	if err != nil || len(data) > maxInspectionRecordBytes || !matchesPin(data, expected) {
		return sourceInspectionRecord{}, ErrEnrollment
	}
	var record sourceInspectionRecord
	if json.Unmarshal(data, &record) != nil {
		return record, ErrEnrollment
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(record) != nil || !bytes.Equal(canonical.Bytes(), data) || record.SchemaVersion != 1 || record.OperationID != imported.OperationID || record.Pins != imported.Pins || record.InstallationID != imported.Build.InstallationID || record.ServerID != imported.Build.ServerID || record.InputSHA256 != imported.Build.InputSHA256 || record.MachineSHA256 != imported.Build.MachineSHA256 || record.ActivationReady || !validEnrollmentSystem(record.SystemPath) {
		return record, ErrEnrollment
	}
	return record, nil
}

func validEnrollmentSystem(path string) bool {
	return filepath.Dir(path) == "/nix/store" && filepath.Clean(path) == path && currentSystemName.MatchString(filepath.Base(path)) && len(filepath.Base(path)) <= 244 && !strings.HasSuffix(path, ".drv")
}

func readEnrollmentIntent(root *os.Root, imported importIntent) (enrollmentIntent, error) {
	var intent enrollmentIntent
	if err := readState(root, "enrollment.json", &intent); err != nil {
		return intent, err
	}
	if intent.OperationID != imported.OperationID || intent.Pins != imported.Pins || intent.EnrollmentSystem != imported.Build.EnrollmentSystem || !validEnrollmentSystem(intent.SourceSystem) || intent.SourceSystem == intent.EnrollmentSystem || !canonicalOperation(intent.LaunchID) || intent.RecoveryDeadline <= 0 || len(intent.Files) > 3 {
		return intent, ErrEnrollment
	}
	if !matchesEnrollmentFiles(intent) {
		return intent, ErrEnrollment
	}
	return intent, nil
}

func matchesEnrollmentFiles(intent enrollmentIntent) bool {
	total := 0
	seen := map[string]bool{}
	for _, file := range intent.Files {
		total += len(file.Bytes)
		if seen[file.User] || !accountName(file.User) || len(file.Bytes) > maxSSHFileBytes || total > maxSSHAuthorityBytes {
			return false
		}
		seen[file.User] = true
	}
	return seen["panel-runner"] && intent.ManagementFingerprint != "" && len(intent.ManagementKey) <= 1024
}

func enrollmentPhase(root *os.Root, intent enrollmentIntent) (string, error) {
	var receipt enrollmentReceipt
	if err := readState(root, "enrollment-receipt.json", &receipt); err != nil {
		return "", err
	}
	if receipt.OperationID != intent.OperationID {
		return "", ErrEnrollment
	}
	switch receipt.Phase {
	case "starting", "preparing", "publishing", "published", "profile_updating", "switching", "awaiting_bootstrap", "enrolled", "recovering", "restored", "needs_attention":
		return receipt.Phase, nil
	default:
		return "", ErrEnrollment
	}
}

func writeEnrollmentPhase(ctx context.Context, root *os.Root, intent enrollmentIntent, phase string) error {
	return writeState(ctx, root, "enrollment-receipt.json", enrollmentReceipt{intent.OperationID, phase}, true)
}

func readEnrollmentLinks() (enrollmentLinks, error) {
	var result enrollmentLinks
	for index, path := range []string{"/run/current-system", "/nix/var/nix/profiles/system", "/run/booted-system"} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !validEnrollmentSystem(resolved) {
			return result, ErrEnrollment
		}
		switch index {
		case 0:
			result.current = resolved
		case 1:
			result.profile = resolved
		case 2:
			result.booted = resolved
		}
	}
	return result, nil
}

func enrollmentResult(imported importIntent, intent enrollmentIntent, status string, links enrollmentLinks) EnrollmentResult {
	deadline := ""
	if intent.RecoveryDeadline > 0 {
		deadline = time.Unix(intent.RecoveryDeadline, 0).UTC().Format(time.RFC3339)
	}
	return EnrollmentResult{Status: status, OperationID: imported.OperationID, InstallationID: imported.Build.InstallationID, ServerID: imported.Build.ServerID, InputSHA256: imported.Build.InputSHA256, SourceSystem: intent.SourceSystem, EnrollmentSystem: imported.Build.EnrollmentSystem, CurrentSystem: links.current, ProfileSystem: links.profile, BootedSystem: links.booted, ManagementFingerprint: intent.ManagementFingerprint, RecoveryDeadline: deadline, ActivationReady: false}
}

func retainEnrollmentSource(ctx context.Context, imported importIntent, source string) error {
	tools := targetImportTools()
	tools.nixStore = imported.Build.EnrollmentSystem + "/sw/bin/nix-store"
	lock, err := privatefile.OpenPrivate(gcLockPath(tools), os.O_RDWR|os.O_CREATE|syscall.O_NONBLOCK)
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = lock.Close() }()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		return ErrEnrollment
	}
	if _, err := runtimeProbe(ctx, tools.nixStore, "--store", "local", "--check-validity", source); err != nil {
		return ErrEnrollment
	}
	root, err := os.OpenRoot(rootsDirectory(tools, imported.Build))
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = root.Close() }()
	if err := root.Symlink(source, "original"); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrEnrollment
	}
	actual, err := root.Readlink("original")
	info, statErr := root.Lstat("original")
	if err != nil || actual != source || statErr != nil || info == nil || info.Mode()&os.ModeSymlink == 0 {
		return ErrEnrollment
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return ErrEnrollment
	}
	if syncEnrollmentDirectory(root) != nil {
		return ErrUnresolved
	}
	return nil
}

func retainEnrollmentInspection(ctx context.Context, root *os.Root, record sourceInspectionRecord) error {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(record) != nil || data.Len() > maxInspectionRecordBytes {
		return ErrEnrollment
	}
	if copyPrepared(ctx, root, "enrollment-inspection.json", bytes.NewReader(data.Bytes()), int64(data.Len())) != nil || syncEnrollmentDirectory(root) != nil {
		return ErrUnresolved
	}
	return nil
}
