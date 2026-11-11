package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"slices"
	"sort"
	"strconv"
	"time"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
)

type runtimeRoots struct {
	state, connector *os.Root
	lock             *os.File
	credentials      []byte
	ledger           runtimeLedger
	finalSystem      string
}

func (r *runtimeRoots) close() {
	clear(r.credentials)
	if r.lock != nil {
		_ = r.lock.Close()
	}
	if r.state != nil {
		_ = r.state.Close()
	}
	if r.connector != nil {
		_ = r.connector.Close()
	}
}

func configurationHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func runtimeConfiguration(ledger runtimeLedger, target *runtimePending) ([]byte, error) {
	contributions := make([]Contribution, 0, len(ledger.Environments)+1)
	var origins []Origin
	found := false
	for _, environment := range ledger.Environments {
		current := environment.Current
		if target != nil && environment.EnvironmentID == target.Key.EnvironmentID {
			current = target.Target
			found = true
		}
		if current != nil {
			contributions = append(contributions, current.Contribution)
			origins = append(origins, current.Origins...)
		}
	}
	if target != nil && !found && target.Target != nil {
		contributions = append(contributions, target.Target.Contribution)
		origins = append(origins, target.Target.Origins...)
	}
	return Compile(ledger.TunnelID, contributions, origins)
}

func validateRuntimeLedger(ledger runtimeLedger) error {
	if ledger.SchemaVersion != 1 || !canonicalID(ledger.InstallationID) || !canonicalID(ledger.ServerID) || !cloudflareID(ledger.AccountID) || !canonicalID(ledger.TunnelID) || !runtimeHash(ledger.ConfigSHA256) || len(ledger.Environments) > maxEnvironments || len(ledger.Allocations) > maxEnvironments*3 {
		return ErrRuntimeState
	}
	ids := make(map[string]bool, len(ledger.Environments))
	for _, environment := range ledger.Environments {
		if !canonicalID(environment.EnvironmentID) || ids[environment.EnvironmentID] {
			return ErrRuntimeState
		}
		ids[environment.EnvironmentID] = true
		for _, current := range []*runtimeContribution{environment.Current, environment.Previous} {
			if current == nil {
				continue
			}
			if !validRuntimeKey(current.Key) || current.Key.EnvironmentID != environment.EnvironmentID || current.Contribution.EnvironmentID != environment.EnvironmentID {
				return ErrRuntimeState
			}
			if _, err := Compile(ledger.TunnelID, []Contribution{current.Contribution}, current.Origins); err != nil {
				return ErrRuntimeState
			}
		}
		if environment.Restored != nil && (!validRuntimeKey(*environment.Restored) || environment.Restored.EnvironmentID != environment.EnvironmentID) {
			return ErrRuntimeState
		}
	}
	keys := make(map[string]bool, len(ledger.Allocations))
	for _, allocation := range ledger.Allocations {
		if !validRuntimeKey(allocation.Key) || keys[allocation.Key.OperationID] || len(allocation.Origins) > maxDomains {
			return ErrRuntimeState
		}
		keys[allocation.Key.OperationID] = true
		for _, origin := range allocation.Origins {
			if origin.EnvironmentID != allocation.Key.EnvironmentID || origin.Service == "" || origin.Port < 1 || origin.Port > 65535 || origin.LoopbackPort == 0 {
				return ErrRuntimeState
			}
		}
		for _, baseline := range []*runtimeContribution{allocation.PriorCurrent, allocation.PriorPrevious} {
			if baseline == nil {
				continue
			}
			if !validRuntimeKey(baseline.Key) || baseline.Key.EnvironmentID != allocation.Key.EnvironmentID || baseline.Contribution.EnvironmentID != allocation.Key.EnvironmentID {
				return ErrRuntimeState
			}
			if _, err := Compile(ledger.TunnelID, []Contribution{baseline.Contribution}, baseline.Origins); err != nil {
				return ErrRuntimeState
			}
		}
	}
	configuration, err := runtimeConfiguration(ledger, nil)
	if err != nil || configurationHash(configuration) != ledger.ConfigSHA256 {
		return ErrRuntimeState
	}
	if pending := ledger.Pending; pending != nil {
		if !validRuntimeKey(pending.Key) || pending.FromRevision != ledger.Revision || !runtimeHash(pending.FromConfigSHA256) || !runtimeHash(pending.ConfigSHA256) || ledger.Revision == math.MaxUint64 {
			return ErrRuntimeState
		}
		if pending.Target != nil && (pending.Target.Contribution.EnvironmentID != pending.Key.EnvironmentID || !validRuntimeKey(pending.Target.Key)) {
			return ErrRuntimeState
		}
		configuration, err = runtimeConfiguration(ledger, pending)
		if err != nil || configurationHash(configuration) != pending.ConfigSHA256 {
			return ErrRuntimeState
		}
	}
	return nil
}

func validRuntimeKey(key runtimeKey) bool {
	return canonicalID(key.OperationID) && canonicalID(key.ReleaseID) && canonicalID(key.EnvironmentID) && runtimeHash(key.ManifestSHA256) && runtimeHash(key.SnapshotSHA256)
}

func createRuntimeDirectory() error {
	parent, err := os.OpenRoot("/var/lib")
	if err != nil {
		return ErrRuntimeState
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Mkdir("panel-routes", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrRuntimeState
	}
	return syncRuntimeDirectory(parent)
}

func openRuntime(operation RuntimeOperation, finalSystem string, create bool) (_ *runtimeRoots, returnErr error) {
	if ValidateRuntimeOperation(operation) != nil || !runtimeSystemPath.MatchString(finalSystem) {
		return nil, ErrInvalid
	}
	r := &runtimeRoots{finalSystem: finalSystem}
	defer func() {
		if returnErr != nil {
			r.close()
		}
	}()
	if create {
		if err := createRuntimeDirectory(); err != nil {
			return nil, err
		}
	}
	var err error
	r.state, err = openRuntimeRoot(runtimeStateDirectory, 0700)
	if err != nil {
		return nil, err
	}
	r.lock, err = runtimeLock(r.state)
	if err != nil {
		return nil, err
	}
	if runtimeDirectoryEntries(r.state, "routes.lock", "ledger.json", "ledger.json.next") != nil {
		return nil, ErrRuntimeState
	}
	err = readRuntimeRecord(r.state, "ledger.json", &r.ledger)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, ErrRuntimeState
	}
	if err == nil {
		if validateRuntimeLedger(r.ledger) != nil || r.ledger.InstallationID != operation.InstallationID || r.ledger.ServerID != operation.Snapshot.ServerID {
			return nil, ErrRuntimeState
		}
		if len(operation.Snapshot.Bindings) > 0 && (r.ledger.AccountID != operation.Snapshot.AccountID || r.ledger.TunnelID != operation.Snapshot.TunnelID) {
			return nil, ErrOwnership
		}
	} else if len(operation.Snapshot.Bindings) == 0 {
		return r, nil
	}
	if err := r.loadConnector(operation); err != nil {
		return nil, err
	}
	if r.ledger.SchemaVersion == 0 {
		configuration, err := Compile(operation.Snapshot.TunnelID, nil, nil)
		if err != nil {
			return nil, err
		}
		r.ledger = runtimeLedger{SchemaVersion: 1, InstallationID: operation.InstallationID, ServerID: operation.Snapshot.ServerID, AccountID: operation.Snapshot.AccountID, TunnelID: operation.Snapshot.TunnelID, ConfigSHA256: configurationHash(configuration), Environments: []runtimeEnvironment{}, Allocations: []runtimeAllocation{}}
	}
	return r, nil
}

// The provisioning receipt is nonsecret, protected authority. Its JSON layout
// remains owned by hosthelper; application route state never rewrites it.
func (r *runtimeRoots) loadConnector(operation RuntimeOperation) error {
	private, err := openRuntimeRoot("/var/lib/panel-tunnel", 0700)
	if err != nil {
		return ErrRuntimeState
	}
	defer func() { _ = private.Close() }()
	var final struct {
		Binding struct {
			InstallationID string `json:"installation_id"`
			ServerID       string `json:"server_id"`
			OperationID    string `json:"operation_id"`
			AccountID      string `json:"account_id"`
			Name           string `json:"name"`
			FinalSystem    string `json:"final_system"`
		} `json:"binding"`
		TunnelID string `json:"tunnel_id"`
	}
	if readRuntimeRecord(private, "final.json", &final) != nil || final.Binding.InstallationID != operation.InstallationID || final.Binding.ServerID != operation.Snapshot.ServerID || final.Binding.FinalSystem != r.finalSystem || !canonicalID(final.Binding.OperationID) || final.Binding.Name != "panel-"+final.Binding.ServerID+"-"+final.Binding.OperationID {
		return ErrRuntimeState
	}
	account, tunnel := r.ledger.AccountID, r.ledger.TunnelID
	if r.ledger.SchemaVersion == 0 {
		account, tunnel = operation.Snapshot.AccountID, operation.Snapshot.TunnelID
	}
	if final.Binding.AccountID != account || final.TunnelID != tunnel {
		return ErrOwnership
	}
	r.connector, err = openRuntimeRoot(RuntimeDirectory, 0755)
	if err != nil {
		return ErrRuntimeState
	}
	r.credentials, err = readRuntimeFile(r.connector, "credentials.json", 0600, 4096)
	if err != nil {
		return ErrRuntimeState
	}
	var credentials struct {
		AccountTag   string `json:"AccountTag"`
		TunnelSecret []byte `json:"TunnelSecret"`
		TunnelID     string `json:"TunnelID"`
	}
	defer func() { clear(credentials.TunnelSecret) }()
	if json.Unmarshal(r.credentials, &credentials) != nil || credentials.AccountTag != account || credentials.TunnelID != tunnel || len(credentials.TunnelSecret) != 32 {
		return ErrRuntimeState
	}
	return nil
}

func runtimeContext(ctx context.Context, operation RuntimeOperation) (context.Context, context.CancelFunc, error) {
	now := time.Now()
	if operation.ExecutionDeadline <= now.UnixMilli() || operation.ExecutionDeadline > now.Add(31*time.Minute).UnixMilli() {
		return nil, nil, ErrRuntimeDeadline
	}
	bounded, cancel := context.WithDeadline(ctx, time.UnixMilli(operation.ExecutionDeadline))
	return bounded, cancel, nil
}

func contributionFor(operation RuntimeOperation, origins []Origin) runtimeContribution {
	contribution := Contribution{EnvironmentID: operation.Snapshot.EnvironmentID, Services: slices.Clone(operation.Snapshot.Services), Domains: make([]*panelv1.Domain, 0, len(operation.Snapshot.Bindings))}
	for _, binding := range operation.Snapshot.Bindings {
		contribution.Domains = append(contribution.Domains, &panelv1.Domain{Hostname: binding.Hostname, Service: binding.Service, Port: binding.ContainerPort})
	}
	return runtimeContribution{Key: operationKey(operation), Contribution: contribution, Origins: slices.Clone(origins)}
}

func environmentIndex(ledger *runtimeLedger, environmentID string) int {
	for i := range ledger.Environments {
		if ledger.Environments[i].EnvironmentID == environmentID {
			return i
		}
	}
	return -1
}

func runtimeExpectedCurrent(ledger *runtimeLedger, operation RuntimeOperation) error {
	index := environmentIndex(ledger, operation.Snapshot.EnvironmentID)
	if index < 0 {
		return nil
	}
	environment := ledger.Environments[index]
	if environment.Restored != nil && *environment.Restored == operationKey(operation) {
		return ErrRuntimeConflict
	}
	if environment.Current == nil {
		return nil
	}
	current := ledger.Environments[index].Current
	if current.Key == operationKey(operation) {
		return nil
	}
	if current.Key.ReleaseID != operation.ExpectedCurrentReleaseID {
		return ErrRuntimeConflict
	}
	return nil
}

// PrepareRuntime retains target-selected ports before Compose can bind them.
// Closing these probe sockets does not reserve kernel ports between calls; the
// native runner must verify its exact Docker publications before ApplyRuntime.
func PrepareRuntime(ctx context.Context, operation RuntimeOperation, finalSystem string) (RuntimePreparation, error) {
	ctx, cancel, err := runtimeContext(ctx, operation)
	if err != nil {
		return RuntimePreparation{}, err
	}
	defer cancel()
	r, err := openRuntime(operation, finalSystem, len(operation.Snapshot.Bindings) > 0)
	if errors.Is(err, os.ErrNotExist) && len(operation.Snapshot.Bindings) == 0 {
		return RuntimePreparation{Origins: []Origin{}}, nil
	}
	if err != nil {
		return RuntimePreparation{}, err
	}
	defer r.close()
	return prepareRuntime(ctx, r, operation)
}

func prepareRuntime(ctx context.Context, r *runtimeRoots, operation RuntimeOperation) (RuntimePreparation, error) {
	if r.ledger.SchemaVersion == 0 {
		return RuntimePreparation{Origins: []Origin{}}, nil
	}
	if r.ledger.Pending != nil {
		return RuntimePreparation{}, ErrRuntimeBusy
	}
	if err := runtimeExpectedCurrent(&r.ledger, operation); err != nil {
		return RuntimePreparation{}, err
	}
	key := operationKey(operation)
	for _, allocation := range r.ledger.Allocations {
		if allocation.Key.OperationID == key.OperationID {
			if allocation.Key != key {
				return RuntimePreparation{}, ErrRuntimeConflict
			}
			return RuntimePreparation{Origins: slices.Clone(allocation.Origins), Revision: r.ledger.Revision, ConfigSHA256: r.ledger.ConfigSHA256}, nil
		}
	}
	pruneRuntimeAllocations(&r.ledger, operation.Snapshot.EnvironmentID)
	for _, allocation := range r.ledger.Allocations {
		if allocation.Key.EnvironmentID == key.EnvironmentID && !allocationReferenced(r.ledger, allocation.Key) {
			return RuntimePreparation{}, ErrRuntimeBusy
		}
	}
	if len(r.ledger.Allocations) >= maxEnvironments*3 {
		return RuntimePreparation{}, ErrCapacity
	}
	var origins []Origin
	var err error
	index := environmentIndex(&r.ledger, key.EnvironmentID)
	if index >= 0 {
		previous := r.ledger.Environments[index].Previous
		if previous != nil && previous.Key.ReleaseID == key.ReleaseID {
			origins = slices.Clone(previous.Origins)
		}
	}
	if origins == nil {
		origins, err = allocateRuntimeOrigins(ctx, r.ledger, operation)
		if err != nil {
			return RuntimePreparation{}, err
		}
	}
	contribution := contributionFor(operation, origins)
	pending := runtimePending{Key: key, Target: &contribution}
	if _, err := runtimeConfiguration(r.ledger, &pending); err != nil {
		return RuntimePreparation{}, err
	}
	allocation := runtimeAllocation{Key: key, Origins: origins}
	if index >= 0 {
		allocation.PriorCurrent = r.ledger.Environments[index].Current
		allocation.PriorPrevious = r.ledger.Environments[index].Previous
	}
	r.ledger.Allocations = append(r.ledger.Allocations, allocation)
	if err := saveRuntimeLedger(ctx, r.state, r.ledger); err != nil {
		return RuntimePreparation{}, err
	}
	return RuntimePreparation{Origins: slices.Clone(origins), Revision: r.ledger.Revision, ConfigSHA256: r.ledger.ConfigSHA256}, nil
}

func allocationReferenced(ledger runtimeLedger, key runtimeKey) bool {
	for _, environment := range ledger.Environments {
		if environment.Current != nil && environment.Current.Key == key || environment.Previous != nil && environment.Previous.Key == key {
			return true
		}
	}
	return ledger.Pending != nil && ledger.Pending.Key == key
}

func pruneRuntimeAllocations(ledger *runtimeLedger, environmentID string) {
	index := environmentIndex(ledger, environmentID)
	if index < 0 {
		return
	}
	var retired []runtimeKey
	if key := ledger.Environments[index].Restored; key != nil {
		retired = append(retired, *key)
	}
	current := ledger.Environments[index].Current
	for _, allocation := range ledger.Allocations {
		if current != nil && allocation.Key == current.Key && allocation.PriorPrevious != nil {
			retired = append(retired, allocation.PriorPrevious.Key)
		}
	}
	ledger.Allocations = slices.DeleteFunc(ledger.Allocations, func(allocation runtimeAllocation) bool {
		return slices.Contains(retired, allocation.Key) && !allocationReferenced(*ledger, allocation.Key)
	})
}

func allocateRuntimeOrigins(ctx context.Context, ledger runtimeLedger, operation RuntimeOperation) ([]Origin, error) {
	used := make(map[uint16]bool)
	for _, allocation := range ledger.Allocations {
		for _, origin := range allocation.Origins {
			used[origin.LoopbackPort] = true
		}
	}
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	keys := make(map[originKey]bool)
	origins := make([]Origin, 0, len(operation.Snapshot.Bindings))
	for _, binding := range operation.Snapshot.Bindings {
		key := originKey{operation.Snapshot.EnvironmentID, binding.Service, binding.ContainerPort}
		if keys[key] {
			continue
		}
		keys[key] = true
		found := false
		for range 256 {
			listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
			if err != nil {
				return nil, ErrOrigin
			}
			_, port, err := net.SplitHostPort(listener.Addr().String())
			value, parseErr := strconv.ParseUint(port, 10, 16)
			if err != nil || parseErr != nil || value == 0 {
				_ = listener.Close()
				return nil, ErrOrigin
			}
			if used[uint16(value)] || value == 20241 {
				_ = listener.Close()
				continue
			}
			listeners = append(listeners, listener)
			used[uint16(value)] = true
			origins = append(origins, Origin{key.environment, key.service, key.port, uint16(value)})
			found = true
			break
		}
		if !found {
			return nil, ErrCapacity
		}
	}
	sort.Slice(origins, func(i, j int) bool {
		if origins[i].Service != origins[j].Service {
			return origins[i].Service < origins[j].Service
		}
		return origins[i].Port < origins[j].Port
	})
	return origins, nil
}
