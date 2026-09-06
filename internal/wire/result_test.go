package wire

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
)

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }
func i64ptr(i int64) *int64   { return &i }

// TestResultTypeOmissionSwitch proves MOCK-209.3: switches.omitResultType
// removes the resultType key entirely (absence, not a null/empty value), and
// the conformant form carries the annex value.
func TestResultTypeOmissionSwitch(t *testing.T) {
	si := &ServerInfo{Name: "mcpmock", Version: "0.0.0"}

	t.Run("conformant carries resultType", func(t *testing.T) {
		r := ToolCallResult{
			ResultType: strptr(ResultTypeToolResult),
			Content:    []TextContentBlock{NewTextContentBlock("hi")},
			Meta:       &ResultMeta{ServerInfo: si},
		}
		b, err := MarshalResult(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(b), `"resultType":"toolResult"`) {
			t.Errorf("conformant result missing resultType: %s", b)
		}
	})

	t.Run("omitResultType removes the key", func(t *testing.T) {
		r := ToolCallResult{
			ResultType: nil, // omitResultType
			Content:    []TextContentBlock{NewTextContentBlock("hi")},
			Meta:       &ResultMeta{ServerInfo: si},
		}
		b, err := MarshalResult(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(b), "resultType") {
			t.Errorf("omitResultType left a resultType key: %s", b)
		}
	})
}

// TestServerInfoMetaOmissionSwitch proves MOCK-209.4: switches.omitServerInfoMeta
// removes _meta.serverInfo. It also proves the two-level omission — omitting the
// whole _meta object versus omitting only serverInfo — is expressible.
func TestServerInfoMetaOmissionSwitch(t *testing.T) {
	t.Run("conformant carries _meta.serverInfo", func(t *testing.T) {
		r := ToolListResult{
			ResultType: strptr(ResultTypeToolList),
			Tools:      []ToolDescriptor{},
			Meta:       &ResultMeta{ServerInfo: &ServerInfo{Name: "mcpmock", Version: "1.2.3"}},
		}
		b, _ := MarshalResult(r)
		if !strings.Contains(string(b), `"serverInfo":{"name":"mcpmock","version":"1.2.3"}`) {
			t.Errorf("conformant result missing _meta.serverInfo: %s", b)
		}
	})

	t.Run("omitServerInfoMeta removes serverInfo but keeps _meta", func(t *testing.T) {
		r := ToolListResult{
			ResultType: strptr(ResultTypeToolList),
			Tools:      []ToolDescriptor{},
			Meta:       &ResultMeta{ServerInfo: nil}, // omitServerInfoMeta
		}
		b, _ := MarshalResult(r)
		if strings.Contains(string(b), "serverInfo") {
			t.Errorf("omitServerInfoMeta left a serverInfo key: %s", b)
		}
	})

	t.Run("omitting the whole _meta object", func(t *testing.T) {
		r := ToolListResult{
			ResultType: strptr(ResultTypeToolList),
			Tools:      []ToolDescriptor{},
			Meta:       nil,
		}
		b, _ := MarshalResult(r)
		if strings.Contains(string(b), "_meta") {
			t.Errorf("nil Meta left a _meta key: %s", b)
		}
	})
}

// TestIsErrorOmittedWhenFalse proves builtin-tools.md 4.2 [P-50] / MOCK-201.3:
// isError is emitted only when true; a successful result omits it entirely
// rather than carrying "isError": false.
func TestIsErrorOmittedWhenFalse(t *testing.T) {
	success := ToolCallResult{
		ResultType: strptr(ResultTypeToolResult),
		Content:    []TextContentBlock{NewTextContentBlock("ok")},
	}
	b, _ := MarshalResult(success)
	if strings.Contains(string(b), "isError") {
		t.Errorf("successful result carried isError: %s", b)
	}

	failed := ToolCallResult{
		ResultType: strptr(ResultTypeToolResult),
		Content:    []TextContentBlock{NewTextContentBlock("boom")},
		IsError:    boolptr(true),
	}
	b, _ = MarshalResult(failed)
	if !strings.Contains(string(b), `"isError":true`) {
		t.Errorf("failed result missing isError:true: %s", b)
	}
}

// TestDiscoverFieldOmissionIsAbsentNotNull proves MOCK-201.3: each discover
// field is independently omissible and an omitted field is ABSENT, never null.
func TestDiscoverFieldOmissionIsAbsentNotNull(t *testing.T) {
	t.Run("everything omitted except resultType", func(t *testing.T) {
		r := DiscoverResult{ResultType: strptr(ResultTypeDiscovery)}
		b, _ := MarshalResult(r)
		got := string(b)
		if got != `{"resultType":"discovery"}` {
			t.Errorf("minimal discover result = %s, want only resultType", got)
		}
		// No key should appear as null.
		for _, k := range []string{"supportedVersions", "capabilities", "instructions", "ttlMs", "cacheScope", "_meta"} {
			if strings.Contains(got, k) {
				t.Errorf("omitted field %q leaked into %s", k, got)
			}
		}
	})

	t.Run("ttlMs zero and negative are expressible and distinct from absent", func(t *testing.T) {
		// MOCK-201.5 / MOCK-232: 0 and negative are legal values, distinct from absence.
		zero := DiscoverResult{ResultType: strptr(ResultTypeDiscovery), CacheHints: CacheHints{TTLMs: i64ptr(0)}}
		b, _ := MarshalResult(zero)
		if !strings.Contains(string(b), `"ttlMs":0`) {
			t.Errorf("ttlMs:0 not emitted: %s", b)
		}
		neg := DiscoverResult{ResultType: strptr(ResultTypeDiscovery), CacheHints: CacheHints{TTLMs: i64ptr(-1)}}
		b, _ = MarshalResult(neg)
		if !strings.Contains(string(b), `"ttlMs":-1`) {
			t.Errorf("ttlMs:-1 not emitted: %s", b)
		}
		absent := DiscoverResult{ResultType: strptr(ResultTypeDiscovery)}
		b, _ = MarshalResult(absent)
		if strings.Contains(string(b), "ttlMs") {
			t.Errorf("absent ttlMs leaked: %s", b)
		}
	})

	t.Run("cacheScope accepts arbitrary strings", func(t *testing.T) {
		// MOCK-232.4: not an enum.
		r := DiscoverResult{ResultType: strptr(ResultTypeDiscovery), CacheHints: CacheHints{CacheScope: strptr("bespoke-scope")}}
		b, _ := MarshalResult(r)
		if !strings.Contains(string(b), `"cacheScope":"bespoke-scope"`) {
			t.Errorf("arbitrary cacheScope not emitted: %s", b)
		}
	})
}

// TestByteStableEmissionViaCanonicalEncoder proves the byte-stability
// requirement (design principle §0.1): the same result value emitted twice
// through the jsonrpc response path yields byte-identical bytes, and
// HTML-significant characters survive unescaped.
func TestByteStableEmissionViaCanonicalEncoder(t *testing.T) {
	id := jsonrpc.StringID("req-1")
	build := func() []byte {
		r := ToolCallResult{
			ResultType: strptr(ResultTypeToolResult),
			// <, > and & must survive: a hub asserting on the exact bytes must
			// see them literally, not as \u003c etc.
			Content: []TextContentBlock{NewTextContentBlock("a < b && c > d")},
			Meta:    &ResultMeta{ServerInfo: &ServerInfo{Name: "mcpmock", Version: "1.0.0"}},
		}
		raw, err := MarshalResult(r)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		out, err := jsonrpc.NewResultResponse(id, raw).Encode()
		if err != nil {
			t.Fatalf("encode response: %v", err)
		}
		return out
	}

	first := build()
	second := build()
	if string(first) != string(second) {
		t.Fatalf("emission not byte-stable:\n first=%s\nsecond=%s", first, second)
	}
	if !strings.Contains(string(first), "a < b && c > d") {
		t.Errorf("HTML-significant characters were escaped: %s", first)
	}
	// Sanity: the emitted response is valid JSON and echoes the id.
	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(first, &probe); err != nil {
		t.Fatalf("emitted response is not valid JSON: %v", err)
	}
	if probe.JSONRPC != "2.0" || probe.ID != "req-1" {
		t.Errorf("unexpected envelope: jsonrpc=%q id=%q", probe.JSONRPC, probe.ID)
	}
}

// TestMarshalResultKeepsAuthoredRawJSONVerbatim proves that raw-JSON members
// (capabilities, structuredContent, tool schemas) reach the wire verbatim
// (MOCK-222.4) — the canonical encoder is never applied to a result body.
func TestMarshalResultKeepsAuthoredRawJSONVerbatim(t *testing.T) {
	// Deliberately unusual key order that a canonicaliser would re-sort.
	authored := json.RawMessage(`{"z":1,"a":2}`)
	r := DiscoverResult{
		ResultType:   strptr(ResultTypeDiscovery),
		Capabilities: authored,
	}
	b, _ := MarshalResult(r)
	if !strings.Contains(string(b), `"capabilities":{"z":1,"a":2}`) {
		t.Errorf("authored capabilities key order not preserved: %s", b)
	}
}
