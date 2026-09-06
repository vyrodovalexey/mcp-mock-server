package modern_test

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/goleak"

	"github.com/vyrodovalexey/mcp-mock-server/internal/catalogue"
	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// TestMain installs goleak so the sleep tool's timer-and-goroutine discipline is
// verified module-wide: any test that leaks a goroutine or an unstopped timer
// (the MOCK-107.7 / builtin-tools.md 3.4 obligation on sleep and its
// cancellation path) fails the suite here rather than silently.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// handlers_fixtures_test.go provides the in-package doubles the TASK-018 handler
// tests share: a snapshot that exposes the discover/catalog/omission surface the
// handlers read, an exchange builder, and helpers to decode a handler's raw
// result. They stand in for internal/instance's *Snapshot so the handlers are
// exercised in isolation, exactly as the engine tests double the instance.

// testSeed is a fixed seed so the virtual catalog's generated names are
// reproducible across runs — the fixedness is the point, not the value.
const testSeed uint64 = 0x5678

// handlerSnapshot is a configurable engine.Snapshot that also satisfies the
// handlers' consumer interfaces (Catalog, Discover, OmitResultType,
// OmitServerInfoMeta). It is immutable per test; a test sets its fields before
// building an exchange.
type handlerSnapshot struct {
	discover     *scenario.Discover
	catalog      *catalog.Catalog
	omitRT       bool
	omitSrvInfo  bool
	disabled     map[string]bool
	validatorNil bool
	era          string
	selfCheckOn  bool
	hidden       []string
}

func (s *handlerSnapshot) Gen() uint64 { return 1 }
func (s *handlerSnapshot) Era() string {
	if s.era == "" {
		return "modern"
	}
	return s.era
}
func (s *handlerSnapshot) JournalEnabled() bool { return false }

func (s *handlerSnapshot) MetaValidator() engine.MetaValidator {
	if s.validatorNil {
		return nil
	}
	return engine.AcceptAllMeta
}

func (s *handlerSnapshot) MethodEnabled(method string) bool { return !s.disabled[method] }

func (s *handlerSnapshot) Catalog() *catalog.Catalog    { return s.catalog }
func (s *handlerSnapshot) Discover() *scenario.Discover { return s.discover }
func (s *handlerSnapshot) OmitResultType() bool         { return s.omitRT }
func (s *handlerSnapshot) OmitServerInfoMeta() bool     { return s.omitSrvInfo }
func (s *handlerSnapshot) SelfCheck() bool              { return s.selfCheckOn }
func (s *handlerSnapshot) HiddenCapabilities() []string { return s.hidden }

// bareSnapshot satisfies ONLY engine.Snapshot (not the handler consumer
// interfaces), to prove the handlers fall back gracefully to empty
// configuration rather than panicking when a snapshot does not expose the
// discover/catalog surface.
type bareSnapshot struct{}

func (bareSnapshot) Gen() uint64                         { return 1 }
func (bareSnapshot) Era() string                         { return "modern" }
func (bareSnapshot) JournalEnabled() bool                { return false }
func (bareSnapshot) MetaValidator() engine.MetaValidator { return engine.AcceptAllMeta }
func (bareSnapshot) MethodEnabled(string) bool           { return true }

// newCatalog builds a real virtual catalog at the fixed test seed from a
// scenario catalog config, so tools/list tests exercise the genuine ADR-004
// on-demand path.
func newCatalog(t *testing.T, cfg *scenario.Catalog) *catalog.Catalog {
	t.Helper()
	key := determinism.Root(testSeed).Derive(determinism.DomainInstance, []byte("test"))
	return catalog.New(key, cfg)
}

// exchange builds an engine.Exchange for method with the given raw JSON-RPC
// body, decoded through the real envelope decoder so the handler sees exactly
// what the pipeline would hand it. ctx is the request context.
func exchange(t *testing.T, ctx context.Context, snap engine.Snapshot, body string) *engine.Exchange {
	t.Helper()
	req, err := jsonrpc.DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("decode request %q: %v", body, err)
	}
	return &engine.Exchange{Ctx: ctx, Snapshot: snap, Request: req, Raw: []byte(body)}
}

// call runs one handler by dispatching through a registry, mirroring the engine
// dispatch stage. It returns the raw result and the fault. bcfg configures the
// built-in behaviors.
func call(
	t *testing.T, ctx context.Context, snap engine.Snapshot, bcfg modern.BuiltinConfig, method, body string,
) (json.RawMessage, *engine.Fault) {
	t.Helper()
	reg := engine.NewRegistry()
	modern.RegisterHandlers(reg, bcfg)
	h, ok := reg.Lookup(method)
	if !ok {
		t.Fatalf("no handler registered for %q", method)
	}
	ex := exchange(t, ctx, snap, body)
	return h.Handle(ctx, ex)
}

// mustObject unmarshals result bytes into a generic map, failing the test on a
// decode error, so assertions can index result members.
func mustObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("result is not a JSON object: %v (raw=%s)", err, raw)
	}
	return obj
}

// hasKey reports whether the decoded object contains key.
func hasKey(obj map[string]json.RawMessage, key string) bool {
	_, ok := obj[key]
	return ok
}

// toolsCallBody builds a tools/call request body naming tool with the given raw
// arguments JSON (pass "" to omit arguments entirely).
func toolsCallBody(id, tool, argsJSON string) string {
	inner := `"name":"` + tool + `"`
	if argsJSON != "" {
		inner += `,"arguments":` + argsJSON
	}
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"` + wire.MethodToolsCall +
		`","params":{` + inner + `}}`
}

// discoverBody builds a server/discover request body with the given id.
func discoverBody(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"` + wire.MethodDiscover + `","params":{}}`
}

// toolsListBody builds a tools/list request body with the given id.
func toolsListBody(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"` + wire.MethodToolsList + `","params":{}}`
}

// strptr and boolptr are pointer-literal helpers for scenario config fields.
func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }
func intptr(i int) *int       { return &i }
