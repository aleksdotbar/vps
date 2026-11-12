package installation

import (
	"context"
	"io"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const StateDirectory = "/var/lib/panel"
const MasterKeyFile = "/var/lib/panel-key/master.key"
const runtimeDirectory = "/run/panel/ssh"

func CheckRuntimeUser() error {
	account, err := user.Lookup("panel")
	if err != nil || account == nil {
		return ErrIdentity
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 || strconv.Itoa(os.Geteuid()) != strconv.FormatUint(uid, 10) || strconv.Itoa(os.Getuid()) != strconv.FormatUint(uid, 10) {
		return ErrIdentity
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil || strconv.Itoa(os.Getegid()) != strconv.FormatUint(gid, 10) {
		return ErrIdentity
	}
	groups, err := os.Getgroups()
	if err != nil {
		return ErrIdentity
	}
	for _, group := range groups {
		if strconv.Itoa(group) != strconv.FormatUint(gid, 10) {
			return ErrIdentity
		}
	}
	return nil
}

// Inherited SSH stdin must be rewrapped into Go's poller before deadlines can
// interrupt it. Regular input is accepted only as a same-user protected file.
func ReadHandoff(parent context.Context, input *os.File) (Handoff, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	info, err := input.Stat()
	if err != nil {
		return Handoff{}, ErrInput
	}
	var source *os.File
	if info.Mode().IsRegular() {
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int64(owner.Uid) != int64(os.Geteuid()) || info.Mode().Perm() != 0600 || info.Size() > MaxHandoffBytes {
			return Handoff{}, ErrInput
		}
		source = input
	} else {
		fd, err := unix.FcntlInt(input.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return Handoff{}, ErrInput
		}
		if unix.SetNonblock(fd, true) != nil {
			_ = unix.Close(fd)
			return Handoff{}, ErrInput
		}
		source = os.NewFile(uintptr(fd), "panel-installation-input")
		if source == nil {
			_ = unix.Close(fd)
			return Handoff{}, ErrInput
		}
		defer func() { _ = source.Close() }()
		deadline, _ := ctx.Deadline()
		if source.SetReadDeadline(deadline) != nil {
			return Handoff{}, ErrInput
		}
		stop := context.AfterFunc(ctx, func() { _ = source.Close() })
		defer stop()
	}
	data, err := io.ReadAll(io.LimitReader(source, MaxHandoffBytes+1))
	defer clear(data)
	if err != nil || ctx.Err() != nil || len(data) > MaxHandoffBytes {
		return Handoff{}, ErrInput
	}
	return Parse(data)
}

func rootPublic(path string, maximum int64) ([]byte, error) {
	// The only caller passes the fixed panel-runner authorized_keys path.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) // #nosec G304 -- fixed native public metadata path; root ownership checked before reads.
	if err != nil {
		return nil, ErrIdentity
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, ErrIdentity
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, ErrIdentity
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrIdentity
	}
	return data, nil
}

// confirmInstalled requires root to have published exactly this management key
// for panel-runner. The host identity itself is proven by the helper probe.
func confirmInstalled(input Handoff) error {
	public, err := rootPublic("/var/lib/panel-access/panel-runner/authorized_keys", 1024)
	if err != nil {
		return err
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey(public)
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || len(options) != 1 || options[0] != "restrict" || len(rest) != 0 || ssh.FingerprintSHA256(key) != input.ManagementFingerprint {
		return ErrIdentity
	}
	return nil
}
