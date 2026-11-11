package hostsetup

import (
	"errors"
	"os"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/machine"
)

// Rendered by cloud-init on Hostinger's KVM NixOS image, with documentation addresses.
const hostingerNetwork = `[Address]
Address=192.0.2.86/24

[Address]
Address=2001:db8:41:c482::1/48

[Match]
MACAddress=d6:e8:d4:af:f5:dc
Name=eth0

[Network]
DHCP=no
DNS=192.0.2.6 1.1.1.1 8.8.4.4
Domains=localhost

[Route]
Gateway=192.0.2.254

[Route]
Gateway=2001:db8:41::1
`

func TestHostingerNetworkBecomesStaticMachineNetwork(t *testing.T) {
	network, err := parseCloudInitNetwork([]byte(hostingerNetwork), "srv")
	if err != nil {
		t.Fatal(err)
	}
	item := network.Interfaces[0]
	if network.Backend != "networkd" || network.IPv6Mode != "static" || network.HostName != "srv" || len(network.Interfaces) != 1 ||
		item.Name != "eth0" || item.MACAddress != "d6:e8:d4:af:f5:dc" ||
		len(item.IPv4Addresses) != 1 || item.IPv4Addresses[0].Address != "192.0.2.86" || item.IPv4Addresses[0].PrefixLength != 24 ||
		len(item.IPv6Addresses) != 1 || item.IPv6Addresses[0].PrefixLength != 48 ||
		network.DefaultGateway.Address != "192.0.2.254" || network.DefaultGateway6.Address != "2001:db8:41::1" || network.DefaultGateway6.Interface != "eth0" ||
		len(network.Nameservers) != 3 || len(network.Search) != 0 {
		t.Fatalf("unexpected network: %+v", network)
	}
	// The parsed network must pass the same admission as reviewed input.
	data, err := os.ReadFile("../foundation/testdata/input.json")
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := foundation.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	config := reviewed.Machine
	config.Template = machine.HostingerKVM
	config.Boot.Loader, config.Boot.GrubDevice, config.Boot.EFIMountPoint = "grub-bios", "/dev/sda", ""
	config.Boot.EFICanTouchVariables, config.Boot.EFIInstallAsRemovable = false, false
	config.Boot.Console = []string{"tty0", "ttyS0,115200"}
	config.FileSystems = []machine.FileSystem{{MountPoint: "/", Device: "/dev/disk/by-label/nixos", FSType: "ext4", Options: []string{"defaults"}, NeededForBoot: true}}
	config.Swap = nil
	config.Network = network
	if _, err := newInput(config, reviewed.Operator, reviewed.Bootstrap); err != nil {
		t.Fatalf("discovered Hostinger input rejected: %v", err)
	}
	for name, data := range map[string]string{
		"DHCP":            "[Match]\nName=eth0\n[Network]\nDHCP=yes\n",
		"unknown setting": hostingerNetwork + "[Network]\nIPv6AcceptRA=yes\n",
		"second gateway":  hostingerNetwork + "[Route]\nGateway=192.0.2.253\n",
		"no interface":    "[Network]\nDNS=192.0.2.6\n",
	} {
		if _, err := parseCloudInitNetwork([]byte(data), "srv"); !errors.Is(err, ErrDiscovery) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if _, err := parseCloudInitNetwork([]byte(hostingerNetwork), "Bad_Host"); !errors.Is(err, ErrDiscovery) {
		t.Error("invalid hostname accepted")
	}
}
