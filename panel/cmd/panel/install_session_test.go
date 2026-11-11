package main

import (
	"testing"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/hostsetup"
)

func TestNextStepFollowsRecordedPhasesWithoutRepeatingEffects(t *testing.T) {
	enrollment := func(status string) *hostsetup.EnrollmentResult { return &hostsetup.EnrollmentResult{Status: status} }
	final := func(status string) *hostsetup.FinalResult { return &hostsetup.FinalResult{Status: status} }
	for name, test := range map[string]struct {
		status hostsetup.SetupStatus
		want   installStep
	}{
		"built only":          {hostsetup.SetupStatus{ImportPhase: "imported"}, stepEnroll},
		"enrolling":           {hostsetup.SetupStatus{Enrollment: enrollment("enrolling")}, stepWaitEnrollment},
		"awaiting bootstrap":  {hostsetup.SetupStatus{Enrollment: enrollment("awaiting_bootstrap")}, stepConfirmEnrollment},
		"enrollment restored": {hostsetup.SetupStatus{Enrollment: enrollment("restored")}, stepFailed},
		"enrollment unknown":  {hostsetup.SetupStatus{Enrollment: enrollment("needs_attention")}, stepFailed},
		"panel not started": {hostsetup.SetupStatus{Enrollment: enrollment("enrolled"),
			HTTPS: &hostsetup.SetupHTTPS{Status: "not_started"}}, stepPanel},
		"panel retained": {hostsetup.SetupStatus{Enrollment: enrollment("enrolled"),
			HTTPS: &hostsetup.SetupHTTPS{Status: "retained"}}, stepFinal},
		"final activating":       {hostsetup.SetupStatus{Final: final("activating")}, stepWaitFinal},
		"final awaiting private": {hostsetup.SetupStatus{Final: final("awaiting_private")}, stepConfirmFinal},
		"final active":           {hostsetup.SetupStatus{Final: final("final_active")}, stepDone},
		"final recovering":       {hostsetup.SetupStatus{Final: final("recovering")}, stepFailed},
	} {
		if got := nextStep(test.status); got != test.want {
			t.Errorf("%s: got step %d, want %d", name, got, test.want)
		}
	}
}

func TestProcStatParsesNamesWithSpacesAndParentheses(t *testing.T) {
	stat, err := parseProcStat([]byte("4242 (sshd-session (x) y) S 4241 4242 4242 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 98765 1 2 3"))
	if err != nil || stat.comm != "sshd-session (x) y" || stat.ppid != 4241 || stat.startTicks != 98765 {
		t.Fatalf("parsed %+v, %v", stat, err)
	}
	if _, err := parseProcStat([]byte("1 (init) S 0")); err == nil {
		t.Fatal("truncated status accepted")
	}
}

func TestSessionFreshnessAndTailnetOrigin(t *testing.T) {
	phase := time.Unix(1000, 0)
	session := loginSession{Started: phase.Add(time.Second), SSHConnection: "100.101.1.2 50000 100.111.208.20 22"}
	if !session.startedAfter(phase) || !session.overTailnet() {
		t.Fatal("fresh tailnet session rejected")
	}
	if (loginSession{Started: phase}).startedAfter(phase) {
		t.Fatal("session from before the phase change accepted")
	}
	for _, connection := range []string{
		"203.0.113.5 50000 198.51.100.86 22",
		"",
		"fd7a:115c:a1e0::1 1 2001:db8::1 22",
		"203.0.113.5 50000 100.111.208.20 22",    // public client reached the tailnet address through the host
		"100.111.208.20 50000 100.111.208.20 22", // the host connected to itself
	} {
		if (loginSession{SSHConnection: connection}).overTailnet() {
			t.Fatalf("public connection %q accepted as tailnet", connection)
		}
	}
	if !(loginSession{SSHConnection: "fd7a:115c:a1e0::2 1 fd7a:115c:a1e0::fa36:d015 22"}).overTailnet() {
		t.Fatal("tailnet IPv6 connection rejected")
	}
}
