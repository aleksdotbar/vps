package hostsetup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

func inspectRuntimeSSH(ctx context.Context, enrollment, systemPath string, input foundation.Input, packet []byte) (sshObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var source sourcePacket
	var declared struct {
		Files []string `json:"authorized_keys_files"`
	}
	if json.Unmarshal(packet, &source) != nil || json.Unmarshal(source.SSH, &declared) != nil || len(declared.Files) == 0 || len(declared.Files) > 3 {
		return sshObservation{}, ErrRuntimeSSH
	}
	for index, pattern := range declared.Files {
		if slices.Contains(declared.Files[:index], pattern) {
			return sshObservation{}, ErrRuntimeSSH
		}
	}
	tools := networkTools{systemctl: enrollment + "/sw/bin/systemctl"}
	unit, err := observeNetworkUnit(ctx, tools, "sshd.service")
	if err != nil || unit.active != "active" || unit.load != "loaded" || unit.reload != "no" ||
		!sameNativeFile(unit.fragment, systemPath+"/etc/systemd/system/sshd.service") || !nativeUnitDropins(strings.Fields(unit.dropins), systemPath+"/etc/systemd/system/sshd.service.d") {
		return sshObservation{}, ErrRuntimeSSH
	}
	pid, err := currentSSHPID(ctx, tools.systemctl, systemPath)
	if err != nil {
		return sshObservation{}, err
	}
	config, err := readNativeSSHConfig(ctx, systemPath)
	if err != nil || !admittedSSHConfig(config) {
		return sshObservation{}, ErrRuntimeSSH
	}
	dumps := []string{}
	for _, user := range []string{"root", input.Operator.Name} {
		// -G exits before host-key loading. -T is required by upstream for -C,
		// whose user is a known typed fact; arbitrary address Match is refused.
		data, err := runtimeProbe(ctx, enrollment+"/sw/bin/sshd", "-G", "-T", "-C", "user="+user, "-f", "/etc/ssh/sshd_config")
		if err != nil {
			return sshObservation{}, ErrRuntimeSSH
		}
		parsed, err := parseSSHEffective(data)
		if err != nil || confirmSSHPolicy(parsed, input, declared.Files) != nil {
			return sshObservation{}, ErrRuntimeSSH
		}
		dumps = append(dumps, string(data))
	}
	accounts, err := readRuntimeAccounts(ctx, accountRuntimeFiles{passwd: "/etc/passwd", group: "/etc/group", declaredUsers: "/var/lib/nixos/declarative-users", declaredGroups: "/var/lib/nixos/declarative-groups"})
	if err != nil {
		return sshObservation{}, ErrRuntimeSSH
	}
	files := []sshAuthorityFile{}
	for _, user := range []string{"root", input.Operator.Name} {
		account, exists := accounts.users[user]
		if user == "root" && (!exists || account.uid != 0 || account.home != "/root") {
			return sshObservation{}, ErrRuntimeSSH
		}
		home := "/home/" + user
		if user == "root" {
			home = "/root"
		}
		if exists && account.home != home {
			return sshObservation{}, ErrRuntimeSSH
		}
		for _, entry := range []struct{ kind, pattern, path string }{
			{"rendered", "/etc/ssh/authorized_keys.d/%u", "/etc/ssh/authorized_keys.d/" + user},
			{"home", "%h/.ssh/authorized_keys", home + "/.ssh/authorized_keys"},
			{"runtime", "/var/lib/panel-access/%u/authorized_keys", "/var/lib/panel-access/" + user + "/authorized_keys"},
		} {
			file := sshAuthorityFile{User: user, Source: entry.kind, Path: entry.path, Active: exists && slices.Contains(declared.Files, entry.pattern)}
			switch entry.kind {
			case "rendered":
				file, err = readRenderedSSHKeys(ctx, file, systemPath+"/etc/ssh/authorized_keys.d/"+user)
			case "home":
				if exists {
					file, err = readHomeSSHKeys(ctx, file, home, account.uid)
				}
			case "runtime":
				file, err = readRuntimeSSHKeys(ctx, file, user)
			}
			if err != nil {
				return sshObservation{}, ErrRuntimeSSH
			}
			if file.Active && len(file.Bytes) != 0 {
				if err := confirmSSHReadable(ctx, enrollment+"/sw/bin/test", file.Path, user, accounts); err != nil {
					return sshObservation{}, err
				}
			}
			files = append(files, file)
		}
	}
	files, err = admitSSHAuthority(input, files)
	if err != nil {
		return sshObservation{}, err
	}
	after, err := readNativeSSHConfig(ctx, systemPath)
	if err != nil || string(after) != string(config) {
		return sshObservation{}, ErrRuntimeSSH
	}
	afterPID, err := currentSSHPID(ctx, tools.systemctl, systemPath)
	if err != nil || afterPID != pid {
		return sshObservation{}, ErrRuntimeSSH
	}
	configDigest := sha256.Sum256(config)
	encoded, err := json.Marshal(dumps)
	if err != nil {
		return sshObservation{}, ErrRuntimeSSH
	}
	effectiveDigest := sha256.Sum256(encoded)
	return sshObservation{ConfigSHA256: hex.EncodeToString(configDigest[:]), EffectiveSHA256: hex.EncodeToString(effectiveDigest[:]), Files: files}, nil
}

func confirmSSHReadable(ctx context.Context, executable, path, user string, accounts accountSnapshot) error {
	account, exists := accounts.users[user]
	if !exists || len(accounts.groups) > 4096 {
		return ErrRuntimeSSH
	}
	groups := []uint32{}
	for _, group := range accounts.groups {
		if group.members[user] && group.gid != account.gid {
			groups = append(groups, group.gid)
		}
	}
	slices.Sort(groups)
	groups = slices.Compact(groups)
	bounded, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
	defer cancel()
	process := nativeCommand(bounded, executable, "-r", path)
	// The child's complete identity comes from the bounded public account
	// snapshot. Never inherit root's groups or evaluate filesystem ACLs ourselves.
	process.SysProcAttr.Credential = &syscall.Credential{Uid: account.uid, Gid: account.gid, Groups: groups}
	if process.Run() != nil {
		return ErrRuntimeSSH
	}
	return nil
}

func currentSSHPID(ctx context.Context, systemctl, systemPath string) (uint32, error) {
	data, err := runtimeProbe(ctx, systemctl, "show", "--property=MainPID", "--value", "--", "sshd.service")
	if err != nil {
		return 0, ErrRuntimeSSH
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil || pid == 0 || !sameNativeFile("/proc/"+strconv.FormatUint(pid, 10)+"/exe", systemPath+"/sw/bin/sshd") {
		return 0, ErrRuntimeSSH
	}
	return uint32(pid), nil
}

func readNativeSSHConfig(ctx context.Context, systemPath string) ([]byte, error) {
	path := "/etc/ssh/sshd_config"
	if !sameNativeFile(path, systemPath+"/etc/ssh/sshd_config") {
		return nil, ErrRuntimeSSH
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !strings.HasPrefix(resolved, "/nix/store/") {
		return nil, ErrRuntimeSSH
	}
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return nil, ErrRuntimeSSH
	}
	defer func() { _ = root.Close() }()
	file, err := privatefile.OpenNoFollow(root, filepath.Base(resolved), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrRuntimeSSH
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || !sshOwned(info, 0) {
		return nil, ErrRuntimeSSH
	}
	data, err := io.ReadAll(io.LimitReader(setupReader{ctx, file}, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, ErrRuntimeSSH
	}
	return data, nil
}

func sshOwned(info os.FileInfo, uid uint32) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && (owner.Uid == 0 || owner.Uid == uid) && info.Mode().Perm()&0022 == 0
}
