package modern_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// tools_list_test.go covers tools/list: a ≥5000 generated catalog returned
// whole with distinct names and no materialisation (MOCK-221 / crit 3), the
// built-in fallback (echo, sleep, fail in order, MOCK-202.20), authored overlay
// pass-through verbatim (MOCK-222.3/.4), resultType+serverInfo (crit 6) and
// byte-identity (crit 8).

// toolListTools decodes the tools array from a tools/list result.
func toolListTools(t *testing.T, raw json.RawMessage) []wire.ToolDescriptor {
	t.Helper()
	obj := mustObject(t, raw)
	toolsRaw, ok := obj["tools"]
	if !ok {
		t.Fatalf("tools/list result has no tools key: %s", raw)
	}
	var tools []wire.ToolDescriptor
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	return tools
}

// TestToolsListLargeCatalog proves crit 3 / MOCK-221: a 5000-tool generated
// catalog is returned in one response with exactly 5000 DISTINCT names. The
// catalog is virtual (ADR-004) — catalog.New is O(1) and At computes on demand —
// so the handler walking [0,Len) never materialises a stored source slice; the
// 5000-count test at the catalog level (internal/catalogue benchmark) proves the
// no-storage property, and this test proves the handler serves the whole range.
func TestToolsListLargeCatalog(t *testing.T) {
	const n = 5000
	cfg := &scenario.Catalog{Tools: &scenario.Generated{Count: intptr(n)}}
	snap := &handlerSnapshot{catalog: newCatalog(t, cfg)}

	raw, fault := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodToolsList, toolsListBody("1"))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	tools := toolListTools(t, raw)
	if len(tools) != n {
		t.Fatalf("tools/list returned %d tools, want %d", len(tools), n)
	}
	seen := make(map[string]bool, n)
	for _, tool := range tools {
		if tool.Name == "" {
			t.Fatalf("tool with empty name in a generated catalog")
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool name %q — names must be distinct across 5000 items", tool.Name)
		}
		seen[tool.Name] = true
	}
}

// TestToolsListBuiltinFallback proves MOCK-202.20 (crit 3 built-ins): with no
// scenario tool catalog, tools/list returns exactly the three built-ins echo,
// sleep, fail in that fixed order, each advertising an inputSchema. Three empty-
// catalog shapes exercise every fallback path: a bare snapshot (no config
// surface), a config snapshot with a nil catalog, and a config snapshot whose
// catalog was built from an empty tool config.
func TestToolsListBuiltinFallback(t *testing.T) {
	cases := map[string]engine.Snapshot{
		"bare-snapshot":     bareSnapshot{},
		"nil-catalog":       &handlerSnapshot{},
		"empty-tool-config": &handlerSnapshot{catalog: newCatalog(t, &scenario.Catalog{})},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			raw, fault := call(t, context.Background(), snap, modern.BuiltinConfig{},
				wire.MethodToolsList, toolsListBody("1"))
			if fault != nil {
				t.Fatalf("unexpected fault: %v", fault)
			}
			assertBuiltinOrder(t, toolListTools(t, raw))
		})
	}
}

// assertBuiltinOrder checks the tools are exactly echo, sleep, fail in order,
// each with a non-empty inputSchema (MOCK-202.20).
func assertBuiltinOrder(t *testing.T, tools []wire.ToolDescriptor) {
	t.Helper()
	want := []string{modern.ToolEcho, modern.ToolSleep, modern.ToolFail}
	if len(tools) != len(want) {
		t.Fatalf("built-in fallback returned %d tools, want %d (%v)", len(tools), len(want), toolNames(tools))
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("built-in tool %d = %q, want %q", i, tools[i].Name, name)
		}
		if len(tools[i].InputSchema) == 0 {
			t.Errorf("built-in tool %q advertises no inputSchema", name)
		}
	}
}

// toolNames extracts names for a diagnostic message.
func toolNames(tools []wire.ToolDescriptor) []string {
	out := make([]string, len(tools))
	for i, tool := range tools {
		out[i] = tool.Name
	}
	return out
}

// TestToolsListAuthoredOverlayVerbatim proves MOCK-222.3/.4: an authored tool's
// raw-JSON inputSchema is emitted byte-for-byte, including a non-alphabetical key
// order, and the authored item is present.
func TestToolsListAuthoredOverlayVerbatim(t *testing.T) {
	// Keys deliberately NOT in alphabetical order to prove no re-sorting.
	schema := `{"zeta":1,"alpha":2,"nested":{"y":true,"x":false}}`
	cfg := &scenario.Catalog{
		Items: []scenario.AuthoredItem{{
			Name:        "authored-tool",
			InputSchema: json.RawMessage(schema),
		}},
	}
	snap := &handlerSnapshot{catalog: newCatalog(t, cfg)}
	raw, fault := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodToolsList, toolsListBody("1"))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	tools := toolListTools(t, raw)
	var found *wire.ToolDescriptor
	for i := range tools {
		if tools[i].Name == "authored-tool" {
			found = &tools[i]
		}
	}
	if found == nil {
		t.Fatalf("authored tool not present in tools/list: %v", toolNames(tools))
	}
	if string(found.InputSchema) != schema {
		t.Errorf("authored inputSchema not verbatim:\n got=%s\nwant=%s", found.InputSchema, schema)
	}
}

// TestToolsListResultTypeAndServerInfo proves crit 6 for tools/list: resultType
// is "toolList" and _meta.serverInfo is present.
func TestToolsListResultTypeAndServerInfo(t *testing.T) {
	snap := &handlerSnapshot{}
	raw, fault := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodToolsList, toolsListBody("1"))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	obj := mustObject(t, raw)
	if got := string(obj["resultType"]); got != `"`+wire.ResultTypeToolList+`"` {
		t.Errorf("resultType = %s, want %q", got, wire.ResultTypeToolList)
	}
	meta := mustObject(t, obj["_meta"])
	if !hasKey(meta, "serverInfo") {
		t.Errorf("tools/list _meta missing serverInfo: %s", obj["_meta"])
	}
}

// TestToolsListByteIdentical proves crit 8: repeated 5000-tool tools/list
// responses at a fixed seed are byte-identical.
func TestToolsListByteIdentical(t *testing.T) {
	cfg := &scenario.Catalog{Tools: &scenario.Generated{Count: intptr(5000)}}
	snap := &handlerSnapshot{catalog: newCatalog(t, cfg)}
	first, _ := call(t, context.Background(), snap, modern.BuiltinConfig{},
		wire.MethodToolsList, toolsListBody("1"))
	for i := 0; i < 3; i++ {
		next, _ := call(t, context.Background(), snap, modern.BuiltinConfig{},
			wire.MethodToolsList, toolsListBody("1"))
		if string(next) != string(first) {
			t.Fatalf("tools/list body not byte-identical on run %d", i)
		}
	}
}
