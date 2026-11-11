package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/routes"
)

var errUnhealthy = errors.New("declared application health failed")

func project(identity Identity) string {
	return "vps-" + strings.ReplaceAll(identity.EnvironmentID, "-", "")
}

func runtimeCompose(value staged, origins []routes.Origin) ([]byte, error) {
	var model map[string]json.RawMessage
	if json.Unmarshal(value.ApplicationConfig, &model) != nil || model == nil {
		return nil, ErrState
	}
	var services map[string]map[string]json.RawMessage
	if json.Unmarshal(model["services"], &services) != nil || services == nil {
		return nil, ErrState
	}
	for name, service := range services {
		if service == nil {
			return nil, ErrState
		}
		labels := map[string]string{}
		if len(service["labels"]) > 0 && json.Unmarshal(service["labels"], &labels) != nil {
			return nil, ErrState
		}
		if labels == nil {
			labels = map[string]string{}
		}
		labels["vps.installation"] = value.Claim.Identity.InstallationID
		labels["vps.environment"] = value.Claim.Identity.EnvironmentID
		labels["vps.release"] = value.Claim.Identity.ReleaseID
		labels["vps.operation"] = value.Claim.Identity.OperationID
		labels["vps.manifest"] = value.Claim.ManifestSHA256
		service["labels"], _ = json.Marshal(labels)
		type port struct {
			Target    uint32 `json:"target"`
			Published string `json:"published"`
			HostIP    string `json:"host_ip"`
			Protocol  string `json:"protocol"`
		}
		ports := []port{}
		for _, origin := range origins {
			if origin.EnvironmentID != value.Claim.Identity.EnvironmentID || services[origin.Service] == nil || origin.Port < 1 || origin.Port > 65535 || origin.LoopbackPort == 0 {
				return nil, ErrState
			}
			if origin.Service == name {
				ports = append(ports, port{origin.Port, strconv.Itoa(int(origin.LoopbackPort)), "127.0.0.1", "tcp"})
			}
		}
		if len(ports) > 0 {
			service["ports"], _ = json.Marshal(ports)
		}
	}
	encoded, err := json.Marshal(services)
	defer clear(encoded)
	if err != nil {
		return nil, ErrState
	}
	model["services"] = encoded
	result, err := json.Marshal(model)
	if err != nil || len(result) > MaxApplicationBytes {
		clear(result)
		return nil, ErrInvalid
	}
	return result, nil
}

// Inspect only safe runtime metadata. Native inspect's full Config includes
// plaintext environment values and must never be captured as diagnostic output.
const containerTemplate = `{"id":{{json .Id}},"image":{{json .Image}},"reference":{{json .Config.Image}},"labels":{"com.docker.compose.service":{{json (index .Config.Labels "com.docker.compose.service")}},"com.docker.compose.project":{{json (index .Config.Labels "com.docker.compose.project")}},"com.docker.compose.oneoff":{{json (index .Config.Labels "com.docker.compose.oneoff")}},"vps.installation":{{json (index .Config.Labels "vps.installation")}},"vps.environment":{{json (index .Config.Labels "vps.environment")}},"vps.release":{{json (index .Config.Labels "vps.release")}},"vps.operation":{{json (index .Config.Labels "vps.operation")}},"vps.manifest":{{json (index .Config.Labels "vps.manifest")}}},"state":{{json .State.Status}},"health":{{if (index .State "Health")}}{{json (index (index .State "Health") "Status")}}{{else}}""{{end}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"privileged":{{json .HostConfig.Privileged}},"user":{{json .Config.User}},"mounts":{{json (index . "Mounts")}},"tmpfs":{{json (index .HostConfig "Tmpfs")}},"configured_ports":{{json (index .HostConfig "PortBindings")}},"ports":{{json (index .NetworkSettings "Ports")}},"networks":{{json .NetworkSettings.Networks}}}`

type containerObservation struct {
	ID         string            `json:"id"`
	Image      string            `json:"image"`
	Reference  string            `json:"reference"`
	Labels     map[string]string `json:"labels"`
	State      string            `json:"state"`
	Health     string            `json:"health"`
	ReadOnly   bool              `json:"readonly"`
	Privileged bool              `json:"privileged"`
	User       string            `json:"user"`
	Mounts     []struct {
		Type        string
		Destination string
	} `json:"mounts"`
	Tmpfs           map[string]string          `json:"tmpfs"`
	ConfiguredPorts map[string][]portBinding   `json:"configured_ports"`
	Ports           map[string][]portBinding   `json:"ports"`
	Networks        map[string]json.RawMessage `json:"networks"`
}

type portBinding struct {
	HostIP   string
	HostPort string
}

type containerPolicy struct {
	User  string   `json:"user"`
	Tmpfs []string `json:"tmpfs"`
}

func containerPolicies(config []byte) (map[string]containerPolicy, error) {
	var model struct {
		Services map[string]containerPolicy `json:"services"`
	}
	if json.Unmarshal(config, &model) != nil || model.Services == nil {
		return nil, ErrState
	}
	return model.Services, nil
}

func validateContainer(observed containerObservation, material releaseMaterial, policy containerPolicy) (string, error) {
	identity := material.Stage.Claim.Identity
	service := observed.Labels["com.docker.compose.service"]
	artifact, exists := material.Stage.Manifest.Artifact.Services[service]
	if !exists || !validHash(observed.ID) || observed.Labels["com.docker.compose.project"] != project(identity) || observed.Labels["vps.installation"] != identity.InstallationID || observed.Labels["vps.environment"] != identity.EnvironmentID || observed.Labels["vps.release"] != identity.ReleaseID || observed.Labels["vps.operation"] != identity.OperationID || observed.Labels["vps.manifest"] != material.Stage.Claim.ManifestSHA256 || observed.Labels["com.docker.compose.oneoff"] != "False" || observed.Image != material.ImageIDs[service] || observed.Reference != artifact.Image || !observed.ReadOnly || observed.Privileged || len(observed.Networks) != 1 || len(observed.Networks[project(identity)+"_default"]) == 0 {
		return "", ErrState
	}
	if observed.User != policy.User || len(observed.Tmpfs) != len(policy.Tmpfs) {
		return "", ErrState
	}
	for _, entry := range policy.Tmpfs {
		target, options, found := strings.Cut(entry, ":")
		if !found || observed.Tmpfs[target] != options {
			return "", ErrState
		}
	}
	for _, mount := range observed.Mounts {
		if _, declared := observed.Tmpfs[mount.Destination]; mount.Type != "tmpfs" || !declared {
			return "", ErrState
		}
	}
	if !containerPortsMatch(observed.ConfiguredPorts, material.Origins, service) {
		return "", ErrState
	}
	return service, nil
}

func containerPortsMatch(actual map[string][]portBinding, origins []routes.Origin, service string) bool {
	expected := map[string]string{}
	for _, origin := range origins {
		if origin.Service == service {
			expected[strconv.Itoa(int(origin.Port))+"/tcp"] = strconv.Itoa(int(origin.LoopbackPort))
		}
	}
	found := 0
	for target, bindings := range actual {
		if len(bindings) == 0 {
			continue
		}
		if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || expected[target] != bindings[0].HostPort {
			return false
		}
		found++
	}
	return found == len(expected)
}

func containerIDs(ctx context.Context, tools runnerTools, identity Identity) ([]string, error) {
	data, err := dockerCommand(ctx, tools, "/var/empty", "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project="+project(identity))
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(data))
	if len(ids) > 128 {
		return nil, ErrState
	}
	for _, id := range ids {
		if !validHash(id) {
			return nil, ErrState
		}
	}
	return ids, nil
}

func observeContainers(ctx context.Context, tools runnerTools, material releaseMaterial, requireHealthy bool) ([]ServiceObservation, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ids, err := containerIDs(bounded, tools, material.Stage.Claim.Identity)
	if err != nil {
		return nil, err
	}
	if len(ids) != len(material.Stage.Manifest.Artifact.Services) {
		return nil, ErrState
	}
	policies, err := containerPolicies(material.Stage.Manifest.Config)
	if err != nil {
		return nil, err
	}
	services := make(map[string]bool, len(ids))
	result := make([]ServiceObservation, 0, len(ids))
	unhealthy := false
	for _, id := range ids {
		data, err := dockerCommand(bounded, tools, "/var/empty", "container", "inspect", "--format", containerTemplate, "--", id)
		if err != nil {
			return nil, err
		}
		var observed containerObservation
		if json.Unmarshal(data, &observed) != nil {
			return nil, ErrState
		}
		service, err := validateContainer(observed, material, policies[observed.Labels["com.docker.compose.service"]])
		if err != nil || services[service] {
			return nil, ErrState
		}
		services[service] = true
		if observed.State != "running" || observed.Health != "healthy" || !containerPortsMatch(observed.Ports, material.Origins, service) {
			unhealthy = true
		}
		result = append(result, ServiceObservation{Service: service, Image: observed.Reference, ContainerID: observed.ID, State: observed.State, Health: observed.Health})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Service < result[j].Service })
	if requireHealthy && unhealthy {
		return result, errUnhealthy
	}
	return result, nil
}

func awaitHealth(ctx context.Context, tools runnerTools, material releaseMaterial) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		services, err := observeContainers(ctx, tools, material, true)
		if errors.Is(err, errUnhealthy) {
			for _, service := range services {
				if service.Health == "unhealthy" || service.State == "exited" || service.State == "dead" {
					return errUnhealthy
				}
			}
		}
		if err == nil && awaitOrigins(ctx, material) == nil {
			return nil
		}
		// A changing container set while Compose settles can be transient. No
		// mutation is retried; unknown ownership eventually remains needs-attention.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func awaitOrigins(ctx context.Context, material releaseMaterial) error {
	client := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for _, origin := range material.Origins {
		for _, domain := range material.Stage.Manifest.Domains {
			if domain.Service != origin.Service || domain.Port != origin.Port {
				continue
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(int(origin.LoopbackPort))+"/", nil)
			if err != nil {
				return ErrState
			}
			request.Host = domain.Hostname
			response, err := client.Do(request)
			if err != nil {
				return errUnhealthy
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16384))
			_ = response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 500 {
				return errUnhealthy
			}
		}
	}
	return nil
}

func verifyProjectOwnership(ctx context.Context, tools runnerTools, candidate releaseMaterial, previous *releaseMaterial) error {
	policies, err := containerPolicies(candidate.Stage.Manifest.Config)
	if err != nil {
		return err
	}
	var previousPolicies map[string]containerPolicy
	if previous != nil {
		previousPolicies, err = containerPolicies(previous.Stage.Manifest.Config)
		if err != nil {
			return err
		}
	}

	ids, err := containerIDs(ctx, tools, candidate.Stage.Claim.Identity)
	if err != nil {
		return err
	}
	for _, id := range ids {
		data, err := dockerCommand(ctx, tools, "/var/empty", "container", "inspect", "--format", containerTemplate, "--", id)
		if err != nil {
			return err
		}
		var observed containerObservation
		if json.Unmarshal(data, &observed) != nil {
			return ErrState
		}
		if _, err := validateContainer(observed, candidate, policies[observed.Labels["com.docker.compose.service"]]); err == nil {
			continue
		}
		if previous != nil {
			if _, err := validateContainer(observed, *previous, previousPolicies[observed.Labels["com.docker.compose.service"]]); err == nil {
				continue
			}
		}
		return ErrState
	}
	return verifyProjectNetwork(ctx, tools, candidate.Stage.Manifest)
}

func verifyProjectNetwork(ctx context.Context, tools runnerTools, manifest Manifest) error {
	name := project(manifest.Identity) + "_default"
	data, err := dockerCommand(ctx, tools, "/var/empty", "network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	if strings.TrimSpace(string(data)) != name {
		return ErrState
	}
	data, err = dockerCommand(ctx, tools, "/var/empty", "network", "inspect", "--format", `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"internal":{{json .Internal}},"ipv6":{{json .EnableIPv6}},"options":{{json .Options}},"labels":{{json .Labels}}}`, "--", name)
	if err != nil {
		return err
	}
	var network struct {
		Name, Driver, Scope string
		Internal            bool
		IPv6                bool
		Options, Labels     map[string]string
	}
	if json.Unmarshal(data, &network) != nil || network.Name != name || network.Driver != "bridge" || network.Scope != "local" || network.Internal || network.IPv6 || network.Options["com.docker.network.bridge.name"] != manifest.BridgeName || network.Labels["com.docker.compose.project"] != project(manifest.Identity) {
		return ErrState
	}
	return nil
}
