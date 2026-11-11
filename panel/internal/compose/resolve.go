package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/releases"
)

const (
	maxFileBytes   = 256 << 10
	maxConfigBytes = 2 << 20
)

var (
	ErrUnsafe        = errors.New("compose configuration is incompatible with the stateless runtime")
	ErrMissingSecret = errors.New("a declared runtime secret is missing or empty")
	ErrUnavailable   = errors.New("pinned Docker Compose is unavailable")
)

type Resolver struct{ command string }
type Network struct {
	ProjectName string
	BridgeName  string
}
type SecretBinding struct {
	Service  string `json:"service"`
	Variable string `json:"variable"`
	Secret   string `json:"secret"`
}
type Prepared struct {
	Config   []byte
	Services []string
	Bindings []SecretBinding
}

func New(command string) (*Resolver, error) {
	resolved, err := exec.LookPath(command)
	if err != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// SAFETY: The executable is operator-controlled installation configuration;
	// fixed argv invokes no shell or repository-supplied command.
	process := exec.CommandContext(ctx, resolved, "version", "--short") // #nosec G204,G702 -- trusted executable with fixed arguments.
	process.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "DOCKER_CONFIG=/nonexistent"}
	process.WaitDelay = time.Second
	var output boundedBuffer
	output.limit = 128
	process.Stdout = &output
	process.Stderr = io.Discard
	if err := process.Run(); err != nil || strings.TrimSpace(output.String()) != "5.4.0" {
		return nil, ErrUnavailable
	}
	return &Resolver{command: resolved}, nil
}

// Resolve copies two bounded files from one immutable source root. The raw gate
// runs before native Compose can follow any repository-controlled file reference.
// availableSecrets contains presence/nonempty flags, never plaintext values.
func (r *Resolver) Resolve(ctx context.Context, tree, override string, artifact releases.Artifact, availableSecrets map[string]bool, network Network) (Prepared, error) {
	if !safePath(override) || override == "compose.yaml" || !projectName.MatchString(network.ProjectName) || !bridgeName.MatchString(network.BridgeName) {
		return Prepared{}, ErrUnsafe
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return Prepared{}, ErrUnsafe
	}
	defer func() { _ = root.Close() }()
	files := make([][]byte, 0, 2)
	for _, path := range []string{"compose.yaml", override} {
		file, err := privatefile.OpenNoFollow(root, path, os.O_RDONLY, 0)
		if err != nil {
			return Prepared{}, ErrUnsafe
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return Prepared{}, ErrUnsafe
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > maxFileBytes {
			return Prepared{}, ErrUnsafe
		}
		files = append(files, data)
	}
	for _, service := range artifact.Services {
		if _, collision := availableSecrets[service.ImageVariable]; collision || reserved(service.ImageVariable) {
			return Prepared{}, ErrUnsafe
		}
	}
	references := make(map[string]bool)
	for _, data := range files {
		if err := gate(data, artifact, availableSecrets, references); err != nil {
			return Prepared{}, err
		}
	}
	directory, err := os.MkdirTemp("", "panel-compose-")
	if err != nil {
		return Prepared{}, ErrUnavailable
	}
	defer func() { _ = os.RemoveAll(directory) }()
	for index, name := range []string{"compose.yaml", "override.yaml"} {
		if err := os.WriteFile(filepath.Join(directory, name), files[index], 0600); err != nil {
			return Prepared{}, ErrUnavailable
		}
	}
	nonce := rand.Text()
	placeholders := make(map[string]string, len(availableSecrets))
	values := make(map[string]string, len(artifact.Services)+len(availableSecrets))
	for _, service := range artifact.Services {
		values[service.ImageVariable] = service.Image
	}
	for name := range references {
		placeholder := "PANEL_SECRET_" + nonce + "_" + name
		placeholders[placeholder] = name
		values[name] = placeholder
	}
	var environment strings.Builder
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		environment.WriteString(name + "=" + values[name] + "\n")
	}
	if err := os.WriteFile(filepath.Join(directory, "inputs.env"), []byte(environment.String()), 0600); err != nil {
		return Prepared{}, ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// SAFETY: Private files passed the raw gate; project name is validated and
	// command is the installation-owned, version-checked executable. No shell.
	process := exec.CommandContext(bounded, r.command, "--env-file", filepath.Join(directory, "inputs.env"), "--project-name", network.ProjectName, "-f", filepath.Join(directory, "compose.yaml"), "-f", filepath.Join(directory, "override.yaml"), "config", "--format", "json") // #nosec G204 -- trusted executable, fixed flags, isolated admitted input files.
	process.WaitDelay = 2 * time.Second
	process.Dir = directory
	process.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "DOCKER_CONFIG=" + directory, "COMPOSE_DISABLE_ENV_FILE=1"}
	output := boundedBuffer{limit: maxConfigBytes}
	diagnostics := boundedBuffer{limit: 32 << 10}
	process.Stdout = &output
	process.Stderr = &diagnostics
	if err := process.Run(); err != nil {
		return Prepared{}, ErrUnsafe
	} // Native diagnostics are never logged/returned.
	return admitResolved(output.Bytes(), artifact, placeholders, network)
}

var projectName = regexp.MustCompile(`^vps-[a-f0-9]{32}$`)
var bridgeName = regexp.MustCompile(`^vps-[a-f0-9]{10}$`)
var variable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var expression = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:\?|\?)[^{}$]*)?\}$`)

func safePath(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\\\x00") && len(path) <= 512 && len(strings.Split(path, "/")) <= 16 && !strings.HasPrefix(path, "../") && path != ".." && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml"))
}
func gate(data []byte, artifact releases.Artifact, secrets map[string]bool, references map[string]bool) error {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&document) != nil || len(document.Content) != 1 {
		return ErrUnsafe
	}
	var extra yaml.Node
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return ErrUnsafe
	}
	visited := 0
	if err := safeYAML(&document, 0, &visited); err != nil {
		return err
	}
	top := document.Content[0]
	if top.Kind != yaml.MappingNode {
		return ErrUnsafe
	}
	for index := 0; index < len(top.Content); index += 2 {
		if top.Content[index].Value != "services" {
			return ErrUnsafe
		}
	}
	services := mapping(top, "services")
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) > 128 {
		return ErrUnsafe
	}
	for index := 0; index < len(services.Content); index += 2 {
		name := services.Content[index].Value
		service := services.Content[index+1]
		expected, exists := artifact.Services[name]
		if !exists || service.Kind != yaml.MappingNode {
			return ErrUnsafe
		}
		for field := 0; field < len(service.Content); field += 2 {
			key := service.Content[field].Value
			value := service.Content[field+1]
			if !allowedService[key] {
				return ErrUnsafe
			}
			if key == "image" {
				match := expression.FindStringSubmatch(value.Value)
				if value.Kind != yaml.ScalarNode || len(match) != 3 || match[1] != expected.ImageVariable {
					return ErrUnsafe
				}
			} else if key == "environment" {
				if value.Kind != yaml.MappingNode {
					return ErrUnsafe
				}
				for entry := 0; entry < len(value.Content); entry += 2 {
					name := value.Content[entry].Value
					entryValue := value.Content[entry+1]
					if !variable.MatchString(name) || entryValue.Kind != yaml.ScalarNode || entryValue.Tag == "!!null" {
						return ErrUnsafe
					}
					match := expression.FindStringSubmatch(entryValue.Value)
					if len(match) == 3 {
						if reserved(match[1]) {
							return ErrUnsafe
						}
						nonempty, exists := secrets[match[1]]
						references[match[1]] = true
						if !exists || (match[2] == ":?" && !nonempty) {
							return ErrMissingSecret
						}
					} else {
						if _, declared := secrets[name]; declared {
							return ErrUnsafe
						}
						if interpolates(entryValue.Value) {
							return ErrUnsafe
						}
					}
				}
			} else if hasInterpolation(value) {
				return ErrUnsafe
			}
		}
	}
	return nil
}

var allowedService = map[string]bool{"image": true, "environment": true, "command": true, "entrypoint": true, "working_dir": true, "user": true, "init": true, "read_only": true, "cap_drop": true, "security_opt": true, "cpus": true, "mem_limit": true, "pids_limit": true, "stop_grace_period": true, "restart": true, "healthcheck": true, "logging": true, "tmpfs": true, "expose": true, "depends_on": true, "labels": true}

func safeYAML(node *yaml.Node, depth int, visited *int) error {
	*visited++
	if depth > 64 || *visited > 20000 || node.Kind == yaml.AliasNode || node.Anchor != "" || node.Style&yaml.TaggedStyle != 0 {
		return ErrUnsafe
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] || interpolates(key.Value) {
				return ErrUnsafe
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := safeYAML(child, depth+1, visited); err != nil {
			return err
		}
	}
	return nil
}
func mapping(node *yaml.Node, key string) *yaml.Node {
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}
func interpolates(value string) bool {
	return strings.Contains(strings.ReplaceAll(value, "$$", ""), "$")
}
func hasInterpolation(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode && interpolates(node.Value) {
		return true
	}
	for _, child := range node.Content {
		if hasInterpolation(child) {
			return true
		}
	}
	return false
}

type boundedBuffer struct {
	// Embedding bytes.Buffer promotes ReadFrom, bypassing Write in io.Copy.
	buffer bytes.Buffer
	limit  int
}

func (buffer *boundedBuffer) Len() int       { return buffer.buffer.Len() }
func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }
func (buffer *boundedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > buffer.limit-buffer.Len() {
		return 0, errors.New("process output limit exceeded")
	}
	return buffer.buffer.Write(data)
}

// Config remains a native Compose secret-reference template, not directly
// executable JSON. P09 must escape dollars in inserted plaintext secrets for
// up's interpolation pass while preserving native escapes in static values.
func admitResolved(data []byte, artifact releases.Artifact, placeholders map[string]string, network Network) (Prepared, error) {
	return admitResolvedPolicy(data, artifact, placeholders, network, false)
}

func admitResolvedPolicy(data []byte, artifact releases.Artifact, placeholders map[string]string, network Network, preparedInput bool) (Prepared, error) {
	var model map[string]json.RawMessage
	if json.Unmarshal(data, &model) != nil || model == nil {
		return Prepared{}, ErrUnsafe
	}
	var services map[string]map[string]json.RawMessage
	if json.Unmarshal(model["services"], &services) != nil || services == nil || len(services) != len(artifact.Services) {
		return Prepared{}, ErrUnsafe
	}
	prepared := Prepared{}
	for key := range model {
		if key != "services" && key != "name" && key != "networks" {
			return Prepared{}, ErrUnsafe
		}
	}
	for name, service := range services {
		if service == nil {
			return Prepared{}, ErrUnsafe
		}
		for key := range service {
			if key != "networks" && !allowedService[key] {
				return Prepared{}, ErrUnsafe
			}
		}
		var networks map[string]json.RawMessage
		if json.Unmarshal(service["networks"], &networks) != nil || len(networks) != 1 || !bytes.Equal(bytes.TrimSpace(networks["default"]), []byte("null")) {
			return Prepared{}, ErrUnsafe
		}
		expected, exists := artifact.Services[name]
		if !exists {
			return Prepared{}, ErrUnsafe
		}
		var image, user string
		var cpus float64
		var memory string
		var pids int64
		var readOnly bool
		if json.Unmarshal(service["image"], &image) != nil || image != expected.Image || json.Unmarshal(service["user"], &user) != nil || !nonroot(user) || json.Unmarshal(service["cpus"], &cpus) != nil || cpus <= 0 || cpus > 64 || json.Unmarshal(service["mem_limit"], &memory) != nil || json.Unmarshal(service["pids_limit"], &pids) != nil || pids < 1 || pids > 1024 || json.Unmarshal(service["read_only"], &readOnly) != nil || !readOnly {
			return Prepared{}, ErrUnsafe
		}
		memoryBytes, err := strconv.ParseInt(memory, 10, 64)
		if err != nil || memoryBytes < 1 || memoryBytes > 64<<30 {
			return Prepared{}, ErrUnsafe
		}
		var caps, security []string
		if json.Unmarshal(service["cap_drop"], &caps) != nil || len(caps) != 1 || caps[0] != "ALL" || json.Unmarshal(service["security_opt"], &security) != nil || len(security) != 1 || security[0] != "no-new-privileges:true" {
			return Prepared{}, ErrUnsafe
		}
		var labels map[string]string
		if len(service["labels"]) > 0 && json.Unmarshal(service["labels"], &labels) != nil {
			return Prepared{}, ErrUnsafe
		}
		for label := range labels {
			if strings.HasPrefix(label, "com.docker.compose.") || strings.HasPrefix(label, "vps.") {
				return Prepared{}, ErrUnsafe
			}
		}
		health, validHealth := safeHealth(service["healthcheck"])
		if !validHealth || !safeTmpfs(service["tmpfs"]) {
			return Prepared{}, ErrUnsafe
		}
		service["healthcheck"] = health
		if len(service["logging"]) == 0 {
			service["logging"] = json.RawMessage(`{"driver":"local","options":{"max-size":"10m","max-file":"3"}}`)
		} else if (!preparedInput || !nativeDefaultLogging(service["logging"])) && !safeLogging(service["logging"]) {
			return Prepared{}, ErrUnsafe
		}
		var stop string
		if json.Unmarshal(service["stop_grace_period"], &stop) != nil {
			return Prepared{}, ErrUnsafe
		}
		grace, err := time.ParseDuration(stop)
		if err != nil || grace <= 0 || grace > time.Minute {
			return Prepared{}, ErrUnsafe
		}
		var environment map[string]string
		if len(service["environment"]) > 0 && json.Unmarshal(service["environment"], &environment) != nil {
			return Prepared{}, ErrUnsafe
		}
		for key, value := range environment {
			if secret, exists := placeholders[value]; exists {
				prepared.Bindings = append(prepared.Bindings, SecretBinding{Service: name, Variable: key, Secret: secret})
				environment[key] = "${" + secret + "?required runtime secret}"
			}
		}
		if len(service["environment"]) > 0 {
			service["environment"], _ = json.Marshal(environment)
		}
		for field, value := range service {
			if field == "environment" {
				continue
			}
			for placeholder := range placeholders {
				if bytes.Contains(value, []byte(placeholder)) {
					return Prepared{}, ErrUnsafe
				}
			}
		}
		prepared.Services = append(prepared.Services, name)
	}
	sort.Strings(prepared.Services)
	sort.Slice(prepared.Bindings, func(i, j int) bool {
		if prepared.Bindings[i].Service == prepared.Bindings[j].Service {
			return prepared.Bindings[i].Variable < prepared.Bindings[j].Variable
		}
		return prepared.Bindings[i].Service < prepared.Bindings[j].Service
	})
	model["services"], _ = json.Marshal(services)
	model["name"], _ = json.Marshal(network.ProjectName)
	model["networks"], _ = json.Marshal(map[string]any{"default": map[string]any{"name": network.ProjectName + "_default", "driver": "bridge", "driver_opts": map[string]string{"com.docker.network.bridge.name": network.BridgeName}}})
	prepared.Config, _ = json.Marshal(model)
	return prepared, nil
}
func nonroot(user string) bool {
	parts := strings.Split(user, ":")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		id, err := strconv.ParseUint(part, 10, 31)
		if err != nil || id == 0 {
			return false
		}
	}
	return true
}
func safeLogging(data []byte) bool {
	var logging struct {
		Driver  string            `json:"driver"`
		Options map[string]string `json:"options"`
	}
	if json.Unmarshal(data, &logging) != nil || logging.Driver != "json-file" || len(logging.Options) != 2 {
		return false
	}
	files, err := strconv.Atoi(logging.Options["max-file"])
	if err != nil || files < 1 || files > 5 {
		return false
	}
	size := logging.Options["max-size"]
	if !strings.HasSuffix(size, "m") {
		return false
	}
	megabytes, err := strconv.Atoi(strings.TrimSuffix(size, "m"))
	return err == nil && megabytes >= 1 && megabytes <= 10
}
func safeHealth(data []byte) (json.RawMessage, bool) {
	var health struct {
		Test          []string `json:"test"`
		Disable       bool     `json:"disable"`
		Interval      string   `json:"interval"`
		Timeout       string   `json:"timeout"`
		Retries       int      `json:"retries"`
		StartInterval string   `json:"start_interval"`
		StartPeriod   string   `json:"start_period"`
	}
	if json.Unmarshal(data, &health) != nil || health.Disable || len(health.Test) < 2 || len(health.Test) > 32 || (health.Test[0] != "CMD" && health.Test[0] != "CMD-SHELL") || health.Retries < 1 || health.Retries > 30 {
		return nil, false
	}
	interval, err := time.ParseDuration(health.Interval)
	if err != nil || interval < time.Second || interval > 5*time.Minute {
		return nil, false
	}
	timeout, err := time.ParseDuration(health.Timeout)
	if err != nil || timeout < time.Second || timeout > 30*time.Second {
		return nil, false
	}
	// Docker fills omitted or zero health timings from the image. Emit explicit
	// positive values so image metadata cannot bypass the admitted bounds.
	if health.StartInterval == "" {
		health.StartInterval = "5s"
	}
	if health.StartPeriod == "" {
		health.StartPeriod = "10s"
	}
	startupInterval, err := time.ParseDuration(health.StartInterval)
	if err != nil || startupInterval < time.Second || startupInterval > 5*time.Minute {
		return nil, false
	}
	startupPeriod, err := time.ParseDuration(health.StartPeriod)
	if err != nil || startupPeriod <= 0 || startupPeriod > 5*time.Minute {
		return nil, false
	}
	encoded, err := json.Marshal(health)
	return encoded, err == nil
}
func safeTmpfs(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	var mounts []string
	if json.Unmarshal(data, &mounts) != nil || len(mounts) > 1 {
		return false
	}
	return len(mounts) == 0 || mounts[0] == "/tmp:size=16777216,noexec,nosuid"
}

func reserved(name string) bool {
	if strings.HasPrefix(name, "COMPOSE_") || strings.HasPrefix(name, "DOCKER_") || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "LD_") {
		return true
	}
	switch name {
	case "PATH", "HOME", "PWD", "OLDPWD", "SHELL", "USER", "TMPDIR", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "COMSPEC", "SYSTEMROOT":
		return true
	}
	return false
}
