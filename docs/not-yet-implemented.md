# Not yet implemented

Audience: **everyone**. Read this before assuming a requirement from
`specification/requirements.md` is available.

The requirements document defines 81 numbered `MOCK-nnn` MUSTs plus several
deliverables. **This build (Phase 1) delivers 10 of them in full, plus
partial/subset coverage of a few more** (detailed below, including one
correction to the architect's own phase table — see
[A note on `MOCK-209`](#a-note-on-mock-209)). The other ~70 are sequenced
into Phases 2–12 of
[`specification/implementation-plan.md`](../specification/implementation-plan.md)
and **do not exist in this binary at all** — the scenario schema accepts
their configuration keys (so a scenario file can describe them without
failing validation), but nothing reads those keys yet.

This page exists because §10.6 of the requirements asks documentation to
include a fault catalogue and a full requirement-coverage map. **Neither can
be written honestly yet**: fault injection is Phase 9, and the coverage map
depends on the hub-side `HUB-nnn` requirements document, which is not in
this repository (`GAP-004`, external, unresolved). What follows is the
closest honest substitute — what phase delivers what, and what the Phase 1
mock side already covers per component.

## Table of contents

- [Delivered in Phase 1](#delivered-in-phase-1)
- [A note on `MOCK-209`](#a-note-on-mock-209)
- [Everything else, by phase](#everything-else-by-phase)
- [What this means for a hub integration today](#what-this-means-for-a-hub-integration-today)

## Delivered in Phase 1

Fully covered, per
[`specification/traceability.md`](../specification/traceability.md) §5 and
verified directly against the running binary in this documentation pass:

| Requirement | What it is |
|---|---|
| `MOCK-101` | Container image / deployment contract (distroless, non-root, three ports). |
| `MOCK-102` | `streamable-http` and `stdio` transports. |
| `MOCK-105` | `/metrics`, `/healthz`, `/readyz`. |
| `MOCK-107` | Go library embedding (`StartTest`, startup budget). |
| `MOCK-203` | `switches.validateMeta` (`strict`/`lenient`/`off`). |
| `MOCK-601` | Journal data model. |
| `MOCK-602` | Journal query over the control API. |
| `MOCK-701` | Scenario document schema and validation. |
| `MOCK-703` | `extends` composition. |
| `MOCK-704` | `--seed` determinism. |

Partial / subset coverage, also verified:

| Requirement | Phase 1 subset | Full requirement lands in |
|---|---|---|
| `MOCK-104` (control API) | 8 read-only routes over HTTP + unix socket, loopback-by-default bind gate. | Phase 4 (instance mutation, catalogue/fault control, per-request token enforcement). |
| `MOCK-201` (`server/discover`) | Returns a result; `capabilities`/`instructions`/`serverInfo` are emitted verbatim when scenario-authored. | Phase 2 (fuller shape, paging/caching interplay). |
| `MOCK-202` (`tools/list`/`tools/call`) | The three built-in tools (`echo`, `sleep`, `fail`) only. | Phase 2 (method completeness), Phase 3 (the virtual catalogue beyond built-ins). |
| `MOCK-209` (result-shape switches) | `omitResultType`, `omitServerInfoMeta`, `hideFromCapabilities`, `selfCheck` — all verified live. See the note below. | Phase 2 for the remaining `nonConformant` switches. |
| `MOCK-212` (cancellation) | Verified only via the `sleep` tool observing context cancellation. | Phase 2 (general cancellation). |
| `MOCK-222` (authored catalogue items) | The three built-in tools are authored items; arbitrary authored tools/prompts/resources are schema-accepted but not exercised. | Phase 3. |
| `MOCK-603` (assertions) | 3 of the 8 assertions named in the requirement (`AssertNoHeader`, `AssertHeaderNotValue`, `AssertRequestCount` + its `AtMost`/`AtLeast` variants), each in a fatal and non-fatal form. | Phase 10 for the remaining 5. |
| `MOCK-702` (control mutation) | None — Phase 1's control API is read-only. | Phase 4. |
| `MOCK-901`–`903` (performance) | Smoke-measured on a laptop, not the target host; see [`docs/deployment.md#measured-performance`](deployment.md#measured-performance). | Phase 11 (gated, target hardware). |

## A note on `MOCK-209`

`specification/traceability.md` §2 lists `MOCK-209`'s coverage phase as
**2**, not 1. Direct verification in this documentation pass — reading the
scenario schema, calling the switches through a running server, and running
the passing tests `TestHideFromCapabilitiesFromScenarioFile` and
`TestSelfCheckFromScenarioFileRejectsMalformed` — shows `omitResultType`,
`omitServerInfoMeta`, `hideFromCapabilities` and `selfCheck` all work end to
end from a scenario file in this build, and the default scenario
(`scenarios/happy-path.yaml`) lists `MOCK-209` under its own
`coversRequirements`. **The code and the two specification documents
disagree; this documentation follows the code and the passing tests**, since
they are directly observable and the traceability table is not
self-verifying. This discrepancy is reported to the architect in the
delivery report for this task rather than silently resolved here.

## Everything else, by phase

None of the following exists in this build. Configuration keys for most of
them validate successfully against the scenario schema (they are declared as
the *eventual* schema) but have **no runtime effect** — do not configure them
expecting behaviour.

| Phase | Theme | Key requirements |
|---|---|---|
| 2 | Modern protocol completeness | `MOCK-204`–`211` (header mirroring, era detection interplay, shape/SSE toggle, non-conformant switches), `MOCK-231`–`232` (pagination, caching) |
| 3 | Catalogue engine | `MOCK-221`, `223`–`228` (virtual generation at scale, edge-case primitives, ordering, drift, required scopes), `MOCK-233`, `MOCK-606` |
| 4 | Fleet, listeners, TLS/mTLS, control completeness | `MOCK-103` (multi-instance fleets), `MOCK-106` (TLS/mTLS), `MOCK-702` (control mutation), `MOCK-904` (fleet scale) |
| 5 | MRTR — 🔴 blocked by `GAP-003` | `MOCK-241`–`247`, `MOCK-605` |
| 6 | Subscriptions and streaming scale — 🔴 blocked by `GAP-003`, `GAP-005` | `MOCK-251`–`257`, `MOCK-903` (full scale gate) |
| 7 | Legacy and dual era — 🔴 blocked by `GAP-003` | `MOCK-301`–`306` |
| 8 | Authorization | `MOCK-401`–`407` |
| 9 | Fault and chaos injection | `MOCK-501`–`508`, the §5 preamble, the hostile-content corpus (`MOCK-507`) |
| 10 | Journal and assertions completeness | `MOCK-604` (golden-file snapshotting), the remaining 5 of 8 `MOCK-603` assertions, `MOCK-902` full gate |
| 11 | Performance, scenario library, docs, signing | `MOCK-705` (scenario library — 9 of 10 scenarios beyond `happy-path`), `MOCK-901` full gate, image signing |
| 12 (optional) | Record/replay | `MOCK-706` (a SHOULD, not a MUST) |

Phases 5, 6 and 7 are marked blocked in the architect's own plan because they
depend on wire-format details this project could not verify against a public
MCP specification (`GAP-003` — see the
[root README's caveat](../README.md#-the-wire-format-is-not-a-public-standard)).
Building them further would mean committing to unratified guesses about a
protocol this project cannot confirm.

## What this means for a hub integration today

- You can exercise: the modern happy path for `server/discover`, `tools/list`,
  `tools/call` against `echo`/`sleep`/`fail`, `_meta` validation in all three
  modes, the four Phase-1 switches, the journal, and the read-only control
  API.
- You cannot yet exercise: pagination, a real generated catalogue at scale,
  multi-round tool results, subscriptions/SSE push, legacy-protocol
  emulation, authorization, or any deliberate fault/chaos behaviour. A
  scenario file that configures any of these will validate (the schema
  accepts the future shape) and then silently do nothing — the config key is
  round-tripped, not acted on.
- If your hub test needs any of the above, treat this build as not ready for
  that test yet, rather than assuming a schema-valid scenario key implies a
  working feature.
