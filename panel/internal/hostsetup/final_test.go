package hostsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/installation"
)

func finalFixture(t *testing.T) (*finalSetup, finalIntent) {
	t.Helper()
	operation, build, _ := buildFixture(t)
	path := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	pins := adoptionPins{strings.Repeat("1", 64)}
	setup := &finalSetup{root: root, imported: importIntent{OperationID: operation, Pins: pins, Build: build}, enrolled: enrollmentIntent{EnrollmentSystem: build.EnrollmentSystem, SourceSystem: "/nix/store/" + strings.Repeat("4", 32) + "-nixos-system-source", ManagementFingerprint: "SHA256:fixture"}, input: foundation.Input{Operator: foundation.Operator{Name: "operator"}}}
	https := panelHTTPSIntent{OperationID: setup.imported.OperationID, Pins: pins, Origin: "https://panel.fixture.ts.net", NodeID: "nFixture", Proof: installation.Result{Status: "private_proven", OperationID: setup.imported.OperationID, InstallationID: build.InstallationID, ServerID: build.ServerID, InputSHA256: build.InputSHA256, ManagementFingerprint: setup.enrolled.ManagementFingerprint, ObservedAt: time.Now().UTC().Format(time.RFC3339)}}
	intent := finalIntent{OperationID: setup.imported.OperationID, Pins: pins, EnrollmentSystem: build.EnrollmentSystem, FinalSystem: build.FinalSystem, Operator: "operator", HTTPS: https, Challenge: strings.Repeat("a", 64), LaunchID: uuid.NewString(), RecoveryDeadline: time.Now().Add(12 * time.Minute).Unix(), IndependentRecoveryVerified: true}
	if err := writeState(t.Context(), root, "panel-https.json", https, false); err != nil {
		t.Fatal(err)
	}
	return setup, intent
}
func TestFinalAdmissionAndIntentRejectUnboundAuthority(t *testing.T) {
	if !admitFinal([]byte(`{"schema_version":1,"independent_recovery_verified":true}`)) {
		t.Fatal("explicit recovery attestation refused")
	}
	for _, data := range []string{`{}`, `{"schema_version":1,"independent_recovery_verified":false}`, `{"schema_version":1,"independent_recovery_verified":true,"extra":true}`, `{"schema_version":1,"independent_recovery_verified":true,"schema_version":1}`, `{"Schema_Version":1,"independent_recovery_verified":true}`, strings.Repeat(" ", 4097)} {
		if admitFinal([]byte(data)) {
			t.Fatal("ambiguous/missing recovery attestation admitted")
		}
	}
	setup, intent := finalFixture(t)
	if err := writeState(t.Context(), setup.root, "final.json", intent, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readFinalIntent(setup); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*finalIntent)
	}{
		{"operation", func(i *finalIntent) { i.OperationID = uuid.NewString() }},
		{"pins", func(i *finalIntent) { i.Pins.BuildSHA256 = strings.Repeat("e", 64) }},
		{"final closure", func(i *finalIntent) { i.FinalSystem = i.EnrollmentSystem }},
		{"operator", func(i *finalIntent) { i.Operator = "root" }},
		{"origin", func(i *finalIntent) { i.HTTPS.Origin = "https://other.fixture.ts.net" }},
		{"native node", func(i *finalIntent) { i.HTTPS.NodeID = "other" }},
		{"challenge", func(i *finalIntent) { i.Challenge = "short" }},
		{"recovery not acknowledged", func(i *finalIntent) { i.IndependentRecoveryVerified = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := intent
			test.change(&changed)
			if err := writeState(t.Context(), setup.root, "final.json", changed, true); err != nil {
				t.Fatal(err)
			}
			if _, err := readFinalIntent(setup); !errors.Is(err, ErrFinal) {
				t.Fatalf("changed authority admitted: %v", err)
			}
		})
	}
}
func TestFinalGuardNeverRepeatsInterruptedOrExternalEffects(t *testing.T) {
	setup, intent := finalFixture(t)
	now := time.Now()
	for _, phase := range []string{"preparing", "profile_updating", "switching", "awaiting_private", "recovering", "restored", "final_active", "needs_attention"} {
		if finalMayStart(intent, phase, now) {
			t.Fatalf("phase %s allowed second execution", phase)
		}
	}
	if !finalMayStart(intent, "starting", now) {
		t.Fatal("timely first start rejected")
	}
	late := intent
	late.RecoveryDeadline = now.Add(11*time.Minute - time.Second).Unix()
	if finalMayStart(late, "starting", now) {
		t.Fatal("late effect exceeded recovery guard")
	}
	owned := enrollmentLinks{intent.FinalSystem, intent.FinalSystem, setup.enrolled.SourceSystem}
	receipt := finalReceipt{OperationID: intent.OperationID, Phase: "awaiting_private"}
	if !finalMayRecover(intent, setup.enrolled, receipt, owned) {
		t.Fatal("owned failed activation cannot restore enrollment")
	}
	for _, phase := range []string{"recovering", "restored", "final_active"} {
		changed := receipt
		changed.Phase = phase
		if finalMayRecover(intent, setup.enrolled, changed, owned) {
			t.Fatalf("phase %s allowed recovery replay", phase)
		}
	}
	external := owned
	external.profile = "/nix/store/" + strings.Repeat("5", 32) + "-nixos-system-external"
	if finalMayRecover(intent, setup.enrolled, receipt, external) {
		t.Fatal("external profile overwritten")
	}

}
func TestFinalConfirmationRequiresFreshManagementChallengeAndObservedGeneration(t *testing.T) {
	setup, intent := finalFixture(t)
	links := enrollmentLinks{intent.FinalSystem, intent.FinalSystem, setup.enrolled.SourceSystem}
	proof := hosthelper.ProbeResponse{SchemaVersion: 1, InstallationID: setup.imported.Build.InstallationID, ServerID: setup.imported.Build.ServerID, Challenge: intent.Challenge, Role: "panel", Stage: "final", CurrentSystem: links.current, SystemProfile: links.profile, BootedSystem: links.booted}
	data, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyFinalProbe(data, setup, intent, links) {
		t.Fatal("exact final probe refused")
	}
	for _, test := range []struct {
		name   string
		change func(*hosthelper.ProbeResponse)
	}{
		{"challenge", func(p *hosthelper.ProbeResponse) { p.Challenge = strings.Repeat("b", 64) }},
		{"installation", func(p *hosthelper.ProbeResponse) { p.InstallationID = uuid.NewString() }},
		{"server", func(p *hosthelper.ProbeResponse) { p.ServerID = uuid.NewString() }},
		{"stage", func(p *hosthelper.ProbeResponse) { p.Stage = "enrollment" }},
		{"current", func(p *hosthelper.ProbeResponse) { p.CurrentSystem = intent.EnrollmentSystem }},
		{"profile", func(p *hosthelper.ProbeResponse) { p.SystemProfile = intent.EnrollmentSystem }},
		{"booted", func(p *hosthelper.ProbeResponse) { p.BootedSystem = "external" }},
		{"readiness", func(p *hosthelper.ProbeResponse) { p.Ready = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := proof
			test.change(&changed)
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if verifyFinalProbe(encoded, setup, intent, links) {
				t.Fatal("unbound final proof admitted")
			}
		})
	}
	if verifyFinalProbe(append(data, []byte(" {}")...), setup, intent, links) || verifyFinalProbe(make([]byte, (8<<10)+1), setup, intent, links) {
		t.Fatal("unbounded/ambiguous proof admitted")
	}
}
func TestFinalActorRequiresDeclaredSudoIdentity(t *testing.T) {
	t.Setenv("SUDO_USER", "root")
	t.Setenv("SUDO_UID", "0")
	t.Setenv("SUDO_GID", "0")
	if confirmFinalOperator("operator") == nil {
		t.Fatal("unrelated sudo identity accepted")
	}
	setup, intent := finalFixture(t)
	if writeState(context.Background(), setup.root, "final-receipt.json", finalReceipt{OperationID: intent.OperationID, Phase: "final_active", ConfirmedAt: intent.RecoveryDeadline}, false) != nil {
		t.Fatal("fixture publication failed")
	}
	if _, err := finalPhase(setup.root, intent); !errors.Is(err, ErrFinal) {
		t.Fatal("expired final confirmation receipt accepted")
	}
}

func TestSettledFinalNeedsOnlyConfirmationAndBootedGeneration(t *testing.T) {
	setup, intent := finalFixture(t)
	identity := hostIdentity{InstallationID: setup.imported.Build.InstallationID, ServerID: setup.imported.Build.ServerID, Role: foundation.Panel}
	receipt := finalReceipt{OperationID: intent.OperationID, Phase: "final_active", ConfirmedAt: 100, RecoveryDeadline: 101, Identity: &identity, FinalSystem: intent.FinalSystem, BootedSystem: setup.enrolled.SourceSystem}
	if err := setup.root.Remove("panel-https.json"); err != nil {
		t.Fatal(err)
	}
	links := enrollmentLinks{intent.FinalSystem, intent.FinalSystem, intent.FinalSystem}
	if err := writeState(t.Context(), setup.root, "final-receipt.json", receipt, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readSettledFinal(setup.root, links); err != nil {
		t.Fatal("confirmed generation requires installation history:", err)
	}
	pending := links
	pending.booted = setup.enrolled.SourceSystem
	if _, err := readSettledFinal(setup.root, pending); err != nil {
		t.Fatal("known pre-reboot settlement lost setup-pending observation:", err)
	}
	pending.booted = intent.EnrollmentSystem
	if _, err := readSettledFinal(setup.root, pending); !errors.Is(err, ErrFinal) {
		t.Fatal("unbooted generation admitted")
	}
	for _, change := range []func(*finalReceipt){
		func(r *finalReceipt) { r.Phase = "awaiting_private" },
		func(r *finalReceipt) { r.Identity = nil },
		func(r *finalReceipt) { r.ConfirmedAt = r.RecoveryDeadline },
		func(r *finalReceipt) { r.FinalSystem = intent.EnrollmentSystem },
	} {
		changed := receipt
		change(&changed)
		if err := writeState(t.Context(), setup.root, "final-receipt.json", changed, true); err != nil {
			t.Fatal(err)
		}
		if _, err := readSettledFinal(setup.root, links); !errors.Is(err, ErrFinal) {
			t.Fatal("unconfirmed or mismatched authority admitted")
		}
	}
}
