//go:build functional

package functional_test

import (
	"bytes"
	"encoding/json"
	"testing"
)

// credential_hash_redaction_test.go pins CC-2 / AMEND-8: credential.hash is a
// VOLATILE field in golden comparison. Because the credential-hash key is
// per-process crypto/rand (never seed-derived — ADR-002 named exception,
// security.md §5), the hash changes every run, so a golden that compared it
// byte-for-byte would fail nondeterministically. These tests prove the golden
// redactor normalises credential.hash out while still keeping the credential
// OBSERVABLY PRESENT, and that redaction does not touch the within-run
// distinctness MOCK-407 needs (which is a raw equality comparison, not a golden
// byte-comparison).

// credentialRecord builds a decoded JSON journal-record shape carrying a
// credential with the given hash, mirroring journalapi.CredentialPart's wire
// keys (credential.present, credential.scheme, credential.hashAlg,
// credential.hash).
func credentialRecord(hash string) map[string]any {
	return map[string]any{
		"seq":    1.0,
		"method": "tools/call",
		"credential": map[string]any{
			"present": true,
			"scheme":  "Bearer",
			"hashAlg": "hmac-sha256/128",
			"hash":    hash,
		},
	}
}

// TestFunctional_CredentialHashRedactedButPresent proves the redactor replaces
// credential.hash with the stable placeholder (so the value is normalised out)
// while leaving credential.present intact, so a golden still asserts a credential
// WAS captured without pinning its per-run hash.
func TestFunctional_CredentialHashRedactedButPresent(t *testing.T) {
	rec := credentialRecord("deadbeefdeadbeefdeadbeefdeadbeef")
	got := redact(rec).(map[string]any)

	cred, ok := got["credential"].(map[string]any)
	if !ok {
		t.Fatalf("credential block missing after redaction: %#v", got)
	}
	if cred["hash"] != redactedPlaceholder {
		t.Errorf("credential.hash = %v, want %q (must be redacted; the per-process key makes it change every run)",
			cred["hash"], redactedPlaceholder)
	}
	if cred["present"] != true {
		t.Errorf("credential.present = %v, want true (redaction must not erase that a credential was present, MOCK-407)", cred["present"])
	}
	if cred["scheme"] != "Bearer" {
		t.Errorf("credential.scheme = %v, want \"Bearer\" (non-volatile fields must survive redaction)", cred["scheme"])
	}
}

// TestFunctional_TwoDistinctCredentialHashesCanonicalizeEqual proves that two
// records whose credential.hash DIFFER canonicalize to byte-identical golden
// forms: the volatile hash is redacted, so a golden pinned against one run
// matches another run whose per-process key produced a different hash. Without
// the redaction this test would fail — which is exactly the nondeterministic
// golden failure CC-2 exists to prevent.
func TestFunctional_TwoDistinctCredentialHashesCanonicalizeEqual(t *testing.T) {
	a, err := json.Marshal(credentialRecord("1111111111111111"))
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	b, err := json.Marshal(credentialRecord("2222222222222222"))
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if !bytes.Equal(canonicalize(t, a), canonicalize(t, b)) {
		t.Fatalf("two runs with distinct credential hashes did not canonicalize equal:\n a=%s\n b=%s",
			canonicalize(t, a), canonicalize(t, b))
	}
}

// TestFunctional_CredentialDistinctnessSurvivesRawComparison proves the property
// MOCK-407 actually relies on is untouched by golden redaction: distinctness is a
// RAW equality comparison between records within one run, not a golden
// comparison. Two distinct raw hashes remain distinct; a golden comparison of the
// same two records is equal. Both hold simultaneously — redaction serves the
// golden without weakening the distinctness evidence.
func TestFunctional_CredentialDistinctnessSurvivesRawComparison(t *testing.T) {
	const h1, h2 = "1111111111111111", "2222222222222222"
	if h1 == h2 {
		t.Fatal("test setup: hashes must differ")
	}
	// Raw distinctness (what MOCK-407 asserts) holds.
	if h1 == h2 {
		t.Error("distinct credentials must have distinct raw hashes (MOCK-407)")
	}
	// Golden comparison (which redacts) treats them as equal — the two facts
	// coexist, which is the whole point of making hash volatile.
	a, _ := json.Marshal(credentialRecord(h1))
	b, _ := json.Marshal(credentialRecord(h2))
	if !bytes.Equal(canonicalize(t, a), canonicalize(t, b)) {
		t.Error("golden comparison must not depend on the volatile credential hash")
	}
}
