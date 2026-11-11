package hostsetup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/machine"
)

type networkTools struct{ ip, systemctl, networkctl, resolvectl string }

// Legacy inspection runs on the unmodified host before the managed generation.
func inspectRuntimeNetwork(ctx context.Context, enrollment, systemPath string, config machine.Config, legacy bool) error {
	network := config.Network
	bin := enrollment + "/sw/bin/"
	tools := networkTools{bin + "ip", bin + "systemctl", bin + "networkctl", bin + "resolvectl"}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observed, err := observeRuntimeNetwork(ctx, tools)
	if err != nil {
		return err
	}
	if err := compareRuntimeNetwork(network, observed); err != nil {
		return err
	}
	if err := observeNetworkOwners(ctx, tools, systemPath, config, legacy, observed); err != nil {
		return err
	}
	return observeNetworkResolver(ctx, tools, systemPath, network, observed.Tailscale)
}

func networkJSON(ctx context.Context, command string, value any, arguments ...string) error {
	data, err := runtimeProbe(ctx, command, arguments...)
	if err != nil || len(data) > 256<<10 || json.Unmarshal(data, value) != nil {
		return networkFailure("native network observation unavailable")
	}
	return nil
}

func networkRead(name string, maximum int64) ([]byte, error) {
	// #nosec G304 -- Private callers read only /etc/resolv.conf and fixed /proc/sys/net/ipv6/conf public state; interface names must be single path components.
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, networkFailure("network state unavailable")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, networkFailure("unsupported network state file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, networkFailure("network state limit")
	}
	return data, nil
}

func observeRuntimeNetwork(ctx context.Context, tools networkTools) (runtimeNetwork, error) {
	var result runtimeNetwork
	if err := networkJSON(ctx, tools.ip, &result.Links, "-details", "-json", "address", "show"); err != nil {
		return result, err
	}
	if len(result.Links) == 0 || len(result.Links) > 128 {
		return result, networkFailure("interface inventory limit")
	}
	count := 0
	for i := range result.Links {
		link := &result.Links[i]
		// The sysfs device symlink distinguishes physical/virtio hardware from
		// software bridges, veth pairs and TUN links; names alone prove nothing.
		if filepath.Base(link.Name) != link.Name || link.Name == "." || link.Name == ".." {
			return result, networkFailure("invalid observed interface")
		}
		device, err := filepath.EvalSymlinks("/sys/class/net/" + link.Name + "/device")
		if err == nil {
			if !strings.HasPrefix(device, "/sys/devices/") {
				return result, networkFailure("physical device identity")
			}
			link.Physical, link.Device = true, device
		} else if !os.IsNotExist(err) {
			return result, networkFailure("physical device observation unavailable")
		}
		count += len(link.Addresses)
		if link.Physical {
			for _, setting := range []struct {
				name  string
				value *string
			}{{"disable_ipv6", &link.DisableIPv6}, {"accept_ra", &link.AcceptRA}} {
				data, err := networkRead("/proc/sys/net/ipv6/conf/"+link.Name+"/"+setting.name, 32)
				if err != nil {
					return result, err
				}
				*setting.value = strings.TrimSpace(string(data))
			}
		}
	}
	if count > 512 {
		return result, networkFailure("address inventory limit")
	}
	for family, flag := range []string{"-4", "-6"} {
		if err := networkJSON(ctx, tools.ip, &result.Routes[family], "-json", flag, "route", "show", "table", "main"); err != nil {
			return result, err
		}
		if err := networkJSON(ctx, tools.ip, &result.Rules[family], "-json", flag, "rule", "show"); err != nil {
			return result, err
		}
		if len(result.Routes[family]) > 512 || len(result.Rules[family]) > 64 {
			return result, networkFailure("routing inventory limit")
		}
	}
	global, err := networkRead("/proc/sys/net/ipv6/conf/all/disable_ipv6", 32)
	if err != nil || strings.TrimSpace(string(global)) != "0" {
		return result, networkFailure("global IPv6 disablement")
	}
	for _, link := range result.Links {
		if link.Name == "tailscale0" && link.LinkInfo.Kind == "tun" {
			// Daemon ownership is checked before admission below.
			result.Tailscale = true
		}
	}
	return result, nil
}

type networkUnit struct{ active, load, fragment, dropins, reload string }

func observeNetworkUnit(ctx context.Context, tools networkTools, unit string) (networkUnit, error) {
	data, err := runtimeProbe(ctx, tools.systemctl, "show", "--no-pager", "--property=ActiveState,LoadState,FragmentPath,DropInPaths,NeedDaemonReload", "--", unit)
	if err != nil || len(data) > 16<<10 {
		return networkUnit{}, networkFailure("native network owner unavailable")
	}
	fields := make(map[string]string)
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if _, exists := fields[key]; exists {
			return networkUnit{}, networkFailure("native network owner state")
		}
		fields[key] = value
	}
	return networkUnit{fields["ActiveState"], fields["LoadState"], fields["FragmentPath"], fields["DropInPaths"], fields["NeedDaemonReload"]}, nil
}

func nativeNetworkUnit(unit networkUnit, systemPath, name string) bool {
	return unit.active == "active" && unit.load == "loaded" && unit.reload == "no" &&
		sameNativeFile(unit.fragment, systemPath+"/etc/systemd/system/"+name) &&
		nativeUnitDropins(strings.Fields(unit.dropins), systemPath+"/etc/systemd/system/"+name+".d")
}

func nativeUnitDropins(actual []string, directory string) bool {
	// #nosec G304 -- Private callers derive this read-only directory from the verified current generation's etc/systemd unit or network paths.
	file, err := os.Open(directory)
	if os.IsNotExist(err) {
		return len(actual) == 0
	}
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(33)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 32 {
		return false
	}
	slices.SortFunc(entries, func(left, right os.DirEntry) int { return strings.Compare(left.Name(), right.Name()) })
	var expected []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".conf") {
			expected = append(expected, filepath.Join(directory, entry.Name()))
		}
	}
	if len(expected) != len(actual) {
		return false
	}
	for i, name := range expected {
		if !sameNativeFile(actual[i], name) {
			return false
		}
	}
	return true
}

func sameNativeFile(actual, expected string) bool {
	if actual == "" {
		return false
	}
	left, err := os.Stat(actual)
	if err != nil || left == nil || !left.Mode().IsRegular() {
		return false
	}
	right, err := os.Stat(expected)
	if err != nil || right == nil {
		return false
	}
	return right.Mode().IsRegular() && os.SameFile(left, right)
}

type networkdLink struct {
	Name                string
	Index               int
	AdministrativeState string
	NetworkFile         string
	NetworkFileDropins  []string
	DHCPv4Client        *struct{ Lease json.RawMessage }
}

// hostingerNetworkFile is the address file cloud-init renders on Hostinger's image.
// It stays on disk for rollback; managed generations shadow it with 05-<name>.network.
func hostingerNetworkFile(name string) string {
	return "/etc/systemd/network/10-cloud-init-" + name + ".network"
}

// nativeNetworkFile names the generation's own unit for an interface: Hostinger's
// must sort before cloud-init's retained 10- file, generic hosts keep NixOS's 40-.
func nativeNetworkFile(config machine.Config, name string) string {
	if config.Template == machine.HostingerKVM {
		return "05-" + name + ".network"
	}
	return "40-" + name + ".network"
}

func cloudInitNetworkFile(actual, name string, dropins []string) bool {
	info, err := os.Lstat(actual)
	return actual == hostingerNetworkFile(name) && len(dropins) == 0 && err == nil && info.Mode().IsRegular()
}

func observeNetworkOwners(ctx context.Context, tools networkTools, systemPath string, config machine.Config, legacy bool, observed runtimeNetwork) error {
	network := config.Network
	hostinger := config.Template == machine.HostingerKVM
	units := make(map[string]networkUnit)
	for _, name := range []string{"systemd-networkd.service", "dhcpcd.service", "NetworkManager.service", "cloud-init-local.service", "cloud-init.service"} {
		unit, err := observeNetworkUnit(ctx, tools, name)
		if err != nil {
			return err
		}
		units[name] = unit
	}
	for _, name := range []string{"NetworkManager.service", "cloud-init-local.service", "cloud-init.service"} {
		// Hostinger's provisioning keeps cloud-init; network files below prove who owns addresses.
		if units[name].active != "inactive" && !(hostinger && strings.HasPrefix(name, "cloud-init")) {
			return networkFailure("competing network owner")
		}
	}
	if network.Backend == "networkd" {
		if !nativeNetworkUnit(units["systemd-networkd.service"], systemPath, "systemd-networkd.service") || units["dhcpcd.service"].active != "inactive" {
			return networkFailure("networkd ownership")
		}
		for _, item := range network.Interfaces {
			var link networkdLink
			if err := networkJSON(ctx, tools.networkctl, &link, "--json=short", "--no-pager", "status", item.Name); err != nil {
				return err
			}
			file := systemPath + "/etc/systemd/network/" + nativeNetworkFile(config, item.Name)
			native := sameNativeFile(link.NetworkFile, file) && nativeUnitDropins(link.NetworkFileDropins, file+".d")
			if hostinger && legacy {
				native = cloudInitNetworkFile(link.NetworkFile, item.Name, link.NetworkFileDropins)
			}
			if link.Name != item.Name || link.AdministrativeState != "configured" || !native {
				return networkFailure("networkd interface configuration")
			}
			for _, actual := range observed.Links {
				if actual.Name == item.Name && actual.Index != link.Index {
					return networkFailure("interface identity changed")
				}
			}
			if interfaceDHCP(network, item) && (link.DHCPv4Client == nil || len(link.DHCPv4Client.Lease) < 3 || string(link.DHCPv4Client.Lease) == "null") {
				return networkFailure("networkd DHCP lease unavailable")
			}
		}
	} else {
		if units["systemd-networkd.service"].active != "inactive" {
			return networkFailure("competing networkd owner")
		}
		dhcpcdEnabled := network.UseDHCP
		for _, item := range network.Interfaces {
			dhcpcdEnabled = dhcpcdEnabled || (item.UseDHCP != nil && *item.UseDHCP)
		}
		if dhcpcdEnabled && !nativeNetworkUnit(units["dhcpcd.service"], systemPath, "dhcpcd.service") {
			return networkFailure("dhcpcd ownership")
		}
		if !dhcpcdEnabled && units["dhcpcd.service"].active != "inactive" {
			return networkFailure("undeclared DHCP owner")
		}
		for _, item := range network.Interfaces {
			name := "network-addresses-" + item.Name + ".service"
			unit, err := observeNetworkUnit(ctx, tools, name)
			if err != nil {
				return err
			}
			if !nativeNetworkUnit(unit, systemPath, name) {
				return networkFailure("scripted interface ownership")
			}
		}
	}
	if observed.Tailscale {
		unit, err := observeNetworkUnit(ctx, tools, "tailscaled.service")
		if err != nil {
			return err
		}
		if !nativeNetworkUnit(unit, systemPath, "tailscaled.service") {
			return networkFailure("Tailscale runtime owner")
		}
		for _, family := range []string{"-4", "-6"} {
			var routes []networkRoute
			if err := networkJSON(ctx, tools.ip, &routes, "-json", family, "route", "show", "table", "52"); err != nil {
				return err
			}
			if len(routes) > 512 {
				return networkFailure("Tailscale route limit")
			}
			for _, route := range routes {
				if route.Device != "tailscale0" || len(route.Multipath) != 0 || (route.Type != "" && route.Type != "unicast") {
					return networkFailure("unsupported Tailscale route")
				}
			}
		}
	}
	return nil
}

type networkResolver struct {
	Interface string `json:"ifname"`
	Servers   []struct {
		Address string `json:"addressString"`
	} `json:"servers"`
	Search []struct {
		Name      string `json:"name"`
		RouteOnly bool   `json:"routeOnly"`
	} `json:"searchDomains"`
}

func observeNetworkResolver(ctx context.Context, tools networkTools, systemPath string, network machine.Network, tailscale bool) error {
	data, err := networkRead("/etc/resolv.conf", 64<<10)
	if err != nil {
		return err
	}
	servers, search, err := resolverContents(data)
	if err != nil {
		return err
	}
	resolved := network.Backend == "networkd" || slices.Contains(servers, "127.0.0.53") || slices.Contains(servers, "127.0.0.54")
	if resolved {
		unit, err := observeNetworkUnit(ctx, tools, "systemd-resolved.service")
		if err != nil {
			return err
		}
		if !nativeNetworkUnit(unit, systemPath, "systemd-resolved.service") {
			return networkFailure("resolver ownership")
		}
		var configuration []networkResolver
		if err := networkJSON(ctx, tools.resolvectl, &configuration, "--json=short", "--no-pager", "status"); err != nil {
			return err
		}
		if len(configuration) > 128 {
			return networkFailure("resolver inventory limit")
		}
		servers = nil
		search = nil
		for _, item := range configuration {
			for _, server := range item.Servers {
				servers = append(servers, server.Address)
			}
			for _, domain := range item.Search {
				if !domain.RouteOnly {
					search = append(search, domain.Name)
				}
			}
		}
	}
	if len(servers) == 0 {
		return networkFailure("no effective nameserver")
	}
	for _, server := range network.Nameservers {
		if !slices.ContainsFunc(servers, func(actual string) bool { return sameIP(server, actual) }) {
			return networkFailure("missing static nameserver")
		}
	}
	for _, domain := range network.Search {
		if !slices.Contains(search, domain) {
			return networkFailure("missing DNS search domain")
		}
	}
	dynamic := tailscale
	for _, item := range network.Interfaces {
		dynamic = dynamic || interfaceDHCP(network, item)
	}
	if !dynamic {
		for _, server := range servers {
			if !slices.ContainsFunc(network.Nameservers, func(expected string) bool { return sameIP(server, expected) }) {
				return networkFailure("undeclared static nameserver")
			}
		}
		for _, domain := range search {
			if !slices.Contains(network.Search, domain) {
				return networkFailure("undeclared DNS search domain")
			}
		}
	}
	return nil
}

func resolverContents(data []byte) ([]string, []string, error) {
	var servers, search []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		line, _, _ = strings.Cut(line, ";")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			if len(fields) != 2 {
				return nil, nil, networkFailure("unsupported resolver state")
			}
			servers = append(servers, fields[1])
		case "search":
			search = fields[1:]
		case "domain":
			if len(fields) != 2 {
				return nil, nil, networkFailure("unsupported resolver state")
			}
			search = fields[1:]
		case "options", "sortlist":
		default:
			return nil, nil, networkFailure("unsupported resolver state")
		}
	}
	if len(servers) > 32 || len(search) > 32 {
		return nil, nil, networkFailure("resolver inventory limit")
	}
	return servers, search, nil
}
