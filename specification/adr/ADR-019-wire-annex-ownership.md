---
id: ADR-019
title: mcpmock authors and owns a normative wire annex for protocol revision 2026-07-28
status: proposed — REQUIRES RATIFICATION
date: 2026-09-04
reversibility: HARD — every handler, schema, golden file and hub test depends on it
requirements: MOCK-201…MOCK-212, MOCK-221…233, MOCK-241…247, MOCK-251…257, MOCK-301…306
blocks: Phase 2 onwards
---

# ADR-019 — Wire annex ownership

## Context

**VERIFIED as an absence.** `specification/requirements.md` refers throughout to protocol
revision `2026-07-28` and to specific constructs:

| Construct | Where referenced | What requirements.md actually specifies |
|---|---|---|
| `server/discover` | `MOCK-201` | the *names* of six result fields: `supportedVersions`, `capabilities` (incl. `extensions`), `instructions`, `serverInfo`, `ttlMs`, `cacheScope`. No types, no nesting, no example. |
| `-32020` HeaderMismatch | `MOCK-204` | code + name + that it accompanies HTTP 400 |
| `-32021` MissingRequiredClientCapability | `MOCK-206` | code + name + `data.requiredCapabilities` |
| `-32022` UnsupportedProtocolVersion | `MOCK-205` | code + name + a "configurable `supported` list" |
| `_meta` with `protocolVersion`, `clientCapabilities` | `MOCK-203` | field names only |
| `Mcp-Method`, `Mcp-Name`, `Mcp-Param-*`, `MCP-Protocol-Version` | `MOCK-204` | names; "Base64-sentinel decoding"; "numeric comparison of integers" — **the sentinel format itself is not given** |
| `resultType` | `MOCK-209`, `MOCK-503` | that it is on every result; "wrong `resultType`", "unknown `resultType`" — **the value set is not given** |
| `InputRequiredResult`, `inputRequests`, `inputResponses`, `requestState` | `MOCK-241`…`247` | field names and behaviour; no schema |
| `subscriptions/listen`, `notifications/subscriptions/acknowledged`, `io.modelcontextprotocol/subscriptionId` | `MOCK-251`…`257` | names and behaviour; no filter schema |
| `io.modelcontextprotocol/logLevel` | `MOCK-211` | name only |

I could not confirm that revision `2026-07-28`, these error codes, `server/discover`,
`resultType`, MRTR / `InputRequiredResult` or `subscriptions/listen` correspond to any publicly
published MCP specification. **This document makes no claim either way** — it may be an internal
or unreleased revision to which this repository simply does not have the text.

What is certain is the engineering consequence: an implementer cannot write `MOCK-201` from
`MOCK-201`. There are perhaps forty wire-level details missing, and each one silently invented
produces a mock that tests the hub against a fiction.

## Decision

**mcpmock authors a normative wire annex and treats it as a first-class, ratified deliverable —
not as implementation detail.**

- Location: `specification/contracts/wire-2026-07-28.md` (prose + tables) and
  `specification/contracts/wire-2026-07-28.schema.json` (JSON Schema 2020-12 for every request,
  result, error `data` payload, notification and header encoding).
  **Status (AMEND-4, 2026-09-04):** the schema file now exists, versioned `v0`/`v1alpha1`, covering
  the **Phase 1 subset** — `_meta` envelope, `server/discover`, `tools/list`, `tools/call`, the
  JSON-RPC envelope, `-32602` `data.missing`. It grows one phase at a time, tracking the
  `internal/wire` split table in `implementation-plan.md`. The schema is deliberately **absent**
  rather than permissive for not-yet-implemented surface, so a premature emission fails loudly.
  Tool-result shapes (`[P-39]`…`[P-52]`) live in `contracts/builtin-tools.md`, sharing this annex's
  `[P-nn]` numbering.
- Legacy eras get `wire-legacy-2025.md` covering `2025-11-25` / `2025-06-18` / `2025-03-26` to the
  extent `MOCK-301`…`306` require.
- Every statement in the annex is labelled:
  - `[from requirements.md §x]` — restating the normative source;
  - `[derived]` — logically forced by something stated;
  - `[PROPOSED]` — invented by us to fill a gap, **requires ratification**.
- The annex is the input to `internal/wire`, to the independent test client (ADR-017), to the
  golden fixtures, and to the self-check schema (`MOCK-244`).
- **`[PROPOSED]` items are enumerated in `gap-analysis.md` and are a ratification gate.** Phase 2
  of the implementation plan does not start until they are resolved.

Ratification means one of:
1. The real `2026-07-28` specification is supplied, and the annex is rewritten to match; or
2. The requester confirms the `[PROPOSED]` shapes as the definition mcpmock will emulate.

Until then the scenario schema stays at `apiVersion: mcpmock.dev/v1alpha1` and the Go module
stays at `v0` (ADR-001), because the annex leaks into both.

### Design accommodation while unratified

The architecture is built so that ratification is a **data change, not a rewrite**:

- All wire shapes live in `internal/wire`, generated from / validated against the annex schema.
  No wire literal appears in a handler.
- Header names, `_meta` keys and error codes are constants in one file per era.
- The sentinel encoding is a single encoder/decoder pair behind an interface, with the format as a
  parameter.
- `resultType` values are a table, not a switch statement.
- Golden fixtures are named by `MOCK-nnn`, so a ratification diff shows exactly which requirements
  changed meaning.

This is the cheapest possible insurance and it costs almost nothing to build that way from the
start. Building it any other way makes ratification a month of rework.

## Options considered

1. **Invent the details silently in code** — rejected. This is the failure mode the whole
   engagement is trying to avoid: a hub validated against a fiction, with no record of what was
   assumed. It also makes every hub test defect ambiguous.
2. **Refuse to proceed until the specification arrives** — rejected: Phase 1's walking skeleton
   (transport, journal, config, control API, deployment) needs almost none of the disputed
   surface, so blocking everything would waste the whole cycle.
3. **Implement only the legacy eras, which may be publicly documented** — rejected: `2026-07-28`
   is the *primary* target per the requirements header, and the hub's modern path is the point.
4. **Author a labelled annex and gate on ratification (chosen).**

## Consequences

**Positive.** Every invented detail is visible, attributed and challengeable. The mock becomes the
reference implementation of a written thing rather than of someone's memory. If the real
specification later differs, the diff is mechanical and its blast radius is enumerable.

**Negative.** Real up-front writing effort (estimated 2–4 days) before Phase 2 handler work, and a
ratification round trip with the requester. That is the honest cost of the gap; it does not go
away by ignoring it, it just moves to debugging time later.

**Forecloses.** Claiming conformance to MCP `2026-07-28`. mcpmock's documentation must say it
emulates the revision **as described in `requirements.md` plus this annex**, and must not claim
specification conformance. Anything else would be a false claim about a document we do not have.

**Blocks.** Phases 2, 3, 5, 6, 7 of `implementation-plan.md`. GAP-003.
