package hostsetup

import (
	"context"
	"errors"
	"path/filepath"
)

var ErrRuntimeAccounts = errors.New("runtime accounts or generation identity are unreadable or do not match")

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

func resolveAccountShell(shell string) string {
	if resolved, err := filepath.EvalSymlinks(shell); err == nil {
		return resolved
	}
	return shell
}
