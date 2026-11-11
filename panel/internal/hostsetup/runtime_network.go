package hostsetup

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/aleksdotbar/vps/panel/internal/machine"
)

var ErrRuntimeNetwork = errors.New("runtime network does not preserve reviewed machine settings")

func networkFailure(reason string) error { return fmt.Errorf("%w: %s", ErrRuntimeNetwork, reason) }

type networkLink struct {
	Index    int      `json:"ifindex"`
	Name     string   `json:"ifname"`
	Address  string   `json:"address"`
	MTU      int      `json:"mtu"`
	Flags    []string `json:"flags"`
	Master   string   `json:"master"`
	LinkInfo struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
	Addresses   []networkAddress `json:"addr_info"`
	Physical    bool
	Device      string
	DisableIPv6 string
	AcceptRA    string
}

type networkAddress struct {
	Family     string `json:"family"`
	Local      string `json:"local"`
	Prefix     int    `json:"prefixlen"`
	Scope      string `json:"scope"`
	Dynamic    bool   `json:"dynamic"`
	Tentative  bool   `json:"tentative"`
	DADFailed  bool   `json:"dadfailed"`
	Deprecated bool   `json:"deprecated"`
}

type networkRoute struct {
	Destination string   `json:"dst"`
	Gateway     string   `json:"gateway"`
	Device      string   `json:"dev"`
	Protocol    string   `json:"protocol"`
	Scope       string   `json:"scope"`
	Source      string   `json:"prefsrc"`
	Metric      int      `json:"metric"`
	Type        string   `json:"type"`
	Flags       []string `json:"flags"`
	Multipath   []any    `json:"nexthops"`
	Table       any      `json:"table"`
}

type runtimeNetwork struct {
	Links     []networkLink
	Routes    [2][]networkRoute
	Rules     [2][]map[string]any
	Tailscale bool
}

func interfaceDHCP(network machine.Network, item machine.Interface) bool {
	if item.UseDHCP != nil {
		return *item.UseDHCP
	}
	return network.UseDHCP && len(item.IPv4Addresses) == 0
}

// Native source equality proves declared policy. This comparison detects runtime
// drift without treating DHCP leases or kernel connected routes as static intent.
func compareRuntimeNetwork(network machine.Network, observed runtimeNetwork) error {
	declared := make(map[string]machine.Interface, len(network.Interfaces))
	for _, item := range network.Interfaces {
		declared[item.Name] = item
	}
	seen := make(map[string]bool, len(observed.Links))
	for _, link := range observed.Links {
		if link.Name == "" || seen[link.Name] || link.Index <= 0 {
			return networkFailure("interface inventory")
		}
		seen[link.Name] = true
		item, exists := declared[link.Name]
		if !exists {
			if link.Physical {
				for _, address := range link.Addresses {
					if address.Scope == "global" {
						return networkFailure("undeclared physical interface address")
					}
				}
			} else if !knownVirtualLink(link) {
				return networkFailure("unsupported virtual interface")
			}
			continue
		}
		if !link.Physical || link.Device == "" || link.LinkInfo.Kind != "" || link.Master != "" || !slices.Contains(link.Flags, "UP") ||
			(item.MACAddress != "" && !strings.EqualFold(item.MACAddress, link.Address)) || (item.MTU != nil && *item.MTU != link.MTU) {
			return networkFailure("physical interface identity or state")
		}
		if link.AcceptRA != "0" || (network.IPv6Mode == "disabled" && link.DisableIPv6 != "1") || (network.IPv6Mode == "static" && link.DisableIPv6 != "0") {
			return networkFailure("physical IPv6 policy")
		}
		if err := compareInterfaceAddresses(item, interfaceDHCP(network, item), link.Addresses); err != nil {
			return err
		}
	}
	for name := range declared {
		if !seen[name] {
			return networkFailure("missing physical interface")
		}
	}
	dhcp, defaultRoute := false, false
	for _, item := range network.Interfaces {
		dhcp = dhcp || interfaceDHCP(network, item)
	}
	for family, routes := range observed.Routes {
		if err := compareNetworkRoutes(network, declared, observed.Links, routes, family == 1); err != nil {
			return err
		}
		for _, route := range routes {
			if route.Destination == "default" && route.Gateway != "" {
				defaultRoute = true
			}
		}
		if err := compareNetworkRules(observed.Rules[family], observed.Tailscale); err != nil {
			return err
		}
	}
	if dhcp && !defaultRoute {
		return networkFailure("missing usable default route")
	}
	return nil
}

func knownVirtualLink(link networkLink) bool {
	if link.Name == "lo" {
		return true
	}
	switch link.LinkInfo.Kind {
	case "veth":
		return true
	case "bridge":
		return link.Name == "docker0" || strings.HasPrefix(link.Name, "br-") || strings.HasPrefix(link.Name, "vps-")
	case "tun":
		return link.Name == "tailscale0"
	}
	return false
}

func compareInterfaceAddresses(item machine.Interface, dhcp bool, observed []networkAddress) error {
	wanted := make(map[netip.Prefix]bool)
	for _, address := range append(slices.Clone(item.IPv4Addresses), item.IPv6Addresses...) {
		prefix, err := netip.ParsePrefix(address.Address + "/" + strconv.Itoa(address.PrefixLength))
		if err != nil {
			return networkFailure("invalid reviewed address")
		}
		wanted[prefix] = false
	}
	lease := false
	for _, address := range observed {
		prefix, err := netip.ParsePrefix(address.Local + "/" + strconv.Itoa(address.Prefix))
		if err != nil {
			return networkFailure("invalid observed address")
		}
		if address.Tentative || address.DADFailed || address.Deprecated {
			return networkFailure("unusable interface address")
		}
		if dhcp && prefix.Addr().Is4() && address.Dynamic && address.Scope == "global" && !prefix.Addr().IsUnspecified() {
			lease = true
		}
		if _, exists := wanted[prefix]; exists {
			wanted[prefix] = true
			continue
		}
		if prefix.Addr().IsLinkLocalUnicast() {
			continue
		}
		if dhcp && prefix.Addr().Is4() && address.Dynamic && address.Scope == "global" && !prefix.Addr().IsUnspecified() {
			lease = true
			continue
		}
		return networkFailure("undeclared interface address")
	}
	for _, found := range wanted {
		if !found {
			return networkFailure("missing static address")
		}
	}
	// Static and DHCP addresses may intentionally coexist.
	if dhcp && !lease {
		return networkFailure("missing usable DHCP address")
	}
	return nil
}

func routeDestination(value string, ipv6 bool) (netip.Prefix, error) {
	if value == "default" {
		if ipv6 {
			value = "::/0"
		} else {
			value = "0.0.0.0/0"
		}
	}
	if !strings.Contains(value, "/") {
		if ipv6 {
			value += "/128"
		} else {
			value += "/32"
		}
	}
	return netip.ParsePrefix(value)
}

func routeMatches(route networkRoute, destination netip.Prefix, via, scope string, metric *int, onlink bool, source string, ipv6 bool) bool {
	actual, err := routeDestination(route.Destination, ipv6)
	if err != nil || actual.Masked() != destination.Masked() || !sameIP(route.Gateway, via) {
		return false
	}
	actualScope := route.Scope
	if actualScope == "" {
		actualScope = "global"
	}
	return (scope == "" || actualScope == scope) && (metric == nil || route.Metric == *metric) &&
		(!onlink || slices.Contains(route.Flags, "onlink")) && (source == "" || sameIP(source, route.Source))
}

func sameIP(left, right string) bool {
	if left == "" || right == "" {
		return left == right
	}
	a, errA := netip.ParseAddr(left)
	b, errB := netip.ParseAddr(right)
	return errA == nil && errB == nil && a == b
}

func compareNetworkRoutes(network machine.Network, declared map[string]machine.Interface, links []networkLink, observed []networkRoute, ipv6 bool) error {
	matched := make([]bool, len(observed))
	for name, item := range declared {
		wanted := item.IPv4Routes
		if ipv6 {
			wanted = item.IPv6Routes
		}
		for _, route := range wanted {
			prefix, err := routeDestination(route.Address+"/"+strconv.Itoa(route.PrefixLength), ipv6)
			if err != nil {
				return networkFailure("invalid reviewed route")
			}
			found := false
			for i, actual := range observed {
				if actual.Device == name && routeMatches(actual, prefix, route.Via, route.Scope, route.Metric, route.OnLink, "", ipv6) {
					matched[i] = true
					found = true
				}
			}
			if !found {
				return networkFailure("missing static route")
			}
		}
	}
	gateway := network.DefaultGateway
	if ipv6 {
		gateway = network.DefaultGateway6
	}
	if gateway != nil {
		prefix, _ := routeDestination("default", ipv6)
		gatewayPrefix, err := routeDestination(gateway.Address, ipv6)
		if err != nil {
			return networkFailure("invalid reviewed gateway")
		}
		gatewayScope := "link"
		if ipv6 {
			gatewayScope = "global"
		}
		found := false
		for i, actual := range observed {
			if actual.Device == gateway.Interface && routeMatches(actual, prefix, gateway.Address, "", gateway.Metric, false, gateway.Source, ipv6) {
				matched[i] = true
				found = true
			}
			// The native scripted backend installs a direct host route to the
			// reviewed gateway before its default route, including off-link gateways.
			if network.Backend == "dhcpcd" && actual.Device == gateway.Interface && actual.Protocol == "static" &&
				routeMatches(actual, gatewayPrefix, "", gatewayScope, gateway.Metric, false, "", ipv6) {
				matched[i] = true
			}
		}
		if !found {
			return networkFailure("missing default gateway")
		}
	}
	for i, route := range observed {
		if len(route.Multipath) != 0 || (route.Type != "" && route.Type != "unicast") {
			return networkFailure("unsupported route type")
		}
		item, exists := declared[route.Device]
		if !exists {
			if route.Destination == "default" {
				return networkFailure("default route on undeclared interface")
			}
			allowed := false
			for _, link := range links {
				if link.Name == route.Device && ((!link.Physical && knownVirtualLink(link)) || connectedNetworkRoute(route, link, ipv6)) {
					allowed = true
				}
			}
			if !allowed {
				return networkFailure("route on undeclared interface")
			}
			continue
		}
		if matched[i] {
			continue
		}
		connected := false
		for _, link := range links {
			if link.Name == route.Device && connectedNetworkRoute(route, link, ipv6) {
				connected = true
			}
		}
		if connected {
			continue
		}
		prefix, err := routeDestination(route.Destination, ipv6)
		if err != nil {
			return networkFailure("invalid observed route")
		}
		if prefix.Addr().IsLinkLocalUnicast() && route.Gateway == "" {
			continue
		}
		if !ipv6 && interfaceDHCP(network, item) && route.Protocol == "dhcp" {
			continue
		}
		return networkFailure("undeclared physical route")
	}
	return nil
}

func connectedNetworkRoute(route networkRoute, link networkLink, ipv6 bool) bool {
	if route.Protocol != "kernel" || route.Gateway != "" || route.Destination == "default" {
		return false
	}
	prefix, err := routeDestination(route.Destination, ipv6)
	if err != nil {
		return false
	}
	for _, address := range link.Addresses {
		local, err := netip.ParseAddr(address.Local)
		if err == nil && local.Is6() == ipv6 && netip.PrefixFrom(local, address.Prefix).Masked() == prefix.Masked() {
			return true
		}
	}
	return false
}

// Kernel rules are fixed. Tailscale's four rules are admitted only with native
// current-generation daemon ownership and a separately checked table 52.
func compareNetworkRules(rules []map[string]any, tailscale bool) error {
	seen := make(map[int]bool)
	for _, rule := range rules {
		for key := range rule {
			if !slices.Contains([]string{"priority", "src", "table", "protocol", "fwmark", "fwmask", "action"}, key) {
				return networkFailure("unsupported policy routing")
			}
		}
		priority, ok := rule["priority"].(float64)
		if !ok || priority != float64(int(priority)) || seen[int(priority)] || rule["src"] != "all" {
			return networkFailure("unsupported policy routing")
		}
		seen[int(priority)] = true
		table := fmt.Sprint(rule["table"])
		mark, mask := fmt.Sprint(rule["fwmark"]), fmt.Sprint(rule["fwmask"])
		plain := rule["fwmark"] == nil && rule["fwmask"] == nil && rule["action"] == nil
		if plain && ((priority == 0 && table == "local") || (priority == 32766 && table == "main") || (priority == 32767 && table == "default")) {
			continue
		}
		marked := mark == "0x80000" && mask == "0xff0000"
		if tailscale && ((marked && priority == 5210 && table == "main") || (marked && priority == 5230 && table == "default") ||
			(marked && priority == 5250 && rule["action"] == "unreachable" && rule["table"] == nil) || (plain && priority == 5270 && table == "52")) {
			continue
		}
		return networkFailure("unsupported policy routing")
	}
	if !seen[0] || !seen[32766] {
		return networkFailure("missing kernel routing policy")
	}
	if tailscale && (seen[5210] || seen[5230] || seen[5250] || seen[5270]) && (!seen[5210] || !seen[5230] || !seen[5250] || !seen[5270]) {
		return networkFailure("incomplete Tailscale routing policy")
	}
	return nil
}
