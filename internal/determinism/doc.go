// Package determinism is the kernel that makes design principle §0.1 true:
// same config + same seed ⇒ byte-identical responses. It implements ADR-002 —
// a hierarchical, content-addressed seed tree, a ChaCha8 pseudo-random source
// derived from any node of that tree, and a virtual clock that replaces the
// wall clock in response bodies.
//
// # The seed tree
//
// A single uint64 --seed anchors everything (MOCK-704). From it:
//
//	root        := determinism.Root(seed)
//	instanceKey := root.Derive(determinism.DomainInstance, []byte(instanceName))
//	requestKey  := instanceKey.Derive(determinism.DomainRequest,
//	                   []byte(method),
//	                   rawJSONRPCID,        // bytes exactly as received
//	                   canonicalBodyHash)   // sha256 of RFC 8785-style canonical body
//	rng         := requestKey.NewLazyRNG() // *rand.Rand built on first draw only
//
// Every derivation is HMAC-SHA256: Root keys the HMAC by the big-endian seed
// over a fixed label; [Key.Derive] keys the HMAC by the parent key over
// domain || 0x00 || parts. The result is a pure function of its inputs, so it
// is identical across processes, across GOMAXPROCS settings, and — crucially —
// regardless of the order in which concurrent requests are handled.
//
// # Why content-addressed, not sequential
//
// The rejected-but-tempting design is one shared *rand.Rand, or a per-request
// RNG seeded from a request counter. Both make the stream a request draws from
// depend on how many other requests drew first, which depends on goroutine
// scheduling — so replaying the same test can produce different bytes. That is
// not determinism; it only looks like it in single-threaded tests. Here two
// concurrent requests with identical bytes get identical streams, two different
// requests never share a stream, and there is no cross-request mutable RNG
// state anywhere. See ADR-002 options 1–4.
//
// # Fixed draw order (ADR-002 rule 2)
//
// Within a request the RNG is consumed in the pipeline order of
// architecture.md §5. New random decisions are appended to the end of a stage's
// draws, never inserted, or all downstream golden files shift. Each stage
// documents and tests its draw count. This package provides the RNG; the engine
// (TASK-014) owns the draw-order discipline and its per-stage draw-count tests.
//
// # Concurrency contract
//
// [Key] is an immutable value type with no internal mutable state; a Key may be
// copied, stored in a snapshot, and read concurrently by any number of
// goroutines. [Root], [Key.Derive] and [VirtualClock] are pure functions safe
// for unrestricted concurrent use.
//
// A *rand.Rand returned by [Key.RNG] or by the accessor from [Key.NewLazyRNG]
// is NOT safe for concurrent draws: it carries mutable stream position. The
// intended usage is one RNG per request, drawn from on a single goroutine in
// fixed order. The accessor from [Key.NewLazyRNG] may itself be called
// concurrently (construction is once-only), but the *rand.Rand it hands back
// must be drawn from by one goroutine at a time.
//
// This package holds no process-global mutable state and runs no init()-time
// side effects (ADR-007). Every value is reachable only from an explicit seed,
// which is what lets ≥200 logical instances share the kernel without
// interference.
//
// # Boundary of the determinism guarantee
//
// What IS covered — reproducible byte-for-byte from (seed, request bytes):
//
//   - every derived key, and therefore every RNG stream and every value drawn
//     from it (ids, cursors, jitter, seeded ordering, generated catalog
//     names);
//   - every timestamp that appears in a response body, via [VirtualClock],
//     which never reads the wall clock.
//
// What is NOT covered — deliberate, documented exceptions:
//
//   - Response TIMING. A sleep behavior and injected latency (MOCK-501) delay
//     real wall-clock time; §0.1's "byte-identical" is read as applying to
//     response bytes, not to how long they took (ADR-002 GAP-008). A sleep
//     reports its configured duration, never a measurement, and does not
//     advance [VirtualClock] (builtin-tools.md §1.4).
//   - Count-triggered faults (MOCK-501..508 "count") use atomic counters whose
//     assignment across concurrent requests is arrival-order dependent. This is
//     a genuine residual nondeterminism ADR-002 routes to GAP-007; it lives in
//     the fault engine, not here, and this package neither provides nor blesses
//     a shared counter.
//   - Journal wall timestamps, metrics and log lines. These are evidence about
//     the run, not modeled response content, and legitimately use time.Now()
//     elsewhere in the module. This package's clock is only for response
//     bodies.
//
// # Import rule and dependencies
//
// The package depends only on the standard library: crypto/sha256,
// encoding/binary, math/rand/v2, sync and time. HMAC-SHA256 is implemented in
// hmac.go rather than imported from crypto/hmac, because the streaming
// crypto/hmac API allocates per call and ADR-002's key derivation runs on the
// request hot path; crypto/hmac is used only in the tests, as the reference
// oracle that pins the hand-rolled primitive to the canonical algorithm. Per
// architecture.md §6.1 this package sits directly above internal/ordered in the
// layering, but needs nothing from it at compile time, so it imports no other
// internal package. It adds no third-party dependency (ADR-013): Go 1.27's
// math/rand/v2 already provides ChaCha8.
//
// # Golden vectors
//
// testdata/vectors.json pins Root, Derive, RNG and VirtualClock outputs for a
// fixed seed and fixed derivation paths. Those bytes are a contract: changing
// the derivation constants (a domain string, the root label, the separator, the
// clock bound) changes the vectors and is a breaking change requiring a
// scenario apiVersion bump, because it invalidates every golden fixture and
// every recorded seed downstream (ADR-002 reversibility: HARD).
package determinism
