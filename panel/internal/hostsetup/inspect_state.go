package hostsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"github.com/google/uuid"
)

const maxInspectionRecordBytes = maxInspectionBytes + maxSSHRecordBytes + (8 << 10)

type sourceInspectionRecord struct {
	SchemaVersion   int                `json:"schema_version"`
	OperationID     string             `json:"operation_id"`
	InstallationID  string             `json:"installation_id"`
	ServerID        string             `json:"server_id"`
	InputSHA256     string             `json:"input_sha256"`
	MachineSHA256   string             `json:"machine_sha256"`
	SystemPath      string             `json:"system_path"`
	Pins            adoptionPins       `json:"pins"`
	ActivationReady bool               `json:"activation_ready"`
	Packet          json.RawMessage    `json:"native_packet"`
	Storage         storageObservation `json:"storage"`
	SSH             sshObservation     `json:"ssh"`
}

// The source packet and its operation/input authority are one atomic record.
// Ordinary state helpers retain their smaller 64 KiB bound. Later runtime
// consumers must validate this record's bounds and bindings before using packet
// account/access evidence; its mere existence never establishes readiness.
func writeSourceInspection(ctx context.Context, root *os.Root, result Inspected, pins adoptionPins, packet []byte, storage storageObservation, ssh sshObservation) (string, error) {
	if len(ssh.Files) > 6 {
		return "", ErrInspection
	}
	total := 0
	for _, file := range ssh.Files {
		total += len(file.Bytes)
		if len(file.Bytes) > maxSSHFileBytes || total > maxSSHAuthorityBytes {
			return "", ErrInspection
		}
	}
	encodedSSH, err := json.Marshal(ssh)
	if err != nil || len(encodedSSH) > maxSSHRecordBytes {
		return "", ErrInspection
	}
	record := sourceInspectionRecord{SchemaVersion: 1, OperationID: result.OperationID, InstallationID: result.InstallationID, ServerID: result.ServerID,
		InputSHA256: result.InputSHA256, MachineSHA256: result.MachineSHA256, SystemPath: result.SystemPath, Pins: pins, ActivationReady: false, Packet: packet, Storage: storage, SSH: ssh}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if len(packet) > maxInspectionBytes || encoder.Encode(record) != nil || data.Len() > maxInspectionRecordBytes {
		return "", ErrInspection
	}
	name := ".inspection-" + uuid.NewString()
	defer func() { _ = root.Remove(name) }()
	if copyPrepared(ctx, root, name, bytes.NewReader(data.Bytes()), int64(data.Len())) != nil {
		return "", ErrInspection
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if root.Rename(name, "source-inspection.json") != nil {
		return "", ErrInspection
	}
	directory, err := root.Open(".")
	if err != nil {
		return "", ErrInspection
	}
	defer func() { _ = directory.Close() }()
	if directory.Sync() != nil {
		return "", ErrInspection
	}
	digest := sha256.Sum256(data.Bytes())
	return hex.EncodeToString(digest[:]), nil
}
