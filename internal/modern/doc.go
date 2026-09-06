// Package modern implements the 2026-07-28 ("modern" era) protocol behavior of
// mcpmock that sits above the [engine] spine and consumes the protocol
// vocabulary from [wire] (architecture.md §6.1: C-12 Modern handlers).
//
// # Phase 1 scope
//
// Phase 1 delivers the stage-4 _meta validation ([MetaValidator], MOCK-203,
// TASK-017) and the three method handlers server/discover, tools/list and
// tools/call with the built-in echo/sleep/fail tools (TASK-018). The validator
// and the handlers are independent: the validator plugs into pipeline stage 4,
// the handlers into the dispatch stage (6) through the engine registry.
//
// # The three handlers (TASK-018, MOCK-201/202/209/221/222)
//
// [RegisterHandlers] binds all three into an [engine.Registry]. Each is
// stateless except the tools/call handler, which captures the scenario-derived
// [BuiltinConfig]; all per-request configuration is read from ex.Snapshot, so one
// registration serves every request and dispatch stays a lock-free map lookup.
//
//   - server/discover ([handleDiscover], MOCK-201): returns supportedVersions,
//     capabilities, instructions, serverInfo, ttlMs and cacheScope, each
//     independently configurable via spec.discover and each independently
//     omissible as an ABSENT key (never null, MOCK-201.3). Wire clauses: annex §5,
//     4.2 [P-21], 4.3/4.4 [P-22], 4.5/4.6 [P-23].
//   - tools/list ([handleToolsList], MOCK-221/222): returns the whole tool
//     catalog UNPAGINATED (cursors are Phase 2), reading the virtual catalog
//     through At/Len so a ≥5000-tool catalog is never materialized (ADR-004);
//     falls back to the three built-in descriptors when the scenario authors no
//     catalog. Wire clauses: annex 4.2 [P-21], the toolListResult/toolDescriptor
//     shapes.
//   - tools/call ([toolsCallHandler], MOCK-202, MOCK-212, builtin-tools.md):
//     echo, sleep and fail. echo returns its arguments as canonical JSON
//     ([P-42]/[P-43]); sleep delays under a cancellable select and reports
//     virtual (intended) time, rejecting a request over maxSleepMs rather than
//     clamping by default ([P-45]..[P-48], MOCK-212); fail defaults to toolError
//     ([P-49]) with the scenario — not the caller — selecting the mode ([P-52]).
//
// Every result carries a resultType from the internal/wire table and
// _meta.serverInfo, each removable by the MOCK-209 omission switches. The
// handlers draw no RNG and read no clock on the response path, so two identical
// requests at a fixed seed produce byte-identical bodies (design principle §0.1).
//
// # Provisional wire status (GAP-003)
//
// Every wire detail the handlers emit is authored-annex provisional and carries
// NO conformance claim; the [P-nn] items each handler depends on are named in its
// file doc. Ratification is a single-package edit because every wire literal is
// sourced from [wire] (ADR-019 containment) and the built-in shapes are data.
//
// # Stage-4 _meta validation (MOCK-203, AMEND-6)
//
// [MetaValidator] fills the engine's stage-4 seam ([engine.MetaValidator]).
// The owning instance builds one from switches.validateMeta and returns it from
// its snapshot; the pipeline invokes it at stage 4. This is how _meta validation
// is wired in WITHOUT restructuring the pipeline (engine/doc.go states this is
// the required insertion mechanism).
//
// It validates params._meta exactly as a conformant server would and maps a
// missing required field to JSON-RPC -32602 / HTTP 400 (annex 2.3 / 2.4 / 2.12
// [P-11]; wire clause references: 2.3 protocolVersion, 2.4 clientCapabilities,
// 2.5 [P-06] the params._meta location, 2.12 [P-11] the data.missing payload).
// error.data.missing names exactly which fields were absent (MOCK-203.4).
//
// The three switches.validateMeta modes (requirements-spec.md MOCK-203 table,
// AMEND-6):
//
//   - strict (default): reject a missing protocolVersion, a missing
//     clientCapabilities, or an absent _meta, with -32602 / 400; record the
//     rejected fields (accepted=false, metric outcome="rejected").
//   - lenient: ACCEPT such a request but still COMPUTE and record the missing
//     set (accepted=true, metric outcome="tolerated"), so a hub that silently
//     relies on server rejection has a visible, reproducible deficiency. Relaxes
//     ONLY the presence of the two required fields — nothing structural.
//   - off: skip the check; do NOT compute the missing set (missing always empty,
//     metric outcome="skipped"). The empty-vs-populated missing set, and the
//     skipped-vs-tolerated metric outcome, are the observable differences from
//     lenient (MOCK-203.7 / 203.8).
//
// Structural well-formedness is enforced in EVERY mode, including off: a
// present-but-non-object params is -32600 / 400, and a present-but-non-object
// _meta is -32602 / 400 in strict and lenient. off treats a non-object _meta as
// absent because it computes nothing to reject on. A structurally broken request
// cannot be faithfully decoded into the journal, which MOCK-601 / MOCK-203.6
// require, so the relaxation is semantic (field presence), never syntactic.
//
// # Layering and containment
//
// This package depends on [engine] (the Exchange/Fault/MetaValidator seam) and
// [wire] (the _meta keys, error codes and data-payload shape), and on nothing in
// internal/obs: the metric is recorded through the consumer-defined
// [MetaMetricRecorder], which the instance injects. No wire literal — method
// name, _meta key, error code or resultType value — appears in this package;
// every such value comes from [wire] (ADR-019 containment). The wire error
// object itself is still produced in exactly one place, engine/emit.go.
package modern
