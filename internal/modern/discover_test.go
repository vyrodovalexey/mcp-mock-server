package modern_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// discover_test.go covers server/discover: every configurable field present
// (MOCK-201.1, TASK-018 crit 1), each field omitted as an ABSENT key not null
// (MOCK-201.3, crit 2), resultType+serverInfo present (crit 6), and byte-
// identity at a fixed seed (crit 8).

// fullDiscover is a spec.discover configuring every one of the six fields, so a
// present-fields test can assert all six appear.
func fullDiscover() *scenario.Discover {
	return &scenario.Discover{
		SupportedVersions: []string{"2026-07-28", "2025-06-01"},
		Capabilities:      json.RawMessage(`{"tools":{"listChanged":true},"extensions":{"x":1}}`),
		Instructions:      strptr("be helpful"),
		ServerInfo:        json.RawMessage(`{"name":"authored","version":"9.9.9"}`),
		TTLMs:             json.RawMessage(`60000`),
		CacheScope:        json.RawMessage(`"public"`),
	}
}

// TestDiscoverAllFieldsPresent proves MOCK-201.1 (crit 1) and crit 6: every
// configured discover field appears, resultType is "discovery", and
// _meta.serverInfo is present (and reflects the authored serverInfo).
func TestDiscoverAllFieldsPresent(t *testing.T) {
	snap := &handlerSnapshot{discover: fullDiscover()}
	raw, fault := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodDiscover, discoverBody("1"))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	obj := mustObject(t, raw)

	for _, key := range []string{
		"resultType", "supportedVersions", "capabilities", "instructions",
		"ttlMs", "cacheScope", "_meta",
	} {
		if !hasKey(obj, key) {
			t.Errorf("discover result missing configured key %q (raw=%s)", key, raw)
		}
	}

	if got := string(obj["resultType"]); got != `"`+wire.ResultTypeDiscovery+`"` {
		t.Errorf("resultType = %s, want %q", got, wire.ResultTypeDiscovery)
	}
	// capabilities emitted verbatim, extension keys preserved (MOCK-201.2 shape).
	if got := string(obj["capabilities"]); got != `{"tools":{"listChanged":true},"extensions":{"x":1}}` {
		t.Errorf("capabilities not verbatim: %s", got)
	}
	// _meta.serverInfo reflects the authored identity.
	meta := mustObject(t, obj["_meta"])
	if !hasKey(meta, "serverInfo") {
		t.Fatalf("_meta missing serverInfo: %s", obj["_meta"])
	}
	if got := string(meta["serverInfo"]); got != `{"name":"authored","version":"9.9.9"}` {
		t.Errorf("serverInfo = %s, want authored identity", got)
	}
}

// TestDiscoverOmittedFieldsAbsent proves MOCK-201.3 (crit 2): a discover with no
// configuration omits every optional field as an ABSENT key — none appears with
// a null value. resultType and _meta.serverInfo remain present (they are not
// spec.discover fields; they are always-on unless a MOCK-209 switch removes
// them).
func TestDiscoverOmittedFieldsAbsent(t *testing.T) {
	snap := &handlerSnapshot{discover: nil} // no discover config at all
	raw, fault := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodDiscover, discoverBody("1"))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	obj := mustObject(t, raw)

	for _, key := range []string{
		"supportedVersions", "capabilities", "instructions", "ttlMs", "cacheScope",
	} {
		if hasKey(obj, key) {
			t.Errorf("omitted discover field %q present (raw=%s) — MOCK-201.3 requires absent, not null", key, raw)
		}
	}
	// Absent, so certainly not present-with-null: assert no "null" appears for these.
	if raw2 := string(raw); containsNullField(raw2) {
		t.Errorf("discover result carries a null-valued field, violating absent-not-null: %s", raw2)
	}
	// Always-on fields still present.
	if !hasKey(obj, "resultType") || !hasKey(obj, "_meta") {
		t.Errorf("resultType and _meta must remain present with no switches: %s", raw)
	}
}

// TestDiscoverPerFieldOmission proves crit 2 at the granularity of a single
// field: configuring only instructions leaves instructions present and every
// other optional field absent.
func TestDiscoverPerFieldOmission(t *testing.T) {
	snap := &handlerSnapshot{discover: &scenario.Discover{Instructions: strptr("only this")}}
	raw, fault := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodDiscover, discoverBody("1"))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	obj := mustObject(t, raw)
	if !hasKey(obj, "instructions") {
		t.Errorf("configured instructions absent: %s", raw)
	}
	for _, key := range []string{"supportedVersions", "capabilities", "ttlMs", "cacheScope"} {
		if hasKey(obj, key) {
			t.Errorf("unconfigured field %q present: %s", key, raw)
		}
	}
}

// TestDiscoverByteIdentical proves crit 8: two identical requests at the same
// seed produce byte-identical bodies.
func TestDiscoverByteIdentical(t *testing.T) {
	snap := &handlerSnapshot{discover: fullDiscover()}
	first, _ := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodDiscover, discoverBody("1"))
	for i := 0; i < 8; i++ {
		next, _ := call(t, context.Background(), snap, modern.BuiltinConfig{},
			wire.MethodDiscover, discoverBody("1"))
		if string(next) != string(first) {
			t.Fatalf("discover body not byte-identical on run %d:\n first=%s\n next =%s", i, first, next)
		}
	}
}

// containsNullField is a coarse check that no top-level key is emitted with a
// literal null value, used to reinforce the absent-not-null rule. It looks for
// the substring `:null` which cannot appear in the fixture's non-null members.
func containsNullField(s string) bool {
	for i := 0; i+5 <= len(s); i++ {
		if s[i:i+5] == ":null" {
			return true
		}
	}
	return false
}
