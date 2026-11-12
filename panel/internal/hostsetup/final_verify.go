package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
)

func verifyFinalProbe(data []byte, setup *finalSetup, intent finalIntent, links enrollmentLinks) bool {
	var response hosthelper.ProbeResponse
	if len(data) == 0 || len(data) > 8<<10 || json.Unmarshal(data, &response) != nil {
		return false
	}
	canonical, err := json.Marshal(response)
	return err == nil && bytes.Equal(bytes.TrimSpace(data), canonical) && response.SchemaVersion == 1 &&
		response.InstallationID == setup.imported.Build.InstallationID && response.ServerID == setup.imported.Build.ServerID &&
		response.Challenge == intent.Challenge && response.Role == "panel" && response.Stage == "final" && !response.Ready &&
		response.CurrentSystem == intent.FinalSystem && response.SystemProfile == intent.FinalSystem && response.BootedSystem == links.booted &&
		finalAt(intent, setup.enrolled, links, intent.FinalSystem)
}

func verifyFinalPanel(ctx context.Context, setup *finalSetup, selected panelSelf, target string, bootEnabled bool) error {
	stage := "enrollment"
	if target == setup.imported.Build.FinalSystem {
		stage = "final"
	}
	if _, err := readAccountCandidate(ctx, target+"/etc/panel-host.json", stage, setup.imported.Build); err != nil {
		return ErrFinal
	}
	if bootEnabled {
		// NixOS owns this wants link. A runtime-only enable is not boot persistence.
		if !sameNativeFile("/etc/systemd/system/multi-user.target.wants/panel.service", target+"/etc/systemd/system/panel.service") {
			return ErrFinal
		}
		enabled, err := runtimeProbe(ctx, target+"/sw/bin/systemctl", "is-enabled", "--", "panel.service")
		if err != nil || strings.TrimSpace(string(enabled)) != "enabled" {
			return ErrFinal
		}
	}
	before, err := panelService(ctx, target)
	if err != nil || !panelProcess(target, before) || panelHealth(ctx, selected) != nil {
		return ErrFinal
	}
	after, err := panelService(ctx, target)
	current, identityErr := panelNativeSelf(ctx)
	if err != nil || identityErr != nil || before != after || !panelProcess(target, after) || !selected.same(current) {
		return ErrFinal
	}
	return nil
}

// Both admission immediately before the switch and post-switch observation use
// the existing native runtime checks. No Nix evaluation/build or source rewrite
// is performed against the operator's old /etc/nixos configuration.
func verifyFinalHost(ctx context.Context, setup *finalSetup, intent finalIntent) error {
	links, err := readEnrollmentLinks()
	if err != nil || !finalOwnsObservedLinks(intent, setup.enrolled, links) || links.current != links.profile {
		return ErrFinal
	}
	target := links.current
	stage := "enrollment"
	if target == intent.FinalSystem {
		stage = "final"
	}
	if _, err := readAccountCandidate(ctx, target+"/etc/panel-host.json", stage, setup.imported.Build); err != nil {
		return ErrFinal
	}
	tools := targetImportTools()
	tools.nixStore = intent.EnrollmentSystem + "/sw/bin/nix-store"
	if complete, err := observeRootedImport(ctx, tools, setup.imported.Build); err != nil || !complete {
		return ErrUnresolved
	}
	if _, err := inspectRuntimeStorage(ctx, intent.EnrollmentSystem, setup.input.Machine); err != nil {
		return err
	}
	if err := inspectRuntimeNetwork(ctx, intent.EnrollmentSystem, target, setup.input.Machine, false); err != nil {
		return err
	}
	// sshd policy comes from the built generation; the operator's fresh private
	// session and the management probe prove access before confirmation.
	parent, err := openSSHDirectory("/var/lib", 0)
	if err != nil {
		return ErrFinal
	}
	err = checkEnrollmentAccess(ctx, parent, "panel-access", setup.enrolled.Files, false)
	_ = parent.Close()
	if err != nil {
		return ErrFinal
	}
	var https panelHTTPSIntent
	if readState(setup.root, "panel-https.json", &https) != nil || https != intent.HTTPS {
		return ErrFinal
	}
	// Publication is observation-only here: the already-active exact file must
	// exist, rather than silently repairing configuration at the mutation boundary.
	info, err := os.Lstat("/var/lib/panel-config")
	if err != nil || info == nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return ErrFinal
	}
	config, err := openSSHDirectory("/var/lib/panel-config", 0)
	if err != nil {
		return ErrFinal
	}
	directory, err := config.Open(".")
	if err != nil {
		_ = config.Close()
		return ErrFinal
	}
	names, err := directory.Readdirnames(2)
	_ = directory.Close()
	if (err != nil && !errors.Is(err, io.EOF)) || len(names) != 1 || names[0] != "serve.env" {
		_ = config.Close()
		return ErrFinal
	}
	file, err := openPrepared(config, "serve.env", 1024, 0)
	if err != nil {
		_ = config.Close()
		return ErrFinal
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 1025))
	_ = file.Close()
	_ = config.Close()
	if readErr != nil || string(data) != "PANEL_ORIGIN="+intent.HTTPS.Origin+"\n" {
		return ErrFinal
	}
	if target == intent.FinalSystem {
		unit, err := observeNetworkUnit(ctx, networkTools{systemctl: intent.EnrollmentSystem + "/sw/bin/systemctl"}, "nftables.service")
		if err != nil || !nativeNetworkUnit(unit, target, "nftables.service") {
			return ErrFinal
		}
		// The fixed final config must enforce the selected public-key-only policy;
		// remote IPv4/IPv6 public-port denial remains external acceptance evidence.
		data, err := runtimeProbe(ctx, intent.EnrollmentSystem+"/sw/bin/sshd", "-G", "-T", "-C", "user="+setup.input.Operator.Name, "-f", "/etc/ssh/sshd_config")
		if err != nil {
			return ErrFinal
		}
		settings, err := parseSSHEffective(data)
		if err != nil || settings.scalar("authenticationmethods") != "publickey" {
			return ErrFinal
		}
	}
	after, err := readEnrollmentLinks()
	if err != nil || after != links {
		return ErrFinal
	}
	return nil
}
