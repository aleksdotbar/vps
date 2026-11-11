package deployment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

const maxRegistryOperations = 512

func registryProof(value *RegistryCredential) (string, []byte, error) {
	if value == nil {
		return "", nil, nil
	}
	// #nosec G117 -- Transient protected target registry staging; callers clear the buffer, never log or archive its bytes.
	data, err := json.Marshal(value)
	if err != nil {
		return "", nil, ErrInvalid
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), data, nil
}

// /run clears registry credentials on reboot. Durable application release
// material contains only the private proof, never a retained pull token.
func openRegistryRoot(create bool) (*os.Root, error) {
	parent, err := os.OpenRoot("/run")
	if err != nil {
		return nil, ErrState
	}
	defer func() { _ = parent.Close() }()
	return child(parent, "panel-deploy-registry", create)
}

func publishRegistry(ctx context.Context, root *os.Root, expected claim, data []byte, arm registryExpiryArm) error {
	if root == nil || arm == nil {
		return ErrState
	}
	listing, err := root.Open(".")
	if err != nil {
		return ErrState
	}
	entries, readErr := listing.ReadDir(maxRegistryOperations + 1)
	_ = listing.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) >= maxRegistryOperations {
		return ErrState
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			return ErrState
		}
	}
	directory, err := child(root, expected.Identity.OperationID, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if directoryEntries(directory, "expiry.json", "credential.json") != nil {
		return ErrState
	}
	hash := sha256.Sum256(data)
	proof := hex.EncodeToString(hash[:])
	expiry := newRegistryExpiry(expected, proof)
	if _, err := directory.Lstat("expiry.json"); err == nil {
		expiry, err = readRegistryExpiry(directory, expected.Identity.OperationID)
		if err != nil || expiry.Claim != expected || expiry.CredentialSHA256 != proof {
			return ErrState
		}
	} else if !errors.Is(err, os.ErrNotExist) || putRecord(ctx, directory, "expiry.json", expiry) != nil {
		return ErrState
	}
	if arm(ctx, expiry) != nil {
		return ErrState
	}
	if existing, err := readFile(directory, "credential.json"); err == nil {
		defer clear(existing)
		if !bytes.Equal(existing, data) {
			return ErrState
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if ctx.Err() != nil || time.Now().UnixMilli() >= expected.Identity.ExecutionDeadline {
		return ErrDeadline
	}
	return publish(ctx, directory, "credential.json", data)
}

func readRegistry(root *os.Root, operationID string) (RegistryCredential, error) {
	if root == nil {
		return RegistryCredential{}, ErrState
	}
	directory, err := child(root, operationID, false)
	if err != nil {
		return RegistryCredential{}, err
	}
	defer func() { _ = directory.Close() }()
	var credential RegistryCredential
	err = readRecord(directory, "credential.json", &credential)
	return credential, err
}

func cleanupRegistry(root *os.Root, operationID string) error {
	if root == nil {
		return nil
	}
	if _, err := root.Lstat(operationID); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return ErrState
	}
	operation, err := child(root, operationID, false)
	if err != nil {
		return err
	}
	defer func() { _ = operation.Close() }()
	if directoryEntries(operation, "expiry.json", "credential.json", "docker-login") != nil {
		return ErrState
	}
	expiry, err := readRegistryExpiry(operation, operationID)
	if err != nil {
		return err
	}
	var credential RegistryCredential
	err = readRecord(operation, "credential.json", &credential)
	defer clear(credential.Token)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if err == nil {
		proof, data, proofErr := registryProof(&credential)
		clear(data)
		if proofErr != nil || proof != expiry.CredentialSHA256 || operation.Remove("credential.json") != nil {
			return ErrState
		}
	}
	if _, err := operation.Lstat("docker-login"); err == nil {
		directory, err := child(operation, "docker-login", false)
		if err != nil {
			return err
		}
		defer func() { _ = directory.Close() }()
		if directoryEntries(directory, "config.json") != nil {
			return ErrState
		}
		info, statErr := directory.Stat("config.json")
		if statErr == nil {
			if !protected(info, false) || directory.Remove("config.json") != nil {
				return ErrState
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return ErrState
		}
		if syncDirectory(directory) != nil || operation.Remove("docker-login") != nil {
			return ErrState
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if operation.Remove("expiry.json") != nil || syncDirectory(operation) != nil || root.Remove(operationID) != nil || syncDirectory(root) != nil {
		return ErrState
	}
	return nil
}

func prepareImages(ctx context.Context, registry *os.Root, value staged, tools runnerTools) (map[string]string, error) {
	operationID := value.Claim.Identity.OperationID
	defer func() { _ = cleanupRegistry(registry, operationID) }()
	directory := "/var/empty"
	if value.RegistrySHA256 != "" {
		credential, err := readRegistry(registry, operationID)
		if err != nil {
			return nil, err
		}
		defer clear(credential.Token)
		proof, data, err := registryProof(&credential)
		clear(data)
		if err != nil || proof != value.RegistrySHA256 {
			return nil, ErrState
		}
		operation, err := child(registry, operationID, false)
		if err != nil {
			return nil, err
		}
		defer func() { _ = operation.Close() }()
		login, err := child(operation, "docker-login", true)
		if err != nil {
			return nil, err
		}
		defer func() { _ = login.Close() }()
		if directoryEntries(login) != nil {
			return nil, ErrState
		}
		directory = operation.Name() + "/docker-login"
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = runnerCommand(bounded, tools.docker, []string{"login", "ghcr.io", "--username", credential.Username, "--password-stdin"}, credential.Token, dockerEnvironment(directory), 4096)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	images := make(map[string]string, len(value.Manifest.Artifact.Services))
	for service, artifact := range value.Manifest.Artifact.Services {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err := runnerCommand(bounded, tools.docker, []string{"image", "pull", "--quiet", "--", artifact.Image}, nil, dockerEnvironment(directory), 4096)
		cancel()
		if err != nil {
			return nil, err
		}
		id, err := inspectImage(ctx, tools, artifact.Image)
		if err != nil {
			return nil, err
		}
		images[service] = id
	}
	if cleanupRegistry(registry, operationID) != nil {
		return nil, ErrState
	}
	return images, nil
}
