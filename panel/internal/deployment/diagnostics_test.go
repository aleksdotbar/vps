package deployment

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeLogStripsControlSequencesAndKeepsTail(t *testing.T) {
	raw := "\x1b[31mError:\x1b[0m bad token\r\x07\nnext\tline\n\n"
	if got := sanitizeLog([]byte(raw), maxLogTailBytes); got != "Error: bad token\nnext\tline" {
		t.Fatalf("sanitized %q", got)
	}
	long := strings.Repeat("old line\n", 1000) + "last line"
	got := sanitizeLog([]byte(long), 64)
	if len(got) > 64 || !strings.HasSuffix(got, "last line") || strings.HasPrefix(got, "ld") {
		t.Fatalf("tail %q", got)
	}
	if sanitizeLog([]byte("\xff\xfeok"), 16) != "ok" {
		t.Fatal("invalid UTF-8 was kept")
	}
}

func TestDiagnosticsFitStatusBudgetAndValidate(t *testing.T) {
	values := make([]ServiceDiagnostic, maxDiagnosticServices)
	for index := range values {
		// HTML-escaped characters expand sixfold in JSON; the budget must still hold.
		values[index] = ServiceDiagnostic{Service: "svc" + string(rune('a'+index)), State: "exited", ExitCode: 1, LogTail: sanitizeLog([]byte(strings.Repeat("<&>\n", 1000)), maxLogTailBytes)}
	}
	bounded := boundDiagnostics(values)
	data, err := json.Marshal(bounded)
	if err != nil || len(data) > maxDiagnosticsBytes || len(bounded) != maxDiagnosticServices || !validDiagnostics(bounded) {
		t.Fatalf("bounded %d bytes, valid=%v", len(data), validDiagnostics(bounded))
	}
	for _, invalid := range [][]ServiceDiagnostic{
		{{Service: "bad name", State: "exited"}},
		{{Service: "bot", State: "Exited!"}},
		{{Service: "bot", State: "exited", LogTail: "\x1b[31mraw"}},
		make([]ServiceDiagnostic, maxDiagnosticServices+1),
	} {
		if validDiagnostics(invalid) {
			t.Fatalf("accepted %#v", invalid)
		}
	}
}
