package hostsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
)

var ErrRuntimeAccounts = errors.New("runtime accounts do not preserve existing local identities")

type publicAccount struct {
	Name         string   `json:"name"`
	UID          *uint32  `json:"uid"`
	Group        string   `json:"group"`
	ExtraGroups  []string `json:"extraGroups"`
	Home         string   `json:"home"`
	CreateHome   bool     `json:"createHome"`
	HomeMode     string   `json:"homeMode"`
	IsNormalUser bool     `json:"isNormalUser"`
	IsSystemUser bool     `json:"isSystemUser"`
	Shell        *string  `json:"shell"`
}

type publicGroup struct {
	Name    string   `json:"name"`
	GID     *uint32  `json:"gid"`
	Members []string `json:"members"`
}

type publicAccountPolicy struct {
	Accounts []publicAccount `json:"accounts"`
	Groups   []publicGroup   `json:"groups"`
}

type accountCandidate struct {
	SchemaVersion  int    `json:"schema_version"`
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	Role           string `json:"role"`
	Stage          string `json:"stage"`
	SourceSHA      string `json:"source_sha"`
	InputSHA256    string `json:"input_sha256"`
	MachineSHA256  string `json:"machine_sha256"`
	MutableUsers   *bool  `json:"mutable_users"`
	publicAccountPolicy
}

type runtimeAccount struct {
	uid, gid uint32
	home     string
	shell    string
}

type runtimeAccountGroup struct {
	gid     uint32
	members map[string]bool
}

type accountHome struct {
	uid, gid, mode uint32
}

type accountSnapshot struct {
	users          map[string]runtimeAccount
	groups         map[string]runtimeAccountGroup
	declaredUsers  map[string]bool
	declaredGroups map[string]bool
	homes          map[string]accountHome
}

// Native mutableUsers keeps unmanaged identities, but deletes omitted previously
// declared identities and replaces memberships of previously declared users.
// The native allocator remains responsible for new null UID/GID assignments.
func inspectRuntimeAccounts(ctx context.Context, build localBuild, packet []byte) error {
	var source sourcePacket
	if len(packet) > maxInspectionBytes || json.Unmarshal(packet, &source) != nil {
		return ErrRuntimeAccounts
	}
	var policy publicAccountPolicy
	if json.Unmarshal(source.Accounts, &policy.Accounts) != nil || json.Unmarshal(source.Groups, &policy.Groups) != nil {
		return ErrRuntimeAccounts
	}
	resolvePolicyAccountShells(&policy)
	observed, err := readRuntimeAccounts(ctx, accountRuntimeFiles{
		passwd: "/etc/passwd", group: "/etc/group",
		declaredUsers: "/var/lib/nixos/declarative-users", declaredGroups: "/var/lib/nixos/declarative-groups",
	})
	if err != nil {
		return err
	}
	for _, stage := range []struct{ name, path string }{{"enrollment", build.EnrollmentSystem}, {"final", build.FinalSystem}} {
		candidate, err := readAccountCandidate(ctx, stage.path+"/etc/panel-host.json", stage.name, build)
		if err != nil {
			return err
		}
		resolvePolicyAccountShells(&candidate.publicAccountPolicy)
		if err := observeAccountHomes(ctx, candidate.Accounts, observed.homes); err != nil {
			return err
		}
		if err := compareRuntimeAccounts(policy, candidate.publicAccountPolicy, observed); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func readAccountCandidate(ctx context.Context, path, stage string, build localBuild) (accountCandidate, error) {
	data, err := readAccountFile(ctx, path, maxInspectionBytes, false)
	if err != nil {
		return accountCandidate{}, err
	}
	var candidate accountCandidate
	if decodeAccountMetadata(data, &candidate) != nil || candidate.SchemaVersion != 1 ||
		candidate.InstallationID != build.InstallationID || candidate.ServerID != build.ServerID ||
		candidate.Role != string(build.Role) || candidate.Stage != stage || candidate.SourceSHA != build.SourceSHA ||
		candidate.InputSHA256 != build.InputSHA256 || candidate.MachineSHA256 != build.MachineSHA256 ||
		candidate.MutableUsers == nil || !*candidate.MutableUsers {
		return accountCandidate{}, ErrRuntimeAccounts
	}
	if _, _, err := indexAccountPolicy(candidate.publicAccountPolicy); err != nil {
		return accountCandidate{}, err
	}
	return candidate, nil
}

func compareRuntimeAccounts(source, candidate publicAccountPolicy, observed accountSnapshot) error {
	users, groups, err := indexAccountPolicy(candidate)
	if err != nil {
		return err
	}
	sourceUsers, sourceGroups, err := indexAccountPolicy(source)
	if err != nil {
		return err
	}
	usedUIDs := make(map[uint32]string, len(observed.users))
	usedGIDs := make(map[uint32]string, len(observed.groups))
	for name, user := range observed.users {
		// DynamicUser reuses a matching static account. Mutable NixOS users may
		// retain unmanaged identities, so the native declaration guard is not enough.
		if name == "panel-cloudflared" {
			return ErrRuntimeAccounts
		}
		if _, duplicate := usedUIDs[user.uid]; duplicate {
			usedUIDs[user.uid] = ""
		} else {
			usedUIDs[user.uid] = name
		}
	}
	for name, group := range observed.groups {
		if name == "panel-cloudflared" || group.members["panel-cloudflared"] {
			return ErrRuntimeAccounts
		}
		if _, duplicate := usedGIDs[group.gid]; duplicate {
			usedGIDs[group.gid] = ""
		} else {
			usedGIDs[group.gid] = name
		}
	}
	for name := range observed.declaredUsers {
		if _, exists := users[name]; !exists {
			return ErrRuntimeAccounts
		}
	}
	for name := range observed.declaredGroups {
		if _, exists := groups[name]; !exists {
			return ErrRuntimeAccounts
		}
	}
	for name := range sourceUsers {
		if _, exists := observed.users[name]; exists {
			if _, retained := users[name]; !retained {
				return ErrRuntimeAccounts
			}
		}
	}
	for name := range sourceGroups {
		if _, exists := observed.groups[name]; exists {
			if _, retained := groups[name]; !retained {
				return ErrRuntimeAccounts
			}
		}
	}
	for name, group := range groups {
		if existing, exists := observed.groups[name]; exists && usedGIDs[existing.gid] != name {
			return ErrRuntimeAccounts
		}
		if existing, exists := observed.groups[name]; exists && group.GID != nil && *group.GID != existing.gid {
			return ErrRuntimeAccounts
		}
		if group.GID != nil {
			if owner, used := usedGIDs[*group.GID]; used && owner != name {
				return ErrRuntimeAccounts
			}
		}
		if existing, exists := observed.groups[name]; exists {
			members := make(map[string]bool, len(group.Members))
			for _, member := range group.Members {
				members[member] = true
			}
			for member := range existing.members {
				if observed.declaredUsers[member] && !members[member] {
					return ErrRuntimeAccounts
				}
			}
		}
	}
	for name, user := range users {
		existing, exists := observed.users[name]
		if exists && (name == "panel" || name == "panel-runner") {
			// Native mutableUsers can retain supplementary groups when adopting an
			// unmanaged account. Never adopt another service's privileged identity.
			if !observed.declaredUsers[name] {
				return ErrRuntimeAccounts
			}
			for groupName, group := range observed.groups {
				if groupName != user.Group && group.members[name] {
					return ErrRuntimeAccounts
				}
			}
		}
		if exists && usedUIDs[existing.uid] != name {
			return ErrRuntimeAccounts
		}
		if user.UID != nil {
			if owner, used := usedUIDs[*user.UID]; used && owner != name {
				return ErrRuntimeAccounts
			}
		}
		gid, knownGID := candidateGroupID(user.Group, groups, observed.groups)
		if !knownGID && exists {
			return ErrRuntimeAccounts
		}
		if exists {
			if (user.UID != nil && *user.UID != existing.uid) || gid != existing.gid || user.Home != existing.home {
				return ErrRuntimeAccounts
			}
			if previous, declared := sourceUsers[name]; declared {
				if previous.IsNormalUser != user.IsNormalUser || previous.IsSystemUser != user.IsSystemUser ||
					(previous.Shell != nil && *previous.Shell != existing.shell) {
					return ErrRuntimeAccounts
				}
			}
			if user.Shell != nil && !compatibleAccountShell(existing.shell, *user.Shell) {
				return ErrRuntimeAccounts
			}
		}
		if home, present := observed.homes[user.Home]; user.CreateHome && present {
			uid := user.UID
			if exists {
				uid = &existing.uid
			}
			mode, err := strconv.ParseUint(user.HomeMode, 8, 12)
			if err != nil || uid == nil || !knownGID || home.uid != *uid || home.gid != gid || home.mode != uint32(mode) {
				return ErrRuntimeAccounts
			}
		}
	}
	return nil
}

func candidateGroupID(name string, groups map[string]publicGroup, existing map[string]runtimeAccountGroup) (uint32, bool) {
	if group, exists := existing[name]; exists {
		return group.gid, true
	}
	if group, exists := groups[name]; exists && group.GID != nil {
		return *group.GID, true
	}
	return 0, false
}

func observeAccountHomes(ctx context.Context, users []publicAccount, homes map[string]accountHome) error {
	for _, user := range users {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !user.CreateHome {
			continue
		}
		info, err := os.Lstat(user.Home)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return ErrRuntimeAccounts
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return ErrRuntimeAccounts
		}
		homes[user.Home] = accountHome{uid: stat.Uid, gid: stat.Gid, mode: stat.Mode & 07777}
	}
	return nil
}

func resolveAccountShell(shell string) string {
	if resolved, err := filepath.EvalSymlinks(shell); err == nil {
		return resolved
	}
	return shell
}

func resolvePolicyAccountShells(policy *publicAccountPolicy) {
	for index := range policy.Accounts {
		if shell := policy.Accounts[index].Shell; shell != nil {
			resolved := resolveAccountShell(*shell)
			policy.Accounts[index].Shell = &resolved
		}
	}
}

var defaultBashShell = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-bash-interactive-[0-9][A-Za-z0-9.+_-]*/bin/bash$`)
var defaultNoLoginShell = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-shadow-[0-9][A-Za-z0-9.+_-]*/bin/nologin$`)

func compatibleAccountShell(current, candidate string) bool {
	if current == candidate {
		return true
	}
	// Locked native defaults use bash-interactive for login shells and shadow's
	// nologin for service users. Different trusted package versions retain those
	// semantics; arbitrary same-named executables do not establish compatibility.
	return (defaultBashShell.MatchString(current) && defaultBashShell.MatchString(candidate)) ||
		(defaultNoLoginShell.MatchString(current) && defaultNoLoginShell.MatchString(candidate))
}
