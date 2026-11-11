package foundation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/machine"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/input.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCanonicalFoundationBindsExactMachineAndNormalizesPublicKey(t *testing.T) {
	data := fixture(t)
	input, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input.Operator.PublicKey, "generic-fixture") {
		t.Fatal("authorized-key comment retained")
	}
	canonical, err := Canonical(input)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Machine       json.RawMessage `json:"machine"`
		MachineSHA256 string          `json:"machine_sha256"`
	}
	if err := json.Unmarshal(canonical, &output); err != nil {
		t.Fatal(err)
	}
	machineData, expectedSHA, err := machine.Canonical(input.Machine)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(output.Machine)
	if !bytes.Equal(output.Machine, machineData) || output.MachineSHA256 != expectedSHA || expectedSHA != hex.EncodeToString(digest[:]) || bytes.HasSuffix(canonical, []byte("\n")) {
		t.Fatal("envelope does not bind exact canonical machine bytes")
	}
	var reordered map[string]json.RawMessage
	if err := json.Unmarshal(data, &reordered); err != nil {
		t.Fatal(err)
	}
	reencoded, err := json.Marshal(reordered)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(reencoded)
	if err != nil {
		t.Fatal(err)
	}
	again.Operator.PublicKey += " another comment\n"
	stable, err := Canonical(again)
	if err != nil || !bytes.Equal(canonical, stable) {
		t.Fatalf("equivalent field order/whitespace/key changed identity: %v", err)
	}
	input.Role = Server
	changed, err := Canonical(input)
	if err != nil || bytes.Equal(canonical, changed) {
		t.Fatalf("role change not bound: %v", err)
	}
	if err := json.Unmarshal(changed, &output); err != nil || output.MachineSHA256 != expectedSHA {
		t.Fatalf("envelope-only change altered machine identity: %v", err)
	}
	input.Machine.Boot.Console[0], input.Machine.Boot.Console[1] = input.Machine.Boot.Console[1], input.Machine.Boot.Console[0]
	changed, err = Canonical(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(changed, &output); err != nil || output.MachineSHA256 == expectedSHA {
		t.Fatalf("ordered machine change not bound: %v", err)
	}
}

func TestFoundationRejectsMalformedAuthorityAndPolicyWithoutReflection(t *testing.T) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, fixture(t)); err != nil {
		t.Fatal(err)
	}
	valid := compact.String()
	var original Input
	if err := json.Unmarshal(compact.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	keyJSON, err := json.Marshal(original.Operator.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, old, replacement string
	}{
		{"unknown authority", `"role":"panel"`, `"role":"panel","private_key":"sensitive-input"`},
		{"generated hash supplied", `"role":"panel"`, `"role":"panel","machine_sha256":"sensitive-input"`},
		{"duplicate role", `"role":"panel"`, `"role":"server","role":"panel"`},
		{"case alias", `"role":"panel"`, `"ROLE":"panel"`},
		{"missing role", `"role":"panel",`, ``},
		{"null role", `"role":"panel"`, `"role":null`},
		{"wrong role type", `"role":"panel"`, `"role":["panel"]`},
		{"unsupported role", `"role":"panel"`, `"role":"sensitive-input"`},
		{"schema type", `"schema_version":1`, `"schema_version":"1"`},
		{"nil UUID", `11111111-1111-4111-8111-111111111111`, `00000000-0000-0000-0000-000000000000`},
		{"compact UUID", `11111111-1111-4111-8111-111111111111`, `11111111111141118111111111111111`},
		{"invalid server UUID", `22222222-2222-4222-8222-222222222222`, `sensitive-input`},
		{"noncanonical UUID case", `22222222-2222-4222-8222-222222222222`, `AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA`},
		{"root operator", `"name":"operator"`, `"name":"root"`},
		{"product operator", `"name":"operator"`, `"name":"panel"`},
		{"dynamic connector operator", `"name":"operator"`, `"name":"panel-cloudflared"`},
		{"service operator", `"name":"operator"`, `"name":"sshd"`},
		{"build operator", `"name":"operator"`, `"name":"nixbld7"`},
		{"systemd operator", `"name":"operator"`, `"name":"systemd-network"`},
		{"unsafe operator", `"name":"operator"`, `"name":"-sensitive-input"`},
		{"operator password", `"name":"operator"`, `"name":"operator","password":"sensitive-input"`},
		{"duplicate operator", `"name":"operator"`, `"name":"root","name":"operator"`},
		{"missing operator", `"name":"operator",`, ``},
		{"operator case", `"name":"operator"`, `"NAME":"operator"`},
		{"null key", `"public_key":` + string(keyJSON), `"public_key":null`},
		{"key options", `"public_key":"ssh-ed25519`, `"public_key":"command=\"sensitive-input\" ssh-ed25519`},
		{"key algorithm", `"public_key":"ssh-ed25519`, `"public_key":"ssh-rsa`},
		{"additional key line", `generic-fixture"`, `generic-fixture\nssh-ed25519 sensitive-input"`},
		{"oversized key entry", `generic-fixture"`, strings.Repeat("x", 1024) + `"`},
		{"private key", `"public_key":"ssh-ed25519`, `"public_key":"-----BEGIN OPENSSH PRIVATE KEY----- `},
		{"bootstrap missing boolean", `"password_authentication":false,`, ``},
		{"bootstrap null boolean", `"password_authentication":false`, `"password_authentication":null`},
		{"bootstrap wrong boolean type", `"public_ssh22":true`, `"public_ssh22":"true"`},
		{"bootstrap duplicate", `"public_ssh22":true`, `"public_ssh22":false,"public_ssh22":true`},
		{"bootstrap alias", `"public_ssh22":true`, `"PUBLIC_SSH22":true`},
		{"bootstrap unsupported policy", `"root_login":"prohibit-password"`, `"root_login":"forced-commands-only"`},
		{"custom SSH port", `"public_ssh22":true`, `"public_ssh22":true,"port":2222`},
		{"unsafe machine", `"fs_type":"ext4"`, `"fs_type":"ext4","auto_format":true`},
		{"machine duplicate", `"host_name":"generic-host"`, `"host_name":"generic-host","host_name":"other"`},
		{"trailing data", valid, valid + `{}`},
		{"oversized input", valid, valid + strings.Repeat(" ", MaxInputBytes)},
		{"invalid encoding", valid, valid + string([]byte{255})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(valid, test.old) {
				t.Fatal("fixture mutation does not match")
			}
			input := strings.Replace(valid, test.old, test.replacement, 1)
			parsed, err := Parse([]byte(input))
			if !errors.Is(err, ErrInvalidInput) || parsed.Role != "" || err.Error() != ErrInvalidInput.Error() {
				t.Fatalf("unsafe input accepted or reflected: %v", err)
			}
		})
	}
}
