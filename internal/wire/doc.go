// Package wire is the single owner of mcpmock's MCP protocol vocabulary for
// protocol revision 2026-07-28: the method names, _meta keys, resultType
// values, error codes and result/envelope shapes that the modern hub surface
// speaks.
//
// # Unratified — no conformance claim
//
// This revision is UNRATIFIED. mcpmock could not confirm that revision
// 2026-07-28, its error codes, server/discover, resultType or the _meta shape
// correspond to any publicly published Model Context Protocol specification.
// mcpmock therefore emulates specification/requirements.md PLUS an authored,
// provisional annex (specification/contracts/wire-2026-07-28.md and its schema
// wire-2026-07-28.schema.json), and makes NO CLAIM of conformance to the Model
// Context Protocol. Nothing in this package should be read as a statement about
// what MCP does or does not define (ADR-019, GAP-003).
//
// # Provenance labels
//
// Every exported symbol carries, in its doc comment, its annex clause number
// and a provenance label so a reader can tell invented content from standard
// content and can price the cost of ratification:
//
//   - [R]     Restated from requirements.md, cited in the annex. Stable modulo
//     the meaning of the requirement itself.
//   - [D]     Derived — logically forced by an [R] item or by JSON-RPC 2.0 /
//     RFC 9110 / RFC 8785. Stable.
//   - [P-nn]  PROPOSED in the annex to fill a gap an implementer cannot proceed
//     past. Subject to change when GAP-003 is ratified; each [P-nn]
//     marks a localized, enumerable ratification cost.
//
// # Why this package exists (ADR-019 containment)
//
// ADR-019 is the governing decision: no wire literal — no method name, no
// _meta key, no resultType value, no MCP error code — may appear anywhere
// outside this package (and test/mcpclient, which holds its own independent
// transcription on purpose, ADR-017). Containing the vocabulary here is what
// makes ratification a single-package change rather than a month of rework: on
// ratification, the [P-nn] constants and the annex schema change together, the
// golden fixtures are regenerated, and handler code does not change because the
// wire shapes are data, not code.
//
// TestNoWireLiteralsEscape enforces the containment rule as a Go test; the
// equivalent make wire-literal-check gate is owned by the task permitted to
// edit the Makefile.
//
// # Phase 1 subset only
//
// Per the AMEND-3 Phase 1 / Phase 2 split table in implementation-plan.md, this
// package currently carries ONLY the Phase 1 wire surface: the three methods
// server/discover, tools/list and tools/call; the _meta envelope and its
// validation vocabulary; the three-entry resultType table; result-level
// serverInfo, ttlMs and cacheScope; the -32602 data.missing payload; and the
// standard JSON-RPC codes plus -32601.
//
// Phase 2+ surface — mirrored headers and the sentinel codec, -32020/-32021/
// -32022, cursor/nextCursor pagination, SSE framing, MRTR, subscriptions and
// the legacy eras — is deliberately ABSENT rather than present-and-permissive
// (AMEND-4). That is a correctness feature: there is no constant, no type and
// no table entry for a Phase 2 construct, so a Phase 1 caller that tries to
// emit one fails to compile or has nothing to look up, rather than silently
// emitting a shape that has not been reviewed. The package grows one phase at a
// time by ADDING entries; it is never rewritten.
//
// # Layering, statelessness and safety
//
// This package sits directly above internal/jsonrpc (architecture.md §6.1) and
// builds on it rather than duplicating it: id preservation, the envelope types,
// the standard JSON-RPC codes and the byte-stable canonical encoder all come
// from there. Result emission goes through jsonrpc.Response.Encode so responses
// are byte-identical under a fixed seed (design principle §0.1).
//
// The package holds no process-global mutable state (ADR-007): the resultType
// table and default catalogs are returned by constructor functions or are
// read-only lookups, so the ~200 logical instances that share this vocabulary
// never contend on it. Every exported value is safe for concurrent reads.
package wire
