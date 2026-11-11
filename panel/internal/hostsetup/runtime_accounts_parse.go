package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type accountRuntimeFiles struct {
	passwd, group, declaredUsers, declaredGroups string
}

func readAccountFile(ctx context.Context, path string, limit int64, optional bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrRuntimeAccounts
	}
	file := os.NewFile(uintptr(fd), "account-policy")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrRuntimeAccounts
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrRuntimeAccounts
	}
	data, err := io.ReadAll(io.LimitReader(setupReader{ctx, file}, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrRuntimeAccounts
	}
	return data, nil
}

func decodeAccountMetadata(data []byte, candidate *accountCandidate) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(candidate) != nil {
		return ErrRuntimeAccounts
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrRuntimeAccounts
	}
	return nil
}

func indexAccountPolicy(policy publicAccountPolicy) (map[string]publicAccount, map[string]publicGroup, error) {
	if policy.Accounts == nil || policy.Groups == nil || len(policy.Accounts) > 4096 || len(policy.Groups) > 4096 {
		return nil, nil, ErrRuntimeAccounts
	}
	users := make(map[string]publicAccount, len(policy.Accounts))
	groups := make(map[string]publicGroup, len(policy.Groups))
	for _, user := range policy.Accounts {
		_, duplicate := users[user.Name]
		if !accountName(user.Name) || !accountName(user.Group) || duplicate || !filepath.IsAbs(user.Home) || filepath.Clean(user.Home) != user.Home ||
			(user.Shell != nil && (!filepath.IsAbs(*user.Shell) || filepath.Clean(*user.Shell) != *user.Shell)) {
			return nil, nil, ErrRuntimeAccounts
		}
		if _, err := strconv.ParseUint(user.HomeMode, 8, 12); err != nil {
			return nil, nil, ErrRuntimeAccounts
		}
		users[user.Name] = user
	}
	for _, group := range policy.Groups {
		_, duplicate := groups[group.Name]
		if !accountName(group.Name) || duplicate {
			return nil, nil, ErrRuntimeAccounts
		}
		for _, member := range group.Members {
			if !accountName(member) {
				return nil, nil, ErrRuntimeAccounts
			}
		}
		groups[group.Name] = group
	}
	for _, user := range users {
		if _, exists := groups[user.Group]; !exists {
			return nil, nil, ErrRuntimeAccounts
		}
	}
	return users, groups, nil
}

func readRuntimeAccounts(ctx context.Context, paths accountRuntimeFiles) (accountSnapshot, error) {
	observed := accountSnapshot{
		users: make(map[string]runtimeAccount), groups: make(map[string]runtimeAccountGroup), homes: make(map[string]accountHome),
	}
	passwd, err := readAccountFile(ctx, paths.passwd, 1<<20, false)
	if err != nil {
		return observed, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(passwd), "\n"), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 7 || !accountName(fields[0]) || len(observed.users) >= 4096 {
			return observed, ErrRuntimeAccounts
		}
		if _, duplicate := observed.users[fields[0]]; duplicate {
			return observed, ErrRuntimeAccounts
		}
		uid, uidError := strconv.ParseUint(fields[2], 10, 32)
		gid, gidError := strconv.ParseUint(fields[3], 10, 32)
		if uidError != nil || gidError != nil {
			return observed, ErrRuntimeAccounts
		}
		// Password placeholders and descriptions are never copied into evidence.
		observed.users[fields[0]] = runtimeAccount{uid: uint32(uid), gid: uint32(gid), home: fields[5], shell: resolveAccountShell(fields[6])}
	}
	groupData, err := readAccountFile(ctx, paths.group, 1<<20, false)
	if err != nil {
		return observed, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(groupData), "\n"), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 4 || !accountName(fields[0]) || len(observed.groups) >= 4096 {
			return observed, ErrRuntimeAccounts
		}
		if _, duplicate := observed.groups[fields[0]]; duplicate {
			return observed, ErrRuntimeAccounts
		}
		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return observed, ErrRuntimeAccounts
		}
		members := make(map[string]bool)
		if fields[3] != "" {
			for _, member := range strings.Split(fields[3], ",") {
				if !accountName(member) {
					return observed, ErrRuntimeAccounts
				}
				members[member] = true
			}
		}
		observed.groups[fields[0]] = runtimeAccountGroup{gid: uint32(gid), members: members}
	}
	observed.declaredUsers, err = readDeclarativeNames(ctx, paths.declaredUsers)
	if err != nil {
		return observed, err
	}
	observed.declaredGroups, err = readDeclarativeNames(ctx, paths.declaredGroups)
	return observed, err
}

func readDeclarativeNames(ctx context.Context, path string) (map[string]bool, error) {
	data, err := readAccountFile(ctx, path, maxInspectionBytes, true)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool)
	for _, name := range strings.Fields(string(data)) {
		if !accountName(name) || names[name] || len(names) >= 4096 {
			return nil, ErrRuntimeAccounts
		}
		names[name] = true
	}
	return names, nil
}

func accountName(name string) bool {
	return name != "" && len(name) <= 256 && !strings.ContainsAny(name, ":\x00\n\r\t ")
}
