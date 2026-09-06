package journal_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// TestSentinelNeverInSerializedOutput is the security test the task singles out:
// a request carrying a sentinel bearer token in its Authorization header is
// captured, and every serialized form of the resulting journal — the JSON array,
// the NDJSON stream, and per-record JSON — is scanned for the raw token. It must
// appear nowhere (MOCK-601.5 / MOCK-407.2, security.md §5).
//
// The scan is over the *serialized bytes*, not the in-memory struct, because a
// hash that stringified back to the raw value would slip a struct-field check
// (task risk note). Redaction is proven directly: the Authorization header value
// is replaced by the redaction marker and no longer contains the token.
func TestSentinelNeverInSerializedOutput(t *testing.T) {
	t.Parallel()
	c := newCapturer(t, journalapi.Config{Enabled: true, Mode: journalapi.CaptureFull})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 16})

	// A request whose Authorization header, and a Cookie, both carry the token.
	in := fullInput()
	in.HTTP.Headers = [][2]string{
		{"Authorization", "Bearer " + sentinelToken},
		{"Cookie", "session=" + sentinelToken},
		{"X-Safe", "harmless"},
	}
	in.Credential = "Bearer " + sentinelToken
	if _, err := c.Commit(context.Background(), ring, in); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	recs := ring.Snapshot()

	// 1. JSON array form.
	arr, err := json.Marshal(recs)
	if err != nil {
		t.Fatalf("marshal array: %v", err)
	}
	assertNoToken(t, "json-array", arr)

	// 2. NDJSON stream form (through the public writer).
	var nd bytes.Buffer
	w := journalapi.NewWriter(&nd)
	if err := w.WriteAll(recs); err != nil {
		t.Fatalf("ndjson write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("ndjson close: %v", err)
	}
	assertNoToken(t, "ndjson", nd.Bytes())

	// 3. Streaming export form.
	var exp bytes.Buffer
	if _, err := ring.ExportNDJSON(context.Background(), &exp, journal.ExportOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	assertNoToken(t, "export-ndjson", exp.Bytes())

	// 4. Direct redaction assertion: the Authorization value is the marker, not
	//    the token; its name, casing and position are preserved.
	headers := recs[0].HTTP.Headers
	if headers[0][0] != "Authorization" {
		t.Fatalf("header[0] name = %q, want Authorization (position/casing lost)", headers[0][0])
	}
	if !strings.HasPrefix(headers[0][1], journalapi.RedactedHeaderValue) {
		t.Errorf("Authorization value not redacted: %q", headers[0][1])
	}
	if strings.Contains(headers[0][1], sentinelToken) {
		t.Errorf("redacted Authorization still contains the token")
	}
	if headers[1][0] != "Cookie" || strings.Contains(headers[1][1], sentinelToken) {
		t.Errorf("Cookie not redacted: %v", headers[1])
	}
	if headers[2] != [2]string{"X-Safe", "harmless"} {
		t.Errorf("non-sensitive header altered: %v", headers[2])
	}
}

// assertNoToken fails if the sentinel token appears anywhere in b.
func assertNoToken(t *testing.T, form string, b []byte) {
	t.Helper()
	if bytes.Contains(b, []byte(sentinelToken)) {
		t.Fatalf("sentinel token leaked into %s serialized output", form)
	}
}

// TestConfiguredRedactHeaders covers the configurable redaction list: a header
// named only in Config.RedactHeaders is redacted too, case-insensitively.
func TestConfiguredRedactHeaders(t *testing.T) {
	t.Parallel()
	cfg := journalapi.Config{Enabled: true, RedactHeaders: []string{"X-Custom-Secret"}}
	c := newCapturer(t, cfg)
	ring := journal.New(cfg)
	in := fullInput()
	in.HTTP.Headers = [][2]string{
		{"x-custom-secret", sentinelToken},
		{"X-Plain", "ok"},
	}
	in.Credential = ""
	if _, err := c.Commit(context.Background(), ring, in); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	headers := ring.Snapshot()[0].HTTP.Headers
	if strings.Contains(headers[0][1], sentinelToken) {
		t.Errorf("configured redact header not redacted: %v", headers[0])
	}
	if !strings.HasPrefix(headers[0][1], journalapi.RedactedHeaderValue) {
		t.Errorf("configured redact header value not a marker: %v", headers[0])
	}
	if headers[1] != [2]string{"X-Plain", "ok"} {
		t.Errorf("plain header altered: %v", headers[1])
	}
}
