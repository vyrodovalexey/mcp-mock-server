// Package jsonrpc implements the JSON-RPC 2.0 envelope layer and the canonical
// JSON encoder that together make mcpmock's byte-identical-response guarantee
// (design principle §0.1) achievable.
//
// It sits directly above internal/ordered in the module dependency graph
// (architecture.md §6) and is imported by internal/wire (TASK-010), which adds
// the MCP-specific method names, _meta keys and result-type table on top. This
// package contains only JSON-RPC 2.0 itself, which is a ratified standard and
// therefore not part of the provisional wire surface GAP-003 tracks.
//
// # What lives here
//
//   - [ID] — a request/response id preserved as the raw bytes received, with
//     its [IDKind] recoverable. Numeric 1, string "1", 1.0 and null are all
//     distinct, which MOCK-247 (retry-id distinctness) and echo-back fidelity
//     require, and which the ADR-002 request-key derivation depends on.
//   - [Request] / [Response] / [Error] — envelope types whose member presence
//     is tracked, so a caller can both build well-formed messages and represent
//     the pathological ones MOCK-503 emits.
//   - [Canonical] / [CanonicalJSON] — an RFC 8785-style canonical encoder used
//     solely on the hashing/derivation path (ADR-002, ADR-003 §4), never for
//     response emission.
//   - The standard JSON-RPC error-code constants (codes.go).
//
// # Two encoders, deliberately distinct
//
// This package emits JSON on two different paths that must not be confused:
//
//  1. Response emission ([Response.MarshalJSON]) preserves authored key order
//     and authored bytes. MOCK-222.4 requires hand-authored result bodies —
//     including deliberately unusual key orders and malformed structures — to
//     reach the client verbatim. Ordering is the caller's (via ordered.Map or a
//     raw json.RawMessage); this package only disables HTML escaping so that <,
//     > and & survive.
//
//  2. Canonicalisation ([Canonical]) re-sorts object keys and normalises
//     numbers and string escapes. It exists only to feed sha256 on the
//     derivation path and for golden-file digests. Applying it to a response
//     would destroy authored order, so it never touches [Response.Result].
//
// # Byte-stability guarantees
//
// The canonical encoder guarantees that two inputs parsing to the same JSON
// value produce identical bytes, and inputs parsing to different values produce
// different bytes. It achieves this by addressing each way encoding/json would
// otherwise leak instability:
//
//   - Key order: object members are emitted sorted by UTF-16 code-unit order
//     (RFC 8785 §3.2.3). encoding/json sorts map keys but gives no control over
//     struct field order or over a hand-built object; canonicalisation parses
//     to a neutral tree and sorts unconditionally.
//   - HTML escaping: disabled. encoding/json escapes <, > and & to
//     \u003c/\u003e/\u0026 by default; both the canonical encoder and response
//     emission set SetEscapeHTML(false) so those bytes are literal.
//   - Number formatting: integral values render with no fraction or exponent
//     (1, 1.0 and 1e0 all become "1"); non-integral values use the shortest
//     round-tripping decimal; integers beyond float64's exact range keep their
//     exact digits. This is the RFC 8785 §3.2.2.3 ECMAScript form.
//   - Empty versus absent and nil versus empty: canonicalisation operates on
//     parsed JSON, where a member is either present with a value or absent —
//     there is no Go nil-slice-versus-empty-slice ambiguity, because a Go []T
//     never reaches the encoder; only its already-serialized JSON does. On the
//     response path the same discipline applies: an absent field is controlled
//     by the caller's json tags (omitempty) and by ID presence, both explicit.
//
// The canonical encoder is a pure function of its input and holds no
// package-global mutable state (ADR-007); it is safe for concurrent use by any
// number of goroutines, as are the envelope types once constructed. This
// matters because §200 logical instances share this code on the hot path.
package jsonrpc
