package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func fixture() Config {
	limit := 100
	dhcp := true
	return Config{
		SchemaVersion: 1,
		System:        "x86_64-linux",
		StateVersion:  "26.05",
		Boot: Boot{
			Loader: "grub-bios", GrubDevice: "/dev/vda", ConfigurationLimit: &limit,
			InitrdAvailableKernelModules: []string{"virtio_pci", "virtio_blk"},
			Console:                      []string{"tty0", "ttyS0,115200n8"},
		},
		FileSystems: []FileSystem{{MountPoint: "/", Device: "/dev/disk/by-uuid/11111111-2222-3333-4444-555555555555", FSType: "ext4", Options: []string{"defaults"}, NeededForBoot: true}},
		Network: Network{
			Backend: "dhcpcd", HostName: "test-host", IPv6Mode: "disabled",
			Interfaces: []Interface{{Name: "ens3", UseDHCP: &dhcp}},
		},
	}
}

func TestCanonicalInputPreservesNativeProjectionValues(t *testing.T) {
	bios, _, err := Canonical(fixture())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(bios); err != nil {
		t.Fatalf("BIOS fixture failed admission: %v", err)
	}
	config := fixture()
	config.Boot.Loader = "systemd-boot"
	config.Boot.GrubDevice = ""
	config.Boot.EFIMountPoint = "/boot/efi"
	config.Boot.EFICanTouchVariables = true
	config.Boot.ConfigurationLimit = nil
	config.Boot.InitrdSystemd = true
	config.FileSystems = append(config.FileSystems, FileSystem{MountPoint: "/boot/efi", Device: "/dev/vda1", FSType: "vfat", Options: []string{"umask=0077"}})
	config.Network.Backend = "networkd"
	config.Network.IPv6Mode = "static"
	config.Network.Interfaces[0].UseDHCP = nil
	config.Network.Interfaces[0].IPv4Addresses = []Address{{Address: "192.0.2.10", PrefixLength: 32}}
	config.Network.Interfaces[0].IPv4Routes = []Route{{Address: "192.0.2.1", PrefixLength: 32, Scope: "link"}}
	config.Network.DefaultGateway = &Gateway{Address: "192.0.2.1", Interface: "ens3"}
	config.Network.Interfaces[0].IPv6Addresses = []Address{{Address: "2001:db8::10", PrefixLength: 64}}
	config.Network.DefaultGateway6 = &Gateway{Address: "fe80::1", Interface: "ens3"}
	config.Swap = []Swap{{Device: "/swapfile"}}
	data, digest, err := Canonical(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	again, againDigest, err := Canonical(parsed)
	if err != nil || !bytes.Equal(data, again) || digest != againDigest || len(digest) != 64 {
		t.Fatalf("canonical machine identity changed: %v", err)
	}
	if parsed.StateVersion != "26.05" || parsed.Boot.EFIMountPoint != "/boot/efi" || parsed.Boot.ConfigurationLimit != nil || parsed.Network.Backend != "networkd" || parsed.Network.Interfaces[0].UseDHCP != nil || parsed.Network.Interfaces[0].IPv4Routes[0].Scope != "link" || parsed.Boot.Console[1] != "ttyS0,115200n8" {
		t.Fatal("boot or network representation changed")
	}
	config.FileSystems[1].Options = []string{"mask=0077"}
	if err := Validate(config); !errors.Is(err, ErrInvalidMachine) {
		t.Fatalf("nonexistent ESP mount option admitted: %v", err)
	}
}

func TestDestructiveOrExecutableMachineInputsAreRejected(t *testing.T) {
	valid, err := json.Marshal(fixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		input string
	}{
		{"filesystem autoformat", strings.Replace(string(valid), `"fs_type":"ext4"`, `"fs_type":"ext4","auto_format":true`, 1)},
		{"filesystem resize", strings.Replace(string(valid), `"defaults"`, `"x-systemd.growfs"`, 1)},
		{"filesystem makefs", strings.Replace(string(valid), `"defaults"`, `"x-systemd.makefs"`, 1)},
		{"root noexec", strings.Replace(string(valid), `"defaults"`, `"noexec"`, 1)},
		{"swap creation", strings.Replace(string(valid), `"network":`, `"swap":[{"device":"/swapfile","size":2048}],"network":`, 1)},
		{"password hash", strings.Replace(string(valid), `"schema_version":1`, `"schema_version":1,"hashed_password":"sensitive-input"`, 1)},
		{"arbitrary Nix", strings.Replace(string(valid), `"schema_version":1`, `"schema_version":1,"imports":["/etc/nixos/configuration.nix"]`, 1)},
		{"arbitrary kernel command", strings.Replace(string(valid), `"console":["tty0","ttyS0,115200n8"]`, `"console":["ttyS0 init=/bin/sh"]`, 1)},
		{"duplicate setting", strings.Replace(string(valid), `"use_dhcp":false`, `"use_dhcp":false,"use_dhcp":true`, 1)},
		{"case alias", strings.Replace(string(valid), `"state_version"`, `"STATE_VERSION"`, 1)},
		{"null boolean", strings.Replace(string(valid), `"initrd_systemd":false`, `"initrd_systemd":null`, 1)},
		{"trailing data", string(valid) + `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse([]byte(test.input)); !errors.Is(err, ErrInvalidMachine) || strings.Contains(err.Error(), "sensitive-input") {
				t.Fatalf("unsafe input admitted or leaked: %v", err)
			}
		})
	}
}

func TestUnsupportedRequiredMachineBehaviorIsRejected(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"separate mount", func(config *Config) {
			config.FileSystems = append(config.FileSystems, FileSystem{MountPoint: "/nix", Device: "/dev/vda3", FSType: "ext4", Options: []string{"defaults"}})
		}},
		{"SLAAC", func(config *Config) { config.Network.IPv6Mode = "slaac" }},
		{"missing firmware filesystem", func(config *Config) {
			config.Boot.Loader = "systemd-boot"
			config.Boot.GrubDevice = ""
			config.Boot.EFIMountPoint = "/boot"
		}},
		{"lost previous generation", func(config *Config) { limit := 1; config.Boot.ConfigurationLimit = &limit }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := fixture()
			test.change(&config)
			if err := Validate(config); !errors.Is(err, ErrInvalidMachine) {
				t.Fatalf("unsupported machine admitted: %v", err)
			}
		})
	}
}

func hostinger() Config {
	config := fixture()
	config.Template = HostingerKVM
	config.Boot.GrubDevice = "/dev/sda"
	config.Boot.Console = []string{"tty0", "ttyS0,115200"}
	config.FileSystems[0].Device = "/dev/disk/by-label/nixos"
	config.Network = Network{
		Backend: "networkd", HostName: "srv", IPv6Mode: "static",
		Nameservers: []string{"153.92.2.6", "1.1.1.1"},
		Interfaces: []Interface{{
			Name: "eth0", MACAddress: "d6:e8:d4:af:f5:dc",
			IPv4Addresses: []Address{{Address: "192.0.2.86", PrefixLength: 24}},
			IPv6Addresses: []Address{{Address: "2001:db8:41:c482::1", PrefixLength: 48}},
		}},
		DefaultGateway:  &Gateway{Address: "192.0.2.254", Interface: "eth0"},
		DefaultGateway6: &Gateway{Address: "2001:db8:41::1", Interface: "eth0"},
	}
	return config
}

func TestHostingerTemplateRequiresItsImageShape(t *testing.T) {
	data, _, err := Canonical(hostinger())
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := Parse(data); err != nil || parsed.Template != HostingerKVM {
		t.Fatalf("Hostinger input failed admission: %v", err)
	}
	for name, change := range map[string]func(*Config){
		"unknown template": func(config *Config) { config.Template = "other" },
		"EFI loader": func(config *Config) {
			config.Boot.Loader, config.Boot.GrubDevice, config.Boot.EFIMountPoint = "grub-efi", "", "/boot"
		},
		"other disk":     func(config *Config) { config.Boot.GrubDevice = "/dev/vda" },
		"other root":     func(config *Config) { config.FileSystems[0].Device = "/dev/sda1" },
		"swap":           func(config *Config) { config.Swap = []Swap{{Device: "/swapfile"}} },
		"other console":  func(config *Config) { config.Boot.Console = []string{"tty0"} },
		"dhcpcd backend": func(config *Config) { config.Network.Backend = "dhcpcd" },
	} {
		config := hostinger()
		change(&config)
		if err := Validate(config); !errors.Is(err, ErrInvalidMachine) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}
