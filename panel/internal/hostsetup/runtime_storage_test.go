package hostsetup

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/machine"
)

func storageFixture(t *testing.T, efi bool) (machine.Config, storageFacts) {
	t.Helper()
	data, err := os.ReadFile("../foundation/testdata/input.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := foundation.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	config := input.Machine
	root, disk := blockIdentity{8, 1}, blockIdentity{8, 0}
	facts := storageFacts{EFI: efi, Files: map[string]storageFile{
		config.FileSystems[0].Device: {Kind: "block", Device: root}, "/dev/vda1": {Kind: "block", Device: root}, "/dev/vda": {Kind: "block", Device: disk}, "/": {Kind: "directory", Device: root, Inode: 2}},
		Blocks: map[blockIdentity]storageBlock{root: {Name: "/dev/vda1", Kind: "part", MajorMinor: "8:1", Parent: "/dev/vda", FileSystem: "ext4", UUID: "root-id", PartitionUUID: "root-part"}, disk: {Name: "/dev/vda", Kind: "disk", MajorMinor: "8:0", PhysicalDisk: true}},
		Mounts: []storageMount{{Target: "/", Source: "/dev/vda1", FileSystem: "ext4", Options: "rw,relatime", MajorMinor: "8:1", Root: "/"},
			{Target: "/nix/store", Source: "/dev/vda1[/nix/store]", FileSystem: "ext4", Options: "ro,nodev,nosuid,relatime", MajorMinor: "8:1", Root: "/nix/store"},
			{Target: "/run", Source: "tmpfs", FileSystem: "tmpfs", Options: "rw,nodev,nosuid", MajorMinor: "0:21", Root: "/"}}, Swaps: []storageSwap{}}
	if efi {
		config.Boot.Loader = "systemd-boot"
		config.Boot.GrubDevice = ""
		config.Boot.EFIMountPoint = "/boot"
		config.FileSystems = append(config.FileSystems, machine.FileSystem{MountPoint: "/boot", Device: "/dev/disk/by-uuid/esp-id", FSType: "vfat", Options: []string{"umask=0077", "noexec", "nosuid", "nodev"}})
		esp := blockIdentity{8, 2}
		facts.Files["/dev/disk/by-uuid/esp-id"] = storageFile{Kind: "block", Device: esp}
		facts.Files["/dev/vda2"] = storageFile{Kind: "block", Device: esp}
		facts.Files["/boot"] = storageFile{Kind: "directory", Device: esp, Inode: 2}
		facts.Blocks[esp] = storageBlock{Name: "/dev/vda2", Kind: "part", MajorMinor: "8:2", Parent: "/dev/vda", FileSystem: "vfat", UUID: "esp-id", PartitionUUID: "esp-part"}
		facts.Mounts = append(facts.Mounts, storageMount{Target: "/boot", Source: "/dev/vda2", FileSystem: "vfat", Options: "rw,nosuid,nodev,noexec,fmask=0077,dmask=0077", MajorMinor: "8:2", Root: "/"})
	}
	if err := machine.Validate(config); err != nil {
		t.Fatal(err)
	}
	return config, facts
}

func TestRuntimeStorageRejectsDeviceMountAndFirmwareDrift(t *testing.T) {
	for _, name := range []string{"accepted alias", "wrong block alias", "mount stat drift", "read-only root", "subtree root", "storage layer", "virtual parent", "different store bind", "extra persistent mount", "wrong firmware", "partition bootdisk"} {
		t.Run(name, func(t *testing.T) {
			config, facts := storageFixture(t, false)
			switch name {
			case "wrong block alias":
				facts.Files[config.FileSystems[0].Device] = storageFile{Kind: "block", Device: blockIdentity{8, 9}}
			case "mount stat drift":
				facts.Files["/"] = storageFile{Kind: "directory", Device: blockIdentity{8, 9}}
			case "read-only root":
				facts.Mounts[0].Options = "ro,relatime"
			case "subtree root":
				facts.Mounts[0].Root = "/subtree"
			case "storage layer":
				row := facts.Blocks[blockIdentity{8, 1}]
				row.Kind = "crypt"
				facts.Blocks[blockIdentity{8, 1}] = row
			case "virtual parent":
				row := facts.Blocks[blockIdentity{8, 0}]
				row.PhysicalDisk = false
				facts.Blocks[blockIdentity{8, 0}] = row
			case "different store bind":
				facts.Mounts[1].Root = "/unrelated"
			case "extra persistent mount":
				facts.Mounts = append(facts.Mounts, storageMount{Target: "/data", FileSystem: "ext4", MajorMinor: "8:3", Root: "/"})
			case "wrong firmware":
				facts.EFI = true
			case "partition bootdisk":
				facts.Files[config.Boot.GrubDevice] = facts.Files[config.FileSystems[0].Device]
			}
			observation, err := admitRuntimeStorage(config, facts)
			if name == "accepted alias" {
				if err != nil || len(observation.Mounts) != 1 || observation.BIOSDisk == nil {
					t.Fatalf("supported native alias refused: %v", err)
				}
			} else if !errors.Is(err, ErrRuntimeStorage) {
				t.Fatalf("storage drift accepted: %v", err)
			}
		})
	}
	config, facts := storageFixture(t, false)
	disk := blockIdentity{8, 0}
	whole := facts.Blocks[disk]
	whole.FileSystem = "ext4"
	facts.Blocks[disk] = whole
	facts.Files[config.FileSystems[0].Device] = storageFile{Kind: "block", Device: disk}
	facts.Files["/"] = storageFile{Kind: "directory", Device: disk}
	facts.Mounts[0].MajorMinor = "8:0"
	facts.Mounts[1].MajorMinor = "8:0"
	boot := blockIdentity{8, 16}
	config.Boot.GrubDevice = "/dev/vdb"
	facts.Files["/dev/vdb"] = storageFile{Kind: "block", Device: boot}
	facts.Blocks[boot] = storageBlock{Name: "/dev/vdb", Kind: "disk", MajorMinor: "8:16", PhysicalDisk: true}
	if observation, err := admitRuntimeStorage(config, facts); err != nil || observation.BIOSDisk == nil || *observation.BIOSDisk != boot {
		t.Fatalf("whole root or separate declared BIOS disk refused: %v", err)
	}
}

func TestRuntimeStoragePreservesESPPermissionMasks(t *testing.T) {
	for _, options := range []struct {
		value    string
		accepted bool
	}{
		{"rw,noexec,nosuid,nodev,fmask=0077,dmask=0077", true},
		{"rw,noexec,nosuid,nodev,fmask=0177,dmask=0077", true},
		{"rw,noexec,nosuid,nodev,fmask=0022,dmask=0077", false},
		{"rw,noexec,nosuid,nodev,fmask=0077,dmask=0022", false},
		{"rw,nosuid,nodev,fmask=0077,dmask=0077", false},
	} {
		config, facts := storageFixture(t, true)
		facts.Mounts[len(facts.Mounts)-1].Options = options.value
		_, err := admitRuntimeStorage(config, facts)
		if (err == nil) != options.accepted {
			t.Fatalf("ESP mask/flags %s admitted=%t: %v", options.value, options.accepted, err)
		}
	}
}

func TestRuntimeStorageMatchesExistingSwapIdentityAndPriority(t *testing.T) {
	for _, name := range []string{"accepted file alias", "inode changed", "priority changed", "missing signature", "undeclared swap", "unsafe file", "whole swap disk"} {
		t.Run(name, func(t *testing.T) {
			config, facts := storageFixture(t, false)
			priority := 10
			config.Swap = []machine.Swap{{Device: "/swapfile", Priority: &priority}}
			file := storageFile{Kind: "file", Device: blockIdentity{8, 1}, Inode: 123, UID: 0, Permissions: 0600}
			facts.Files["/swapfile"] = file
			facts.Files["/var/swap/active"] = file
			facts.Swaps = []storageSwap{{Name: "/var/swap/active", Kind: "file", Priority: 10, Signature: true}}
			switch name {
			case "inode changed":
				file.Inode++
				facts.Files["/var/swap/active"] = file
			case "priority changed":
				facts.Swaps[0].Priority = -2
			case "missing signature":
				facts.Swaps[0].Signature = false
			case "undeclared swap":
				config.Swap = nil
			case "unsafe file":
				file.Permissions = 0644
				facts.Files["/swapfile"] = file
				facts.Files["/var/swap/active"] = file
			case "whole swap disk":
				config.Swap[0].Device = "/dev/vdc"
				config.Swap[0].Priority = nil
				device := blockIdentity{8, 32}
				facts.Files["/dev/vdc"] = storageFile{Kind: "block", Device: device}
				facts.Blocks[device] = storageBlock{Name: "/dev/vdc", Kind: "disk", MajorMinor: "8:32", FileSystem: "swap", PhysicalDisk: true}
				facts.Swaps = []storageSwap{{Name: "/dev/vdc", Kind: "partition", Priority: -2, Signature: true}}
			}
			_, err := admitRuntimeStorage(config, facts)
			accepted := name == "accepted file alias" || name == "whole swap disk"
			if (err == nil) != accepted {
				t.Fatalf("swap identity/policy admitted=%t: %v", accepted, err)
			}
		})
	}
}

func TestRuntimeStorageNativeMountAndSwapMetadataFormats(t *testing.T) {
	root := t.TempDir()
	info := filepath.Join(root, "mountinfo")
	contents := "21 1 8:1 / / rw,relatime - ext4 /dev/vda1 rw\n22 21 8:1 /nix/store /nix/store ro,nodev,nosuid - ext4 /dev/vda1 rw\n"
	if err := os.WriteFile(info, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := runtimeProbe(t.Context(), nativeTool(t, "PANEL_TEST_FINDMNT", "findmnt"), "--json", "--kernel", "--tab-file", info, "--list", "--all", "--output", "TARGET,SOURCE,FSTYPE,OPTIONS,MAJ:MIN,FSROOT")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Filesystems []storageMount `json:"filesystems"`
	}
	if json.Unmarshal(data, &parsed) != nil || len(parsed.Filesystems) != 2 || parsed.Filesystems[1].Root != "/nix/store" || parsed.Filesystems[0].MajorMinor != "8:1" {
		t.Fatal("native flat mount identities or fsroot lost")
	}
	swaps, err := parseStorageSwaps([]byte("/var/swap/with\\x20space file -2\n/dev/vda3 partition 10\n"))
	if err != nil || len(swaps) != 2 || swaps[0].Name != "/var/swap/with space" || swaps[1].Priority != 10 {
		t.Fatalf("native raw swap format misread: %v", err)
	}
	image := make([]byte, 64<<10)
	binary.LittleEndian.PutUint32(image[1024:1028], 1)
	binary.LittleEndian.PutUint32(image[1028:1032], 15)
	copy(image[4096-10:4096], "SWAPSPACE2")
	path := filepath.Join(root, "public-swap-header")
	if err := os.WriteFile(path, image, 0600); err != nil {
		t.Fatal(err)
	}
	signature, err := runtimeProbe(t.Context(), nativeTool(t, "PANEL_TEST_BLKID", "blkid"), "--probe", "--output", "value", "--match-tag", "TYPE", "--match-types", "swap", "--", path)
	if err != nil || string(signature) != "swap\n" {
		t.Fatalf("native signature-only probe does not admit existing swap files: %v", err)
	}
}
