package deployment

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type registryExpiry struct {
	Claim            claim  `json:"claim"`
	LaunchID         string `json:"launch_id"`
	CredentialSHA256 string `json:"credential_sha256"`
}

type registryExpiryArm func(context.Context, registryExpiry) error

func registryExpiryUnit(value registryExpiry) string {
	return "panel-deploy-registry-" + value.Claim.Identity.OperationID
}
func registryExpiryDescription(value registryExpiry) string {
	return "Panel registry expiry " + value.Claim.Identity.OperationID + " " + value.LaunchID
}

func armRegistryExpiry(ctx context.Context, value registryExpiry) error {
	tools := systemTools(value.Claim.FinalSystem)
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if registryExpiryActive(bounded, tools, value) == nil {
		return nil
	}
	arguments := []string{"--quiet", "--no-ask-password", "--collect", "--service-type=exec", "--expand-environment=no", "--unit=" + registryExpiryUnit(value), "--description=" + registryExpiryDescription(value), "--property=MemoryMax=128M", "--property=CPUQuota=100%", "--property=TasksMax=32", "--property=RuntimeMaxSec=30", "--property=TimeoutStopSec=5", "--property=KillMode=control-group", "--property=UMask=0077", "--property=StandardOutput=null", "--property=StandardError=null", "--on-calendar=@" + strconv.FormatInt(1+(value.Claim.Identity.ExecutionDeadline-1)/1000, 10), "--timer-property=AccuracySec=1s", "--timer-property=RemainAfterElapse=no", "--timer-property=Description=" + registryExpiryDescription(value), "--", tools.env, "-i", "HOME=/var/empty", "PATH=/nonexistent", "LANG=C", "LC_ALL=C", "GOMEMLIMIT=64MiB", tools.panel, "deploy-registry-cleanup", "--operation-id", value.Claim.Identity.OperationID}
	if _, err := runnerCommand(bounded, tools.systemdRun, arguments, nil, nil, 4096); err != nil {
		return err
	}
	return registryExpiryActive(bounded, tools, value)
}

func registryExpiryActive(ctx context.Context, tools runnerTools, value registryExpiry) error {
	// Verify that the scheduled native timer belongs to this exact protected
	// attempt before permitting a plaintext credential publication.
	data, err := runnerCommand(ctx, tools.systemctl, []string{"show", "--property=LoadState,ActiveState,Description,Transient", "--", registryExpiryUnit(value) + ".timer"}, nil, nil, 4096)
	if err != nil {
		return err
	}
	properties := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ErrState
		}
		if _, exists := properties[key]; exists {
			return ErrState
		}
		properties[key] = value
	}
	if len(properties) != 4 || properties["LoadState"] != "loaded" || properties["ActiveState"] != "active" || properties["Transient"] != "yes" || properties["Description"] != registryExpiryDescription(value) {
		return ErrState
	}
	return nil
}

func readRegistryExpiry(directory *os.Root, operationID string) (registryExpiry, error) {
	var value registryExpiry
	if readRecord(directory, "expiry.json", &value) != nil || ValidateIdentity(value.Claim.Identity) != nil || value.Claim.Identity.OperationID != operationID || !validHash(value.Claim.ManifestSHA256) || !finalSystemPath.MatchString(value.Claim.FinalSystem) || !validID(value.LaunchID) || !validHash(value.CredentialSHA256) {
		return value, ErrState
	}
	return value, nil
}

func CleanupExpiredRegistry(ctx context.Context, operationID string) error {
	if !validID(operationID) {
		return ErrInvalid
	}
	root, err := openRegistryRoot(false)
	if err != nil {
		parent, openErr := os.OpenRoot("/run")
		if openErr != nil {
			return ErrState
		}
		defer func() { _ = parent.Close() }()
		if _, statErr := parent.Lstat("panel-deploy-registry"); errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		return ErrState
	}
	defer func() { _ = root.Close() }()
	return cleanupExpiredRegistry(ctx, root, operationID, time.Now())
}

func cleanupExpiredRegistry(ctx context.Context, root *os.Root, operationID string, now time.Time) error {
	if !validID(operationID) || ctx.Err() != nil {
		return ErrInvalid
	}
	if _, err := root.Lstat(operationID); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return ErrState
	}
	directory, err := child(root, operationID, false)
	if err != nil {
		return err
	}
	value, err := readRegistryExpiry(directory, operationID)
	_ = directory.Close()
	if err != nil {
		return err
	}
	if now.UnixMilli() < value.Claim.Identity.ExecutionDeadline {
		return ErrDeadline
	}
	return cleanupRegistry(root, operationID)
}

func newRegistryExpiry(expected claim, proof string) registryExpiry {
	return registryExpiry{Claim: expected, LaunchID: uuid.NewString(), CredentialSHA256: proof}
}
