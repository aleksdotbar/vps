package hostsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/machine"
)

func inspectRuntimeStorage(ctx context.Context, enrollment string, config machine.Config) (storageObservation, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*runtimeProbeTimeout)
	defer cancel()
	facts := storageFacts{Blocks: make(map[blockIdentity]storageBlock), Files: make(map[string]storageFile)}
	firmware, err := os.Stat("/sys/firmware/efi")
	if err != nil && !os.IsNotExist(err) {
		return storageObservation{}, ErrRuntimeStorage
	}
	if err == nil {
		if firmware == nil || !firmware.IsDir() {
			return storageObservation{}, ErrRuntimeStorage
		}
		facts.EFI = true
	}
	mountData, err := runtimeProbe(bounded, enrollment+"/sw/bin/findmnt", "--json", "--kernel", "--list", "--all", "--output", "TARGET,SOURCE,FSTYPE,OPTIONS,MAJ:MIN,FSROOT")
	if err != nil {
		return storageObservation{}, ErrRuntimeStorage
	}
	var mounted struct {
		Filesystems []storageMount `json:"filesystems"`
	}
	if json.Unmarshal(mountData, &mounted) != nil || len(mounted.Filesystems) == 0 || len(mounted.Filesystems) > 1024 {
		return storageObservation{}, ErrRuntimeStorage
	}
	facts.Mounts = mounted.Filesystems
	swapData, err := runtimeProbe(bounded, enrollment+"/sw/bin/swapon", "--show=NAME,TYPE,PRIO", "--raw", "--noheadings")
	if err != nil {
		return storageObservation{}, ErrRuntimeStorage
	}
	facts.Swaps, err = parseStorageSwaps(swapData)
	if err != nil || len(facts.Swaps) != len(config.Swap) {
		return storageObservation{}, ErrRuntimeStorage
	}
	for _, filesystem := range config.FileSystems {
		if collectStorageBlock(bounded, enrollment, filesystem.Device, &facts) != nil {
			return storageObservation{}, ErrRuntimeStorage
		}
		mount, err := statStorageObject(filesystem.MountPoint)
		if err != nil || mount.Kind != "directory" {
			return storageObservation{}, ErrRuntimeStorage
		}
		facts.Files[filesystem.MountPoint] = mount
	}
	if config.Boot.Loader == "grub-bios" && collectStorageBlock(bounded, enrollment, config.Boot.GrubDevice, &facts) != nil {
		return storageObservation{}, ErrRuntimeStorage
	}
	for _, expected := range config.Swap {
		file, err := statStorageObject(expected.Device)
		if err != nil || (file.Kind != "file" && file.Kind != "block") {
			return storageObservation{}, ErrRuntimeStorage
		}
		facts.Files[expected.Device] = file
		if file.Kind == "block" && collectStorageBlock(bounded, enrollment, expected.Device, &facts) != nil {
			return storageObservation{}, ErrRuntimeStorage
		}
	}
	for index, active := range facts.Swaps {
		if !filepath.IsAbs(active.Name) || filepath.Clean(active.Name) != active.Name || len(active.Name) > 4096 || strings.ContainsAny(active.Name, "\x00\r\n") {
			return storageObservation{}, ErrRuntimeStorage
		}
		file, err := statStorageObject(active.Name)
		if err != nil {
			return storageObservation{}, ErrRuntimeStorage
		}
		facts.Files[active.Name] = file
		matched := false
		for _, expected := range config.Swap {
			matched = matched || sameStorageFile(file, facts.Files[expected.Device])
		}
		// Probe only an admitted active swap object, never arbitrary file input.
		if !matched {
			return storageObservation{}, ErrRuntimeStorage
		}
		signature, err := runtimeProbe(bounded, enrollment+"/sw/bin/blkid", "--probe", "--output", "value", "--match-tag", "TYPE", "--match-types", "swap", "--", active.Name)
		if err != nil || string(signature) != "swap\n" {
			return storageObservation{}, ErrRuntimeStorage
		}
		after, err := statStorageObject(active.Name)
		if err != nil || after != file {
			return storageObservation{}, ErrRuntimeStorage
		}
		facts.Swaps[index].Signature = true
	}
	// Recheck every observed alias/inode after the probes; a changed target is
	// drift, not permission to pair stale metadata with a replacement device.
	for path, before := range facts.Files {
		after, err := statStorageObject(path)
		if err != nil || after != before {
			return storageObservation{}, ErrRuntimeStorage
		}
	}
	return admitRuntimeStorage(config, facts)
}

func collectStorageBlock(ctx context.Context, enrollment, path string, facts *storageFacts) error {
	file, err := statStorageObject(path)
	if err != nil || file.Kind != "block" {
		return ErrRuntimeStorage
	}
	facts.Files[path] = file
	if _, present := facts.Blocks[file.Device]; present {
		return nil
	}
	if len(facts.Blocks) >= 16 {
		return ErrRuntimeStorage
	}
	data, err := runtimeProbe(ctx, enrollment+"/sw/bin/lsblk", "--json", "--paths", "--nodeps", "--properties-by", "blkid", "--output", "NAME,TYPE,MAJ:MIN,PKNAME,FSTYPE,UUID,PARTUUID", "--", path)
	if err != nil {
		return ErrRuntimeStorage
	}
	var devices struct {
		Blockdevices []storageBlock `json:"blockdevices"`
	}
	if json.Unmarshal(data, &devices) != nil || len(devices.Blockdevices) != 1 {
		return ErrRuntimeStorage
	}
	block := devices.Blockdevices[0]
	identity, err := parseBlockIdentity(block.MajorMinor)
	if err != nil || identity != file.Device || (block.Kind != "part" && block.Kind != "disk") || filepath.Dir(block.Name) != "/dev" || filepath.Clean(block.Name) != block.Name || len(block.UUID) > 128 || len(block.PartitionUUID) > 128 {
		return ErrRuntimeStorage
	}
	canonical, err := statStorageObject(block.Name)
	if err != nil || canonical != file {
		return ErrRuntimeStorage
	}
	facts.Files[block.Name] = canonical
	if block.Kind == "disk" {
		device, err := os.Stat("/sys/dev/block/" + block.MajorMinor + "/device")
		if err != nil || device == nil {
			return ErrRuntimeStorage
		}
		block.PhysicalDisk = device.IsDir()
		if !block.PhysicalDisk || block.Parent != "" {
			return ErrRuntimeStorage
		}
	} else if block.Parent == "" || filepath.Dir(block.Parent) != "/dev" || filepath.Clean(block.Parent) != block.Parent {
		return ErrRuntimeStorage
	}
	facts.Blocks[file.Device] = block
	if block.Parent != "" {
		return collectStorageBlock(ctx, enrollment, block.Parent, facts)
	}
	return nil
}

func statStorageObject(path string) (storageFile, error) {
	info, err := os.Stat(path)
	if err != nil || info == nil {
		return storageFile{}, ErrRuntimeStorage
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return storageFile{}, ErrRuntimeStorage
	}
	object := storageFile{UID: stat.Uid, Permissions: uint32(info.Mode().Perm())}
	switch {
	case info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0:
		object.Kind = "block"
		object.Device = blockIdentity{unix.Major(stat.Rdev), unix.Minor(stat.Rdev)}
	case info.Mode().IsRegular():
		object.Kind = "file"
		object.Device = blockIdentity{unix.Major(stat.Dev), unix.Minor(stat.Dev)}
		object.Inode = stat.Ino
	case info.IsDir():
		object.Kind = "directory"
		object.Device = blockIdentity{unix.Major(stat.Dev), unix.Minor(stat.Dev)}
		object.Inode = stat.Ino
	default:
		return storageFile{}, ErrRuntimeStorage
	}
	return object, nil
}

// swapon 2.42.4 has no JSON output. Native raw columns hex-escape unsafe name
// bytes; decode that one field and retain only the bounded active-swap metadata.
func parseStorageSwaps(data []byte) ([]storageSwap, error) {
	result := []storageSwap{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || len(result) >= 4 {
			return nil, ErrRuntimeStorage
		}
		name, err := strconv.Unquote(`"` + fields[0] + `"`)
		if err != nil {
			return nil, ErrRuntimeStorage
		}
		priority, err := strconv.ParseInt(fields[2], 10, 16)
		if err != nil || (fields[1] != "file" && fields[1] != "partition") {
			return nil, ErrRuntimeStorage
		}
		result = append(result, storageSwap{Name: name, Kind: fields[1], Priority: int(priority)})
	}
	return result, nil
}

func parseBlockIdentity(value string) (blockIdentity, error) {
	majorText, minorText, ok := strings.Cut(value, ":")
	if !ok {
		return blockIdentity{}, ErrRuntimeStorage
	}
	major, err := strconv.ParseUint(majorText, 10, 32)
	if err != nil {
		return blockIdentity{}, ErrRuntimeStorage
	}
	minor, err := strconv.ParseUint(minorText, 10, 32)
	if err != nil || major == 0 || fmt.Sprintf("%d:%d", major, minor) != value {
		return blockIdentity{}, ErrRuntimeStorage
	}
	return blockIdentity{uint32(major), uint32(minor)}, nil
}
