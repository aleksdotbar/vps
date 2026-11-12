package hosthelper

import "testing"

func TestRunnerToolsComeFromItsLaunchGeneration(t *testing.T) {
	system := "/nix/store/" + "0123456789abcdfghijklmnpqrsvwxyz" + "-nixos-system-nixos-26.05"
	if got, ok := launchedSystem(system + "/sw/bin/panel"); !ok || got != system {
		t.Fatalf("launch generation = %q, %v", got, ok)
	}
	for _, executable := range []string{"panel", "/run/current-system/sw/bin/panel", system + "/sw/bin/docker", "/tmp/x-nixos-system-a/sw/bin/panel"} {
		if _, ok := launchedSystem(executable); ok {
			t.Fatalf("accepted %q", executable)
		}
	}
}
