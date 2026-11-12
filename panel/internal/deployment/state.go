package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

const maxRecordBytes = MaxStageBytes + MaxApplicationBytes

type claim struct {
	Identity       Identity `json:"identity"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	// FinalSystem records the generation that staged the release. A later
	// nixos-rebuild may replace it; operations then use the current generation.
	FinalSystem string `json:"final_system"`
}

// sameOperation matches a retained claim regardless of the generation that staged it.
func (c claim) sameOperation(other claim) bool {
	return c.Identity == other.Identity && c.ManifestSHA256 == other.ManifestSHA256
}

type staged struct {
	Claim             claim           `json:"claim"`
	Manifest          Manifest        `json:"manifest"`
	ApplicationConfig json.RawMessage `json:"application_config"`
	RegistrySHA256    string          `json:"registry_sha256,omitempty"`
}

func protected(info os.FileInfo, directory bool) bool {
	if info == nil || info.IsDir() != directory || !directory && !info.Mode().IsRegular() {
		return false
	}
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Geteuid() && info.Mode().Perm() == mode && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func child(root *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		if err := root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, ErrState
		}
		if syncDirectory(root) != nil {
			return nil, ErrState
		}
	}
	file, err := privatefile.OpenNoFollow(root, name, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, ErrState
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !protected(info, true) {
		return nil, ErrState
	}
	result, err := root.OpenRoot(name)
	if err != nil {
		return nil, ErrState
	}
	after, err := result.Stat(".")
	if err != nil || !protected(after, true) || !os.SameFile(info, after) {
		_ = result.Close()
		return nil, ErrState
	}
	return result, nil
}

func directoryEntries(root *os.Root, allowed ...string) error {
	file, err := root.Open(".")
	if err != nil {
		return ErrState
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(len(allowed) + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > len(allowed) {
		return ErrState
	}
	for _, entry := range entries {
		found := false
		for _, name := range allowed {
			found = found || entry.Name() == name
		}
		if !found {
			return ErrState
		}
	}
	return nil
}

func syncDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return ErrState
	}
	defer func() { _ = file.Close() }()
	if file.Sync() != nil {
		return ErrState
	}
	return nil
}

func readFile(root *os.Root, name string) ([]byte, error) {
	file, err := privatefile.OpenNoFollow(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !protected(info, false) || info.Size() > maxRecordBytes {
		return nil, ErrState
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil || len(data) > maxRecordBytes {
		clear(data)
		return nil, ErrState
	}
	return data, nil
}

func readRecord(root *os.Root, name string, value any) error {
	data, err := readFile(root, name)
	if err != nil {
		return err
	}
	defer clear(data)
	if json.Unmarshal(data, value) != nil {
		return ErrState
	}
	canonical, err := json.Marshal(value)
	defer clear(canonical)
	if err != nil || !bytes.Equal(data, canonical) {
		return ErrState
	}
	return nil
}

func publish(ctx context.Context, root *os.Root, name string, data []byte) error {
	if len(data) > maxRecordBytes || ctx.Err() != nil {
		return ErrState
	}
	directory, err := root.Open(".")
	if err != nil {
		return ErrState
	}
	defer func() { _ = directory.Close() }()
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrState
	}
	file := os.NewFile(uintptr(fd), "deployment-staging")
	if file == nil {
		_ = unix.Close(fd)
		return ErrState
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil || file.Sync() != nil || ctx.Err() != nil {
		return ErrState
	}
	if unix.Linkat(fd, "", int(directory.Fd()), name, unix.AT_EMPTY_PATH) != nil || directory.Sync() != nil {
		return ErrState
	}
	return nil
}

// Stage uses only the admitted final generation's fixed Docker/Compose tool.
// No application starts; native validation must pass before exclusion/publication.
func Stage(ctx context.Context, root *os.Root, request StageRequest, finalSystem string) (Response, error) {
	var registry *os.Root
	if request.Registry != nil {
		var err error
		registry, err = openRegistryRoot(true)
		if err != nil {
			return Response{}, err
		}
		defer func() { _ = registry.Close() }()
	}
	return stage(ctx, root, request, finalSystem, validationTool{finalSystem + "/sw/bin/docker", []string{"compose"}}, time.Now, registry, armRegistryExpiry)
}

func stage(ctx context.Context, root *os.Root, request StageRequest, finalSystem string, tool validationTool, clock func() time.Time, registryRoot *os.Root, armExpiry registryExpiryArm) (Response, error) {
	now := clock()
	info, err := root.Stat(".")
	if err != nil || !protected(info, true) {
		return Response{}, ErrState
	}
	if request.SchemaVersion != 1 || request.Action != "deploy_stage" || !validHash(request.Challenge) || !finalSystemPath.MatchString(finalSystem) {
		return Response{}, ErrInvalid
	}
	// Admit one minute of inter-host clock skew without changing the panel's
	// absolute deadline. This slice cannot start or extend remote execution.
	if request.Manifest.Identity.ExecutionDeadline <= now.UnixMilli() || request.Manifest.Identity.ExecutionDeadline > now.Add(31*time.Minute).UnixMilli() {
		return Response{}, ErrDeadline
	}
	ctx, cancel := context.WithDeadlineCause(ctx, time.UnixMilli(request.Manifest.Identity.ExecutionDeadline), ErrDeadline)
	defer cancel()
	if err := context.Cause(ctx); err != nil {
		return Response{}, err
	}
	application, err := Materialize(request)
	if err != nil {
		return Response{}, err
	}
	defer clear(application)
	hash, err := ManifestSHA256(request.Manifest)
	if err != nil {
		return Response{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return Response{}, err
	}
	proof, registry, err := registryProof(request.Registry)
	defer clear(registry)
	if err != nil {
		return Response{}, err
	}
	expected := claim{request.Manifest.Identity, hash, finalSystem}
	environment, err := child(root, request.Manifest.Identity.EnvironmentID, true)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = environment.Close() }()
	lock, err := privatefile.OpenNoFollow(environment, "effect.lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return Response{}, ErrState
	}
	defer func() { _ = lock.Close() }()
	info, err = lock.Stat()
	if err != nil || !protected(info, false) {
		return Response{}, ErrState
	}
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return Response{}, ErrBusy
	}
	if directoryEntries(environment, "effect.lock", "active.json", "operations", "current.json", ".next") != nil {
		return Response{}, ErrState
	}
	var active claim
	err = readRecord(environment, "active.json", &active)
	if err == nil {
		if active != expected {
			next := staged{Claim: expected, Manifest: request.Manifest, ApplicationConfig: application, RegistrySHA256: proof}
			if validateNative(ctx, application, tool) != nil {
				return Response{}, ErrInvalid
			}
			if request.Manifest.Identity.ExecutionDeadline <= clock().UnixMilli() {
				return Response{}, ErrDeadline
			}
			if err := admitNextStage(ctx, environment, active, expected, next); err != nil {
				return Response{}, err
			}
			if registry != nil && publishRegistry(ctx, registryRoot, expected, registry, armExpiry) != nil {
				return Response{}, ErrState
			}
			return response("deploy_stage", request.Challenge, expected, clock()), nil
		}
		record, err := readStage(environment, expected)
		defer clear(record.ApplicationConfig)
		if err != nil || record.RegistrySHA256 != proof || !bytes.Equal(record.ApplicationConfig, application) {
			return Response{}, ErrState
		}
		if proof != "" {
			if _, executionErr := readExecution(environment, expected); errors.Is(executionErr, os.ErrNotExist) {
				if publishRegistry(ctx, registryRoot, expected, registry, armExpiry) != nil {
					return Response{}, ErrState
				}
			}
			credential, readErr := readRegistry(registryRoot, request.Manifest.Identity.OperationID)
			defer clear(credential.Token)
			credentialProof, credentialData, proofErr := registryProof(&credential)
			clear(credentialData)
			if readErr != nil || proofErr != nil || credentialProof != proof {
				return Response{}, ErrState
			}
		}
		if syncDirectory(environment) != nil {
			return Response{}, ErrState
		}
		if err := context.Cause(ctx); err != nil {
			return Response{}, err
		}
		return response("deploy_stage", request.Challenge, expected, clock()), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Response{}, ErrState
	}
	if directoryEntries(environment, "effect.lock", "operations", ".next") != nil {
		return Response{}, ErrState
	}
	validationErr := validateNative(ctx, application, tool)
	if err := context.Cause(ctx); err != nil {
		return Response{}, err
	}
	if request.Manifest.Identity.ExecutionDeadline <= clock().UnixMilli() {
		return Response{}, ErrDeadline
	}
	if validationErr != nil {
		return Response{}, ErrInvalid
	}
	if registry != nil && publishRegistry(ctx, registryRoot, expected, registry, armExpiry) != nil {
		return Response{}, ErrState
	}
	record := staged{Claim: expected, Manifest: request.Manifest, ApplicationConfig: application, RegistrySHA256: proof}
	if retainStage(ctx, environment, record) != nil || putRecord(ctx, environment, "active.json", expected) != nil {
		return Response{}, ErrState
	}

	if err := context.Cause(ctx); err != nil {
		return Response{}, err
	}
	return response("deploy_stage", request.Challenge, expected, clock()), nil
}

func retainStage(ctx context.Context, environment *os.Root, next staged) error {
	directory, err := operationRoot(environment, next.Claim.Identity.OperationID, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	old, err := readStage(environment, next.Claim)
	defer clear(old.ApplicationConfig)
	if err == nil {
		if old.RegistrySHA256 != next.RegistrySHA256 || !bytes.Equal(old.ApplicationConfig, next.ApplicationConfig) {
			return ErrState
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return putRecord(ctx, directory, "stage.json", next)
}

func readStage(environment *os.Root, expected claim) (staged, error) {
	var record staged
	directory, err := operationRoot(environment, expected.Identity.OperationID, false)
	if err != nil {
		return record, err
	}
	defer func() { _ = directory.Close() }()
	if err := readRecord(directory, "stage.json", &record); err != nil {
		return record, err
	}
	if !finalSystemPath.MatchString(record.Claim.FinalSystem) || record.Claim.Identity != expected.Identity || record.Claim.ManifestSHA256 != expected.ManifestSHA256 || expected.FinalSystem != "" && record.Claim.FinalSystem != expected.FinalSystem || record.Manifest.Identity != expected.Identity {
		return record, ErrState
	}
	hash, err := ManifestSHA256(record.Manifest)
	if err != nil || hash != expected.ManifestSHA256 || len(record.ApplicationConfig) > MaxApplicationBytes || !json.Valid(record.ApplicationConfig) {
		return record, ErrState
	}
	return record, nil
}

// Status is a protected receipt observation, never application health. It does
// not take the mutation lock, so future detached work cannot block observation.
func Status(root *os.Root, request StatusRequest, finalSystem string) (Response, error) {
	info, err := root.Stat(".")
	if err != nil || !protected(info, true) {
		return Response{}, ErrState
	}
	if request.SchemaVersion != 1 || request.Action != "deploy_status" || !validHash(request.Challenge) || !validHash(request.ManifestSHA256) || ValidateIdentity(request.Identity) != nil || !finalSystemPath.MatchString(finalSystem) {
		return Response{}, ErrInvalid
	}
	expected := claim{request.Identity, request.ManifestSHA256, finalSystem}
	environment, err := child(root, request.Identity.EnvironmentID, false)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = environment.Close() }()
	if directoryEntries(environment, "effect.lock", "active.json", "operations", "current.json", ".next") != nil {
		return Response{}, ErrState
	}
	var active claim
	if readRecord(environment, "active.json", &active) != nil || !active.sameOperation(expected) {
		return Response{}, ErrState
	}
	record, err := readStage(environment, expected)
	defer clear(record.ApplicationConfig)
	if err != nil {
		result := response("deploy_status", request.Challenge, expected, time.Now())
		result.Phase = "needs_attention"
		result.Reason = "staging_incomplete"
		return result, nil
	}
	return response("deploy_status", request.Challenge, expected, time.Now()), nil
}

func response(action, challenge string, claim claim, now time.Time) Response {
	result := Response{SchemaVersion: 1, Action: action, Challenge: challenge, Identity: claim.Identity, ManifestSHA256: claim.ManifestSHA256, Phase: "staged", FinalSystem: claim.FinalSystem, Ready: false}
	if now.UnixMilli() >= claim.Identity.ExecutionDeadline {
		result.Phase = "needs_attention"
		result.Reason = "execution_deadline_exceeded"
	}
	return result
}
