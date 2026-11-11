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
