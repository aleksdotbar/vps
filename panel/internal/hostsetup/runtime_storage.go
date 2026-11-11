package hostsetup

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/aleksdotbar/vps/panel/internal/machine"
)

var ErrRuntimeStorage = errors.New("runtime storage does not match the reviewed supported layout")

type blockIdentity struct {
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
}

type storageFile struct {
	Kind        string
	Device      blockIdentity
	Inode       uint64
	UID         uint32
	Permissions uint32
}

type storageMount struct {
	Target     string `json:"target"`
	Source     string `json:"source"`
	FileSystem string `json:"fstype"`
	Options    string `json:"options"`
	MajorMinor string `json:"maj:min"`
	Root       string `json:"fsroot"`
}

type storageBlock struct {
	Name          string `json:"name"`
	Kind          string `json:"type"`
	MajorMinor    string `json:"maj:min"`
	Parent        string `json:"pkname"`
	FileSystem    string `json:"fstype"`
	UUID          string `json:"uuid"`
	PartitionUUID string `json:"partuuid"`
	PhysicalDisk  bool   `json:"-"`
}

type storageSwap struct {
	Name      string `json:"name"`
	Kind      string `json:"type"`
	Priority  int    `json:"prio"`
	Signature bool   `json:"-"`
}

type storageFacts struct {
	EFI    bool
	Mounts []storageMount
	Blocks map[blockIdentity]storageBlock
	Files  map[string]storageFile
	Swaps  []storageSwap
}

type storageMountObservation struct {
	Target        string        `json:"target"`
	Device        blockIdentity `json:"device"`
	ParentDisk    blockIdentity `json:"parent_disk"`
	UUID          string        `json:"uuid,omitempty"`
	PartitionUUID string        `json:"partition_uuid,omitempty"`
	Flags         []string      `json:"flags"`
}

type storageSwapObservation struct {
	Path     string        `json:"path"`
	Kind     string        `json:"kind"`
	Device   blockIdentity `json:"device"`
	Inode    uint64        `json:"inode,omitempty"`
	Priority int           `json:"priority"`
}

type storageObservation struct {
	BIOSDisk *blockIdentity            `json:"bios_disk,omitempty"`
	EFI      bool                      `json:"efi"`
	Mounts   []storageMountObservation `json:"mounts"`
	Swaps    []storageSwapObservation  `json:"swaps"`
}

func admitRuntimeStorage(config machine.Config, facts storageFacts) (storageObservation, error) {
	if (config.Boot.Loader != "grub-bios") != facts.EFI || len(facts.Mounts) == 0 || len(facts.Mounts) > 1024 || len(facts.Swaps) != len(config.Swap) {
		return storageObservation{}, ErrRuntimeStorage
	}
	result := storageObservation{EFI: facts.EFI, Swaps: []storageSwapObservation{}}
	declared := make(map[string]machine.FileSystem, len(config.FileSystems))
	var root blockIdentity
	for _, filesystem := range config.FileSystems {
		declared[filesystem.MountPoint] = filesystem
		file, ok := facts.Files[filesystem.Device]
		if !ok || file.Kind != "block" {
			return storageObservation{}, ErrRuntimeStorage
		}
		if filesystem.MountPoint == "/" {
			root = file.Device
		}
	}
	seen := make(map[string]bool, len(declared))
	for _, mount := range facts.Mounts {
		filesystem, expected := declared[mount.Target]
		if !expected {
			if !admittedAuxiliaryMount(mount, root) {
				return storageObservation{}, ErrRuntimeStorage
			}
			continue
		}
		device, err := parseBlockIdentity(mount.MajorMinor)
		mounted, ok := facts.Files[mount.Target]
		if err != nil || seen[mount.Target] || device != facts.Files[filesystem.Device].Device || !ok || mounted.Kind != "directory" || mounted.Device != device || mount.FileSystem != filesystem.FSType || mount.Root != "/" {
			return storageObservation{}, ErrRuntimeStorage
		}
		flags, ok := admittedMountFlags(filesystem, mount.Options)
		if !ok {
			return storageObservation{}, ErrRuntimeStorage
		}
		block, parent, ok := plainStorageDevice(device, facts)
		if !ok || block.FileSystem != filesystem.FSType {
			return storageObservation{}, ErrRuntimeStorage
		}
		if config.Boot.Loader == "grub-bios" && mount.Target == "/" {
			grub, ok := facts.Files[config.Boot.GrubDevice]
			if !ok || grub.Kind != "block" {
				return storageObservation{}, ErrRuntimeStorage
			}
			disk, ok := facts.Blocks[grub.Device]
			if !ok || disk.Kind != "disk" || disk.Parent != "" || !disk.PhysicalDisk {
				return storageObservation{}, ErrRuntimeStorage
			}
			identity := grub.Device
			result.BIOSDisk = &identity
		}
		seen[mount.Target] = true
		result.Mounts = append(result.Mounts, storageMountObservation{Target: mount.Target, Device: device, ParentDisk: parent, UUID: block.UUID, PartitionUUID: block.PartitionUUID, Flags: flags})
	}
	if len(seen) != len(declared) {
		return storageObservation{}, ErrRuntimeStorage
	}
	matched := make(map[int]bool, len(facts.Swaps))
	for _, expected := range config.Swap {
		file, ok := facts.Files[expected.Device]
		if !ok {
			return storageObservation{}, ErrRuntimeStorage
		}
		found := -1
		for index, active := range facts.Swaps {
			actual, ok := facts.Files[active.Name]
			if !ok || !sameStorageFile(file, actual) {
				continue
			}
			if found != -1 || matched[index] || !active.Signature || (expected.Priority != nil && *expected.Priority != active.Priority) {
				return storageObservation{}, ErrRuntimeStorage
			}
			if file.Kind == "block" {
				block, _, ok := plainStorageDevice(file.Device, facts)
				if !ok || block.FileSystem != "swap" || active.Kind != "partition" {
					return storageObservation{}, ErrRuntimeStorage
				}
			} else if file.Kind != "file" || active.Kind != "file" || file.Device != root || file.UID != 0 || file.Permissions != 0600 {
				return storageObservation{}, ErrRuntimeStorage
			}
			found = index
		}
		if found < 0 {
			return storageObservation{}, ErrRuntimeStorage
		}
		matched[found] = true
		result.Swaps = append(result.Swaps, storageSwapObservation{Path: expected.Device, Kind: file.Kind, Device: file.Device, Inode: file.Inode, Priority: facts.Swaps[found].Priority})
	}
	slices.SortFunc(result.Mounts, func(left, right storageMountObservation) int { return strings.Compare(left.Target, right.Target) })
	return result, nil
}

func plainStorageDevice(device blockIdentity, facts storageFacts) (storageBlock, blockIdentity, bool) {
	block, ok := facts.Blocks[device]
	if !ok {
		return storageBlock{}, blockIdentity{}, false
	}
	if block.Kind == "disk" {
		return block, device, block.Parent == "" && block.PhysicalDisk
	}
	if block.Kind != "part" || block.Parent == "" {
		return storageBlock{}, blockIdentity{}, false
	}
	parent, ok := facts.Files[block.Parent]
	if !ok || parent.Kind != "block" {
		return storageBlock{}, blockIdentity{}, false
	}
	disk, ok := facts.Blocks[parent.Device]
	if !ok || disk.Kind != "disk" || disk.Parent != "" || !disk.PhysicalDisk {
		return storageBlock{}, blockIdentity{}, false
	}
	return block, parent.Device, true
}

func sameStorageFile(left, right storageFile) bool {
	return left.Kind == right.Kind && left.Device == right.Device && (left.Kind == "block" || (left.Kind == "file" && left.Inode != 0 && left.Inode == right.Inode))
}

func admittedMountFlags(expected machine.FileSystem, options string) ([]string, bool) {
	values := strings.Split(options, ",")
	if !slices.Contains(values, "rw") || slices.Contains(values, "ro") || (expected.MountPoint == "/" && slices.Contains(values, "noexec")) {
		return nil, false
	}
	flags := []string{"rw"}
	for _, flag := range []string{"noexec", "nosuid", "nodev"} {
		present := slices.Contains(values, flag)
		if slices.Contains(expected.Options, flag) && !present {
			return nil, false
		}
		if present {
			flags = append(flags, flag)
		}
	}
	if expected.FSType == "vfat" {
		wantFile, wantDirectory, wantF, wantD, ok := fatPermissionMasks(expected.Options)
		if !ok {
			return nil, false
		}
		file, directory, hasFile, hasDirectory, ok := fatPermissionMasks(values)
		if !ok || (wantF && (!hasFile || file&wantFile != wantFile)) || (wantD && (!hasDirectory || directory&wantDirectory != wantDirectory)) {
			return nil, false
		}
		if hasFile {
			flags = append(flags, fmt.Sprintf("fmask=%04o", file))
		}
		if hasDirectory {
			flags = append(flags, fmt.Sprintf("dmask=%04o", directory))
		}
	}
	return flags, true
}

func fatPermissionMasks(options []string) (file, directory uint64, hasFile, hasDirectory, valid bool) {
	for _, option := range options {
		name, value, ok := strings.Cut(option, "=")
		if !ok || (name != "umask" && name != "fmask" && name != "dmask") {
			continue
		}
		mask, err := strconv.ParseUint(value, 8, 12)
		if err != nil {
			return 0, 0, false, false, false
		}
		switch name {
		case "umask":
			file, directory, hasFile, hasDirectory = mask, mask, true, true
		case "fmask":
			file, hasFile = mask, true
		case "dmask":
			directory, hasDirectory = mask, true
		}
	}
	return file, directory, hasFile, hasDirectory, true
}

func admittedAuxiliaryMount(mount storageMount, root blockIdentity) bool {
	if mount.Target == "/nix/store" {
		device, err := parseBlockIdentity(mount.MajorMinor)
		options := strings.Split(mount.Options, ",")
		return err == nil && device == root && mount.FileSystem == "ext4" && mount.Root == "/nix/store" && slices.Contains(options, "ro") && slices.Contains(options, "nodev") && slices.Contains(options, "nosuid") && !slices.Contains(options, "rw")
	}
	virtual := false
	for _, prefix := range []string{"/dev", "/sys", "/proc", "/run", "/tmp"} {
		virtual = virtual || mount.Target == prefix || strings.HasPrefix(mount.Target, prefix+"/")
	}
	if !virtual {
		return false
	}
	switch mount.FileSystem {
	case "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "cgroup", "cgroup2", "pstore", "securityfs", "debugfs", "tracefs", "configfs", "efivarfs", "hugetlbfs", "mqueue", "bpf", "autofs", "binfmt_misc", "ramfs", "nsfs", "fusectl":
		return true
	default:
		return false
	}
}
