package deployment

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"buf.build/go/protovalidate"

	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
)

const MaxApplicationBytes = 2 << 20

var registryUsername = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// Materialize checks the original nonsecret release and emits only declared
// per-service secret values. Returned bytes belong only in protected target
// storage. Callers clear them after use; no plaintext fingerprint is exposed.
func Materialize(request StageRequest) ([]byte, error) {
	manifest := request.Manifest
	if ValidateIdentity(manifest.Identity) != nil || manifest.EnvironmentVersion < 1 || manifest.SecretVersion < 1 || !validHash(manifest.ReleaseSHA256) || !validHash(manifest.RouteSnapshotSHA256) || len(manifest.Domains) > 32 || len(request.SecretValues) > 64 {
		return nil, ErrInvalid
	}
	artifactData, err := json.Marshal(manifest.Artifact)
	if err != nil {
		return nil, ErrInvalid
	}
	artifact, err := releases.ParseArtifact(artifactData, manifest.Artifact.SourceSHA)
	if err != nil {
		return nil, ErrInvalid
	}
	prepared, err := compose.ValidatePrepared(manifest.Config, artifact, manifest.Bindings, compose.Network{ProjectName: "vps-" + strings.ReplaceAll(manifest.Identity.EnvironmentID, "-", ""), BridgeName: manifest.BridgeName})
	if err != nil {
		return nil, ErrInvalid
	}
	snapshot := manifest.RouteSnapshot
	routeHash, err := routes.SnapshotSHA256(snapshot)
	if err != nil || routeHash != manifest.RouteSnapshotSHA256 || snapshot.OperationID != manifest.Identity.OperationID || snapshot.ReleaseID != manifest.Identity.ReleaseID || snapshot.EnvironmentID != manifest.Identity.EnvironmentID || snapshot.ServerID != manifest.Identity.ServerID || len(snapshot.Bindings) != len(manifest.Domains) || !slices.Equal(snapshot.Services, prepared.Services) || snapshot.InstallationID != "" && snapshot.InstallationID != manifest.Identity.InstallationID {
		return nil, ErrInvalid
	}
	for _, domain := range manifest.Domains {
		found := false
		if domain == nil {
			return nil, ErrInvalid
		}
		for _, binding := range snapshot.Bindings {
			found = found || binding.Hostname == domain.Hostname && binding.Service == domain.Service && binding.ContainerPort == domain.Port
		}
		if !found {
			return nil, ErrInvalid
		}
	}
	if request.Registry != nil && (!registryUsername.MatchString(request.Registry.Username) || len(request.Registry.Token) == 0 || len(request.Registry.Token) > 16384 || !utf8.Valid(request.Registry.Token) || slices.Contains(request.Registry.Token, byte(0)) || slices.Contains(request.Registry.Token, byte('\n')) || slices.Contains(request.Registry.Token, byte('\r'))) {
		return nil, ErrInvalid
	}
	domains, err := json.Marshal(manifest.Domains)
	if err != nil {
		return nil, ErrInvalid
	}
	hash, err := releases.SnapshotSHA256(manifest.Config, domains, manifest.Identity.EnvironmentID, manifest.SecretVersion)
	if err != nil || hash != manifest.ReleaseSHA256 {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, len(manifest.Domains))
	for _, domain := range manifest.Domains {
		if domain == nil || protovalidate.Validate(domain) != nil || !slices.Contains(prepared.Services, domain.Service) || seen[domain.Hostname] {
			return nil, ErrInvalid
		}
		seen[domain.Hostname] = true
	}
	required := make(map[string]bool)
	for _, binding := range prepared.Bindings {
		required[binding.Secret] = true
	}
	if len(required) != len(request.SecretValues) {
		return nil, ErrInvalid
	}
	for name, value := range request.SecretValues {
		if !required[name] || len(value) > 16384 || !utf8.Valid(value) || slices.Contains(value, byte(0)) {
			return nil, ErrInvalid
		}
	}
	if err := boundMaterializedConfig(prepared, request.SecretValues); err != nil {
		return nil, err
	}
	var model map[string]json.RawMessage
	if json.Unmarshal(prepared.Config, &model) != nil || model == nil {
		return nil, ErrInvalid
	}
	var services map[string]map[string]json.RawMessage
	if json.Unmarshal(model["services"], &services) != nil || services == nil {
		return nil, ErrInvalid
	}
	environments := make(map[string]map[string]string)
	for _, binding := range prepared.Bindings {
		service, exists := services[binding.Service]
		if !exists || service == nil {
			return nil, ErrInvalid
		}
		environment, loaded := environments[binding.Service]
		if !loaded {
			if json.Unmarshal(service["environment"], &environment) != nil {
				return nil, ErrInvalid
			}
			environments[binding.Service] = environment
		}
		if environment == nil {
			return nil, ErrInvalid
		}
		value, exists := request.SecretValues[binding.Secret]
		if !exists {
			return nil, ErrInvalid
		}
		// The template already escapes static dollars. Escape only inserted
		// values for the next native Compose interpolation pass, exactly once.
		environment[binding.Variable] = strings.ReplaceAll(string(value), "$", "$$")
	}
	for service, environment := range environments {
		entry := services[service]
		if entry == nil {
			return nil, ErrInvalid
		}
		encoded, err := json.Marshal(environment)
		if err != nil {
			return nil, ErrInvalid
		}
		defer clear(encoded)
		entry["environment"] = encoded
	}
	data, err := json.Marshal(services)
	if err != nil {
		return nil, ErrInvalid
	}
	defer clear(data)
	model["services"] = data
	materialized, err := json.Marshal(model)
	if err != nil || len(materialized) > MaxApplicationBytes {
		clear(materialized)
		return nil, ErrInvalid
	}
	return materialized, nil
}

// Bound repeated expansion before constructing per-binding plaintext strings.
// Each unique secret is escaped and JSON encoded at most once for sizing; its
// 16 KiB input bound keeps that temporary allocation small regardless of reuse.
func boundMaterializedConfig(prepared compose.Prepared, values map[string][]byte) error {
	size := int64(len(prepared.Config))
	deltas := make(map[string]int64, len(values))
	for _, binding := range prepared.Bindings {
		delta, cached := deltas[binding.Secret]
		if !cached {
			value, exists := values[binding.Secret]
			if !exists {
				return ErrInvalid
			}
			encoded, err := json.Marshal(strings.ReplaceAll(string(value), "$", "$$"))
			if err != nil {
				return ErrInvalid
			}
			reference, err := json.Marshal("${" + binding.Secret + "?required runtime secret}")
			if err != nil {
				clear(encoded)
				return ErrInvalid
			}
			delta = int64(len(encoded)) - int64(len(reference))
			clear(encoded)
			deltas[binding.Secret] = delta
		}
		size += delta
	}
	if size > MaxApplicationBytes {
		return ErrInvalid
	}
	return nil
}
