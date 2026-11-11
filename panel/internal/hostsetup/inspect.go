package hostsetup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

type Inspected struct {
	Status           string `json:"status"`
	OperationID      string `json:"operation_id"`
	InstallationID   string `json:"installation_id"`
	ServerID         string `json:"server_id"`
	InputSHA256      string `json:"input_sha256"`
	SystemPath       string `json:"system_path"`
	MachineSHA256    string `json:"machine_sha256"`
	InspectionSHA256 string `json:"inspection_sha256"`
	ActivationReady  bool   `json:"activation_ready"`
}

// Inspect binds the host configuration to the recorded input and checks live storage and network.
// Private access and the remaining activation/recovery proofs are separate.
func Inspect(ctx context.Context, operationID, buildSHA256 string) (Inspected, error) {
	if os.Geteuid() != 0 {
		return Inspected{}, errors.New("installer inspection requires root")
	}
	pins := adoptionPins{BuildSHA256: buildSHA256}
	if !canonicalOperation(operationID) || !pins.valid() {
		return Inspected{}, ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	info, err := os.Lstat(hostStateDirectory)
	if err != nil || !info.IsDir() || privatefile.PrivateDirectory(hostStateDirectory) != nil {
		return Inspected{}, ErrIdentity
	}
	root, err := os.OpenRoot(hostStateDirectory)
	if err != nil {
		return Inspected{}, ErrIdentity
	}
	defer func() { _ = root.Close() }()
	lock, err := stateLock(root)
	if err != nil {
		return Inspected{}, err
	}
	defer func() { _ = lock.Close() }()
	intent, err := readImportIntent(root, operationID, pins)
	if err != nil {
		return Inspected{}, err
	}
	return inspectRetained(ctx, root, intent)
}

// Caller owns setup.lock and has validated the immutable retained intent.
func inspectRetained(ctx context.Context, root *os.Root, intent importIntent) (Inspected, error) {
	operationID, pins := intent.OperationID, intent.Pins
	var receipt importReceipt
	if readState(root, "receipt.json", &receipt) != nil || receipt.OperationID != operationID || receipt.Phase != "imported" {
		return Inspected{}, ErrUnresolved
	}
	enrollment := intent.Build.EnrollmentSystem
	tools := targetImportTools()
	tools.nixStore = enrollment + "/sw/bin/nix-store"
	observed, err := observeRootedImport(ctx, tools, intent.Build)
	if err != nil || !observed {
		return Inspected{}, ErrUnresolved
	}
	input, err := retainedInspectionInput(ctx, root, intent)
	if err != nil {
		return Inspected{}, err
	}
	links := [3]string{"/run/current-system", "/run/booted-system", "/nix/var/nix/profiles/system"}
	before, err := consistentSystemLinks(links, "/nix/store")
	if err != nil {
		return Inspected{}, err
	}
	memoryLimit, available := inspectionMemoryLimit(availableMemoryKiB())
	if !available {
		return Inspected{}, ErrResources
	}
	command := []string{enrollment + "/sw/bin/env", "-i", "HOME=/var/empty", "LANG=C", "LC_ALL=C", "NIX_CONF_DIR=/var/empty", "NIX_USER_CONF_FILES=", "NIX_CONFIG=", "NIX_PATH=",
		"XDG_CONFIG_HOME=/var/empty", "XDG_CACHE_HOME=/var/empty",
		enrollment + "/sw/bin/nix", "--extra-experimental-features", "nix-command", "eval", "--store", "dummy://", "--read-only", "--offline", "--impure", "--json", "--max-jobs", "0", "--cores", "1",
		"--option", "allow-import-from-derivation", "false", "--option", "allow-unsafe-native-code-during-evaluation", "false", "--option", "substituters", "", "--option", "builders", "",
		"--file", intent.Build.PanelStorePath + "/share/panel/nix/installer/inspect-legacy.nix"}
	packet, err := runSourceInspection(ctx, inspectionTools{systemdRun: enrollment + "/sw/bin/systemd-run", systemctl: enrollment + "/sw/bin/systemctl", memoryMaxBytes: memoryLimit}, operationID, command)
	if err != nil {
		return Inspected{}, err
	}
	if err := confirmSourcePacket(packet, input, intent.Build.InputSHA256); err != nil {
		return Inspected{}, err
	}
	storage, err := inspectRuntimeStorage(ctx, enrollment, input.Machine)
	if err != nil {
		return Inspected{}, err
	}
	if err := inspectRuntimeNetwork(ctx, enrollment, before, input.Machine, true); err != nil {
		return Inspected{}, err
	}
	after, err := consistentSystemLinks(links, "/nix/store")
	if err != nil || after != before {
		return Inspected{}, ErrSourceMismatch
	}
	result := Inspected{Status: "source_inspected", OperationID: operationID, InstallationID: intent.Build.InstallationID, ServerID: intent.Build.ServerID,
		InputSHA256: intent.Build.InputSHA256, SystemPath: before, MachineSHA256: intent.Build.MachineSHA256, ActivationReady: false}
	// Fresh provider images carry no hand-made accounts or SSH authority to preserve;
	// the record keeps its SSH field empty for compatibility.
	result.InspectionSHA256, err = writeSourceInspection(ctx, root, result, pins, packet, storage, sshObservation{Files: []sshAuthorityFile{}})
	if err != nil {
		return Inspected{}, err
	}
	return result, nil
}

func retainedInspectionInput(ctx context.Context, root *os.Root, intent importIntent) (foundation.Input, error) {
	artifact, err := root.OpenRoot("artifact")
	if err != nil {
		return foundation.Input{}, ErrIdentity
	}
	defer func() { _ = artifact.Close() }()
	read := func(name string, maximum int64) ([]byte, error) {
		file, err := openPrepared(artifact, name, maximum, int64(os.Geteuid()))
		if err != nil {
			return nil, ErrIdentity
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(io.LimitReader(setupReader{ctx, file}, maximum+1))
		if err != nil || int64(len(data)) > maximum {
			return nil, ErrIdentity
		}
		return data, nil
	}
	data, err := read("input.json", foundation.MaxInputBytes)
	if err != nil {
		return foundation.Input{}, err
	}
	return checkBuildInput(intent.Build, data)
}

var currentSystemName = regexp.MustCompile(`^[0-9abcdfghijklmnpqrsvwxyz]{32}-nixos-system-[A-Za-z0-9+._?=-]{1,198}$`)

func consistentSystemLinks(links [3]string, store string) (string, error) {
	var expected string
	for _, link := range links {
		resolved, err := filepath.EvalSymlinks(link)
		if err != nil || filepath.Clean(resolved) != resolved || filepath.Dir(resolved) != store ||
			!currentSystemName.MatchString(filepath.Base(resolved)) || strings.HasSuffix(resolved, ".drv") || len(filepath.Base(resolved)) > 244 {
			return "", ErrSourceMismatch
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return "", ErrSourceMismatch
		}
		if expected != "" && resolved != expected {
			return "", ErrSourceMismatch
		}
		expected = resolved
	}
	return expected, nil
}
