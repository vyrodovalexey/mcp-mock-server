package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunDispatch covers the top-level dispatcher's exit codes for missing,
// unknown and help arguments (criterion 7: never a panic, always a usage message
// and a non-zero exit for a bad invocation).
func TestRunDispatch(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string // substring expected on stderr
	}{
		{"no args", nil, exitIO, "Usage:"},
		{"unknown command", []string{"frobnicate"}, exitIO, `unknown command "frobnicate"`},
		{"help flag", []string{"--help"}, exitOK, "Commands:"},
		{"help word", []string{"help"}, exitOK, "Commands:"},
		{"version", []string{"version"}, exitOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tc.args, &out, &errOut)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if tc.wantErr != "" && !strings.Contains(errOut.String(), tc.wantErr) {
				t.Errorf("stderr %q does not contain %q", errOut.String(), tc.wantErr)
			}
		})
	}
}

// TestUsageDocumentsExitCodes asserts the top-level help spells out the exit-code
// contract, since CI depends on those codes (MOCK-701.5).
func TestUsageDocumentsExitCodes(t *testing.T) {
	for _, want := range []string{"0  success", "1  scenario validation error", "2  I/O error"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage text missing exit-code line %q", want)
		}
	}
}
