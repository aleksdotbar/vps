package hostsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/installation"
)

var ErrPanelHTTPS = errors.New("private panel HTTPS is not proven; bootstrap access remains required")

var ErrPanelConfigStorage = errors.New("panel configuration filesystem must support native anonymous files and atomic publication")

type PanelHTTPSResult struct {
	Status           string `json:"status"`
	OperationID      string `json:"operation_id"`
	InstallationID   string `json:"installation_id"`
	ServerID         string `json:"server_id"`
	InputSHA256      string `json:"input_sha256"`
	EnrollmentSystem string `json:"enrollment_system"`
	Origin           string `json:"origin"`
	ObservedAt       string `json:"observed_at"`
	Ready            bool   `json:"ready"`
	ActivationReady  bool   `json:"activation_ready"`
}

type panelHTTPSIntent struct {
	OperationID string              `json:"operation_id"`
	Pins        adoptionPins        `json:"pins"`
	Origin      string              `json:"origin"`
	NodeID      string              `json:"node_id"`
	Proof       installation.Result `json:"proof"`
}

func panelProofMatches(proof installation.Result, imported importIntent, enrolled enrollmentIntent) bool {
	observed, err := time.Parse(time.RFC3339, proof.ObservedAt)
	return err == nil && !observed.IsZero() && observed.UTC().Format(time.RFC3339) == proof.ObservedAt &&
		proof.Status == "private_proven" && !proof.Ready && proof.OperationID == imported.OperationID &&
		proof.InstallationID == imported.Build.InstallationID && proof.ServerID == imported.Build.ServerID &&
		proof.InputSHA256 == imported.Build.InputSHA256 && proof.ManagementFingerprint == enrolled.ManagementFingerprint
}

// StartPanelHTTPS consumes confirmed enrollment and the existing panel-UID
// private proof. It neither opens SQLite as root nor closes bootstrap access.
func StartPanelHTTPS(parent context.Context, operationID, buildSHA string) (PanelHTTPSResult, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	pins := adoptionPins{buildSHA}
	if !canonicalOperation(operationID) || !pins.valid() {
		return PanelHTTPSResult{}, ErrIdentity
	}
	root, err := enrollmentStateRoot()
	if err != nil {
		return PanelHTTPSResult{}, err
	}
	defer func() { _ = root.Close() }()
	lock, err := stateLock(root)
	if err != nil {
		return PanelHTTPSResult{}, err
	}
	defer func() { _ = lock.Close() }()
	imported, err := readImportIntent(root, operationID, pins)
	if err != nil {
		return PanelHTTPSResult{}, err
	}
	enrolled, err := readEnrollmentIntent(root, imported)
	if err != nil {
		return PanelHTTPSResult{}, err
	}
	if phase, err := enrollmentPhase(root, enrolled); err != nil || phase != "enrolled" {
		return PanelHTTPSResult{}, ErrPanelHTTPS
	}
	links, err := readEnrollmentLinks()
	if err != nil || !enrollmentAt(enrolled, links, enrolled.EnrollmentSystem) {
		return PanelHTTPSResult{}, ErrPanelHTTPS
	}
	selected, err := panelNativeSelf(ctx)
	if err != nil {
		return PanelHTTPSResult{}, err
	}
	var retained panelHTTPSIntent
	err = readState(root, "panel-https.json", &retained)
	if errors.Is(err, os.ErrNotExist) {
		unit, err := panelService(ctx, enrolled.EnrollmentSystem)
		if err != nil || unit.active != "inactive" || !panelConfigAbsent() {
			return PanelHTTPSResult{}, ErrPanelHTTPS
		}
		// This subprocess is the actual panel runtime identity, with its own process
		// lock and encrypted credential. No private key crosses this root boundary.
		data, err := panelPrivateProof(ctx, enrolled.EnrollmentSystem, operationID)
		if err != nil || json.Unmarshal(data, &retained.Proof) != nil || !panelProofMatches(retained.Proof, imported, enrolled) {
			return PanelHTTPSResult{}, ErrPanelHTTPS
		}
		canonical, err := json.Marshal(retained.Proof)
		if err != nil || string(canonical)+"\n" != string(data) {
			return PanelHTTPSResult{}, ErrPanelHTTPS
		}
		retained.OperationID, retained.Pins, retained.Origin, retained.NodeID = operationID, pins, selected.origin, selected.nodeID
		if writeState(ctx, root, "panel-https.json", retained, false) != nil {
			return PanelHTTPSResult{}, ErrUnresolved
		}
	} else if err != nil {
		return PanelHTTPSResult{}, ErrPanelHTTPS
	}
	if retained.OperationID != operationID || retained.Pins != pins || retained.Origin != selected.origin || retained.NodeID != selected.nodeID || !panelProofMatches(retained.Proof, imported, enrolled) {
		return PanelHTTPSResult{}, ErrPanelHTTPS
	}
	before, err := panelNativeSelf(ctx)
	links, linkErr := readEnrollmentLinks()
	if err != nil || linkErr != nil || !selected.same(before) || !enrollmentAt(enrolled, links, enrolled.EnrollmentSystem) {
		return PanelHTTPSResult{}, ErrPanelHTTPS
	}
	if err := publishPanelOrigin(ctx, root, selected.origin); err != nil {
		return PanelHTTPSResult{}, err
	}
	unit, err := panelService(ctx, enrolled.EnrollmentSystem)
	if err != nil {
		return PanelHTTPSResult{}, err
	}
	if unit.active == "inactive" || unit.active == "failed" {
		// Native start is idempotent. Unknown return never authorizes replacement,
		// restart, config repair, or stopping another process; retry observes first.
		if _, err := runtimeProbe(ctx, enrolled.EnrollmentSystem+"/sw/bin/systemctl", "start", "--no-block", "--", "panel.service"); err != nil {
			return PanelHTTPSResult{}, ErrPanelHTTPS
		}
	} else if unit.active != "active" && unit.active != "activating" {
		return PanelHTTPSResult{}, ErrPanelHTTPS
	}
	if err := awaitPanelHTTPS(ctx, enrolled.EnrollmentSystem, selected); err != nil {
		return PanelHTTPSResult{}, err
	}
	after, err := panelNativeSelf(ctx)
	links, linkErr = readEnrollmentLinks()
	if err != nil || linkErr != nil || !selected.same(after) || !enrollmentAt(enrolled, links, enrolled.EnrollmentSystem) {
		return PanelHTTPSResult{}, ErrPanelHTTPS
	}
	result := PanelHTTPSResult{Status: "private_https_proven", OperationID: operationID, InstallationID: imported.Build.InstallationID, ServerID: imported.Build.ServerID, InputSHA256: imported.Build.InputSHA256, EnrollmentSystem: enrolled.EnrollmentSystem, Origin: selected.origin, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	if writeState(ctx, root, "panel-https-observation.json", result, true) != nil {
		return PanelHTTPSResult{}, ErrUnresolved
	}
	return result, nil
}
