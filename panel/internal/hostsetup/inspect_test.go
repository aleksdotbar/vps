package hostsetup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
)

func sourcePacketFixture(t *testing.T) (sourcePacket, foundation.Input, string) {
	t.Helper()
	_, build, data := buildFixture(t)
	input, err := checkBuildInput(build, data)
	if err != nil {
		t.Fatal(err)
	}
	observed := input.Machine
	observed.Boot.InitrdAvailableKernelModules = append(append([]string(nil), observed.Boot.InitrdAvailableKernelModules...), "native-default-module")
	machineData, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	mutable := true
	return sourcePacket{SchemaVersion: 1, SystemPath: "/nix/store/" + strings.Repeat("1", 32) + "-nixos-system-generic-host", ReviewedInputSHA256: build.InputSHA256,
		Unsupported: []string{}, Machine: machineData, MutableUsers: &mutable, Accounts: json.RawMessage(`[]`), Groups: json.RawMessage(`[]`), SSH: json.RawMessage(`{}`)}, input, build.InputSHA256
}

func TestInspectionRequiresReviewedInputAndNativePolicyBeforeSourceCheckpoint(t *testing.T) {
	packet, input, expected := sourcePacketFixture(t)
	encode := func(value sourcePacket) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	original := encode(packet)
	if err := confirmSourcePacket(original, input, expected, packet.SystemPath); err != nil {
		t.Fatalf("native defaults must not become a raw reviewed-machine hash mismatch: %v", err)
	}
	for _, name := range []string{"input binding", "generation mismatch", "unsupported policy", "invalid observed shape", "duplicate field", "field alias", "missing field", "oversized", "trailing packet"} {
		t.Run(name, func(t *testing.T) {
			value := packet
			data := original
			switch name {
			case "input binding":
				value.ReviewedInputSHA256 = strings.Repeat("f", 64)
				data = encode(value)
			case "generation mismatch":
				value.SystemPath = "/untrusted/sensitive-source"
				data = encode(value)
			case "unsupported policy":
				value.Unsupported = []string{"access-policy"}
				data = encode(value)
			case "invalid observed shape":
				value.Machine = json.RawMessage(`{"schema_version":99}`)
				data = encode(value)
			case "duplicate field":
				data = bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
			case "field alias":
				data = bytes.Replace(data, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
			case "missing field":
				data = bytes.Replace(data, []byte(`"groups":[],`), nil, 1)
			case "oversized":
				data = make([]byte, maxInspectionBytes+1)
			case "trailing packet":
				data = append(append([]byte(nil), data...), []byte(` {}`)...)
			}
			err := confirmSourcePacket(data, input, expected, packet.SystemPath)
			if !errors.Is(err, ErrSourceMismatch) || strings.Contains(err.Error(), "sensitive-source") {
				t.Fatalf("unsafe source packet accepted or reflected: %v", err)
			}
		})
	}
}

func TestInspectionRequiresCurrentBootedAndProfileToStayOnSameSystem(t *testing.T) {
	store := t.TempDir()
	first := filepath.Join(store, strings.Repeat("1", 32)+"-nixos-system-first")
	second := filepath.Join(store, strings.Repeat("2", 32)+"-nixos-system-second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var links [3]string
	for index, name := range []string{"current", "booted", "profile"} {
		links[index] = filepath.Join(t.TempDir(), name)
		if err := os.Symlink(first, links[index]); err != nil {
			t.Fatal(err)
		}
	}
	before, err := consistentSystemLinks(links, store)
	if err != nil || before != first {
		t.Fatalf("same system links rejected: %v", err)
	}
	if err := os.Remove(links[1]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, links[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := consistentSystemLinks(links, store); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("different booted generation accepted: %v", err)
	}
	for _, link := range links {
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(second, link); err != nil {
			t.Fatal(err)
		}
	}
	after, err := consistentSystemLinks(links, store)
	if err != nil || after == before {
		t.Fatalf("changed system epoch was not observable: %v", err)
	}
}

func TestInspectionRecordKeepsPacketAndAuthorityAtomicWithoutReadiness(t *testing.T) {
	packet, input, expected := sourcePacketFixture(t)
	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	result := Inspected{Status: "source_inspected", OperationID: input.InstallationID, InstallationID: input.InstallationID, ServerID: input.ServerID,
		InputSHA256: expected, SystemPath: packet.SystemPath, MachineSHA256: strings.Repeat("a", 64), ActivationReady: false}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	pins := adoptionPins{BuildSHA256: strings.Repeat("b", 64)}
	hash, err := writeSourceInspection(t.Context(), root, result, pins, data, storageObservation{}, sshObservation{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := root.ReadFile("source-inspection.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(stored)
	var record sourceInspectionRecord
	if json.Unmarshal(stored, &record) != nil || record.Pins != pins || record.InputSHA256 != expected || record.ActivationReady || hex.EncodeToString(digest[:]) != hash {
		t.Fatal("inspection authority or readiness changed in stored record")
	}
	if err := confirmSourcePacket(record.Packet, input, expected, packet.SystemPath); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSourceInspection(t.Context(), root, result, pins, make([]byte, maxInspectionBytes+1), storageObservation{}, sshObservation{}); !errors.Is(err, ErrInspection) {
		t.Fatalf("oversized packet persisted: %v", err)
	}
	if _, err := writeSourceInspection(t.Context(), root, result, pins, data, storageObservation{}, sshObservation{Files: []sshAuthorityFile{{Bytes: make([]byte, maxSSHFileBytes+1)}}}); !errors.Is(err, ErrInspection) {
		t.Fatalf("oversized SSH authority persisted: %v", err)
	}
	retained, err := root.ReadFile("source-inspection.json")
	if err != nil || !bytes.Equal(retained, stored) {
		t.Fatal("failed inspection replaced prior bound record")
	}
}

func TestInspectionMemoryAdmissionKeepsHeadroomAndHardCeiling(t *testing.T) {
	for _, test := range []struct {
		available, expected int64
		admitted            bool
	}{
		{-1, 0, false}, {0, 0, false}, {(1536 << 10) - 1, 0, false}, {1536 << 10, 1280 << 20, true}, {2048 << 10, 1792 << 20, true},
		{2304 << 10, 2 << 30, true}, {math.MaxInt64, 2 << 30, true},
	} {
		limit, admitted := inspectionMemoryLimit(test.available)
		if limit != test.expected || admitted != test.admitted {
			t.Fatalf("memory admission for%dKiB: got%d/%t", test.available, limit, admitted)
		}
	}
}
