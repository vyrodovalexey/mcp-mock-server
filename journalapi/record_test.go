package journalapi_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	japi "github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// sampleRecord builds a representative record exercising header fidelity,
// duplicate/differently-cased headers, raw JSON-RPC ids, decoded meta and an
// MRTR chain. It is the fixture the round-trip and NDJSON tests share.
func sampleRecord(seq uint64) japi.Record {
	wall := time.Date(2026, 9, 4, 9, 18, 47, 123456789, time.UTC)
	exp := wall.Add(time.Hour)
	return japi.Record{
		SchemaVersion: japi.SchemaVersion,
		Seq:           seq,
		Instance:      "upstream-a",
		Generation:    7,
		WallTime:      wall,
		MonoNs:        424242,
		DurationNs:    999,
		Transport:     japi.TransportHTTP,
		Direction:     japi.DirectionInboundRequest,
		Era:           "modern",
		Peer:          "127.0.0.1:54321",
		HTTP: &japi.HTTPPart{
			Method: "POST",
			Path:   "/mcp",
			Query:  "trace=1",
			// Duplicate name with different casing, plus a redacted secret.
			Headers: [][2]string{
				{"x-mcp-header", "first"},
				{"X-Mcp-Header", "second"},
				{"Content-Type", "application/json"},
				{"Authorization", japi.RedactedHeaderValue + "deadbeef>"},
			},
			Status: 200,
			RespHeaders: [][2]string{
				{"Content-Type", "application/json"},
			},
		},
		JSONRPC: japi.JSONRPCPart{
			ID:         json.RawMessage(`"1"`),
			Method:     "tools/call",
			Name:       "search",
			Params:     json.RawMessage(`{"q":"x"}`),
			Body:       []byte(`{"jsonrpc":"2.0"}`),
			BodyLength: 17,
		},
		Meta: map[string]json.RawMessage{
			"protocolVersion":           json.RawMessage(`"2026-07-28"`),
			"io.modelcontextprotocol/x": json.RawMessage(`{"k":1}`),
			"clientCapabilities":        json.RawMessage(`{"sampling":{}}`),
		},
		Response: &japi.ResponsePart{
			Status: 200,
			Shape:  "json",
			Body:   []byte(`{"result":{}}`),
		},
		Credential: &japi.CredentialPart{
			Present:   true,
			Scheme:    "Bearer",
			HashAlg:   "hmac-sha256/128",
			Hash:      "0011223344556677",
			ExpiresAt: &exp,
			Decision:  "accepted",
		},
		MetaValidation: &japi.MetaValidationPart{
			Mode:    "lenient",
			Outcome: "tolerated",
			Missing: []string{"clientCapabilities"},
		},
		Correlation: japi.CorrelationPart{
			TraceID:          "0af7651916cd43dd8448eb211c80319c",
			SpanID:           "b7ad6b7169203331",
			TraceparentRaw:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			TraceparentValid: true,
			Sampled:          true,
		},
		MRTR: &japi.MRTRPart{
			ChainID:   "chain-1",
			Round:     1,
			InitialID: json.RawMessage(`"1"`),
		},
	}
}

func TestRecordJSONRoundTrip(t *testing.T) {
	t.Parallel()
	rec := sampleRecord(41)

	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got japi.Record
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b2, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(b, b2) {
		t.Fatalf("JSON not stable across round-trip:\n first: %s\nsecond: %s", b, b2)
	}
}

func TestRecordHeaderFidelityPreserved(t *testing.T) {
	t.Parallel()
	rec := sampleRecord(1)
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got japi.Record
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := [][2]string{
		{"x-mcp-header", "first"},
		{"X-Mcp-Header", "second"},
		{"Content-Type", "application/json"},
		{"Authorization", japi.RedactedHeaderValue + "deadbeef>"},
	}
	if len(got.HTTP.Headers) != len(want) {
		t.Fatalf("header count = %d, want %d", len(got.HTTP.Headers), len(want))
	}
	for i := range want {
		if got.HTTP.Headers[i] != want[i] {
			t.Errorf("header[%d] = %v, want %v (order/casing/duplicates must survive)",
				i, got.HTTP.Headers[i], want[i])
		}
	}
}

func TestRecordRawIDTypePreserved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   json.RawMessage
	}{
		{"numeric", json.RawMessage(`1`)},
		{"string", json.RawMessage(`"1"`)},
		{"float", json.RawMessage(`1.0`)},
		{"null", json.RawMessage(`null`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := japi.Record{SchemaVersion: japi.SchemaVersion, Seq: 1}
			rec.JSONRPC.ID = tc.id
			b, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got japi.Record
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(got.JSONRPC.ID) != string(tc.id) {
				t.Errorf("id = %s, want %s (raw JSON type must be preserved)",
					got.JSONRPC.ID, tc.id)
			}
		})
	}
}

// TestCredentialHasNoRawField is the structural credential-safety control
// (MOCK-407.2): the serialised CredentialPart must expose no field that could
// carry a raw secret, and marshalling a populated credential must never emit the
// sentinel raw value.
func TestCredentialHasNoRawField(t *testing.T) {
	t.Parallel()
	const secret = "Bearer sk-super-secret-value"
	cred := japi.CredentialPart{
		Present:  true,
		Scheme:   "Bearer",
		HashAlg:  "hmac-sha256/128",
		Hash:     "0011223344556677",
		Decision: "accepted",
	}
	b, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The set of JSON keys is fixed and contains none that could hold a secret.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	forbidden := []string{"raw", "value", "token", "authorization", "secret", "plaintext", "clear"}
	for k := range keys {
		for _, f := range forbidden {
			if strings.EqualFold(k, f) {
				t.Errorf("CredentialPart exposes forbidden field %q; a raw credential must be unrepresentable", k)
			}
		}
	}
	// A struct field for a secret does not exist: even if a caller tries, there
	// is nowhere to put the secret, so it can never reach the serialised form.
	if strings.Contains(string(b), secret) {
		t.Errorf("serialised credential contained the raw secret: %s", b)
	}
}

// TestRecordSerialisedJournalHasNoSecret scans a whole serialised record — the
// NDJSON form that would end up in a CI artifact — for a fixture token, matching
// the intent of MOCK-601.5 at the contract layer: nothing in the type graph
// carries a raw credential, and the redaction marker survives verbatim through
// the NDJSON writer (which disables HTML escaping).
func TestRecordSerialisedJournalHasNoSecret(t *testing.T) {
	t.Parallel()
	const fixtureToken = "eyJ-FIXTURE-TOKEN-DO-NOT-LEAK"
	rec := sampleRecord(1)
	// Simulate capture having redacted the Authorization header.
	rec.HTTP.Headers[3] = [2]string{"Authorization", japi.RedactedHeaderValue + "abcd1234>"}

	var buf bytes.Buffer
	w := japi.NewWriter(&buf)
	if err := w.Write(rec); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if strings.Contains(buf.String(), fixtureToken) {
		t.Fatalf("fixture token leaked into serialised record")
	}
	if !strings.Contains(buf.String(), japi.RedactedHeaderValue) {
		t.Fatalf("expected redaction marker in serialised record, got: %s", buf.String())
	}
}

func TestConfigWithDefaults(t *testing.T) {
	t.Parallel()
	got := japi.Config{Enabled: true}.WithDefaults()
	if got.Mode != japi.CaptureFull {
		t.Errorf("Mode = %q, want %q", got.Mode, japi.CaptureFull)
	}
	if got.MaxRecords != japi.DefaultMaxRecords {
		t.Errorf("MaxRecords = %d, want %d", got.MaxRecords, japi.DefaultMaxRecords)
	}
	if got.MaxBytes != japi.DefaultMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", got.MaxBytes, japi.DefaultMaxBytes)
	}
	if got.Overflow != japi.OverflowDropOldest {
		t.Errorf("Overflow = %q, want %q", got.Overflow, japi.OverflowDropOldest)
	}
	if got.BlockTimeout != japi.DefaultBlockTimeout {
		t.Errorf("BlockTimeout = %v, want %v", got.BlockTimeout, japi.DefaultBlockTimeout)
	}
}

// TestConfigDisabledZeroValue documents that the disabled path is the zero
// value and needs no allocation: an unset Config is disabled.
func TestConfigDisabledZeroValue(t *testing.T) {
	t.Parallel()
	var c japi.Config
	if c.Enabled {
		t.Fatal("zero-value Config must be disabled so the MOCK-901 path is free")
	}
}
