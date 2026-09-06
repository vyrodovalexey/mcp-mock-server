package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// TestOversizedJSONRPCIDDoesNotPanic is the REV-006 regression at the engine
// boundary: a JSON-RPC id is client-controlled and ADR-002 feeds its raw bytes
// verbatim into the request-key derivation. An id longer than the derivation's
// fixed inner buffer must NOT crash the process — a panic here is recovered
// per-connection on HTTP but process-fatal on the stdio worker pool, and the id
// is remotely attacker-controlled. The request must instead complete normally,
// deriving the key through the streaming fallback.
//
// The handler draws from ex.Rand, which forces the lazy request-key derivation
// (the exact path that would have panicked); if derivation panicked, Handle
// would not return and the drawn value would never be produced.
func TestOversizedJSONRPCIDDoesNotPanic(t *testing.T) {
	inst := newInstance(1, "inst", false)
	p := engine.NewPipeline(registryFor(randomHandler()))

	// A ~100 KB string id, far past the 256-byte inner derivation buffer.
	hugeID := `"` + strings.Repeat("A", 100*1024) + `"`
	raw := []byte(`{"jsonrpc":"2.0","id":` + hugeID +
		`,"method":"` + wire.MethodToolsCall +
		`","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`)

	sink := engine.NewBufferedSink()
	if err := p.Handle(context.Background(), buildExchange(
		context.Background(), inst, engine.KindStdio, raw), sink); err != nil {
		t.Fatalf("Handle returned infrastructure error: %v", err)
	}

	// A well-formed result must have been produced (the handler ran and drew
	// from the RNG through the successfully-derived key).
	body := sink.Bytes()
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, body)
	}
	if _, ok := env["result"]; !ok {
		t.Fatalf("response carries no result (id-derivation path failed): %s", body)
	}
}
