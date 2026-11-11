package hostsetup

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentAccessPlanKeepsRestrictedBytesAndRejectsAuthorityReuse(t *testing.T) {
	input, _, _ := sshFixture(t)
	management := []byte(otherAuthorityKey(t) + "\n")
	original := []byte("# existing authority\r\nfrom=\"192.0.2.0/24\",no-pty " + otherAuthorityKey(t) + "\r\n")
	files := []sshAuthorityFile{{User: input.Operator.Name, Source: "rendered", Active: true, Present: true, Bytes: original}, {User: input.Operator.Name, Source: "home", Active: true, Present: true, Bytes: []byte(input.Operator.PublicKey + "\n")}, {User: "root", Source: "runtime", Active: true}}
	plan, key, fingerprint, err := enrollmentAccessPlan(management, input, sshObservation{Files: files})
	if err != nil || len(plan) != 2 || key == "" || !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Fatalf("valid access retention refused: %v", err)
	}
	var retained []byte
	for _, file := range plan {
		if file.User == input.Operator.Name {
			retained = file.Bytes
		}
	}
	if len(retained) == 0 || !bytes.Equal(retained, original) {
		t.Fatal("authorized-key restrictions/comments/line endings changed")
	}
	retained[0] = '!'
	if original[0] != '#' {
		t.Fatal("plan aliases mutable observation")
	}
	for _, name := range []string{"restricted management key", "reused human key", "inactive rendered key", "unmanaged runtime file", "oversized key"} {
		t.Run(name, func(t *testing.T) {
			public := management
			changed := append([]sshAuthorityFile(nil), files...)
			switch name {
			case "restricted management key":
				public = append([]byte("restrict "), management...)
			case "reused human key":
				public = []byte(input.Operator.PublicKey + "\n")
			case "inactive rendered key":
				changed[0].Active = false
			case "unmanaged runtime file":
				changed[2].Present = true
			case "oversized key":
				public = make([]byte, 1025)
			}
			if _, _, _, err := enrollmentAccessPlan(public, input, sshObservation{Files: changed}); !errors.Is(err, ErrEnrollment) {
				t.Fatalf("unsafe authority admitted: %v", err)
			}
		})
	}
}

func TestEnrollmentRecoveryNeverOwnsExternalGenerations(t *testing.T) {
	intent := enrollmentIntent{SourceSystem: "old", EnrollmentSystem: "enrollment"}
	for _, test := range []struct {
		name  string
		links enrollmentLinks
		owned bool
	}{
		{"unstarted", enrollmentLinks{"old", "old", "old"}, true},
		{"profile changed before switch", enrollmentLinks{"old", "enrollment", "old"}, true},
		{"enrollment switched", enrollmentLinks{"enrollment", "enrollment", "old"}, true},
		{"rebooted enrollment", enrollmentLinks{"enrollment", "enrollment", "enrollment"}, true},
		{"external profile", enrollmentLinks{"enrollment", "external", "old"}, false},
		{"external current", enrollmentLinks{"external", "enrollment", "old"}, false},
		{"external boot", enrollmentLinks{"old", "old", "external"}, false},
		{"missing observation", enrollmentLinks{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if enrollmentOwnsLinks(intent, test.links) != test.owned {
				t.Fatal("recovery authority differs from observed generations")
			}
		})
	}
	if enrollmentAt(intent, enrollmentLinks{"old", "enrollment", "old"}, "enrollment") {
		t.Fatal("profile update alone reported activation")
	}
}

func TestEnrollmentPublicationAndRecoveryRejectUnmanagedContent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires explicit root-only temporary native fixture scope")
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	intent := enrollmentIntent{Files: []enrollmentKeyFile{{"operator", []byte("# exact public retained bytes\r\n")}, {"panel-runner", []byte("restrict public-fixture\n")}}}
	if stageEnrollmentAccess(t.Context(), root, intent) != nil {
		t.Fatal("private staging failed")
	}
	if parent.Mkdir("panel-access", 0755) != nil {
		t.Fatal("fixture creation failed")
	}
	if err := publishEnrollmentAccess(t.Context(), root, intent, parent); !errors.Is(err, ErrEnrollment) {
		t.Fatalf("unmanaged destination replaced: %v", err)
	}
	if parent.Remove("panel-access") != nil {
		t.Fatal("fixture removal failed")
	}
	if err := publishEnrollmentAccess(t.Context(), root, intent, parent); err != nil {
		t.Fatal(err)
	}
	if checkEnrollmentAccess(t.Context(), parent, "panel-access", intent.Files, false) != nil {
		t.Fatal("published exact bytes/ownership not observed")
	}
	if err := publishEnrollmentAccess(t.Context(), root, intent, parent); !errors.Is(err, ErrEnrollment) {
		t.Fatal("duplicate publication did not refuse")
	}
	tree, err := parent.OpenRoot("panel-access")
	if err != nil {
		t.Fatal(err)
	}
	if tree.WriteFile("unmanaged", []byte("do not erase"), 0600) != nil {
		t.Fatal("fixture write failed")
	}
	if err := removeEnrollmentAccess(t.Context(), parent, intent); !errors.Is(err, ErrEnrollment) {
		t.Fatalf("unmanaged content deleted: %v", err)
	}
	if tree.Remove("unmanaged") != nil {
		t.Fatal("fixture removal failed")
	}
	_ = tree.Close()
	if err := removeEnrollmentAccess(t.Context(), parent, intent); err != nil {
		t.Fatal(err)
	}
	if err := removeEnrollmentAccess(t.Context(), parent, intent); err != nil {
		t.Fatalf("interrupted removal replay refused: %v", err)
	}
}

func TestEnrollmentNeverEnablesExcludedBootstrapAuthority(t *testing.T) {
	input, settings, _ := sshFixture(t)
	accounts := accountSnapshot{users: map[string]runtimeAccount{"root": {}, input.Operator.Name: {}}}
	settings["allowusers"] = []string{input.Operator.Name}
	if err := confirmEnrollmentLoginUsers(settings, input, accounts); !errors.Is(err, ErrEnrollment) {
		t.Fatal("excluded root bootstrap became allowed")
	}
	settings["allowusers"] = []string{"root"}
	if err := confirmEnrollmentLoginUsers(settings, input, accounts); !errors.Is(err, ErrEnrollment) {
		t.Fatal("existing excluded operator became allowed")
	}
	delete(accounts.users, input.Operator.Name)
	if err := confirmEnrollmentLoginUsers(settings, input, accounts); err != nil {
		t.Fatal("explicit creation of new reviewed operator refused")
	}
	input.Bootstrap.RootLogin = "no"
	settings["allowusers"] = []string{input.Operator.Name}
	if err := confirmEnrollmentLoginUsers(settings, input, accounts); err != nil {
		t.Fatal("disabled root treated as bootstrap authority")
	}
	files := sshObservation{Files: []sshAuthorityFile{{User: "root", Source: "rendered", Active: true, Present: true, Bytes: []byte(otherAuthorityKey(t) + "\n")}}}
	plan, _, _, err := enrollmentAccessPlan([]byte(otherAuthorityKey(t)), input, files)
	if err != nil || len(plan) != 1 || plan[0].User != "panel-runner" {
		t.Fatal("dormant root authority retained")
	}
}

func TestEnrollmentLateOrRepeatedRunnerCannotStartEffects(t *testing.T) {
	now := time.Unix(1700000000, 0)
	intent := enrollmentIntent{RecoveryDeadline: now.Add(12 * time.Minute).Unix()}
	if !enrollmentMayStart(intent, "starting", now) || !enrollmentMayStart(intent, "starting", now.Add(time.Minute)) {
		t.Fatal("safe primary window refused")
	}
	if enrollmentMayStart(intent, "starting", now.Add(time.Minute+time.Second)) || enrollmentMayStart(intent, "publishing", now) || enrollmentMayStart(intent, "switching", now) || enrollmentMayStart(intent, "restored", now) {
		t.Fatal("late or previously started operation authorized another effect")
	}
}
