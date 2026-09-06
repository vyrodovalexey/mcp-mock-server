# Phase 1 golden fixtures — PROVISIONAL (GAP-003)

These files are the Phase 1 wire-behaviour regression contract for the functional
suite (`TASK-027`, `MOCK-604`). Read this before touching any of them.

## They encode PROVISIONAL behaviour, not protocol conformance

The primary protocol revision `2026-07-28` is **not publicly specified**
(GAP-003 / ADR-019). Its wire behaviour is defined only by an **authored
provisional annex** — `specification/contracts/wire-2026-07-28.md` and
`specification/contracts/builtin-tools.md` — every provisional item of which
carries a `[P-nn]` label and `x-conformance-claim: NONE`.

Consequently **every byte in these fixtures encodes a `[P]` decision that could
change on ratification.** A green functional suite proves the server is
*self-consistent with the authored annex*. It does **not** prove conformance to
the real `2026-07-28` protocol. Do not read a passing suite as a conformance
claim.

The `[P]` items these fixtures depend on, per `builtin-tools.md §7` and the wire
annex, include (non-exhaustive): `P-06` (`_meta` location), `P-21` (`resultType`
value set), `P-22` (`serverInfo` shape), `P-39`–`P-52` (built-in tool result
envelope, `echo` two-channel output, canonical-JSON text block, `sleep` text
form and `reject`-default, `fail` `toolError` default and `isError`-when-true).

## They are HAND-AUTHORED, never regenerated on failure

There is deliberately **no `make golden-update` and no `-update` flag** in this
suite (test-strategy.md §0.1). Each fixture was authored by reading the annex /
`builtin-tools.md` and reviewed. The comparison redacts volatile fields
(timestamps, ports, ephemeral ids, credential hashes) deterministically
(`golden_test.go`), so a real behaviour change is a **readable diff** and
environment noise is not.

### When a golden test fails

Exactly one of two things is true:

1. **The change is intended** (a ratification, or a deliberate behaviour change).
   Edit the fixture **by hand, with a reviewer**, so the diff records precisely
   which `MOCK-nnn` changed meaning. A ratification is therefore a deliberate,
   reviewable regeneration — never an automatic overwrite.
2. **The change is not intended.** That is a **defect in the server**. Report it
   and stop; do **not** edit the fixture to make the suite pass. The fixture is
   independent evidence and must stay that way.

## Layout

Fixtures are directoried by the `MOCK-nnn` requirement they serve, so a
ratification diff shows exactly which requirements changed meaning (ADR-019):

| Directory | Proves |
|---|---|
| `MOCK-201/` | `server/discover` result shape (resultType, serverInfo in `_meta`). |
| `MOCK-202/` | `tools/list` catalogue order + schemas; `echo`/`sleep`/`fail` result envelopes (builtin-tools AMEND-2). |
| `MOCK-209/` | `resultType` on every result and `serverInfo` in result `_meta` (conformant/default path). |

## Known Phase 1 gap recorded here (not a golden bug)

`MOCK-209.3` (`switches.omitResultType`) and `MOCK-209.4`
(`switches.omitServerInfoMeta`) are **not implemented** in the server at the time
these fixtures were authored: the switches are accepted by the scenario schema
but have no effect (see the TASK-027 report, "Defects found"). No golden encodes
the omit-switch output, because doing so would ratify buggy behaviour. The
functional suite asserts the *correct* MOCK-209.3/.4 behaviour separately and
that assertion is expected to fail until the defect is fixed.
