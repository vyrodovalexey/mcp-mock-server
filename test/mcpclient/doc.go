// Package mcpclient is a deliberately independent MCP client used to test the
// mcpmock server. It exists to solve the recursion problem described in
// ADR-017: a test tool cannot credibly prove its own correctness using its own
// machinery.
//
// # The anti-circularity rule (ADR-017) — DO NOT REFACTOR THIS AWAY
//
// This package MUST NOT import ANY mcpmock server package. Not
// internal/wire, not internal/jsonrpc, not internal/engine, not the shared
// error-code constants, not "just the types". It declares its OWN copy of
// every wire constant (see wire_constants.go), builds its OWN JSON-RPC
// envelopes, and constructs its OWN _meta and headers, all transcribed by hand
// from the wire annex (specification/contracts/wire-2026-07-28.md), never from
// server code.
//
// The reason is structural, not stylistic. If the server and this client
// shared an encoder, a decoder, or a constant, a bug present in both would
// pass every test that used both — which is the normal failure mode, because
// the same person writing both from the same misreading of the same sentence
// produces the same mistake twice. Duplicating wire constants here is
// therefore CORRECT AND INTENTIONAL. It is not a DRY violation. If you find
// yourself reaching for an import of a server package to avoid repetition,
// stop: that is precisely the failure mode this package exists to prevent, and
// `make deps-check` will fail the build for it (TASK-008 acceptance criterion 1,
// architecture.md §6.1).
//
// # Linting posture
//
// The test/ tree is excluded from golangci-lint by .golangci.yml (path
// exclusion, line 61), and run.tests is false. Lint therefore does NOT analyse
// this package. Its correctness is a REVIEW obligation, reviewed against the
// annex text (ADR-017 §1), not a machine-checked one. This comment states that
// explicitly so a future contributor does not assume "lint is green" implies
// "this file was checked".
//
// # Provisional wire status (GAP-003)
//
// The 2026-07-28 protocol revision is not publicly specified. The wire details
// used here come solely from the authored, provisional annex, which carries no
// conformance claim (v0/v1alpha1, x-conformance-claim: NONE). Every wire
// constant is annotated in wire_constants.go with its annex clause number and
// its [R]/[D]/[P-nn] provenance label. Items labelled [P-nn] are proposals that
// GAP-003 may ratify differently; this package is structured so a later
// ratification is a localised edit to wire_constants.go and nowhere else.
// Nothing here is presented as protocol conformance.
//
// # What this client can do
//
// It drives the Phase 1 surface — server/discover, tools/list, tools/call —
// over both transports required by MOCK-102:
//
//   - streamable-http: JSON-RPC over HTTP POST (see http.go), plus a raw-socket
//     mode that transmits duplicate headers with differing casing without Go's
//     http.Header canonicalisation intervening (required by MOCK-601.2).
//   - stdio: newline-delimited JSON-RPC, one message per line (see stdio.go).
//
// Two capabilities are first-class, not workarounds:
//
//   - Malformed-request construction. This client tests a mock whose job
//     includes rejecting bad input (MOCK-203, MOCK-204). A client that could
//     only produce valid requests could not test the rejection paths. Requests
//     are therefore built as raw bytes that the caller may corrupt at will, and
//     [Request] exposes toggles for omitting each required _meta field.
//   - Raw-byte assertion. Byte-identical determinism (design principle §0.1) is
//     verified at the wire level, so every call returns the response body as
//     raw bytes ([Response.Body]) in addition to a parsed JSON-RPC envelope.
//
// The client makes no assumption about response shape beyond parsing the
// JSON-RPC envelope (jsonrpc/id/result/error), so a Phase 2 SSE sink does not
// require rewriting it (TASK-008 acceptance criterion 6).
//
// # Context and globals
//
// Every network operation takes a context.Context for cancellation and
// timeouts; no context is ever stored in a struct. The package holds no
// package-level mutable state (ADR-007): a [Client] carries its own
// configuration and its own transports.
package mcpclient
