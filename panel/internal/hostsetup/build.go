package hostsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/machine"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

var ErrDeclined = errors.New("discovered host input was not confirmed; nothing was written")

type BuildRequest struct {
	Source    string // store path of this repository's source
	SourceSHA string
	Operator  foundation.Operator
	Confirm   func(summary string) bool
	Log       io.Writer // build progress; nil discards it
}

type Built struct {
	Status           string `json:"status"`
	OperationID      string `json:"operation_id"`
	BuildSHA256      string `json:"build_sha256"`
	InstallationID   string `json:"installation_id"`
	ServerID         string `json:"server_id"`
	InputSHA256      string `json:"input_sha256"`
	EnrollmentSystem string `json:"enrollment_system"`
	FinalSystem      string `json:"final_system"`
}

// Build derives and confirms the host's input, then builds the panel and both
// host systems locally. A recorded build is returned unchanged; an interrupted
// one reuses its confirmed input. Nothing is activated here.
func Build(ctx context.Context, request BuildRequest) (Built, error) {
	if os.Geteuid() != 0 {
		return Built{}, ErrPrivilege
	}
	marker, err := os.Stat("/etc/NIXOS")
	if err != nil || !marker.Mode().IsRegular() || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return Built{}, ErrResources
	}
	if !storeRoot(request.Source) || !gitSHA.MatchString(request.SourceSHA) || request.Confirm == nil {
		return Built{}, ErrHandoff
	}
	root, err := openHostState()
	if err != nil {
		return Built{}, err
	}
	defer func() { _ = root.Close() }()
	lock, err := stateLock(root)
	if err != nil {
		return Built{}, err
	}
	defer func() { _ = lock.Close() }()

	var recorded importIntent
	err = readState(root, "intent.json", &recorded)
	if err == nil {
		intent, err := readImportIntent(root, recorded.OperationID, recorded.Pins)
		if err != nil {
			return Built{}, err
		}
		return builtResult(intent), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Built{}, err
	}

	input, data, err := confirmedInput(ctx, root, request)
	if err != nil {
		return Built{}, err
	}
	paths, err := buildSystems(ctx, request)
	if err != nil {
		return Built{}, err
	}
	_, machineSHA, err := machineCanonical(input)
	if err != nil {
		return Built{}, ErrIdentity
	}
	build := localBuild{InstallationID: input.InstallationID, ServerID: input.ServerID, Role: input.Role, SourceSHA: request.SourceSHA,
		InputSHA256: sha256Text(data), MachineSHA256: machineSHA, PanelStorePath: paths[0], EnrollmentSystem: paths[1], FinalSystem: paths[2]}
	if !build.valid() {
		return Built{}, ErrUnresolved
	}
	if observed, err := observeRootedImport(ctx, targetImportTools(), build); err != nil || !observed {
		return Built{}, ErrUnresolved
	}
	operation := uuid.NewString()
	pins, err := writeBuildIntent(ctx, root, operation, build)
	if err != nil {
		return Built{}, err
	}
	if writeState(ctx, root, "receipt.json", importReceipt{operation, "imported"}, false) != nil {
		return Built{}, ErrUnresolved
	}
	return builtResult(importIntent{OperationID: operation, Build: build, Pins: pins}), nil
}

func openHostState() (*os.Root, error) {
	if err := os.Mkdir(hostStateDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrHandoff
	}
	if privatefile.PrivateDirectory(hostStateDirectory) != nil {
		return nil, ErrHandoff
	}
	// Persist the first /var/lib entry too, so a new identity survives a crash.
	parent, err := os.Open(filepath.Dir(hostStateDirectory))
	if err != nil {
		return nil, ErrUnresolved
	}
	err = parent.Sync()
	if closeErr := parent.Close(); err != nil || closeErr != nil {
		return nil, ErrUnresolved
	}
	root, err := os.OpenRoot(hostStateDirectory)
	if err != nil {
		return nil, ErrHandoff
	}
	return root, nil
}

// confirmedInput reuses previously confirmed input, or discovers new input and
// persists it only after the operator accepts the summary.
func confirmedInput(ctx context.Context, root *os.Root, request BuildRequest) (foundation.Input, []byte, error) {
	if file, err := openPrepared(root, "artifact/input.json", foundation.MaxInputBytes, int64(os.Geteuid())); err == nil {
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(io.LimitReader(setupReader{ctx, file}, foundation.MaxInputBytes+1))
		if err != nil {
			return foundation.Input{}, nil, ErrIdentity
		}
		input, _, err := parseCanonicalInput(data)
		if err != nil || input.Operator != request.Operator {
			return foundation.Input{}, nil, ErrIdentity
		}
		if err := recordIdentity(ctx, root, input); err != nil {
			return foundation.Input{}, nil, err
		}
		return input, data, nil
	}
	if _, err := root.Lstat("artifact"); !errors.Is(err, os.ErrNotExist) {
		return foundation.Input{}, nil, ErrUnresolved
	}
	limit, available := inspectionMemoryLimit(availableMemoryKiB())
	if !available {
		return foundation.Input{}, nil, ErrResources
	}
	tools := inspectionTools{systemdRun: "/run/current-system/sw/bin/systemd-run", systemctl: "/run/current-system/sw/bin/systemctl", memoryMaxBytes: limit}
	input, err := discoverInput(ctx, request.Source, request.Operator, tools)
	if err != nil {
		return foundation.Input{}, nil, err
	}
	if !request.Confirm(inputSummary(input)) {
		return foundation.Input{}, nil, ErrDeclined
	}
	data, err := foundation.Canonical(input)
	if err != nil {
		return foundation.Input{}, nil, ErrIdentity
	}
	if err := writeBuildInput(ctx, root, input, data); err != nil {
		return foundation.Input{}, nil, err
	}
	return input, data, nil
}

// writeBuildInput publishes artifact/ only once its input is complete, then records
// the identity, so an interruption leaves nothing or reusable confirmed input.
func writeBuildInput(ctx context.Context, root *os.Root, input foundation.Input, data []byte) error {
	const staged = "artifact.partial"
	if root.RemoveAll(staged) != nil || root.Mkdir(staged, 0700) != nil {
		return ErrUnresolved
	}
	artifact, err := root.OpenRoot(staged)
	if err != nil {
		return ErrUnresolved
	}
	defer func() { _ = artifact.Close() }()
	if copyPrepared(ctx, artifact, "input.json", bytes.NewReader(data), int64(len(data))) != nil || syncRoot(artifact) != nil {
		return ErrUnresolved
	}
	if root.Rename(staged, "artifact") != nil || syncRoot(root) != nil {
		return ErrUnresolved
	}
	return recordIdentity(ctx, root, input)
}

// recordIdentity writes the identity once and afterwards only accepts the same one.
func recordIdentity(ctx context.Context, root *os.Root, input foundation.Input) error {
	identity := hostIdentity{input.InstallationID, input.ServerID, input.Role}
	var existing hostIdentity
	err := readState(root, "identity.json", &existing)
	switch {
	case err == nil && existing != identity:
		return ErrIdentity
	case errors.Is(err, os.ErrNotExist):
		if writeState(ctx, root, "identity.json", identity, false) != nil {
			return ErrUnresolved
		}
	case err != nil:
		return err
	}
	return nil
}

func syncRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func writeBuildIntent(ctx context.Context, root *os.Root, operation string, build localBuild) (adoptionPins, error) {
	pins, err := buildPin(build)
	if err != nil {
		return adoptionPins{}, err
	}
	if writeState(ctx, root, "intent.json", importIntent{OperationID: operation, Build: build, Pins: pins}, false) != nil {
		return adoptionPins{}, ErrUnresolved
	}
	return pins, nil
}

// buildSystems runs one bounded native build; its out-link roots all three paths.
func buildSystems(ctx context.Context, request BuildRequest) ([3]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Minute)
	defer cancel()
	home := hostStateDirectory + "/build/home"
	if err := os.MkdirAll(home, 0700); err != nil {
		return [3]string{}, ErrUnresolved
	}
	link := hostStateDirectory + "/build/result"
	process := nativeCommand(ctx, "/run/current-system/sw/bin/systemd-run", "--scope", "--quiet", "--no-ask-password",
		"--unit=panel-build-"+uuid.NewString(), "--property=MemoryMax=80%", "--property=TasksMax=4096", "--",
		"/run/current-system/sw/bin/nix", "--extra-experimental-features", "nix-command flakes", "build", "--impure",
		"--out-link", link, "--print-out-paths",
		"--file", request.Source+"/nix/install-build.nix",
		"--argstr", "source", request.Source, "--argstr", "sourceSHA", request.SourceSHA,
		"--arg", "inputJSON", hostStateDirectory+"/artifact/input.json")
	process.Env = []string{"HOME=" + home, "PATH=/run/current-system/sw/bin", "LANG=C", "LC_ALL=C", "NIX_CONFIG=", "NIX_PATH="}
	if request.Log != nil {
		process.Stderr = request.Log
	}
	var output importOutput
	process.Stdout = &output
	if err := process.Run(); err != nil {
		return [3]string{}, fmt.Errorf("%w: native build failed", ErrUnresolved)
	}
	farm := strings.TrimSpace(output.data.String())
	if !storeRoot(farm) {
		return [3]string{}, ErrUnresolved
	}
	var paths [3]string
	for index, name := range []string{"panel", "enrollment", "final"} {
		target, err := os.Readlink(filepath.Join(farm, name))
		if err != nil || !storeRoot(target) {
			return [3]string{}, ErrUnresolved
		}
		paths[index] = target
	}
	return paths, nil
}

func builtResult(intent importIntent) Built {
	return Built{Status: "built", OperationID: intent.OperationID, BuildSHA256: intent.Pins.BuildSHA256, InstallationID: intent.Build.InstallationID,
		ServerID: intent.Build.ServerID, InputSHA256: intent.Build.InputSHA256, EnrollmentSystem: intent.Build.EnrollmentSystem, FinalSystem: intent.Build.FinalSystem}
}

func inputSummary(input foundation.Input) string {
	var summary strings.Builder
	config, network := input.Machine, input.Machine.Network
	fmt.Fprintf(&summary, "Host %s (%s, NixOS %s)\n", network.HostName, describeTemplate(config.Template), config.StateVersion)
	fmt.Fprintf(&summary, "Boot: %s %s%s\n", config.Boot.Loader, config.Boot.GrubDevice, config.Boot.EFIMountPoint)
	for _, fs := range config.FileSystems {
		fmt.Fprintf(&summary, "Filesystem: %s on %s (%s)\n", fs.MountPoint, fs.Device, fs.FSType)
	}
	for _, item := range network.Interfaces {
		var addresses []string
		for _, address := range append(append([]machine.Address(nil), item.IPv4Addresses...), item.IPv6Addresses...) {
			addresses = append(addresses, fmt.Sprintf("%s/%d", address.Address, address.PrefixLength))
		}
		fmt.Fprintf(&summary, "Interface %s %s: %s\n", item.Name, item.MACAddress, strings.Join(addresses, " "))
	}
	if network.DefaultGateway != nil {
		fmt.Fprintf(&summary, "Gateway: %s\n", network.DefaultGateway.Address)
	}
	if network.DefaultGateway6 != nil {
		fmt.Fprintf(&summary, "IPv6 gateway: %s\n", network.DefaultGateway6.Address)
	}
	fmt.Fprintf(&summary, "DNS: %s\n", strings.Join(network.Nameservers, " "))
	fmt.Fprintf(&summary, "Operator: %s %s\n", input.Operator.Name, input.Operator.PublicKey)
	fmt.Fprintf(&summary, "Bootstrap SSH kept until final setup: root login %s, password login %t\n", input.Bootstrap.RootLogin, input.Bootstrap.PasswordAuthentication)
	return summary.String()
}

func describeTemplate(template string) string {
	if template == machine.HostingerKVM {
		return "Hostinger KVM image"
	}
	return "existing configuration"
}

func machineCanonical(input foundation.Input) ([]byte, string, error) {
	return machine.Canonical(input.Machine)
}

func sha256Text(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// RecordedBuild returns this host's recorded build and its operator account,
// or os.ErrNotExist before any build. It reads state and changes nothing.
func RecordedBuild(ctx context.Context) (Built, string, error) {
	if os.Geteuid() != 0 {
		return Built{}, "", ErrPrivilege
	}
	if _, err := os.Lstat(hostStateDirectory); errors.Is(err, os.ErrNotExist) {
		return Built{}, "", os.ErrNotExist
	}
	root, err := enrollmentStateRoot()
	if err != nil {
		return Built{}, "", err
	}
	defer func() { _ = root.Close() }()
	var recorded importIntent
	if err := readState(root, "intent.json", &recorded); err != nil {
		return Built{}, "", err
	}
	intent, err := readImportIntent(root, recorded.OperationID, recorded.Pins)
	if err != nil {
		return Built{}, "", err
	}
	input, err := retainedInspectionInput(ctx, root, intent)
	if err != nil {
		return Built{}, "", err
	}
	return builtResult(intent), input.Operator.Name, nil
}
