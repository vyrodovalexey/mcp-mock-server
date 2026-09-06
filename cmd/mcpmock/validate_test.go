package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScenario writes content to a temp file and returns its path.
func writeScenario(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

const validScenario = `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata:
  name: ok
spec:
  era: modern
  transport:
    kinds: [http]
    http:
      path: /mcp
      listener: shared
`

const invalidScenario = `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata:
  name: bad
spec:
  era: modern
  transport:
    kinds: [http]
    http:
      path: /mcp
      listener: shared
  catalogue:
    tools:
      cont: 10
`

// TestValidateExitCodes is the MOCK-701 contract: validate exits 0 on a valid
// scenario, 1 on a validation error, and 2 on an I/O error, and the actionable
// error is surfaced.
func TestValidateExitCodes(t *testing.T) {
	valid := writeScenario(t, "valid.yaml", validScenario)
	invalid := writeScenario(t, "invalid.yaml", invalidScenario)

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string // substring expected on stderr (text mode)
	}{
		{"valid", []string{valid}, exitOK, "is valid"},
		{"invalid", []string{invalid}, exitValidation, `unknown key "cont"`},
		{"missing file", []string{filepath.Join(t.TempDir(), "nope.yaml")}, exitIO, "read scenario"},
		{"no file arg", nil, exitIO, "exactly one scenario file"},
		{"two file args", []string{valid, invalid}, exitIO, "exactly one scenario file"},
		{"bad output flag", []string{"--output", "xml", valid}, exitIO, "--output must be"},
		{"unknown flag", []string{"--nope", valid}, exitIO, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := runValidate(&out, &errOut, tc.args)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr=%q)", code, tc.wantCode, errOut.String())
			}
			if tc.wantSub != "" && !strings.Contains(errOut.String(), tc.wantSub) {
				t.Errorf("stderr %q does not contain %q", errOut.String(), tc.wantSub)
			}
		})
	}
}

// TestValidateJSONOutput asserts --output json emits the source file and each
// problem's JSON Pointer for a validation error, and a machine-readable ok:true
// for a valid scenario (MOCK-701.5).
func TestValidateJSONOutput(t *testing.T) {
	invalid := writeScenario(t, "invalid.yaml", invalidScenario)
	valid := writeScenario(t, "valid.yaml", validScenario)

	t.Run("invalid", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := runValidate(&out, &errOut, []string{"--output", "json", invalid})
		if code != exitValidation {
			t.Fatalf("exit code = %d, want %d", code, exitValidation)
		}
		var rep validateReport
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("decode json report: %v (raw=%q)", err, out.String())
		}
		if rep.OK {
			t.Error("report.OK = true, want false")
		}
		if rep.Source != invalid {
			t.Errorf("report.Source = %q, want %q", rep.Source, invalid)
		}
		if len(rep.Problems) == 0 {
			t.Fatal("report has no problems")
		}
		if rep.Problems[0].Pointer != "/spec/catalogue/tools/cont" {
			t.Errorf("problem pointer = %q, want /spec/catalogue/tools/cont", rep.Problems[0].Pointer)
		}
	})

	t.Run("valid", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := runValidate(&out, &errOut, []string{"--output", "json", valid})
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d", code, exitOK)
		}
		var rep validateReport
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("decode json report: %v", err)
		}
		if !rep.OK || rep.Kind != "Scenario" {
			t.Errorf("report = %+v, want OK Scenario", rep)
		}
	})
}
