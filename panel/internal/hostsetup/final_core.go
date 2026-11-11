package hostsetup

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var ErrFinal = errors.New("final activation requires verified private operator access and retained recovery; inspect setup state")

type FinalResult struct {
	Status           string `json:"status"`
	OperationID      string `json:"operation_id"`
	InstallationID   string `json:"installation_id"`
	ServerID         string `json:"server_id"`
	InputSHA256      string `json:"input_sha256"`
	EnrollmentSystem string `json:"enrollment_system"`
	FinalSystem      string `json:"final_system"`
	CurrentSystem    string `json:"current_system"`
	ProfileSystem    string `json:"profile_system"`
	BootedSystem     string `json:"booted_system"`
	Origin           string `json:"origin"`
	Challenge        string `json:"challenge"`
	RecoveryDeadline string `json:"recovery_deadline"`
	Ready            bool   `json:"ready"`
	ActivationReady  bool   `json:"activation_ready"`
}

type finalAdmission struct {
	SchemaVersion               int  `json:"schema_version"`
	IndependentRecoveryVerified bool `json:"independent_recovery_verified"`
}

type finalIntent struct {
	OperationID                 string           `json:"operation_id"`
	Pins                        adoptionPins     `json:"pins"`
	EnrollmentSystem            string           `json:"enrollment_system"`
	FinalSystem                 string           `json:"final_system"`
	Operator                    string           `json:"operator"`
	HTTPS                       panelHTTPSIntent `json:"https"`
	Challenge                   string           `json:"challenge"`
	LaunchID                    string           `json:"launch_id"`
	RecoveryDeadline            int64            `json:"recovery_deadline"`
	IndependentRecoveryVerified bool             `json:"independent_recovery_verified"`
}

type finalReceipt struct {
	OperationID      string        `json:"operation_id"`
	Phase            string        `json:"phase"`
	ConfirmedAt      int64         `json:"confirmed_at"`
	Identity         *hostIdentity `json:"identity,omitempty"`
	FinalSystem      string        `json:"final_system,omitempty"`
	BootedSystem     string        `json:"booted_system,omitempty"`
	RecoveryDeadline int64         `json:"recovery_deadline,omitempty"`
}

func admitFinal(data []byte) bool {
	var input finalAdmission
	if len(data) > 4096 || json.Unmarshal(data, &input) != nil {
		return false
	}
	canonical, err := json.Marshal(input)
	return err == nil && bytes.Equal(bytes.TrimSpace(data), canonical) && input.SchemaVersion == 1 && input.IndependentRecoveryVerified
}
func finalOwnsLinks(intent finalIntent, links enrollmentLinks) bool {
	return (links.current == intent.EnrollmentSystem || links.current == intent.FinalSystem) &&
		(links.profile == intent.EnrollmentSystem || links.profile == intent.FinalSystem) &&
		(links.booted == intent.EnrollmentSystem || links.booted == intent.FinalSystem)
}
func finalAt(intent finalIntent, enrolled enrollmentIntent, links enrollmentLinks, target string) bool {
	return links.current == target && links.profile == target && (links.booted == enrolled.SourceSystem || links.booted == intent.EnrollmentSystem || links.booted == intent.FinalSystem)
}
func finalOwnsObservedLinks(intent finalIntent, enrolled enrollmentIntent, links enrollmentLinks) bool {
	if links.booted == enrolled.SourceSystem {
		links.booted = intent.EnrollmentSystem
	}
	return finalOwnsLinks(intent, links)
}
func finalMayStart(intent finalIntent, phase string, now time.Time) bool {
	return phase == "starting" && now.Add(11*time.Minute).Unix() <= intent.RecoveryDeadline
}
func validFinalChallenge(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func finalMayRecover(intent finalIntent, enrolled enrollmentIntent, receipt finalReceipt, links enrollmentLinks) bool {
	if !finalOwnsObservedLinks(intent, enrolled, links) {
		return false
	}
	switch receipt.Phase {
	case "starting", "preparing", "profile_updating", "switching", "awaiting_private", "needs_attention":
		return receipt.ConfirmedAt == 0
	default:
		return false
	}
}
