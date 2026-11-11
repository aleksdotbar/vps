// Package routes compiles one server's locally managed Tunnel configuration.
// It does not allocate ports, establish provider authority or apply configuration.
package routes

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
)

const (
	MaxConfigBytes  = 1 << 20
	CredentialsFile = "/var/lib/panel-cloudflared/credentials.json" // #nosec G101 -- fixed runtime path only; credential bytes remain external.
	maxEnvironments = 512
	maxDomains      = 32
)

var (
	ErrInvalid   = errors.New("invalid environment route contribution")
	ErrOwnership = errors.New("route hostname or origin ownership conflicts")
	ErrOrigin    = errors.New("route lacks an exact assigned loopback origin")
	ErrCapacity  = errors.New("combined routes exceed supported capacity")
)

// Contribution contains immutable release domains and the selected native
// compose.Prepared.Services. Desired settings are not authority for queued work.
type Contribution struct {
	EnvironmentID string
	Services      []string
	Domains       []*panelv1.Domain
}

// Origin comes from executor-owned port assignments, never application input.
// Port is the container port; LoopbackPort is its controlled host publication.
// Callers supply exactly the assignments used by these active contributions.
type Origin struct {
	EnvironmentID string
	Service       string
	Port          uint32
	LoopbackPort  uint16
}

type originKey struct {
	environment, service string
	port                 uint32
}

type ingressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

type tunnelConfig struct {
	Tunnel          string        `json:"tunnel"`
	CredentialsFile string        `json:"credentials-file"`
	Ingress         []ingressRule `json:"ingress"`
}

// Compile requires independently verified server/Tunnel identity and active
// contribution/assignment state. Its JSON bytes are accepted by native
// cloudflared's YAML reader. Credentials remain external to the generated file.
func Compile(tunnelID string, contributions []Contribution, origins []Origin) ([]byte, error) {
	if !canonicalID(tunnelID) {
		return nil, ErrInvalid
	}
	required, err := checkContributions(contributions)
	if err != nil {
		return nil, err
	}
	if len(origins) > maxEnvironments*maxDomains {
		return nil, ErrCapacity
	}
	if len(origins) != len(required) {
		return nil, ErrOrigin
	}
	assignments := make(map[originKey]uint16, len(origins))
	ports := make(map[uint16]bool, len(origins))
	for _, origin := range origins {
		key := originKey{origin.EnvironmentID, origin.Service, origin.Port}
		if _, exists := required[key]; !exists || origin.LoopbackPort == 0 {
			return nil, ErrOrigin
		}
		if _, duplicate := assignments[key]; duplicate || ports[origin.LoopbackPort] {
			return nil, ErrOwnership
		}
		assignments[key] = origin.LoopbackPort
		ports[origin.LoopbackPort] = true
	}
	config := tunnelConfig{Tunnel: tunnelID, CredentialsFile: CredentialsFile, Ingress: make([]ingressRule, 0, len(required)+1)}
	for key, hosts := range required {
		service := "http://127.0.0.1:" + strconv.Itoa(int(assignments[key]))
		for _, hostname := range hosts {
			config.Ingress = append(config.Ingress, ingressRule{Hostname: hostname, Service: service})
		}
	}
	sort.Slice(config.Ingress, func(i, j int) bool { return config.Ingress[i].Hostname < config.Ingress[j].Hostname })
	config.Ingress = append(config.Ingress, ingressRule{Service: "http_status:404"})
	data, err := json.Marshal(config)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(data) > MaxConfigBytes {
		return nil, ErrCapacity
	}
	return data, nil
}

func checkContributions(contributions []Contribution) (map[originKey][]string, error) {
	if len(contributions) > maxEnvironments {
		return nil, ErrCapacity
	}
	environments := make(map[string]bool, len(contributions))
	hostnames := make(map[string]bool)
	required := make(map[originKey][]string)
	for _, contribution := range contributions {
		if !canonicalID(contribution.EnvironmentID) {
			return nil, ErrInvalid
		}
		if environments[contribution.EnvironmentID] {
			return nil, ErrOwnership
		}
		environments[contribution.EnvironmentID] = true
		if len(contribution.Domains) > maxDomains || len(contribution.Services) > 64 {
			return nil, ErrCapacity
		}
		services := make(map[string]bool, len(contribution.Services))
		for _, service := range contribution.Services {
			if service == "" || len(service) > 128 || services[service] {
				return nil, ErrInvalid
			}
			services[service] = true
		}
		for _, domain := range contribution.Domains {
			if domain == nil || len(domain.GetHostname()) > 253 || len(domain.GetService()) > 128 || protovalidate.Validate(domain) != nil {
				return nil, ErrInvalid
			}
			if !services[domain.GetService()] {
				return nil, ErrInvalid
			}
			if hostnames[domain.GetHostname()] {
				return nil, ErrOwnership
			}
			hostnames[domain.GetHostname()] = true
			key := originKey{contribution.EnvironmentID, domain.GetService(), domain.GetPort()}
			required[key] = append(required[key], domain.GetHostname())
		}
	}
	return required, nil
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
