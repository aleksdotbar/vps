package hostsetup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/machine"
)

var ErrDiscovery = errors.New("host discovery found an unsupported existing configuration")

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type discoveredHost struct {
	Hostinger   bool                 `json:"hostinger"`
	Unsupported []string             `json:"unsupported"`
	Machine     json.RawMessage      `json:"machine"`
	Bootstrap   foundation.Bootstrap `json:"bootstrap"`
}

// discoverInput derives the reviewed input from the running host's own NixOS
// configuration. Nothing is written; the caller confirms before persisting it.
func discoverInput(ctx context.Context, source string, operator foundation.Operator, tools inspectionTools) (foundation.Input, error) {
	command := []string{"/run/current-system/sw/bin/env", "-i", "HOME=/var/empty", "LANG=C", "LC_ALL=C", "NIX_CONF_DIR=/var/empty", "NIX_USER_CONF_FILES=", "NIX_CONFIG=", "NIX_PATH=",
		"XDG_CONFIG_HOME=/var/empty", "XDG_CACHE_HOME=/var/empty",
		"/run/current-system/sw/bin/nix", "--extra-experimental-features", "nix-command", "eval", "--store", "dummy://", "--read-only", "--offline", "--impure", "--json", "--max-jobs", "0", "--cores", "1",
		"--option", "allow-import-from-derivation", "false", "--option", "allow-unsafe-native-code-during-evaluation", "false", "--option", "substituters", "", "--option", "builders", "",
		"--file", source + "/nix/installer/discover-host.nix", "host"}
	data, err := runSourceInspection(ctx, tools, "discover-"+uuid.NewString(), command)
	if err != nil {
		return foundation.Input{}, err
	}
	var host discoveredHost
	if json.Unmarshal(data, &host) != nil {
		return foundation.Input{}, ErrDiscovery
	}
	if len(host.Unsupported) != 0 {
		return foundation.Input{}, fmt.Errorf("%w: %s", ErrDiscovery, strings.Join(host.Unsupported, ", "))
	}
	var config machine.Config
	if json.Unmarshal(host.Machine, &config) != nil {
		return foundation.Input{}, ErrDiscovery
	}
	if host.Hostinger {
		hostname, err := os.ReadFile("/proc/sys/kernel/hostname")
		if err != nil {
			return foundation.Input{}, ErrDiscovery
		}
		files, err := filepath.Glob("/etc/systemd/network/10-cloud-init-*.network")
		if err != nil || len(files) != 1 {
			return foundation.Input{}, fmt.Errorf("%w: expected exactly one cloud-init network file", ErrDiscovery)
		}
		// #nosec G304 -- The single path matches a fixed root-owned cloud-init location.
		rendered, err := os.ReadFile(files[0])
		if err != nil {
			return foundation.Input{}, ErrDiscovery
		}
		config.Network, err = parseCloudInitNetwork(rendered, strings.TrimSpace(string(hostname)))
		if err != nil {
			return foundation.Input{}, err
		}
	}
	return newInput(config, operator, host.Bootstrap)
}

func newInput(config machine.Config, operator foundation.Operator, bootstrap foundation.Bootstrap) (foundation.Input, error) {
	value := foundation.Input{SchemaVersion: 1, InstallationID: uuid.NewString(), ServerID: uuid.NewString(), Role: foundation.Panel,
		Operator: operator, Bootstrap: bootstrap, Machine: config}
	// The strict parser owns admission, including machine and operator rules.
	data, err := json.Marshal(value)
	if err != nil {
		return foundation.Input{}, ErrDiscovery
	}
	input, err := foundation.Parse(data)
	if err != nil {
		return foundation.Input{}, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	return input, nil
}

// parseCloudInitNetwork reads the single static networkd file cloud-init renders
// on Hostinger's image. Any setting outside that shape is refused, not dropped.
func parseCloudInitNetwork(data []byte, hostName string) (machine.Network, error) {
	invalid := func(reason string) (machine.Network, error) {
		return machine.Network{}, fmt.Errorf("%w: cloud-init network %s", ErrDiscovery, reason)
	}
	if !hostLabel.MatchString(hostName) {
		return invalid("needs a lowercase single-label hostname")
	}
	item := machine.Interface{}
	network := machine.Network{Backend: "networkd", HostName: hostName, IPv6Mode: "disabled"}
	var gateways []string
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return invalid("syntax")
		}
		switch section + "." + key {
		case "Match.Name":
			item.Name = value
		case "Match.MACAddress":
			item.MACAddress = strings.ToLower(value)
		case "Address.Address":
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return invalid("address")
			}
			address := machine.Address{Address: prefix.Addr().String(), PrefixLength: prefix.Bits()}
			if prefix.Addr().Is6() {
				item.IPv6Addresses = append(item.IPv6Addresses, address)
				network.IPv6Mode = "static"
			} else {
				item.IPv4Addresses = append(item.IPv4Addresses, address)
			}
		case "Network.DHCP":
			if value != "no" {
				return invalid("uses DHCP")
			}
		case "Network.DNS":
			network.Nameservers = append(network.Nameservers, strings.Fields(value)...)
		case "Network.Domains":
			for _, domain := range strings.Fields(value) {
				// systemd-resolved never searches "localhost", which Hostinger's cloud-init emits.
				if domain != "localhost" {
					network.Search = append(network.Search, domain)
				}
			}
		case "Route.Gateway":
			gateways = append(gateways, value)
		default:
			return invalid("setting " + section + "." + key)
		}
	}
	if scanner.Err() != nil || item.Name == "" {
		return invalid("interface")
	}
	for _, value := range gateways {
		address, err := netip.ParseAddr(value)
		if err != nil {
			return invalid("gateway")
		}
		gateway := &machine.Gateway{Address: address.String(), Interface: item.Name}
		if address.Is6() && network.DefaultGateway6 == nil {
			network.DefaultGateway6 = gateway
		} else if address.Is4() && network.DefaultGateway == nil {
			network.DefaultGateway = gateway
		} else {
			return invalid("gateway")
		}
	}
	network.Interfaces = []machine.Interface{item}
	return network, nil
}
