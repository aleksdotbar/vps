// Package machine admits reviewed, nonsecret inputs for an existing NixOS host.
// It does not discover hardware or establish that a target matches those inputs.
package machine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxInputBytes = 64 << 10

var ErrInvalidMachine = errors.New("invalid or unsupported machine input")

type Config struct {
	SchemaVersion int          `json:"schema_version"`
	System        string       `json:"system"`
	StateVersion  string       `json:"state_version"`
	Boot          Boot         `json:"boot"`
	FileSystems   []FileSystem `json:"file_systems"`
	Swap          []Swap       `json:"swap,omitempty"`
	Network       Network      `json:"network"`
	// Template names a fixed provider image whose services the host keeps.
	Template string `json:"template,omitempty"`
}

// HostingerKVM is Hostinger's KVM NixOS image: BIOS GRUB, one labelled ext4
// root, cloud-init provisioning and the QEMU guest agent used by its panel.
const HostingerKVM = "hostinger-kvm"

type Boot struct {
	Loader                       string   `json:"loader"`
	GrubDevice                   string   `json:"grub_device,omitempty"`
	EFIMountPoint                string   `json:"efi_mount_point,omitempty"`
	EFICanTouchVariables         bool     `json:"efi_can_touch_variables"`
	EFIInstallAsRemovable        bool     `json:"efi_install_as_removable"`
	GrubCopyKernels              bool     `json:"grub_copy_kernels"`
	ConfigurationLimit           *int     `json:"configuration_limit"`
	InitrdSystemd                bool     `json:"initrd_systemd"`
	InitrdAvailableKernelModules []string `json:"initrd_available_kernel_modules"`
	InitrdKernelModules          []string `json:"initrd_kernel_modules,omitempty"`
	KernelModules                []string `json:"kernel_modules,omitempty"`
	Console                      []string `json:"console,omitempty"`
}

type FileSystem struct {
	MountPoint    string   `json:"mount_point"`
	Device        string   `json:"device"`
	FSType        string   `json:"fs_type"`
	Options       []string `json:"options"`
	NeededForBoot bool     `json:"needed_for_boot"`
}

type Swap struct {
	Device   string `json:"device"`
	Priority *int   `json:"priority"`
}

type Network struct {
	Backend         string      `json:"backend"`
	HostName        string      `json:"host_name"`
	Domain          string      `json:"domain,omitempty"`
	UseDHCP         bool        `json:"use_dhcp"`
	IPv6Mode        string      `json:"ipv6_mode"`
	Nameservers     []string    `json:"nameservers,omitempty"`
	Search          []string    `json:"search,omitempty"`
	Interfaces      []Interface `json:"interfaces"`
	DefaultGateway  *Gateway    `json:"default_gateway"`
	DefaultGateway6 *Gateway    `json:"default_gateway6"`
}

type Interface struct {
	Name          string    `json:"name"`
	MACAddress    string    `json:"mac_address,omitempty"`
	MTU           *int      `json:"mtu"`
	UseDHCP       *bool     `json:"dhcp"`
	IPv4Addresses []Address `json:"ipv4_addresses,omitempty"`
	IPv6Addresses []Address `json:"ipv6_addresses,omitempty"`
	IPv4Routes    []Route   `json:"ipv4_routes,omitempty"`
	IPv6Routes    []Route   `json:"ipv6_routes,omitempty"`
}

type Address struct {
	Address      string `json:"address"`
	PrefixLength int    `json:"prefix_length"`
}

type Route struct {
	Address      string `json:"address"`
	PrefixLength int    `json:"prefix_length"`
	Via          string `json:"via,omitempty"`
	Scope        string `json:"scope"`
	Metric       *int   `json:"metric"`
	OnLink       bool   `json:"on_link"`
}

type Gateway struct {
	Address   string `json:"address"`
	Interface string `json:"interface"`
	Metric    *int   `json:"metric"`
	Source    string `json:"source,omitempty"`
}

// Parse rejects duplicate and case-aliased keys, unknown fields and trailing data.
// Error messages contain fixed reasons, never supplied strings or parser details.
func Parse(data []byte) (Config, error) {
	if len(data) == 0 || len(data) > MaxInputBytes || !utf8.Valid(data) {
		return Config{}, invalid("input size or encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	remaining := 8192
	if err := scanJSON(decoder, 0, &remaining, false); err != nil {
		return Config{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Config{}, invalid("trailing data")
	}
	var config Config
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil {
		return Config{}, invalid("field or type")
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Canonical returns stable struct-field encoding and its lowercase SHA-256 hex.
// Ordered input lists remain ordered; callers must bind this exact byte hash.
func Canonical(config Config) ([]byte, string, error) {
	if err := Validate(config); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > MaxInputBytes {
		return nil, "", invalid("canonical input size")
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidMachine, reason) }

// Native JSON tokens provide syntax/type parsing. This pass only enforces our
// duplicate/case policy and finite structural budget before typed decoding.
func scanJSON(decoder *json.Decoder, depth int, remaining *int, nullable bool) error {
	*remaining--
	if depth > 16 || *remaining < 0 {
		return invalid("JSON structure limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return invalid("JSON syntax")
	}
	if token == nil && !nullable {
		return invalid("null field")
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return invalid("JSON structure")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		childNullable := false
		if delimiter == '{' {
			keyToken, err := decoder.Token()
			if err != nil {
				return invalid("JSON object")
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] || !jsonKey.MatchString(key) {
				return invalid("duplicate or noncanonical field")
			}
			seen[key] = true
			switch key {
			case "configuration_limit", "priority", "mtu", "dhcp", "metric", "default_gateway", "default_gateway6":
				childNullable = true
			}
		}
		if err := scanJSON(decoder, depth+1, remaining, childNullable); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || (delimiter == '{' && closing != json.Delim('}')) || (delimiter == '[' && closing != json.Delim(']')) {
		return invalid("JSON structure")
	}
	return nil
}

var (
	jsonKey       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	stateVersion  = regexp.MustCompile(`^[0-9]{2}\.(0[1-9]|1[0-2])$`)
	moduleName    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	deviceName    = regexp.MustCompile(`^/dev/((vd|sd|xvd)[a-z]+[0-9]*|nvme[0-9]+n[0-9]+(p[0-9]+)?|disk/by-(uuid|label|partuuid)/[A-Za-z0-9_-]+)$`)
	fileName      = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	hostLabel     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	interfaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,14}$`)
	macAddress    = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)
	vfatMask      = regexp.MustCompile(`^[fdu]mask=0[0-7]{3}$`)
)

func Validate(config Config) error {
	if config.SchemaVersion != 1 || config.System != "x86_64-linux" || !stateVersion.MatchString(config.StateVersion) {
		return invalid("schema, architecture or state version")
	}
	boot := config.Boot
	if boot.Loader != "grub-bios" && boot.Loader != "grub-efi" && boot.Loader != "systemd-boot" {
		return invalid("boot loader")
	}
	if boot.ConfigurationLimit != nil && (*boot.ConfigurationLimit < 2 || *boot.ConfigurationLimit > 1000) {
		return invalid("boot recovery limit")
	}
	if boot.Loader != "systemd-boot" && boot.ConfigurationLimit == nil {
		return invalid("GRUB generation limit")
	}
	if boot.Loader == "grub-bios" {
		if !deviceName.MatchString(boot.GrubDevice) || boot.EFIMountPoint != "" || boot.EFICanTouchVariables || boot.EFIInstallAsRemovable {
			return invalid("BIOS boot settings")
		}
	} else if boot.GrubDevice != "" || !mountPoint(boot.EFIMountPoint) {
		return invalid("EFI boot settings")
	}
	if boot.EFIInstallAsRemovable && (boot.Loader != "grub-efi" || boot.EFICanTouchVariables) {
		return invalid("EFI removable settings")
	}
	if boot.Loader == "systemd-boot" && boot.GrubCopyKernels {
		return invalid("GRUB-only setting")
	}
	if len(boot.InitrdAvailableKernelModules) == 0 || !validTokens(boot.InitrdAvailableKernelModules, 64, moduleName) || !validTokens(boot.InitrdKernelModules, 64, moduleName) || !validTokens(boot.KernelModules, 64, moduleName) {
		return invalid("kernel or initrd settings")
	}
	if len(boot.Console) > 2 {
		return invalid("console count")
	}
	serial, terminal := false, false
	for _, console := range boot.Console {
		switch console {
		case "tty0":
			if terminal {
				return invalid("duplicate console")
			}
			terminal = true
		case "ttyS0", "ttyS0,115200", "ttyS0,115200n8":
			if serial {
				return invalid("duplicate console")
			}
			serial = true
		default:
			return invalid("unsupported console")
		}
	}
	if len(config.FileSystems) == 0 || len(config.FileSystems) > 2 || len(config.Swap) > 4 {
		return invalid("filesystem or swap count")
	}
	mounts, devices := make(map[string]bool), make(map[string]bool)
	for _, fs := range config.FileSystems {
		if !mountPoint(fs.MountPoint) || mounts[fs.MountPoint] || !deviceName.MatchString(fs.Device) || devices[fs.Device] || (fs.FSType != "ext4" && fs.FSType != "vfat") || len(fs.Options) == 0 || len(fs.Options) > 16 {
			return invalid("filesystem layout")
		}
		mounts[fs.MountPoint], devices[fs.Device] = true, true
		if (fs.MountPoint == "/" && fs.FSType != "ext4") || (fs.MountPoint != "/" && fs.FSType != "vfat") {
			return invalid("root filesystem")
		}
		if fs.FSType == "vfat" && (boot.Loader == "grub-bios" || fs.MountPoint != boot.EFIMountPoint) {
			return invalid("ESP filesystem")
		}
		for _, option := range fs.Options {
			if !mountOption(option, fs.FSType) || (fs.MountPoint == "/" && option == "noexec") || (option == "ro" && (fs.MountPoint == "/" || fs.MountPoint == "/boot" || fs.MountPoint == boot.EFIMountPoint)) {
				return invalid("filesystem mount option")
			}
		}
	}
	if !mounts["/"] {
		return invalid("missing root filesystem")
	}
	if boot.Loader != "grub-bios" {
		esp := false
		for _, fs := range config.FileSystems {
			esp = esp || (fs.MountPoint == boot.EFIMountPoint && fs.FSType == "vfat")
		}
		if !esp {
			return invalid("missing existing ESP")
		}
	}
	for _, swap := range config.Swap {
		if (!deviceName.MatchString(swap.Device) && !swapFile(swap.Device)) || devices[swap.Device] || (swap.Priority != nil && (*swap.Priority < 0 || *swap.Priority > 32767)) {
			return invalid("initialized swap")
		}
		devices[swap.Device] = true
	}
	if err := validateNetwork(config.Network); err != nil {
		return err
	}
	return validateTemplate(config)
}

func validateTemplate(config Config) error {
	switch config.Template {
	case "":
		return nil
	case HostingerKVM:
		root := config.FileSystems[0]
		if config.Boot.Loader != "grub-bios" || config.Boot.GrubDevice != "/dev/sda" || len(config.FileSystems) != 1 || len(config.Swap) != 0 ||
			root.Device != "/dev/disk/by-label/nixos" || !slices.Equal(config.Boot.Console, []string{"tty0", "ttyS0,115200"}) || config.Network.Backend != "networkd" {
			return invalid("Hostinger template shape")
		}
		return nil
	}
	return invalid("machine template")
}

func mountPoint(value string) bool {
	switch value {
	case "/", "/boot", "/boot/efi", "/efi":
		return true
	}
	return false
}

func mountOption(value, fsType string) bool {
	switch value {
	case "defaults", "rw", "ro", "noatime", "relatime", "strictatime", "lazytime", "nosuid", "nodev", "noexec", "discard", "nodiscard":
		return true
	case "errors=remount-ro":
		return fsType == "ext4"
	}
	return fsType == "vfat" && vfatMask.MatchString(value)
}

func swapFile(value string) bool {
	return len(value) <= 256 && fileName.MatchString(value) && path.Clean(value) == value && (value == "/swapfile" || strings.HasPrefix(value, "/swap/") || strings.HasPrefix(value, "/var/swap/"))
}

func validTokens(values []string, limit int, pattern *regexp.Regexp) bool {
	if len(values) > limit {
		return false
	}
	seen := make(map[string]bool)
	for _, value := range values {
		if !pattern.MatchString(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func validateNetwork(network Network) error {
	if (network.Backend != "dhcpcd" && network.Backend != "networkd") || !hostLabel.MatchString(network.HostName) || (network.IPv6Mode != "disabled" && network.IPv6Mode != "static") || len(network.Interfaces) == 0 || len(network.Interfaces) > 8 || len(network.Nameservers) > 8 || len(network.Search) > 6 {
		return invalid("physical network settings")
	}
	if network.Domain != "" && !domain(network.Domain) {
		return invalid("DNS domain")
	}
	for _, name := range network.Search {
		if !domain(name) {
			return invalid("DNS search")
		}
	}
	for _, value := range network.Nameservers {
		if !ip(value, false) && (network.IPv6Mode != "static" || !ip(value, true)) {
			return invalid("DNS address")
		}
	}
	interfaces := make(map[string]bool)
	for _, item := range network.Interfaces {
		if !interfaceName.MatchString(item.Name) || interfaces[item.Name] || item.Name == "lo" || strings.HasPrefix(item.Name, "vps-") || strings.HasPrefix(item.Name, "tailscale") || strings.HasPrefix(item.Name, "docker") || (item.MACAddress != "" && !macAddress.MatchString(item.MACAddress)) || (item.MTU != nil && (*item.MTU < 1280 || *item.MTU > 9000)) {
			return invalid("physical interface")
		}
		interfaces[item.Name] = true
		if len(item.IPv4Addresses) > 16 || len(item.IPv6Addresses) > 16 || len(item.IPv4Routes) > 16 || len(item.IPv6Routes) > 16 {
			return invalid("address or route count")
		}
		dhcp := network.UseDHCP
		if item.UseDHCP != nil {
			dhcp = *item.UseDHCP
		}
		if !dhcp && len(item.IPv4Addresses) == 0 && len(item.IPv6Addresses) == 0 {
			return invalid("missing interface address policy")
		}
		if network.IPv6Mode == "disabled" && (len(item.IPv6Addresses) > 0 || len(item.IPv6Routes) > 0) {
			return invalid("disabled IPv6 configuration")
		}
		for _, address := range item.IPv4Addresses {
			if !validAddress(address, false) {
				return invalid("IPv4 address")
			}
		}
		for _, address := range item.IPv6Addresses {
			if !validAddress(address, true) {
				return invalid("IPv6 address")
			}
		}
		for _, route := range item.IPv4Routes {
			if !validRoute(route, false) {
				return invalid("IPv4 route")
			}
		}
		for _, route := range item.IPv6Routes {
			if !validRoute(route, true) {
				return invalid("IPv6 route")
			}
		}
	}
	if !validGateway(network.DefaultGateway, false, interfaces) || !validGateway(network.DefaultGateway6, true, interfaces) || (network.IPv6Mode == "disabled" && network.DefaultGateway6 != nil) {
		return invalid("default gateway")
	}
	return nil
}

func domain(value string) bool {
	if len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func ip(value string, ipv6 bool) bool {
	address, err := netip.ParseAddr(value)
	return err == nil && address.Zone() == "" && address.Is6() == ipv6 && !address.Is4In6() && !address.IsUnspecified() && !address.IsMulticast() && !address.IsLoopback()
}

func validAddress(address Address, ipv6 bool) bool {
	maximum := 32
	if ipv6 {
		maximum = 128
	}
	return ip(address.Address, ipv6) && address.PrefixLength >= 0 && address.PrefixLength <= maximum
}

func validRoute(route Route, ipv6 bool) bool {
	address, err := netip.ParseAddr(route.Address)
	if err != nil || address.Zone() != "" || address.Is6() != ipv6 || address.Is4In6() || (route.Via != "" && !ip(route.Via, ipv6)) || (route.Scope != "link" && route.Scope != "global" && route.Scope != "host") || (route.Metric != nil && (*route.Metric < 0 || *route.Metric > 2147483647)) {
		return false
	}
	prefix := netip.PrefixFrom(address, route.PrefixLength)
	return prefix.IsValid() && prefix.Masked().Addr() == address && (!route.OnLink || route.Via != "")
}

func validGateway(gateway *Gateway, ipv6 bool, interfaces map[string]bool) bool {
	return gateway == nil || (ip(gateway.Address, ipv6) && interfaces[gateway.Interface] && (gateway.Source == "" || ip(gateway.Source, ipv6)) && (gateway.Metric == nil || (*gateway.Metric >= 0 && *gateway.Metric <= 2147483647)))
}
