package globalscheck

import (
	"path/filepath"
	"strings"
	"testing"
)

// parentRoot is the mcpmock module root as seen from this package
// (test/e2e/embed/globalscheck): four levels up.
func parentRoot() string {
	return filepath.Join("..", "..", "..", "..")
}

// TestGlobalsCheck_PublicPackagesClean is TC-028-G.1/G.2: the real public
// packages report zero findings. If a future change introduces a mutable global
// or a side-effecting init() into mcpmock/assert/journalapi/scenario, this fails
// and the finding must be escalated (it is a defect in production code this
// agent may not fix).
func TestGlobalsCheck_PublicPackagesClean(t *testing.T) {
	findings, err := RunReport(parentRoot())
	if err != nil {
		t.Fatalf("RunReport: %v", err)
	}
	for _, f := range findings {
		t.Errorf("MOCK-107.5/107.6 finding: %s", f)
	}
	if len(findings) == 0 {
		t.Logf("public packages clean: no mutable package-level state, no forbidden init() side effect")
	}
}

// TestGlobalsCheck_DetectsInjectedGlobal is TC-028-G.3, the REQUIRED negative
// case (acceptance criterion 5): a deliberately introduced global must be
// flagged. This proves the scanner actually fails when a global exists rather
// than passing vacuously.
func TestGlobalsCheck_DetectsInjectedGlobal(t *testing.T) {
	const src = `package leaky

var Leaked = map[string]int{}
`
	findings, err := scanSrc("leaky.go", src)
	if err != nil {
		t.Fatalf("scanSrc: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 finding for the injected global, got %d: %v", len(findings), findings)
	}
	got := findings[0]
	if got.Kind != "mutable-global" {
		t.Errorf("finding kind = %q, want mutable-global", got.Kind)
	}
	if !strings.Contains(got.Detail, "Leaked") {
		t.Errorf("finding detail %q does not name the injected global Leaked", got.Detail)
	}
}

// TestGlobalsCheck_DetectsInitSideEffect proves the 107.6 arm also fails on a
// forbidden init() call — the second half of the negative case.
func TestGlobalsCheck_DetectsInitSideEffect(t *testing.T) {
	const src = `package sneaky

import "os"

func init() {
	_, _ = os.ReadFile("/etc/passwd")
}
`
	findings, err := scanSrc("sneaky.go", src)
	if err != nil {
		t.Fatalf("scanSrc: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 init-side-effect finding, got %d: %v", len(findings), findings)
	}
	if findings[0].Kind != "init-side-effect" {
		t.Errorf("finding kind = %q, want init-side-effect", findings[0].Kind)
	}
}

// TestGlobalsCheck_DetectsOversizedInitAlloc proves the 4 KiB allocation arm of
// 107.6.
func TestGlobalsCheck_DetectsOversizedInitAlloc(t *testing.T) {
	const src = `package hungry

func init() {
	_ = make([]byte, 8192)
}
`
	findings, err := scanSrc("hungry.go", src)
	if err != nil {
		t.Fatalf("scanSrc: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 oversized-alloc finding, got %d: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0].Detail, "8192") {
		t.Errorf("finding detail %q does not mention the size", findings[0].Detail)
	}
}

// TestGlobalsCheck_AllowsErrorSentinelsAndBlanks proves the allowlist accepts
// the two legitimate package-level var forms — errors.New sentinels and
// blank-identifier compile-time assertions — so the check does not produce false
// positives on the documented public error contract.
func TestGlobalsCheck_AllowsErrorSentinelsAndBlanks(t *testing.T) {
	const src = `package ok

import "errors"

var ErrValidation = errors.New("boom")

var _ = ErrValidation

var _ interface{} = (*struct{})(nil)
`
	findings, err := scanSrc("ok.go", src)
	if err != nil {
		t.Fatalf("scanSrc: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for sentinels/blanks, got %d: %v", len(findings), findings)
	}
}
