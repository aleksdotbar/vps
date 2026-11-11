package deployment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStageProtocolRejectsAmbiguousAuthorityAndResponseHealthClaims(t *testing.T) {
	request, _ := stageFixture(t)
	// #nosec G117 -- Synthetic parser fixture secrets only, never operational credentials.
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(data)
	parsed, err := ParseStage(data)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Clear()
	for _, change := range []func([]byte) []byte{
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"action":"deploy_stage"`), []byte(`"action":"deploy_execute"`), 1)
		},
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
		},
		func(data []byte) []byte {
			return append(append(bytes.Clone(data[:len(data)-1]), []byte(`,"path":"/etc/shadow"`)...), '}')
		},
	} {
		changed := change(data)
		parsed, err := ParseStage(changed)
		parsed.Clear()
		clear(changed)
		if err == nil {
			t.Fatal("ambiguous or caller-selected execution authority accepted")
		}
	}
	hash, err := ManifestSHA256(request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	response := response(request.Action, request.Challenge, claim{request.Manifest.Identity, hash, fixtureFinal}, time.Now())
	for _, change := range []func(*Response){func(value *Response) { value.Ready = true }, func(value *Response) { value.Challenge = strings.Repeat("d", 64) }, func(value *Response) { value.Phase = "succeeded" }} {
		changed := response
		change(&changed)
		encoded, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseResponse(encoded, request.Action, request.Challenge, request.Manifest.Identity, hash); err == nil {
			t.Fatal("staging response claimed readiness or changed challenge")
		}
	}
	status, err := json.Marshal(StatusRequest{1, "deploy_status", request.Challenge, request.Manifest.Identity, hash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseStatus(append(status, bytes.Repeat([]byte(" "), MaxControlBytes)...)); err == nil {
		t.Fatal("control request used staging body bound")
	}
}
