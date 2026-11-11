package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/auth"
)

func TestProtectedInputRejectsTruncationAfterMaximumPassword(t *testing.T) {
	for _, test := range []struct {
		name, input string
		wantError   bool
	}{
		{"maximum password with CRLF", strings.Repeat("x", 1024) + "\r\n", false},
		{"extra data after maximum CRLF", strings.Repeat("x", 1024) + "\r\nextra", true},
		{"overlong password", strings.Repeat("x", 1025), true},
		{"invalid UTF-8", strings.Repeat("x", 12) + string([]byte{255}), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := reader.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := writer.WriteString(test.input); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			previous := os.Stdin
			os.Stdin = reader
			defer func() { os.Stdin = previous }()
			password, err := readPassword()
			defer clear(password)
			if test.wantError && !errors.Is(err, auth.ErrPassword) {
				t.Fatalf("oversized protected input accepted: %v", err)
			}
			if !test.wantError && (err != nil || len(password) != 1024) {
				t.Fatalf("valid framed input rejected: %v", err)
			}
		})
	}
}
