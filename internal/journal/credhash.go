package journal

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// credHashAlg is the value stored in [journalapi.CredentialPart.HashAlg]. It
// names the construction so a reader can tell how the hash was produced: a keyed
// HMAC-SHA256 truncated to 128 bits (security.md §5, MOCK-407.1). It is part of
// the recorded evidence, not merely documentation.
//
//nolint:gosec // G101 false positive: this is an algorithm label, not a credential.
const credHashAlg = "hmac-sha256/128"

// credHashBytes is the truncation length in bytes: 128 bits of a SHA-256 digest
// (security.md §5, MOCK-407.1). Truncating keeps the hex hash compact while
// leaving it collision-resistant for the within-run comparison the assertions
// rely on (MOCK-407.4).
const credHashBytes = 16

// CredHasher turns a presented credential's raw header value into the hashed,
// non-reversible form the journal stores (MOCK-407, security.md §5). It is the
// single place a bearer token could leak into evidence, so it is deliberately
// small and does exactly one thing: it computes a keyed hash and lets the raw
// bytes go out of scope. It never retains, logs or returns the raw value.
//
// The hash is HMAC-SHA256(key, rawValue) truncated to 128 bits, hex-encoded.
// Keying it means a captured journal is not a rainbow-table target for a real
// token a hub accidentally forwarded (security.md §5). The key is a per-process
// crypto/rand value, ALWAYS — never seed-derived, in any mode (ADR-002 "Named
// exceptions to seed-determinism"; security.md §3/§5; AMEND-8). The rationale:
// the seed is deliberately public (printed at startup, served at /v1/seed,
// exported as a metric, MOCK-704), so a seed-derived key would let anyone
// holding a journal export and the seed recompute HMAC(k, candidate) and
// dictionary-attack every recorded credential — voiding the only structural
// protection the journal claims for credentials. A security property beats the
// determinism convenience for this one key. The hash is still reproducible
// within a run (same input, same hasher ⇒ same hash, MOCK-407.3); it is
// deliberately NOT reproducible across runs, and goldens redact it (the "hash"
// field and the <redacted:sha256:...> form are collapsed in the golden
// normalizer), so this breaks no golden.
//
// A CredHasher is immutable and safe for concurrent use: [CredHasher.Hash] only
// reads the stored key and allocates its own HMAC state per call. There is no
// process-global state (ADR-007); each instance owns its own hasher with its own
// independent random key.
type CredHasher struct {
	// key is the per-process random HMAC key. It is the HMAC key; it is never
	// the credential and never leaves this struct.
	key [sha256.Size]byte
}

// NewCredHasher returns a hasher bound to a fresh per-process credential-hash key
// read from crypto/rand — never seed-derived, in any mode (ADR-002 named
// exception; security.md §3/§5; AMEND-8). crypto/rand.Read never returns an error
// and crashes the process irrecoverably on catastrophic entropy failure, so there
// is no weak-key fallback path to accept a lesser key: the key is either full
// crypto-random or the process does not run.
func NewCredHasher() *CredHasher {
	var key [sha256.Size]byte
	// crypto/rand.Read (Go 1.27) never returns an error; it crashes the program
	// on the impossible OS-entropy failure rather than handing back a weak key.
	_, _ = rand.Read(key[:])
	return &CredHasher{key: key}
}

// Hash returns the keyed, truncated, hex-encoded hash of rawValue and never the
// raw value itself. The raw bytes are read once here and are not retained by the
// returned string. An empty input yields an empty string so a caller can treat
// "no credential" and "empty credential" the same way without a special case at
// the call site.
func (h *CredHasher) Hash(rawValue string) string {
	if rawValue == "" {
		return ""
	}
	mac := hmac.New(sha256.New, h.key[:])
	// Write on the stdlib hash never returns an error; the raw value is consumed
	// here and not stored anywhere.
	_, _ = mac.Write([]byte(rawValue))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:credHashBytes])
}

// Credential builds the [journalapi.CredentialPart] for a presented
// Authorization-style header value. It records presence, the auth scheme (the
// first whitespace-delimited token, e.g. "Bearer" or "Basic") and the keyed
// hash — never the raw value. Parsing of JWT claims (aud/iss/sub/scope/exp/kid)
// is a later-phase concern (MOCK-406); Phase 1 records the structurally
// safe subset. A blank rawValue yields (nil, false): no credential was present,
// so no CredentialPart is attached (its omitempty tag keeps it out of the JSON).
func (h *CredHasher) Credential(rawValue string) (*journalapi.CredentialPart, bool) {
	if strings.TrimSpace(rawValue) == "" {
		return nil, false
	}
	return &journalapi.CredentialPart{
		Present: true,
		Scheme:  scheme(rawValue),
		HashAlg: credHashAlg,
		Hash:    h.Hash(rawValue),
	}, true
}

// scheme extracts the auth scheme from a credential header value: the text
// before the first ASCII space. "Bearer eyJ..." yields "Bearer". A value with no
// space is a bare token (e.g. an API key) with no scheme, so the scheme is
// reported empty rather than returned whole — returning it would place raw
// credential material into the record, exactly the leak this package exists to
// prevent (security.md §5).
func scheme(rawValue string) string {
	if i := strings.IndexByte(rawValue, ' '); i >= 0 {
		return rawValue[:i]
	}
	return ""
}
