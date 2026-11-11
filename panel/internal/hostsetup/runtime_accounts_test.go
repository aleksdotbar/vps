package hostsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func accountID(value uint32) *uint32    { return &value }
func accountShell(value string) *string { return &value }

func runtimeAccountFixture() (publicAccountPolicy, publicAccountPolicy, accountSnapshot) {
	oldBash := "/nix/store/" + strings.Repeat("1", 32) + "-bash-interactive-5.2/bin/bash"
	oldNoLogin := "/nix/store/" + strings.Repeat("2", 32) + "-shadow-4.18/bin/nologin"
	source := publicAccountPolicy{
		Accounts: []publicAccount{
			{Name: "root", UID: accountID(0), Group: "root", Home: "/root", HomeMode: "700", Shell: &oldBash},
			{Name: "operator", Group: "users", ExtraGroups: []string{"wheel"}, Home: "/home/operator", HomeMode: "700", CreateHome: true, IsNormalUser: true, Shell: &oldBash},
			{Name: "service", Group: "service", Home: "/var/empty", HomeMode: "700", IsSystemUser: true, Shell: &oldNoLogin},
		},
		Groups: []publicGroup{{Name: "root", GID: accountID(0)}, {Name: "users", GID: accountID(100)},
			{Name: "wheel", GID: accountID(1), Members: []string{"operator"}}, {Name: "service"}},
	}
	candidate := publicAccountPolicy{Accounts: append([]publicAccount(nil), source.Accounts...), Groups: append([]publicGroup(nil), source.Groups...)}
	for index := range candidate.Accounts {
		if index == 2 {
			candidate.Accounts[index].Shell = accountShell("/nix/store/" + strings.Repeat("3", 32) + "-shadow-4.19.4/bin/nologin")
		} else {
			candidate.Accounts[index].Shell = accountShell("/nix/store/" + strings.Repeat("4", 32) + "-bash-interactive-5.3p9/bin/bash")
		}
	}
	observed := accountSnapshot{
		users: map[string]runtimeAccount{
			"root":      {uid: 0, gid: 0, home: "/root", shell: oldBash},
			"operator":  {uid: 1000, gid: 100, home: "/home/operator", shell: oldBash},
			"service":   {uid: 998, gid: 998, home: "/var/empty", shell: oldNoLogin},
			"unmanaged": {uid: 1001, gid: 100, home: "/home/unmanaged", shell: "/unmanaged/custom-shell"},
		},
		groups: map[string]runtimeAccountGroup{
			"root": {gid: 0}, "users": {gid: 100}, "wheel": {gid: 1, members: map[string]bool{"operator": true, "unmanaged": true}},
			"service": {gid: 998}, "unmanaged": {gid: 1001},
		},
		declaredUsers:  map[string]bool{"root": true, "operator": true, "service": true},
		declaredGroups: map[string]bool{"root": true, "users": true, "wheel": true, "service": true},
		homes:          map[string]accountHome{"/home/operator": {uid: 1000, gid: 100, mode: 0700}},
	}
	return source, candidate, observed
}

func TestRuntimeAccountsPreserveNativeIDsAndUnmanagedUsersAcrossDefaultShellVersions(t *testing.T) {
	source, candidate, observed := runtimeAccountFixture()
	if err := compareRuntimeAccounts(source, candidate, observed); err != nil {
		t.Fatalf("native null IDs, default shells, and unmanaged membership must remain supported: %v", err)
	}
	// A first clean host need not already have the selected operator. Native
	// activation allocates its IDs and creates an absent home directory.
	source.Accounts = append(source.Accounts[:1], source.Accounts[2:]...)
	delete(observed.users, "operator")
	delete(observed.declaredUsers, "operator")
	delete(observed.groups["wheel"].members, "operator")
	delete(observed.homes, "/home/operator")
	if err := compareRuntimeAccounts(source, candidate, observed); err != nil {
		t.Fatalf("new operator with absent home must not require a guessed native UID: %v", err)
	}
}

func TestRuntimeAccountsRejectIdentityAndAccessLoss(t *testing.T) {
	for _, name := range []string{"omitted user", "omitted group", "UID change", "UID collision", "existing UID alias", "GID change", "GID collision", "existing GID alias",
		"primary group", "home path", "home owner", "home mode", "declarative membership", "shell authority", "new home collision",
		"dynamic user collision", "dynamic group collision", "dynamic membership collision"} {
		t.Run(name, func(t *testing.T) {
			source, candidate, observed := runtimeAccountFixture()
			switch name {
			case "dynamic user collision":
				observed.users["panel-cloudflared"] = runtimeAccount{uid: 1002, gid: 100, home: "/home/connector", shell: "/unmanaged/custom-shell"}
			case "dynamic group collision":
				observed.groups["panel-cloudflared"] = runtimeAccountGroup{gid: 1002}
			case "dynamic membership collision":
				observed.groups["wheel"].members["panel-cloudflared"] = true
			case "omitted user":
				candidate.Accounts = candidate.Accounts[:2]
			case "omitted group":
				candidate.Groups = candidate.Groups[:3]
			case "UID change":
				candidate.Accounts[1].UID = accountID(1002)
			case "UID collision":
				candidate.Accounts = append(candidate.Accounts, publicAccount{Name: "new-service", UID: accountID(1001), Group: "users", Home: "/var/empty", HomeMode: "700"})
			case "existing UID alias":
				observed.users["unmanaged"] = runtimeAccount{uid: 1000, gid: 100, home: "/home/unmanaged", shell: "/unmanaged/custom-shell"}
			case "GID change":
				candidate.Groups[1].GID = accountID(101)
			case "GID collision":
				candidate.Groups = append(candidate.Groups, publicGroup{Name: "new-group", GID: accountID(1001)})
			case "existing GID alias":
				observed.groups["unmanaged"] = runtimeAccountGroup{gid: 998}
			case "primary group":
				candidate.Accounts[1].Group = "wheel"
			case "home path":
				candidate.Accounts[1].Home = "/home/replaced"
			case "home owner":
				observed.homes["/home/operator"] = accountHome{uid: 1001, gid: 100, mode: 0700}
			case "home mode":
				observed.homes["/home/operator"] = accountHome{uid: 1000, gid: 100, mode: 0755}
			case "declarative membership":
				candidate.Groups[2].Members = nil
			case "shell authority":
				candidate.Accounts[2].Shell = candidate.Accounts[0].Shell
			case "new home collision":
				delete(observed.users, "operator")
				source.Accounts = append(source.Accounts[:1], source.Accounts[2:]...)
			}
			if err := compareRuntimeAccounts(source, candidate, observed); !errors.Is(err, ErrRuntimeAccounts) {
				t.Fatalf("unsafe %s accepted: %v", name, err)
			}
		})
	}
}

func TestRuntimeAccountsRequireBoundMutableCandidateMetadata(t *testing.T) {
	_, policy, _ := runtimeAccountFixture()
	mutable := true
	build := localBuild{InstallationID: "installation", ServerID: "server", Role: "panel", SourceSHA: strings.Repeat("a", 40),
		InputSHA256: strings.Repeat("b", 64), MachineSHA256: strings.Repeat("c", 64)}
	candidate := accountCandidate{SchemaVersion: 1, InstallationID: build.InstallationID, ServerID: build.ServerID, Role: string(build.Role),
		Stage: "enrollment", SourceSHA: build.SourceSHA, InputSHA256: build.InputSHA256, MachineSHA256: build.MachineSHA256,
		MutableUsers: &mutable, publicAccountPolicy: policy}
	path := filepath.Join(t.TempDir(), "panel-host.json")
	for _, name := range []string{"valid", "immutable users", "wrong stage", "wrong input", "credential field"} {
		t.Run(name, func(t *testing.T) {
			value := candidate
			switch name {
			case "immutable users":
				flag := false
				value.MutableUsers = &flag
			case "wrong stage":
				value.Stage = "final"
			case "wrong input":
				value.InputSHA256 = strings.Repeat("d", 64)
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if name == "credential field" {
				data = append([]byte(`{"password":"SENSITIVE_SENTINEL",`), data[1:]...)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = readAccountCandidate(context.Background(), path, "enrollment", build)
			if name == "valid" && err != nil {
				t.Fatal(err)
			}
			if name != "valid" && (!errors.Is(err, ErrRuntimeAccounts) || strings.Contains(err.Error(), "SENSITIVE_SENTINEL")) {
				t.Fatalf("unbound or unsafe metadata accepted/reflected: %v", err)
			}
		})
	}
}

func TestRuntimeAccountsNeverAdoptPrivilegedManagementIdentities(t *testing.T) {
	for _, name := range []string{"panel", "panel-runner"} {
		t.Run(name, func(t *testing.T) {
			source, candidate, observed := runtimeAccountFixture()
			account := publicAccount{Name: name, Group: name, Home: "/var/empty", HomeMode: "700", IsSystemUser: true}
			candidate.Accounts = append(candidate.Accounts, account)
			candidate.Groups = append(candidate.Groups, publicGroup{Name: name})
			observed.users[name] = runtimeAccount{uid: 997, gid: 997, home: "/var/empty"}
			observed.groups[name] = runtimeAccountGroup{gid: 997}
			if err := compareRuntimeAccounts(source, candidate, observed); !errors.Is(err, ErrRuntimeAccounts) {
				t.Fatalf("adopted unmanaged %s identity: %v", name, err)
			}
			observed.declaredUsers[name] = true
			if err := compareRuntimeAccounts(source, candidate, observed); err != nil {
				t.Fatalf("refused existing unprivileged managed %s: %v", name, err)
			}
			observed.groups["wheel"].members[name] = true
			if err := compareRuntimeAccounts(source, candidate, observed); !errors.Is(err, ErrRuntimeAccounts) {
				t.Fatalf("retained unexpected wheel membership for %s: %v", name, err)
			}
		})
	}
}

func TestRuntimeAccountReadsRejectNonregularAndOversizedState(t *testing.T) {
	root := t.TempDir()
	paths := accountRuntimeFiles{passwd: filepath.Join(root, "passwd"), group: filepath.Join(root, "group"),
		declaredUsers: filepath.Join(root, "declarative-users"), declaredGroups: filepath.Join(root, "declarative-groups")}
	for path, data := range map[string]string{paths.passwd: "root:x:0:0:ignored description:/root:/native/bash\n", paths.group: "root:x:0:\n",
		paths.declaredUsers: "root", paths.declaredGroups: "root"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := readRuntimeAccounts(context.Background(), paths)
	if err != nil || observed.users["root"].uid != 0 || !observed.declaredUsers["root"] || !observed.declaredGroups["root"] {
		t.Fatalf("public native state rejected: %v", err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAccountFile(context.Background(), fifo, 1024, false); !errors.Is(err, ErrRuntimeAccounts) {
		t.Fatalf("nonregular account state accepted: %v", err)
	}
	if _, err := readAccountFile(context.Background(), paths.passwd, 8, false); !errors.Is(err, ErrRuntimeAccounts) {
		t.Fatalf("oversized account state accepted: %v", err)
	}
}
