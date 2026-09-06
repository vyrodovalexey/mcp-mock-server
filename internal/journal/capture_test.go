package journal_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// sentinelToken is a fixture bearer token that must never appear in any
// serialized journal output. Its distinctive shape makes a byte scan for it
// unambiguous (MOCK-601.5 / MOCK-407.2).
const sentinelToken = "SUPER-SECRET-BEARER-TOKEN-do-not-leak-42"

// newCapturer builds a capturer for tests with a per-process random
// credential-hash key (NewCredHasher; ADR-002 named exception, AMEND-8). The
// hash is reproducible within a run for a single hasher (MOCK-407.3), which is
// all the assertions here rely on; it is deliberately not reproducible across
// runs.
func newCapturer(t *testing.T, cfg journalapi.Config) *journal.Capturer {
	t.Helper()
	return journal.NewCapturer(cfg, journal.NewCredHasher())
}

// fullInput builds an Input exercising every MOCK-601.1 field, with an
// Authorization header carrying the sentinel token so redaction and hashing are
// covered by the same fixture.
func fullInput() journal.Input {
	return journal.Input{
		Instance:   "test-instance",
		Generation: 7,
		WallTime:   time.Date(2026, 9, 4, 9, 18, 47, 123456789, time.UTC),
		MonoNs:     987654321,
		DurationNs: 4242,
		Transport:  journalapi.TransportHTTP,
		Era:        "modern",
		Peer:       "203.0.113.7:54321",
		HTTP: &journal.HTTPInput{
			Method: "POST",
			Path:   "/mcp",
			Query:  "instance=one",
			Headers: [][2]string{
				{"Content-Type", "application/json"},
				{"Authorization", "Bearer " + sentinelToken},
				{"X-MCP-Header", "first"},
				{"x-mcp-header", "second"},
			},
			Status: 200,
			RespHeaders: [][2]string{
				{"Content-Type", "application/json"},
			},
		},
		Method: "tools/call",
		Name:   "search",
		ID:     json.RawMessage(`"req-1"`),
		Params: json.RawMessage(`{"name":"search"}`),
		Body:   []byte(`{"jsonrpc":"2.0","id":"req-1","method":"tools/call","params":{"name":"search"}}`),
		Meta:   map[string]json.RawMessage{"protocolVersion": json.RawMessage(`"2026-07-28"`), "x-unknown": json.RawMessage(`{"k":1}`)},
		Response: &journal.ResponseInput{
			Status:      200,
			ResultType:  "toolResult",
			Body:        []byte(`{"jsonrpc":"2.0","id":"req-1","result":{"content":[]}}`),
			Shape:       "json",
			CloseReason: "complete",
		},
		Correlation: journalapi.CorrelationPart{
			TraceID:          "0af7651916cd43dd8448eb211c80319c",
			SpanID:           "b7ad6b7169203331",
			Sampled:          true,
			TraceparentRaw:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			TraceparentValid: true,
		},
	}
}

// TestCaptureFullFidelity asserts every MOCK-601.1 field is populated for a
// normal request. It fails if any required field is zero-valued (601.1).
func TestCaptureFullFidelity(t *testing.T) {
	t.Parallel()
	c := newCapturer(t, journalapi.Config{Enabled: true, Mode: journalapi.CaptureFull})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 64})
	in := fullInput()

	seq, err := c.Commit(context.Background(), ring, in)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if seq != 1 {
		t.Fatalf("seq = %d, want 1", seq)
	}
	recs := ring.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(recs))
	}
	r := recs[0]

	checks := []struct {
		name string
		zero bool
	}{
		{"WallTime", r.WallTime.IsZero()},
		{"MonoNs", r.MonoNs == 0},
		{"DurationNs", r.DurationNs == 0},
		{"Transport", r.Transport == ""},
		{"Era", r.Era == ""},
		{"Peer", r.Peer == ""},
		{"HTTP", r.HTTP == nil},
		{"Method", r.JSONRPC.Method == ""},
		{"Name", r.JSONRPC.Name == ""},
		{"ID", len(r.JSONRPC.ID) == 0},
		{"Body", len(r.JSONRPC.Body) == 0},
		{"BodyLength", r.JSONRPC.BodyLength == 0},
		{"Meta", len(r.Meta) == 0},
		{"Credential", r.Credential == nil},
		{"Response", r.Response == nil},
		{"Correlation.TraceID", r.Correlation.TraceID == ""},
		{"Generation", r.Generation == 0},
	}
	for _, ch := range checks {
		if ch.zero {
			t.Errorf("field %s is zero-valued but should be populated (601.1)", ch.name)
		}
	}
	// Meta preserves unknown keys (MOCK-203.6).
	if _, ok := r.Meta["x-unknown"]; !ok {
		t.Errorf("unknown _meta key not preserved")
	}
	// Response fidelity (601.4).
	if r.Response.Status != 200 || r.Response.ResultType != "toolResult" ||
		r.Response.CloseReason != "complete" || r.Response.Shape != "json" {
		t.Errorf("response fields not captured faithfully: %+v", r.Response)
	}
}

// TestCaptureHeaderCasingAndDuplicates covers 601.2: a raw request with
// x-mcp-header twice in different casing is recorded with both occurrences, in
// wire order, with original casing preserved. The Input carries the exact
// [][2]string a raw-socket read produces, so http.Header canonicalization is
// never in the path.
func TestCaptureHeaderCasingAndDuplicates(t *testing.T) {
	t.Parallel()
	c := newCapturer(t, journalapi.Config{Enabled: true})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 8})
	in := fullInput()
	// Replace headers with the raw-socket duplicate-casing shape (601.2).
	in.HTTP.Headers = [][2]string{
		{"X-MCP-Header", "first"},
		{"x-mcp-header", "second"},
	}
	if _, err := c.Commit(context.Background(), ring, in); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got := ring.Snapshot()[0].HTTP.Headers
	want := [][2]string{
		{"X-MCP-Header", "first"},
		{"x-mcp-header", "second"},
	}
	if len(got) != len(want) {
		t.Fatalf("header count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("header[%d] = %v, want %v (casing/order/duplicate lost)", i, got[i], want[i])
		}
	}
}

// TestCaptureModes covers 601.3/601.4: each capture mode bounds what is stored.
func TestCaptureModes(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat("A", 100))
	tests := []struct {
		name          string
		mode          journalapi.CaptureMode
		truncN        int
		wantBodyLen   int
		wantDigestSet bool
		wantTruncated bool
	}{
		{"full", journalapi.CaptureFull, 0, 100, false, false},
		{"truncate", journalapi.CaptureTruncate, 10, 10, true, true},
		{"truncate-beyond", journalapi.CaptureTruncate, 200, 100, true, false},
		{"digest", journalapi.CaptureDigest, 0, 0, true, false},
		{"off", journalapi.CaptureOff, 0, 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := journalapi.Config{Enabled: true, Mode: tc.mode, TruncateBytes: tc.truncN}
			c := newCapturer(t, cfg)
			ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 8})
			in := fullInput()
			in.Body = body
			if _, err := c.Commit(context.Background(), ring, in); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			r := ring.Snapshot()[0].JSONRPC
			if len(r.Body) != tc.wantBodyLen {
				t.Errorf("body len = %d, want %d", len(r.Body), tc.wantBodyLen)
			}
			if (r.BodySHA256 != "") != tc.wantDigestSet {
				t.Errorf("digest set = %v, want %v", r.BodySHA256 != "", tc.wantDigestSet)
			}
			if r.BodyTruncated != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v", r.BodyTruncated, tc.wantTruncated)
			}
			// BodyLength always records the original length.
			if r.BodyLength != len(body) {
				t.Errorf("BodyLength = %d, want %d (original length always recorded)", r.BodyLength, len(body))
			}
		})
	}
}

// TestCaptureCredentialHashed covers 601.5/407: the credential is recorded as a
// hash, and the CredentialPart carries no raw value.
func TestCaptureCredentialHashed(t *testing.T) {
	t.Parallel()
	c := newCapturer(t, journalapi.Config{Enabled: true})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 8})
	if _, err := c.Commit(context.Background(), ring, fullInput()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	cred := ring.Snapshot()[0].Credential
	if cred == nil || !cred.Present {
		t.Fatalf("credential not recorded")
	}
	if cred.Scheme != "Bearer" {
		t.Errorf("scheme = %q, want Bearer", cred.Scheme)
	}
	if cred.HashAlg != "hmac-sha256/128" {
		t.Errorf("hashAlg = %q, want hmac-sha256/128", cred.HashAlg)
	}
	if len(cred.Hash) != 32 { // 128 bits hex
		t.Errorf("hash len = %d, want 32 hex chars (128 bits)", len(cred.Hash))
	}
	if strings.Contains(cred.Hash, sentinelToken) {
		t.Errorf("hash contains raw token")
	}
}

// TestCredentialHashReproducibleAndDistinct covers 407.3/407.4: for a single
// hasher, the same input yields the same hash; different inputs yield different
// hashes. The key is per-process random (AMEND-8), so reproducibility is a
// within-run property of one hasher, which is exactly what the assertions need.
func TestCredentialHashReproducibleAndDistinct(t *testing.T) {
	t.Parallel()
	h := journal.NewCredHasher()
	a1 := h.Hash("Bearer token-A")
	a2 := h.Hash("Bearer token-A")
	b := h.Hash("Bearer token-B")
	if a1 != a2 {
		t.Errorf("same input produced different hashes: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("different inputs produced the same hash: %q", a1)
	}
	if h.Hash("") != "" {
		t.Errorf("empty input should hash to empty string")
	}
}

// TestCredentialHashKeyNotSeedDerived is the REV-004 regression: the
// credential-hash key must be per-process crypto/rand, NEVER derived from the
// public seed (ADR-002 named exception; security.md §3/§5; AMEND-8). Two hashers
// built in the same process — the same way an attacker replaying a fixed seed
// would rebuild them — must produce DIFFERENT hashes for the same credential,
// which is only possible if the key is random and not a function of any public
// input. If this ever fails, an observer holding the public seed plus a journal
// export could recompute HMAC(k, candidate) and dictionary-attack recorded
// credentials.
func TestCredentialHashKeyNotSeedDerived(t *testing.T) {
	t.Parallel()
	const cred = "Bearer some-hub-token"

	// Two independent hashers, as two runs from the SAME public seed would
	// produce them. If the key were seed-derived these would collide.
	h1 := journal.NewCredHasher()
	h2 := journal.NewCredHasher()
	if h1.Hash(cred) == h2.Hash(cred) {
		t.Fatal("SECURITY: two hashers produced the same hash for the same credential; " +
			"the key is not per-process random and is likely seed-derivable (REV-004)")
	}

	// The defeated construction: derive a "credhash" leaf from an instance key
	// an observer can rebuild from the public seed. The real hash must NOT match
	// any hash keyed by that publicly derivable key. The domain is written as a
	// literal because DomainCredHash was DELETED (AMEND-8): "credhash" is no
	// longer in the seed tree, and this test reconstructs precisely the defeated
	// seed-derivation an attacker would attempt — which uses the raw domain
	// string, not any constant this codebase still exports.
	seed := uint64(0x0123456789abcdef)
	instanceKey := determinism.Root(seed).Derive(determinism.DomainInstance, []byte("alpha"))
	publicKey := instanceKey.Derive(determinism.Domain("credhash"))
	mac := hmac.New(sha256.New, publicKey[:])
	_, _ = mac.Write([]byte(cred))
	sum := mac.Sum(nil)
	seedDerivedHash := hex.EncodeToString(sum[:16])

	if h1.Hash(cred) == seedDerivedHash || h2.Hash(cred) == seedDerivedHash {
		t.Fatal("SECURITY: recorded credential hash is reproducible from the public seed (REV-004)")
	}
}

// TestCaptureRejectedBeforeDispatch covers 601.6: a request rejected at the
// _meta validation stage still produces a complete record with the rejection
// reason and the emitted error response.
func TestCaptureRejectedBeforeDispatch(t *testing.T) {
	t.Parallel()
	c := newCapturer(t, journalapi.Config{Enabled: true})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 8})
	in := fullInput()
	in.MetaValidation = &journalapi.MetaValidationPart{
		Mode:    "strict",
		Outcome: "rejected",
		Missing: []string{"protocolVersion"},
	}
	in.Response = &journal.ResponseInput{
		Status: 200,
		Shape:  "json",
		Error: &journalapi.ErrorRecord{
			Code:    -32602,
			Message: "invalid params",
			Data:    json.RawMessage(`{"missing":["protocolVersion"]}`),
		},
	}
	if _, err := c.Commit(context.Background(), ring, in); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	r := ring.Snapshot()[0]
	if r.MetaValidation == nil || r.MetaValidation.Outcome != "rejected" {
		t.Fatalf("meta validation rejection not recorded: %+v", r.MetaValidation)
	}
	if len(r.MetaValidation.Missing) != 1 || r.MetaValidation.Missing[0] != "protocolVersion" {
		t.Errorf("missing fields not recorded: %+v", r.MetaValidation.Missing)
	}
	if r.Response == nil || r.Response.Error == nil || r.Response.Error.Code != -32602 {
		t.Fatalf("rejection error response not recorded: %+v", r.Response)
	}
}

// TestCaptureStdioNoHTTP covers a stdio exchange: HTTP is nil, and the JSON-RPC
// and body fidelity still hold.
func TestCaptureStdioNoHTTP(t *testing.T) {
	t.Parallel()
	c := newCapturer(t, journalapi.Config{Enabled: true})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 8})
	in := fullInput()
	in.HTTP = nil
	in.Transport = journalapi.TransportStdio
	in.Credential = "" // no header to extract from
	if _, err := c.Commit(context.Background(), ring, in); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	r := ring.Snapshot()[0]
	if r.HTTP != nil {
		t.Errorf("HTTP should be nil for stdio")
	}
	if r.Transport != journalapi.TransportStdio {
		t.Errorf("transport = %q, want stdio", r.Transport)
	}
	if r.Credential != nil {
		t.Errorf("no credential should be recorded when none presented")
	}
}

// TestCaptureRecordOwnsBytes proves the record does not alias the caller's
// buffers: mutating the input body after Commit does not change the stored bytes
// (ADR-005: records own their bytes).
func TestCaptureRecordOwnsBytes(t *testing.T) {
	t.Parallel()
	c := newCapturer(t, journalapi.Config{Enabled: true, Mode: journalapi.CaptureFull})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 8})
	in := fullInput()
	in.Body = []byte("original-body")
	if _, err := c.Commit(context.Background(), ring, in); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for i := range in.Body {
		in.Body[i] = 'X'
	}
	got := string(ring.Snapshot()[0].JSONRPC.Body)
	if got != "original-body" {
		t.Errorf("stored body aliased caller buffer: got %q", got)
	}
}

// TestCommitDisabledZeroAllocs is the MOCK-901 proof for the capture path: with
// journaling disabled, Commit builds nothing and allocates nothing. The
// enabled-check must come before any record construction, body copy or hash.
func TestCommitDisabledZeroAllocs(t *testing.T) {
	// No t.Parallel(): AllocsPerRun must run without interference.
	c := newCapturer(t, journalapi.Config{Enabled: false})
	ring := journal.New(journalapi.Config{Enabled: false, MaxRecords: 64})
	ctx := context.Background()
	in := fullInput()
	allocs := testing.AllocsPerRun(1000, func() {
		_, _ = c.Commit(ctx, ring, in)
	})
	if allocs != 0 {
		t.Fatalf("disabled Commit allocated %.2f objects/op, want 0 (MOCK-901)", allocs)
	}
}
