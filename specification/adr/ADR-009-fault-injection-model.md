---
id: ADR-009
title: Fault injection — selector algebra x trigger x action, ordered rule list
status: accepted
date: 2026-09-04
reversibility: MEDIUM — internal engine is replaceable; the fault vocabulary in the scenario file is HARD
requirements: §5 preamble, MOCK-501…MOCK-508, MOCK-702, MOCK-704
---

# ADR-009 — Fault injection model

## Context

§5's preamble is the requirement that shapes the whole subsystem:

> Every fault MUST be addressable by (method, primitive name, upstream instance) selector and by
> probability, count, or explicit control-API trigger.

That is a three-way product: **selector × trigger × action**, where the action vocabulary spans
`MOCK-501`…`508` — latency, transport, protocol, size, error, content, injection corpus, restart.
Roughly 45 distinct actions.

Implementing this as `if cfg.InjectLatency { … }` scattered through handlers produces 45
conditionals in the hot path, no way to combine faults, and no way to trigger one at runtime.

## Decision

### Model

```yaml
faults:
  - id: slow-search                      # required; the control-API handle and the metric label
    when:                                # SELECTOR — conjunction; omitted field = match-all
      instance: "upstream-a"             # glob
      method: "tools/call"               # glob, e.g. "tools/*"
      name: "tool_004*"                  # primitive name, glob
      era: modern
      principal: { scope: "admin" }      # optional, for auth-dependent faults
      phase: response                    # request | response | stream | connection
    trigger:                             # exactly one of:
      probability: 0.1                   # seeded (ADR-002 domain "fault:<id>")
      # count: { first: 3 }              # first N | afterN | everyNth | between[a,b]
      # armed: true                      # control API: POST /faults/{id}:trigger  (MOCK-702)
    action:
      kind: latency
      latency: { mode: fixed, fixed: 2s }
    once: false
    stopPropagation: false               # if true, later matching rules do not run
```

- **Selector** compiles to a small closure tree at snapshot-build time (not per request). Globs
  are precompiled. Evaluation cost per request is O(#rules) with early exit on the cheapest
  predicate first (method is checked before name, name before principal).
- **Trigger** is evaluated only if the selector matches.
- **Action** is a tagged union; the tag determines which of `latency|transport|protocol|size|
  error|content|corpus|restart` sub-objects is required (JSON Schema `oneOf` on `kind`).

### Evaluation

Rules are an **ordered list** — order comes from the scenario file, which is deterministic.
Default mode is `all`: every matching rule fires, in list order, and actions compose
(latency then error then truncation is a legal and useful combination). `stopPropagation: true`
gives first-match semantics per rule. Rules are grouped by `phase` and evaluated at the
corresponding pipeline stage (`architecture.md §5`, stages 5 and 7), plus a `connection` phase
evaluated in the transport before the pipeline (for `MOCK-502` accept refusal / TLS failure).

### Hot-path cost (`MOCK-901`)

`Snapshot.Faults` carries a precomputed `bloom` of methods mentioned by any rule. If the request's
method is absent from the bloom and no rule is method-less, the entire fault stage is a single
comparison. With an empty fault list — the `MOCK-901` benchmark configuration — the stage is one
nil check. This is why fault evaluation can be unconditional in the pipeline.

### Determinism

`probability` draws from `requestKey.Derive("fault", id).RNG()` — per rule id, so adding a rule
does not perturb existing rules' draws. This is the reason the derivation is keyed by rule id
rather than by list position.

`count` triggers use `atomic.Uint64` per rule. **This is order-dependent under concurrency and is
the one place the design knowingly violates §0.1.** Two mitigations, and one escalation:
- `determinism.strict: true` (default `false`) routes count evaluation through the journal's
  global `Seq`, making "first 3" mean "the 3 requests with the lowest Seq matching this
  selector" — deterministic, at the cost of a small serialisation window.
- The journal records which fault ids fired on each request, so a test can always *observe* what
  happened even when it cannot predict it.
- Escalated as GAP-007 for a decision on whether `strict` should be the default.

### Control-API surface (`MOCK-702`)

```
GET    /v1/instances/{id}/faults              -> current rule list + per-rule fire counts
PUT    /v1/instances/{id}/faults              -> replace rule list (validated against the schema)
PATCH  /v1/instances/{id}/faults/{ruleId}     -> enable/disable/modify one rule
POST   /v1/instances/{id}/faults/{ruleId}:arm -> arm an `armed` trigger for the next N matches
POST   /v1/instances/{id}/faults/{ruleId}:fire -> fire once, synchronously, on the next match
DELETE /v1/instances/{id}/faults/{ruleId}
```

All of these build a new `Snapshot` and swap it (ADR-014), so an in-flight request never sees a
half-applied rule list.

### Actions that are not response mutations

Three action kinds escape the response pipeline and need transport cooperation:

| Kind | Mechanism |
|---|---|
| `transport` (`MOCK-502`: reset, half-close, truncation, accept refusal, TLS handshake failure) | `ResponseSink.Close(CloseAbrupt)` plus a `httpx` hijack path (`http.ResponseController.Hijack`) to send a TCP RST via `SetLinger(0)`. Accept refusal and TLS failure require `listener: own` (ADR-007). |
| `restart` (`MOCK-508`) | stdio: `os.Exit` after a configured delay, with in-flight requests recorded first. HTTP: a `/v1/instances/{id}:restart` control operation plus a Helm/manifest-level rolling-restart recipe in `deployment.md`. "Slow shutdown that never closes streams" is a shutdown-mode flag, not a fault rule. |
| `corpus` (`MOCK-507`) | Content substitution at catalogue-render time, gated by ADR-018. |

Because `restart` can terminate the process, it is refused when mcpmock runs as an embedded
library (`MOCK-107`) unless `WithAllowProcessExit()` was passed — otherwise a hub unit test would
kill its own test binary. This is a real hazard and is enforced, not documented.

## Options considered

1. **Per-handler conditionals** — rejected: 45 actions × 12 methods, unmaintainable, no runtime
   control.
2. **Middleware chain, one middleware per fault kind** — rejected: 45 middlewares on every request;
   selector logic duplicated 45 times; composition order becomes implicit in wiring order.
3. **Scripting hook (Lua/Starlark)** — rejected: destroys determinism and the startup budget;
   turns a config file into a program that must itself be tested.
4. **Declarative rule list with selector × trigger × action (chosen).** Matches the requirement
   text almost word for word, which is a good sign.

## Consequences

**Positive.** New fault kinds are a new `action.kind` plus one function — no changes to selector,
trigger, control API, metrics or journal. §5 becomes a table in `contracts/fault-catalogue.md`
rather than 45 code paths. Every fired fault is journaled by rule id, making `MOCK-604` golden
diffs meaningful.

**Negative.** The `oneOf` on `action.kind` makes the JSON Schema large and its error messages
poor ("did not match any of 8 schemas"). Mitigated by a discriminator-aware error reporter in
`scenario.Validate()` that first reads `kind`, then validates against only that branch, then
reports. This is worth building; generic `oneOf` errors would make the file miserable to author.

Composing actions can produce nonsense (truncate + oversize). We do not prevent it — §0.4 says the
mock must be able to violate the spec — but `mcpmock validate` emits **warnings** (exit 0) for
combinations flagged `suspicious` in the catalogue.

**Forecloses.** Faults conditioned on state the selector cannot see (e.g. "fail the 3rd retry of
this specific MRTR chain"). Partially recovered by `when.mrtrRound: N`, which is added to the
selector for exactly this case.
