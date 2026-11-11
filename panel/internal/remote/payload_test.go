package remote

import (
	"bytes"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestOversizedPayloadIsRejectedBeforeSSHStarts(t *testing.T) {
	private, public := testKey(t)
	defer clear(private)
	target, err := NewTarget("100.64.0.1", "panel", ssh.MarshalAuthorizedKey(public), ssh.FingerprintSHA256(public))
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{command: "/must-not-run", directory: t.TempDir(), slots: make(chan struct{}, 1)}
	result, err := client.Helper(t.Context(), target, private, bytes.Repeat([]byte("x"), maxPayloadBytes+1))
	if !errors.Is(err, ErrInput) || result.Outcome != NotStarted {
		t.Fatalf("oversized payload started SSH: %v %v", result.Outcome, err)
	}
}
