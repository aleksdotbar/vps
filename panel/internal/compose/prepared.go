package compose

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/aleksdotbar/vps/panel/internal/releases"
)

// ValidatePrepared reuses resolved admission for an immutable release template.
// Only recorded whole-value secret references may interpolate. Executor ports
// and labels are not admitted here; staging never publishes application ports.
func ValidatePrepared(data []byte, artifact releases.Artifact, bindings []SecretBinding, network Network) (Prepared, error) {
	if len(data) == 0 || len(data) > maxConfigBytes || len(bindings) > 16384 || !projectName.MatchString(network.ProjectName) || !bridgeName.MatchString(network.BridgeName) {
		return Prepared{}, ErrUnsafe
	}
	placeholders := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if !variable.MatchString(binding.Variable) || !variable.MatchString(binding.Secret) || reserved(binding.Secret) {
			return Prepared{}, ErrUnsafe
		}
		placeholders["${"+binding.Secret+"?required runtime secret}"] = binding.Secret
	}
	prepared, err := admitResolvedPolicy(data, artifact, placeholders, network, true)
	if err != nil || !bytes.Equal(data, prepared.Config) || !slices.Equal(bindings, prepared.Bindings) {
		return Prepared{}, ErrUnsafe
	}
	var static struct {
		Services map[string]map[string]any `json:"services"`
	}
	if json.Unmarshal(data, &static) != nil {
		return Prepared{}, ErrUnsafe
	}
	for _, binding := range bindings {
		environment, ok := static.Services[binding.Service]["environment"].(map[string]any)
		if !ok {
			return Prepared{}, ErrUnsafe
		}
		environment[binding.Variable] = ""
	}
	remaining := 200000
	if !staticPreparedValue(static.Services, 0, &remaining) {
		return Prepared{}, ErrUnsafe
	}
	return prepared, nil
}

func staticPreparedValue(value any, depth int, remaining *int) bool {
	*remaining--
	if depth > 64 || *remaining < 0 {
		return false
	}
	switch value := value.(type) {
	case string:
		return !interpolates(value)
	case []any:
		for _, item := range value {
			if !staticPreparedValue(item, depth+1, remaining) {
				return false
			}
		}
	case map[string]any:
		for key, item := range value {
			if interpolates(key) || !staticPreparedValue(item, depth+1, remaining) {
				return false
			}
		}
	case map[string]map[string]any:
		for key, item := range value {
			if interpolates(key) || !staticPreparedValue(item, depth+1, remaining) {
				return false
			}
		}
	}
	return true
}

func nativeDefaultLogging(data []byte) bool {
	var logging struct {
		Driver  string            `json:"driver"`
		Options map[string]string `json:"options"`
	}
	return json.Unmarshal(data, &logging) == nil && logging.Driver == "local" && len(logging.Options) == 2 && logging.Options["max-size"] == "10m" && logging.Options["max-file"] == "3"
}
