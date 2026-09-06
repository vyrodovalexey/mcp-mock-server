package determinism_test

import (
	"crypto/sha256"
	"fmt"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// Example shows the canonical per-request derivation path from ADR-002 — the
// exact call shape TASK-011 (journal), TASK-014 (engine) and TASK-016
// (catalogue) use to obtain a request's deterministic RNG. The body is hashed
// to a fixed 32-byte value before it becomes a derivation part, which is what
// keeps a large or oddly-framed body from creating an ambiguous preimage.
func Example() {
	const seed = uint64(42)

	// The seed comes from --seed (or a random value printed at startup,
	// MOCK-704). Root anchors the whole tree.
	root := determinism.Root(seed)

	// One subtree per logical instance (ADR-007 isolation).
	instanceKey := root.Derive(determinism.DomainInstance, []byte("orders-mock"))

	// One content-addressed leaf per request: method, the raw JSON-RPC id bytes
	// exactly as received, and sha256 of the canonical body. Concurrency and
	// arrival order are irrelevant — identical bytes always yield this key.
	method := "tools/call"
	rawID := []byte(`"req-7"`)
	bodyHash := sha256.Sum256([]byte(`{"name":"echo","arguments":{"x":1}}`))
	requestKey := instanceKey.Derive(
		determinism.DomainRequest,
		[]byte(method),
		rawID,
		bodyHash[:],
	)

	// The engine hands each pipeline stage a lazy accessor: a request that makes
	// no random decision never builds the RNG (MOCK-901).
	rng := requestKey.NewLazyRNG()

	// A stage that needs randomness draws in fixed order (ADR-002 rule 2).
	fmt.Println(rng().IntN(100))
	// Output: 7
}
