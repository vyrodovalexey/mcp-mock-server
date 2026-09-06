//go:build functional

package functional_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mock_701_validate_test.go — the `mcpmock validate` exit-code contract
// (MOCK-701.5): 0 valid, 1 validation error, 2 I/O/usage error, and --output
// json emitting the JSON Pointer + source. Black-box: it invokes the CLI binary
// built once by TestMain and asserts on exit code and stdout — no internal/
// import.

// runValidate runs `mcpmock validate <args...>` with a bounded context and
// returns exit code and stdout.
func runValidate(t *testing.T, args ...string) (int, string) {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	full := append([]string{"validate"}, args...)
	cmd := exec.CommandContext(c, sharedBinPath, full...)
	var stdout, stderr []byte
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start validate: %v", err)
	}
	stdout, _ = readAll(stdoutPipe)
	stderr, _ = readAll(stderrPipe)
	err := cmd.Wait()
	code := exitCode(err)
	if c.Err() != nil {
		t.Fatalf("validate timed out; stderr: %s", stderr)
	}
	return code, string(stdout)
}

// TestMOCK701_ValidateExitCodes covers the three exit codes with a valid
// scenario, an invalid (schema-violating) scenario, and a missing file.
func TestMOCK701_ValidateExitCodes(t *testing.T) {
	valid := writeTemp(t, "valid.yaml", validScenarioYAML)
	invalid := writeTemp(t, "invalid.yaml", invalidScenarioYAML)

	if code, _ := runValidate(t, valid); code != 0 {
		t.Errorf("valid scenario: exit %d, want 0", code)
	}
	if code, _ := runValidate(t, invalid); code != 1 {
		t.Errorf("invalid scenario: exit %d, want 1", code)
	}
	if code, _ := runValidate(t, filepath.Join(t.TempDir(), "does-not-exist.yaml")); code != 2 {
		t.Errorf("missing file: exit %d, want 2", code)
	}
}

// TestMOCK701_ValidateJSONOutput asserts --output json emits a machine-readable
// report: OK true + source on success; OK false + a problem with a JSON Pointer
// and source on failure.
func TestMOCK701_ValidateJSONOutput(t *testing.T) {
	valid := writeTemp(t, "valid.yaml", validScenarioYAML)
	invalid := writeTemp(t, "invalid.yaml", invalidScenarioYAML)

	code, out := runValidate(t, "--output", "json", valid)
	if code != 0 {
		t.Fatalf("valid --output json: exit %d, want 0; out=%s", code, out)
	}
	var okRep struct {
		OK     bool   `json:"ok"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(out), &okRep); err != nil {
		t.Fatalf("valid json report parse: %v\nout: %s", err, out)
	}
	if !okRep.OK || okRep.Source == "" {
		t.Fatalf("valid json report = %+v, want ok:true + source", okRep)
	}

	code, out = runValidate(t, "--output", "json", invalid)
	if code != 1 {
		t.Fatalf("invalid --output json: exit %d, want 1; out=%s", code, out)
	}
	var badRep struct {
		OK       bool `json:"ok"`
		Problems []struct {
			Pointer string `json:"pointer"`
			Message string `json:"message"`
		} `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &badRep); err != nil {
		t.Fatalf("invalid json report parse: %v\nout: %s", err, out)
	}
	if badRep.OK || len(badRep.Problems) == 0 || badRep.Problems[0].Pointer == "" {
		t.Fatalf("invalid json report = %+v, want ok:false + problems with a pointer", badRep)
	}
}

// TestFunctional_HappyPathScenario asserts the Phase 1 delivered scenario
// scenarios/happy-path.yaml validates via the CLI (705.2 Phase 1 subset).
func TestFunctional_HappyPathScenarioValidates(t *testing.T) {
	path := "../../scenarios/happy-path.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("happy-path scenario missing: %v", err)
	}
	if code, out := runValidate(t, path); code != 0 {
		t.Fatalf("happy-path.yaml did not validate: exit %d\nout: %s", code, out)
	}
}

// readAll drains r to completion, returning the bytes read.
func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

// exitCode extracts the process exit code from a *exec.ExitError, or 0 for nil,
// or -1 for any other error.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// writeTemp writes content to a temp file named name and returns its path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// validScenarioYAML is a minimal schema-valid Phase 1 scenario.
const validScenarioYAML = `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata:
  name: valid-fixture
spec:
  era: modern
  transport:
    kinds: [http]
    http:
      path: /mcp
      listener: shared
`

// invalidScenarioYAML violates additionalProperties:false with an unknown key
// (MOCK-701.4), which the schema rejects — exit 1.
const invalidScenarioYAML = `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata:
  name: invalid-fixture
spec:
  thisKeyIsNotInTheSchema: true
`
