package main

import "testing"

// TestMatchForbidden_DetectsRejectedModules is the REQUIRED negative case for
// the forbidden-direct-dependency arm: every module ADR-013 rejects — including
// versioned-suffix module paths like golang-jwt/jwt/v5 — must be flagged. A gate
// never seen to fail is not known to work.
func TestMatchForbidden_DetectsRejectedModules(t *testing.T) {
	shouldMatch := []string{
		"github.com/spf13/cobra",
		"github.com/spf13/pflag",
		"github.com/gin-gonic/gin",
		"github.com/golang-jwt/jwt/v5", // versioned suffix must still match
		"golang.org/x/crypto",
		"golang.org/x/crypto/chacha20",
		"github.com/google/uuid",
		"gopkg.in/yaml.v3",
	}
	for _, m := range shouldMatch {
		if _, ok := matchForbidden(m); !ok {
			t.Errorf("matchForbidden(%q) = false; a rejected ADR-013 module must be flagged", m)
		}
	}
}

// TestMatchForbidden_AllowsApprovedModules proves the forbidden arm does not
// false-positive on approved runtime/test-only modules — the property that
// keeps the gate trustworthy on day one.
func TestMatchForbidden_AllowsApprovedModules(t *testing.T) {
	shouldNotMatch := []string{
		"github.com/prometheus/client_golang",
		"github.com/santhosh-tekuri/jsonschema/v6",
		"go.opentelemetry.io/otel",
		"sigs.k8s.io/yaml", // NOT gopkg.in/yaml.v3 — the approved YAML path
		"go.uber.org/goleak",
		"github.com/stretchr/testify",
	}
	for _, m := range shouldNotMatch {
		if why, ok := matchForbidden(m); ok {
			t.Errorf("matchForbidden(%q) = true (%s); an approved module must not be flagged", m, why)
		}
	}
}

// TestGoleakIsTestOnlyApproved pins the exact false-positive AMEND-5 was written
// to prevent: goleak, a spec-mandated (MOCK-107.7) test-only dependency, is in
// the approved test-only set. A deps-check that rejected it would be a false
// positive that makes the gate untrustworthy.
func TestGoleakIsTestOnlyApproved(t *testing.T) {
	if _, ok := approvedTestOnly["go.uber.org/goleak"]; !ok {
		t.Fatal("go.uber.org/goleak must be in the approved test-only set (ADR-013 AMEND-5 / MOCK-107.7)")
	}
	if _, forbidden := matchForbidden("go.uber.org/goleak"); forbidden {
		t.Fatal("go.uber.org/goleak must not be on the forbidden-direct list")
	}
}
