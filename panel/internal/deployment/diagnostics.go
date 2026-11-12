package deployment

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ServiceDiagnostic is what an operator needs to see why a candidate failed.
// LogTail is the application's own recent output: operator-visible, bounded and
// stripped of control sequences. Panel state such as secret values is never added.
type ServiceDiagnostic struct {
	Service  string `json:"service"`
	State    string `json:"state"`
	ExitCode int    `json:"exit_code"`
	Health   string `json:"health"`
	LogTail  string `json:"log_tail"`
}

const (
	maxDiagnosticServices = 8
	maxLogTailBytes       = 4096
	maxDiagnosticsBytes   = 24 << 10
	diagnosticLogLines    = "40"
)

// Only safe runtime metadata; Config holds plaintext environment values.
const diagnosticTemplate = `{"service":{{json (index .Config.Labels "com.docker.compose.service")}},"state":{{json .State.Status}},"exit_code":{{json .State.ExitCode}},"health":{{if (index .State "Health")}}{{json (index (index .State "Health") "Status")}}{{else}}""{{end}}}`

// captureDiagnostics is best effort: a failure to observe never blocks recovery.
func captureDiagnostics(ctx context.Context, tools runnerTools, identity Identity) []ServiceDiagnostic {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ids, err := containerIDs(bounded, tools, identity)
	if err != nil {
		return nil
	}
	result := []ServiceDiagnostic{}
	for _, id := range ids {
		if len(result) == maxDiagnosticServices {
			break
		}
		data, err := dockerCommand(bounded, tools, "/var/empty", "container", "inspect", "--format", diagnosticTemplate, "--", id)
		if err != nil {
			continue
		}
		var value ServiceDiagnostic
		if json.Unmarshal(data, &value) != nil || !observedServiceName.MatchString(value.Service) {
			continue
		}
		value.State, value.Health = safeWord(value.State), safeWord(value.Health)
		logs, _ := dockerLogs(bounded, tools, id)
		value.LogTail = sanitizeLog(logs, maxLogTailBytes)
		result = append(result, value)
	}
	return boundDiagnostics(result)
}

// sanitizeLog keeps the last maximum bytes of valid text, dropping terminal
// escape sequences and control characters other than newlines and tabs.
func sanitizeLog(data []byte, maximum int) string {
	text := strings.ToValidUTF8(string(data), "")
	var builder strings.Builder
	for index := 0; index < len(text); {
		character, size := utf8.DecodeRuneInString(text[index:])
		if character == 0x1b {
			index += escapeLength(text[index:])
			continue
		}
		if character == '\n' || character == '\t' || !unicode.IsControl(character) {
			builder.WriteRune(character)
		}
		index += size
	}
	clean := strings.TrimRight(builder.String(), "\n")
	if len(clean) <= maximum {
		return clean
	}
	clean = clean[len(clean)-maximum:]
	for !utf8.ValidString(clean) {
		clean = clean[1:]
	}
	if newline := strings.IndexByte(clean, '\n'); newline >= 0 && newline < len(clean)-1 {
		clean = clean[newline+1:]
	}
	return clean
}

// escapeLength returns the byte length of an ANSI escape sequence at text[0].
func escapeLength(text string) int {
	if len(text) < 2 || text[1] != '[' {
		return min(2, len(text))
	}
	for index := 2; index < len(text); index++ {
		if text[index] >= 0x40 && text[index] <= 0x7e {
			return index + 1
		}
	}
	return len(text)
}

func safeWord(value string) string {
	if len(value) > 32 || strings.IndexFunc(value, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '-' || r == '_') }) >= 0 {
		return ""
	}
	return value
}

// boundDiagnostics shrinks the longest log tails until the encoded set fits the
// status response budget, including JSON escaping.
func boundDiagnostics(values []ServiceDiagnostic) []ServiceDiagnostic {
	for {
		data, err := json.Marshal(values)
		if err != nil {
			return nil
		}
		if len(data) <= maxDiagnosticsBytes {
			return values
		}
		longest := 0
		for index := range values {
			if len(values[index].LogTail) > len(values[longest].LogTail) {
				longest = index
			}
		}
		if values[longest].LogTail == "" {
			return nil
		}
		values[longest].LogTail = sanitizeLog([]byte(values[longest].LogTail), len(values[longest].LogTail)/2)
	}
}

func validDiagnostics(values []ServiceDiagnostic) bool {
	if len(values) > maxDiagnosticServices {
		return false
	}
	data, err := json.Marshal(values)
	if err != nil || len(data) > maxDiagnosticsBytes {
		return false
	}
	for _, value := range values {
		if !observedServiceName.MatchString(value.Service) || safeWord(value.State) != value.State || safeWord(value.Health) != value.Health || len(value.LogTail) > maxLogTailBytes || sanitizeLog([]byte(value.LogTail), maxLogTailBytes) != value.LogTail {
			return false
		}
	}
	return true
}
