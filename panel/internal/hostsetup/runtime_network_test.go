package hostsetup

import (
	"errors"
	"strings"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/machine"
)

func networkFixture() (machine.Network, runtimeNetwork) {
	metric := 100
	network := machine.Network{Backend: "dhcpcd", IPv6Mode: "disabled", Interfaces: []machine.Interface{{Name: "eth0", IPv4Addresses: []machine.Address{{Address: "192.0.2.10", PrefixLength: 24}}}},
		DefaultGateway: &machine.Gateway{Address: "192.0.2.1", Interface: "eth0", Metric: &metric}}
	observed := runtimeNetwork{Links: []networkLink{{Name: "eth0", Index: 2, Address: "02:00:00:00:00:01", Flags: []string{"UP"}, Physical: true, Device: "/sys/devices/pci0000:00/0000:00:01.0", DisableIPv6: "1", AcceptRA: "0",
		Addresses: []networkAddress{{Family: "inet", Local: "192.0.2.10", Prefix: 24, Scope: "global"}}}}}
	observed.Routes[0] = []networkRoute{{Destination: "192.0.2.0/24", Device: "eth0", Protocol: "kernel", Scope: "link"}, {Destination: "default", Gateway: "192.0.2.1", Device: "eth0", Metric: 100}}
	for family := range observed.Rules {
		observed.Rules[family] = []map[string]any{{"priority": float64(0), "src": "all", "table": "local"}, {"priority": float64(32766), "src": "all", "table": "main"}}
	}
	return network, observed
}

func TestRuntimeNetworkRejectsMeaningfulDrift(t *testing.T) {
	cases := []struct {
		name   string
		change func(*machine.Network, *runtimeNetwork)
		reason string
	}{
		{"static address removed", func(_ *machine.Network, runtime *runtimeNetwork) { runtime.Links[0].Addresses = nil }, "missing static address"},
		{"manual address added", func(_ *machine.Network, runtime *runtimeNetwork) {
			runtime.Links[0].Addresses = append(runtime.Links[0].Addresses, networkAddress{Local: "198.51.100.10", Prefix: 24, Scope: "global"})
		}, "undeclared interface address"},
		{"virtual name collision", func(_ *machine.Network, runtime *runtimeNetwork) { runtime.Links[0].Physical = false }, "physical interface identity"},
		{"IPv6 policy changed", func(_ *machine.Network, runtime *runtimeNetwork) { runtime.Links[0].AcceptRA = "1" }, "IPv6 policy"},
		{"gateway changed", func(_ *machine.Network, runtime *runtimeNetwork) { runtime.Routes[0][1].Gateway = "192.0.2.2" }, "missing default gateway"},
		{"explicit metric changed", func(_ *machine.Network, runtime *runtimeNetwork) { runtime.Routes[0][1].Metric = 101 }, "missing default gateway"},
		{"forged kernel default", func(network *machine.Network, runtime *runtimeNetwork) {
			network.DefaultGateway = nil
			runtime.Routes[0][1].Protocol = "kernel"
		}, "undeclared physical route"},
		{"unrepresented kernel route", func(_ *machine.Network, runtime *runtimeNetwork) {
			runtime.Routes[0] = append(runtime.Routes[0], networkRoute{Destination: "198.51.100.0/24", Device: "eth0", Protocol: "kernel"})
		}, "undeclared physical route"},
		{"unknown policy rule", func(_ *machine.Network, runtime *runtimeNetwork) {
			runtime.Rules[0] = append(runtime.Rules[0], map[string]any{"priority": float64(100), "src": "all", "table": float64(100)})
		}, "unsupported policy routing"},
		{"unreviewed physical default", func(network *machine.Network, runtime *runtimeNetwork) {
			network.DefaultGateway = nil
			runtime.Routes[0][1].Device = "eth1"
		}, "default route on undeclared interface"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			network, runtime := networkFixture()
			item.change(&network, &runtime)
			err := compareRuntimeNetwork(network, runtime)
			if !errors.Is(err, ErrRuntimeNetwork) || !strings.Contains(err.Error(), item.reason) {
				t.Fatalf("%s: got %v, want %s", item.name, err, item.reason)
			}
		})
	}
}

func TestRuntimeNetworkPreservesDHCPWithoutFreezingLease(t *testing.T) {
	for _, address := range []string{"192.0.2.10", "198.51.100.20"} {
		network, runtime := networkFixture()
		network.UseDHCP = true
		network.Interfaces[0].IPv4Addresses = nil
		network.DefaultGateway = nil
		runtime.Links[0].Addresses = []networkAddress{{Local: address, Prefix: 24, Scope: "global", Dynamic: true}}
		runtime.Routes[0] = []networkRoute{{Destination: "default", Device: "eth0", Gateway: "192.0.2.1", Protocol: "dhcp"}}
		// A second DHCP interface may receive an address without its own default.
		network.Interfaces = append(network.Interfaces, machine.Interface{Name: "eth1"})
		runtime.Links = append(runtime.Links, networkLink{Name: "eth1", Index: 3, Physical: true, Device: "/sys/devices/pci0000:00/0000:00:02.0", Flags: []string{"UP"}, DisableIPv6: "1", AcceptRA: "0", Addresses: []networkAddress{{Local: "203.0.113.10", Prefix: 24, Scope: "global", Dynamic: true}}})
		if err := compareRuntimeNetwork(network, runtime); err != nil {
			t.Fatalf("lease %s: %v", address, err)
		}
		runtime.Routes[0] = nil
		if err := compareRuntimeNetwork(network, runtime); err == nil || !strings.Contains(err.Error(), "missing usable default") {
			t.Fatalf("lease %s without default: %v", address, err)
		}
	}
}

func TestRuntimeNetworkIgnoresKnownVirtualConnectedNetworks(t *testing.T) {
	network, runtime := networkFixture()
	bridge := networkLink{Name: "vps-0000000001", Index: 5}
	bridge.LinkInfo.Kind = "bridge"
	runtime.Links = append(runtime.Links, bridge)
	runtime.Routes[0] = append(runtime.Routes[0], networkRoute{Destination: "172.18.0.0/16", Device: bridge.Name, Protocol: "kernel"})
	if err := compareRuntimeNetwork(network, runtime); err != nil {
		t.Fatal(err)
	}
	mtu := 1500
	network.Interfaces[0].MTU = &mtu
	runtime.Links[0].MTU = 1400
	if err := compareRuntimeNetwork(network, runtime); err == nil {
		t.Fatal("changed declared MTU accepted")
	}
}

func TestRuntimeNetworkStaticRouteIntent(t *testing.T) {
	network, runtime := networkFixture()
	metric := 50
	network.Interfaces[0].IPv4Routes = []machine.Route{{Address: "198.51.100.0", PrefixLength: 24, Via: "192.0.2.2", Scope: "global", Metric: &metric, OnLink: true}}
	runtime.Routes[0] = append(runtime.Routes[0], networkRoute{Destination: "198.51.100.0/24", Device: "eth0", Gateway: "192.0.2.2", Metric: 50, Flags: []string{"onlink"}})
	if err := compareRuntimeNetwork(network, runtime); err != nil {
		t.Fatal(err)
	}
	runtime.Routes[0][2].Flags = nil
	if err := compareRuntimeNetwork(network, runtime); err == nil {
		t.Fatal("missing declared onlink flag accepted")
	}
}

func TestRuntimeNetworkPreservesNativeScriptedGatewayRoutes(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		network, runtime := networkFixture()
		family, gateway, scope := 0, "192.0.2.1", "link"
		if ipv6 {
			family, gateway, scope = 1, "2001:db8::1", "global"
			network.IPv6Mode = "static"
			network.Interfaces[0].IPv6Addresses = []machine.Address{{Address: "2001:db8::10", PrefixLength: 64}}
			network.DefaultGateway6 = &machine.Gateway{Address: gateway, Interface: "eth0", Metric: network.DefaultGateway.Metric}
			runtime.Links[0].DisableIPv6 = "0"
			runtime.Links[0].Addresses = append(runtime.Links[0].Addresses, networkAddress{Family: "inet6", Local: "2001:db8::10", Prefix: 64, Scope: "global"})
			runtime.Routes[family] = []networkRoute{{Destination: "default", Device: "eth0", Gateway: gateway, Metric: 100}}
		}
		runtime.Routes[family] = append(runtime.Routes[family], networkRoute{Destination: gateway, Device: "eth0", Protocol: "static", Scope: scope, Metric: 100})
		if err := compareRuntimeNetwork(network, runtime); err != nil {
			t.Fatalf("IPv6=%t native gateway reachability: %v", ipv6, err)
		}
		runtime.Routes[family][len(runtime.Routes[family])-1].Metric = 101
		if err := compareRuntimeNetwork(network, runtime); err == nil {
			t.Fatalf("IPv6=%t changed gateway reachability metric accepted", ipv6)
		}
	}
}

func TestHostingerGenerationShadowsCloudInitNetworkFile(t *testing.T) {
	if got := nativeNetworkFile(machine.Config{Template: machine.HostingerKVM}, "eth0"); got != "05-eth0.network" || got >= "10-cloud-init-eth0.network" {
		t.Fatalf("Hostinger network file %q does not sort before cloud-init's", got)
	}
	if got := nativeNetworkFile(machine.Config{}, "ens3"); got != "40-ens3.network" {
		t.Fatalf("generic network file %q", got)
	}
}
