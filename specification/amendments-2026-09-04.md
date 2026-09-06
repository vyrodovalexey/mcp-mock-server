---
title: Specification amendments — 2026-09-04
status: review
version: 1.0.0
updated: 2026-09-04
author: solution-architector
parent: manager-development
run: 2026-09-04_091847
scope: targeted amendment — ten items across two passes. NOT a re-design.
supersedes: nothing. All requirement, ADR, IF and ENT identifiers are preserved.
passes:
  - "pass 1 (AMEND-1…7): gaps found by @task-breakdown before implementation started"
  - "pass 2 (AMEND-8…10): contradictions surfaced by the Phase 1 code review and documentation pass"
---

# Amendments — 2026-09-04

## 0. Summary

Seven amendments closing six specification gaps found by `@task-breakdown` while decomposing
Phase 1, plus one factual correction. Implementation had not started; every item below is a design
defect caught before it became a code defect.

| # | Was | Now | Files | Task impact |
|---|---|---|---|---|
| AMEND-1 | Module path inferred wrongly from a container image tag | `github.com/vyrodovalexey/mcp-mock-server`, verified from `git remote -v` | 4 | **`TASK-002` is complete** |
| AMEND-2 | `echo`/`sleep`/`fail` named, never defined | `contracts/builtin-tools.md` + schema | 3 (1 new) | `TASK-018` **M→L** |
| AMEND-3 | `internal/wire` scheduled Phase 2, needed Phase 1 | Phase 1 subset confirmed and tabulated | 1 | `TASK-010` confirmed, grows |
| AMEND-4 | Wire annex was prose only; `MOCK-201.4` untestable | `contracts/wire-2026-07-28.schema.json` (`v0`) | 4 (1 new) | `TASK-010`, `TASK-027` grow |
| AMEND-5 | ADR-013 omitted `goleak`, which `MOCK-107.7` mandates | Two-tier dependency policy; `goleak` admitted test-only | 2 | `TASK-033` grows |
| AMEND-6 | `validateMeta: lenient` undefined | Defined as tolerate-and-record | 2 | `TASK-017` grows |
| AMEND-7 | Control socket path "documented" nowhere | It *was* in ADR-015; lifecycle now specified | 3 | `TASK-023` grows |

**No new ADRs.** No amendment forced one. AMEND-5 and AMEND-7 amend ADR-013 and ADR-015 in place,
which is what ADR-013 §Enforcement already required for a dependency change ("a new dependency
requires an ADR amendment, not just a `go get`"). Both ADRs record the amendment in their front
matter.

**No requirement, ADR, interface or entity identifier was renumbered or reused.** Four acceptance
criteria were **added** under existing requirement ids (`MOCK-104.5`, `MOCK-203.7`, `MOCK-203.8`,
and `MOCK-202.10`–`202.20` in `builtin-tools.md §6`). `traceability.md` is updated for
`MOCK-104`, `107`, `201`, `202`, `203`, `212` and the contract-coverage list.

---

## AMEND-1 — Module path

### What was wrong

`gap-analysis.md` GAP-001 recommended `github.com/vyrodovalexey/mcpmock`, inferred from
`.github/workflows/ci.yml:183`. **The inference was invalid.** That line references the container
image `vyrodovalexey/keycloak-test:26.5` — a third-party Keycloak test image. It is evidence of a
GitHub *owner*, and never evidence of a repository or module path. An owner name harvested from an
image tag was allowed to stand in for a repository name, and the document did label it an
inference — but a labelled wrong answer still ships a wrong `go.mod`.

### What it is

`github.com/vyrodovalexey/mcp-mock-server` `[stated]`, verified:
`git remote -v` → `git@github.com:vyrodovalexey/mcp-mock-server.git`.

### The subtlety that mattered more than the string

The specification uses `mcpmock/assert`, `mcpmock/scenario`, `mcpmock/journalapi` throughout. These
read like import paths and are **not** — they are package labels. Under the corrected module path
the actual import path is `github.com/vyrodovalexey/mcp-mock-server/assert`. Had only the literal
string been corrected, every one of those labels would have silently become misleading, which is a
worse failure than the original error: it is wrong *and* looks right.

So the amendment does two things: correct the literal, and make the label→import-path mapping
explicit and canonical.

| Package label (used throughout the spec) | Go package | Import path |
|---|---|---|
| `mcpmock` | `mcpmock` | `github.com/vyrodovalexey/mcp-mock-server` |
| `mcpmock/scenario` | `scenario` | `github.com/vyrodovalexey/mcp-mock-server/scenario` |
| `mcpmock/journalapi` | `journalapi` | `github.com/vyrodovalexey/mcp-mock-server/journalapi` |
| `mcpmock/assert` | `assert` | `github.com/vyrodovalexey/mcp-mock-server/assert` |
| `cmd/mcpmock` | `main` | `github.com/vyrodovalexey/mcp-mock-server/cmd/mcpmock` |

The **package** name stays `mcpmock` and the **binary** stays `mcpmock`. Module path ≠ package name
≠ binary name, deliberately, and `architecture.md §6` now says so in terms an implementer cannot
misread.

### Occurrence count

The blast radius was much smaller than "correct every occurrence" implies, because the
specification had consistently used the `MODULE` placeholder rather than committing to a path.
Precisely:

| Kind | Count | Where |
|---|---|---|
| Literal wrong module path `github.com/vyrodovalexey/mcpmock` | **1** | `gap-analysis.md:49` |
| Unresolved placeholders (`DECISION-REQUIRED`, `MODULE`) | **3** | `architecture.md:208, 212, 213` |
| Package labels disambiguated with an explicit import path | **5** | `ADR-001` (4 table rows + 1 prose line) |
| GAP-001/GAP-002 blocking statements now stale | **2** | `implementation-plan.md:66, 92` |
| **Total sites corrected** | **11** | **across 4 files** |
| Correct module path now stated | 17 occurrences | `architecture.md`, `ADR-001`, `implementation-plan.md`, `gap-analysis.md` |

**Files:** `architecture.md`, `adr/ADR-001-single-module-library-first.md`, `gap-analysis.md`,
`implementation-plan.md`.

One occurrence of the wrong path **remains, deliberately**, at `gap-analysis.md:46`: the record of
the withdrawn inference. Deleting it would erase the evidence of how the error happened.
`task-breakdown-phase1.md` also retains four occurrences, all of which quote the wrong path in
order to correct it; that document is `@task-breakdown`'s and was not edited.

### Task impact

**`TASK-002` — "Correct the module path throughout the specification set" — is now COMPLETE.**
It was `XS`/devops. Its acceptance criterion 1 ("a repository-wide search for `vyrodovalexey/mcpmock`
returns zero matches outside a changelog") is satisfied for `specification/` modulo the two
deliberate historical references above, which should be treated as the changelog exemption that
criterion anticipates. `TASK-002` should be **closed, not scheduled**. `TASK-001` (`go.mod`
bootstrap) already carried the correct path and is unaffected.

---

## AMEND-2 — `echo`, `sleep`, `fail`  ·  highest priority

### What was wrong

`implementation-plan.md:73` named three built-in `tools/call` behaviours and `requirements.md §8`
declared them the boundary of permitted side effects. `scenario.schema.json` carried
`behavior.kind: echo|sleep|fail|canned` and `sleepMs`. **That was the entire definition.** No
argument shape, no `inputSchema`/`outputSchema`, no content-block shape, no duration unit stated in
prose, no maximum, no cancellation semantics, no error shape for `fail`, no determinism statement.

This was the highest-priority item because `tools/call` goldens are the Phase 1 regression
contract. Three implementers produce three defensible `echo` shapes; whichever is written first
becomes the baseline every later phase is compared against.

### What changed

New file **`specification/contracts/builtin-tools.md`** — the normative definition. Key decisions:

**Behaviours vs tools.** Separated explicitly. `behavior.kind` is a property any catalogue item may
carry; the three *built-in tools* are default catalogue entries wired to those behaviours, emitted
in the fixed order `echo`, `sleep`, `fail` (ADR-003 ordering) and suppressed when the scenario
declares its own catalogue.

**`echo`.** Returns the whole `arguments` object verbatim through two channels: `content[0].text`
as **canonical JSON** (ADR-003 — keys sorted, which is what makes it golden-stable regardless of
client serialisation order) and `structuredContent.echoed` with JSON types preserved.
**Non-string arguments are not coerced, not rejected, not flattened** — a number stays a number in
`structuredContent` and is rendered as JSON in the text block. A worked example is in the document.
`behavior.echo.key` selects a single argument; an absent key is `-32602` rather than an echoed
`null`, so "absent" and "explicitly null" stay distinguishable.

**`sleep`.** **Units are integer milliseconds**, and every field carries the `Ms` suffix so it
cannot be misread; there is no seconds field and no float duration anywhere. Maximum is
`behavior.maxSleepMs`, default **30 000 ms**. On exceeding it, `onExceedMaxSleep` defaults to
**`reject`** (`-32602`, immediately, no delay incurred) rather than `clamp`, because silent
clamping makes a hub timeout test pass for the wrong reason. Cancellation is normative: the delay
MUST be a `select` on a timer and `ctx.Done()` — a bare `time.Sleep` is a specification violation
because it cannot meet `MOCK-212.1`'s 50 ms bound. On cancellation **no frame is written at all**,
the journal records `cancelled: true`, and the metric increments.

**`fail`.** The open question was the error shape. Resolved: two modes, **`toolError` is the
default** — a *successful* JSON-RPC response carrying `isError: true`, HTTP 200. `protocolError`
(a JSON-RPC `error` object, default code `-32603`) is opt-in. Rationale: `isError: true` is the
MCP-native way for a tool to fail and is the case hub authors most often mishandle, so the
easy-to-write fixture should exercise the easy-to-miss bug. **The scenario selects the mode, not
the caller**, unless `behavior.fail.allowClientOverride: true` (default `false`) — otherwise two
tests sharing a scenario could interfere and a golden would depend on its caller. `isError` is
emitted **only when true**, consistent with `MOCK-201.3`'s absent-not-null rule.

**Determinism.** The `sleep`/virtual-clock interaction (ADR-002) is resolved explicitly:
**`sleep` delays real time and reports virtual time.** The virtual clock is *not* advanced by a
sleep — advancing it would make emitted timestamps depend on how long the request happened to take,
which ADR-002 forbids. `outputSchema.sleptMs` is therefore the *intended* duration, never a
measurement; the measurement lives in the journal's `elapsedNs`, which `MOCK-604` redacts from
golden comparison.

`scenario.schema.json` gained `behavior.echo{key,structured,prefix}`, `maxSleepMs`,
`onExceedMaxSleep`, `allowClientDuration`, `behavior.fail{mode,code,message,data,allowClientOverride}`,
and an `if`/`then` making `result` **required** when `kind: canned` — which it previously was not.
Schema validated as JSON Schema 2020-12 and unit-tested against seven positive/negative cases.

Eleven new acceptance criteria (`202.10`–`202.20`) are in `builtin-tools.md §6`, and fourteen new
`[P]` items (`P-39`–`P-52`) are registered in §7, continuing the wire annex's numbering.

**Files:** `contracts/builtin-tools.md` (new), `contracts/scenario.schema.json`,
`implementation-plan.md`.

### Task impact

**`TASK-018` (three Phase-1 handlers) resizes `M` → `L`.** Its acceptance criterion 4 was one line
covering all three behaviours; it is now eleven criteria plus a self-check obligation from AMEND-4.
The work was always there — it was simply invisible, and would have surfaced as scope discovered
mid-task.

**`TASK-016` (catalogue generator) grows slightly**: it now owns the default three-tool catalogue
and its suppression rule. **`TASK-027` (functional suite and goldens) grows**: goldens for the
canonical-JSON echo case, the clamp/reject boundary, and the cancellation case.

---

## AMEND-3 — `internal/wire` phase assignment

### What was wrong

`implementation-plan.md` listed `internal/wire` under Phase 2 while Phase 1 shipped three methods
and `_meta` validation — all wire surface. ADR-019 requires wire constants to live in exactly one
package and forbids wire literals in handlers. The plan contradicted itself, and the contradiction
resolves badly by default: an implementer following the phase table writes literals into Phase 1
handlers, and ratification then becomes a month of rework instead of a table edit.

### Resolution

**`@task-breakdown`'s resolution is CONFIRMED.** `TASK-010` (`internal/wire` Phase-1 subset) stands
as written. The error was in `implementation-plan.md`, not in the breakdown.

`implementation-plan.md` now carries an explicit **Phase 1 / Phase 2 split table** naming every wire
element and its phase, and the Phase 2 row is reworded: Phase 2 **extends** the package, it does not
create it. The ADR-019 containment rule and `make wire-literal-check` apply from the first Phase 1
handler onward.

**Phase 1:** the three method-name constants; the `_meta` key constants and the `params._meta`
location (`[P-06]`); the `-32602` `data.missing` payload (`[P-11]`); the `resultType` **table** with
its three Phase 1 entries (`[P-21]`); `_meta.serverInfo` (`[P-22]`); the `server/discover` result
field set; `ttlMs`/`cacheScope`; `-32601`; and the new subset JSON Schema.

**Deferred:** the other eight `resultType` values; mirrored headers and the sentinel codec;
`-32020`/`-32021`/`-32022`; pagination; SSE framing; MRTR; subscriptions; legacy eras and era
detection.

One consequence worth flagging: **`switches.selfCheck` moves into Phase 1 scope** for the three
Phase 1 methods, which is what makes `MOCK-201.4` testable — see AMEND-4.

**Files:** `implementation-plan.md`.

### Task impact

`TASK-010` **confirmed, and grows**: it now also owns embedding the subset schema and the
`selfCheck` validation hook. Effort stays `M` but the upper half of the range. No task is
invalidated.

---

## AMEND-4 — The wire annex schema file

### What was wrong

ADR-019 named `specification/contracts/wire-2026-07-28.schema.json` as a deliverable. The file did
not exist. `MOCK-201.4` requires the `server/discover` result to validate against "the annex schema
when `switches.selfCheck` is on", so the criterion was untestable and `@task-breakdown` correctly
excluded it from `TASK-018`.

### What changed

Created **`specification/contracts/wire-2026-07-28.schema.json`** — valid JSON Schema 2020-12,
verified with `Draft202012Validator.check_schema`, and behaviourally tested against ten
representative payloads (five that must pass, five that must fail).

**Scope is the Phase 1 subset only**, matching AMEND-3's split table: the `_meta` envelope, the
JSON-RPC envelope, `server/discover`, `tools/list`, `tools/call`, the error object and the `-32602`
`data.missing` payload. Phase 2+ surface is **absent rather than permissive**, so an accidental
Phase 2 emission during Phase 1 fails the self-check loudly instead of passing silently.

Design points worth recording:

- `#/$defs/phase1Result` is the single self-check entry point for `MOCK-201.4`.
- `discoverResult` requires only `resultType`, because `MOCK-201.3` demands that an omitted field be
  **absent, not null** and each field is independently omissible. The schema enforces the
  discrimination: `{"instructions": null}` is rejected, `{}` is accepted. Verified by test.
- `toolCallResult.isError` is `{"const": true}`, so emitting `"isError": false` is a schema
  violation rather than a silent inconsistency with `builtin-tools.md §4.2`. Verified by test.
- `metaEnvelope.additionalProperties` is **`true` and must stay true** — annex 2.11 and
  `MOCK-203.6` require unknown `_meta` keys to be preserved. A rationale field says so in-band, so
  a future tightening is a visible decision rather than a tidy-up.
- `toolDescriptor.inputSchema`/`outputSchema`/`annotations`/`icons` are `true` (accept anything),
  because `MOCK-222.3` and `MOCK-224.3` require verbatim emission of deliberately malformed
  schemas. The document says this is intentional, not an oversight.
- The error `code` is an unconstrained integer, not an enum, because `MOCK-505` requires arbitrary
  and retired codes to be emittable and `fail` is the Phase 1 seam for that.

### The provisional status is unmissable and machine-readable

Per the constraint that GAP-003 stays open, the file does not resolve any protocol question — it
records the existing `[P]` proposals in checkable form. Provisionality is carried **in-band**:

- `title` begins `PROVISIONAL … NOT RATIFIED`.
- `x-schema-version: v0`, `x-stability: v1alpha1 — may change incompatibly without notice`,
  `x-authored-not-ratified: true`, `x-gap: GAP-003`.
- `x-conformance-claim: "NONE."`
- An `x-WARNING` array — 14 lines placed before the schema body — stating that the file was
  authored, not derived from a published specification, that validating against it proves
  self-consistency and nothing about interoperability, and that scope is Phase 1 only.
- Each `$def` carries an `x-label` with its `[R]`/`[D]`/`[P-nn]` provenance.

`gap-analysis.md` GAP-003 now states explicitly that AMEND-4 **does not narrow the gap**, and the
`[P]` count rises from 38 to 52 with the `builtin-tools.md` additions.

**Files:** `contracts/wire-2026-07-28.schema.json` (new), `contracts/wire-2026-07-28.md`,
`adr/ADR-019-wire-annex-ownership.md`, `requirements-spec.md` (`201.4`), `gap-analysis.md`.

### Task impact

`MOCK-201.4` is now **in Phase 1** and testable. `TASK-010` gains the embed-and-validate hook;
`TASK-018` gains the self-check obligation (its criterion set already implies it); `TASK-027` gains
schema-validation assertions in the golden harness. `TASK-008` (`test/mcpclient`) can now validate
responses against an artifact independently of server code, which strengthens ADR-017's
anti-circularity property — the client and the server assert against the same file rather than
against each other.

---

## AMEND-5 — `goleak` and the missing dependency tier

### What was wrong

`MOCK-107.7` requires goroutine-leak detection "verified with `goleak`", naming the library.
ADR-013's approved set did not contain it, and `make deps-check` was specified as "fails on any
module outside the approved list" — where the only list present was the **runtime** set. The
specification therefore mandated a dependency its own gate would reject.

The root cause was not the missing entry. ADR-013 said "anything test-only is exempt from
criteria 1–3" but **never enumerated a test-only set**, so the exemption had nowhere to land.
`testify` and `go.uber.org/mock` sat in the *rejected* table with prose explaining they were
actually admitted — which is where the ambiguity was hiding.

### Resolution

`goleak` is **admitted as a test-only dependency**, and — as the brief anticipated — the missing
tier distinction is itself the more valuable half of the fix. ADR-013 now defines two tiers
explicitly:

| Tier | Enters a consumer's `go.mod`? | Screen | Enumerated |
|---|---|---|---|
| Runtime | **Yes** — the `MOCK-107` cost | all six criteria | approved runtime set |
| Test-only | **No** — `_test.go` or build-tagged `tools.go` only | criteria 4–6; 1–3 waived | approved test-only set (new) |

The distinction is **mechanical, not a promise**: `go list -deps ./...` excludes test imports, so if
a module appears there it is runtime whatever the author intended. `make deps-check` is now
two-tier accordingly — the runtime graph must contain nothing outside the runtime set (a test-only
module appearing there is a *failure*, meaning it leaked into production code), and the test-only
graph must contain nothing outside the test-only set.

The approved test-only set is `testify`, `go.uber.org/goleak`, `go.uber.org/mock`. Those three were
already de facto in use; they are now listed where the gate can read them.

**Why not hand-roll leak detection.** Considered: a ~40-line `runtime.NumGoroutine()` delta check.
Rejected — it is exactly the kind of test infrastructure that flakes under `-race` and ADR-003's
`-count=20` determinism suite, it cannot name the leaked stack, and `MOCK-107.7` names `goleak`
specifically, so a substitute would require changing the requirement. `goleak` has no transitive
dependencies and never reaches the hub's module graph, so the cost ADR-013 exists to control is not
incurred.

**Files:** `adr/ADR-013-dependency-policy.md` (front matter marked amended), `traceability.md`.

### Task impact

**`TASK-033` (five policy-gate CI jobs) grows**: `deps-check` is now two commands and two lists
rather than one. **`TASK-001`** adds `goleak` to `go.mod`'s test requirements. No task is
invalidated, and the alternative — discovering this when `make deps-check` first ran red in
`TASK-033` — is avoided.

---

## AMEND-6 — `validateMeta: lenient`

### What was wrong

`MOCK-203.5` `[stated]` names three strictness modes; `strict` and `off` are self-evident and
`lenient` was undefined. `TASK-017` could not write an acceptance criterion beyond "does not
reject", and a test author would have had to invent whether `lenient` records, warns, or is silent.

### Resolution — `lenient` is retained, not removed

It has a coherent and distinct meaning, so removing it would have been the wrong call.

`lenient` = **accept, but still compute and record everything a strict server would have rejected.**

The reason the mode exists: a hub that silently depends on the *server* rejecting its malformed
`_meta` will pass against `strict` and fail in production against a lax server. `lenient` makes
that latent bug reproducible. For it to prove anything, the deficiency must remain visible to the
test — hence tolerate-**and-record**, not tolerate-and-forget. That is precisely what distinguishes
it from `off`: `off` skips the work, `lenient` does the work and declines to act on it. The
difference is observable in the journal (`metaValidation.missing` is populated vs always empty) and
in `mcpmock_meta_validation_total{outcome}` (`tolerated` vs `skipped`), which makes both testable.

**What `lenient` relaxes: exactly two things** — the *presence* of `_meta.protocolVersion` and the
*presence* of `_meta.clientCapabilities`. Nothing else. Structural well-formedness is still
enforced: a non-object `_meta` is still `-32602`, a non-object `params` is still `-32600`. A
structurally broken request cannot be decoded into the journal, and `MOCK-203.6`/`MOCK-601` require
that it be. **`lenient` is a semantic relaxation, never a syntactic one.**

A full mode-comparison table is in `requirements-spec.md` under `MOCK-203`, covering rejection
behaviour, whether the missing-set is computed, the three journal fields, the metric outcome label,
and logging. Two new acceptance criteria, `203.7` and `203.8`, make the `lenient`/`off` distinction
directly testable.

**Downstream effect, stated because it is counter-intuitive:** in `lenient`/`off`, an absent
`clientCapabilities` is treated as the **empty capability set**, which makes `MOCK-206`'s `-32021`
*more* likely to fire, not less. `lenient` declines to raise its own error; it does not suppress
anyone else's.

**Files:** `requirements-spec.md`, `contracts/scenario.schema.json` (the enum now carries its
semantics in `description`), `traceability.md`.

### Task impact

**`TASK-017` grows** from "presence checks with a switch" to three specified modes plus a
`metaValidation` journal field and a metric. `TASK-005` (`journalapi` record) gains the
`metaValidation` field on the record contract — **it must be added before `TASK-017`**, and
`TASK-005` is upstream, so the ordering already works. `TASK-013` gains one metric.

---

## AMEND-7 — The control-plane unix socket

### What was wrong — partly

**Correction to the gap report.** G-10 states no path is documented. In fact
`ADR-015` line 80 already specified `${XDG_RUNTIME_DIR:-/tmp}/mcpmock-${pid}.sock`, mode `0600`,
the `--control-socket` override, `--no-control`, stderr announcement, and the macOS 104-byte
fallback. `@task-breakdown` did not see it because ADR-015 was outside its reading list — the same
restriction that produced G-8 and G-9.

What was **genuinely** missing is the socket's *lifecycle*, and that is the part `MOCK-104.4` needs
in order to be an integration test rather than a hope: what happens when a stale socket file
already exists.

### What changed

`ADR-015` gains a normative table under a new heading *The documented path*, which
`requirements-spec.md 104.4` now cites by name. Beyond confirming the existing path:

- **Override precedence.** `--control-socket` > `MCPMOCK_CONTROL_SOCKET` > default. An explicit
  override is used **verbatim** — no pid appended, no length fallback applied; an unusable explicit
  path is a startup error, never silently relocated. Surprising the operator is worse than failing.
- **Permissions.** `0600`, with an explicit `chmod` after `net.Listen` and **before** accept, so a
  permissive `umask` cannot widen it. Mode is verified after bind as part of `104.4`.
- **Directory.** Must pre-exist; mcpmock creates the socket, never the directory.
- **Stale socket — the load-bearing addition.** On `EADDRINUSE`, mcpmock **dials the socket first**.
  Dial succeeds ⇒ a live process owns it ⇒ startup fails with a collision error. Dial refused ⇒
  stale ⇒ unlink, `WARN`, retry bind **once**; a second failure is fatal. *Never unlink without
  probing* — an unconditional unlink silently steals a healthy process's socket, and with ADR-007's
  multi-instance model that is a real scenario, not a theoretical one.
- **Collision.** Defaults embed the pid, so two live instances cannot collide on the default path;
  pid reuse after an unclean kill is handled by the stale rule.
- **Cleanup.** Unlinked on graceful shutdown via the same path that flushes the journal. `SIGKILL`
  runs no cleanup — which is exactly why the stale rule exists and is what makes cleanup failure
  survivable. mcpmock deliberately does **not** sweep other `mcpmock-*.sock` files; that would race
  with concurrent instances.
- **Announcement.** Structured-log key `controlSocket` on stderr at startup.

New acceptance criterion **`MOCK-104.5`** covers stale-unlink-and-rebind vs live-collision-refusal.

**Files:** `adr/ADR-015-control-plane-one-interface-three-frontends.md`, `requirements-spec.md`,
`traceability.md`.

### Task impact

**`TASK-023` grows** by the probe-unlink-retry logic and the post-bind `chmod`, and gains a concrete
path to assert plus `104.5`. Effort stays within its band. The task is not invalidated — it is now
writable, which it was not.

---

## 1. Consolidated Phase 1 task impact

For re-gating `task-breakdown-phase1.md`. **No task is invalidated. One is complete. None is
added.**

| Task | Impact | Action |
|---|---|---|
| `TASK-002` | **COMPLETE** — AMEND-1 performed the work | **Close it.** Removes an `XS` from the plan |
| `TASK-018` | **Resize `M` → `L`** — 1 criterion became 11, plus self-check | Re-estimate |
| `TASK-017` | Grow — three defined modes, journal field, metric | Re-estimate within band |
| `TASK-010` | **Confirmed**; grows by the schema embed + `selfCheck` hook | Keep `M`, upper half |
| `TASK-023` | Grow — stale-socket probe, `chmod`, `104.5` | Re-estimate within band |
| `TASK-027` | Grow — goldens for echo canonical JSON, sleep boundary, cancellation, schema assertions | Re-estimate within band |
| `TASK-033` | Grow — `deps-check` is now two-tier | Minor |
| `TASK-001` | Add `goleak`; module path already correct | Minor |
| `TASK-005` | Add `metaValidation` to the journal record contract | Minor; upstream of `TASK-017`, ordering unaffected |
| `TASK-013` | Add `mcpmock_meta_validation_total` | Minor |
| `TASK-016` | Owns the default three-tool catalogue + suppression rule | Minor |
| `TASK-008` | Can now validate against the schema artifact — strengthens ADR-017 | Opportunity, not obligation |
| All others (22) | **Unaffected** | — |

**Dependency graph unchanged.** No edge added or removed; `TASK-002` simply drops out, and it had
one outbound edge (`→ TASK-028`, "correct import path in embed `go.mod`") which is satisfied by
this document.

**Net effort:** roughly neutral. `TASK-002` is removed; `TASK-018` grows by about the same amount.
The growth elsewhere is work that already existed and was invisible.

## 2. Verification performed

- Both JSON Schema files validated as Draft 2020-12 (`check_schema`).
- `wire-2026-07-28.schema.json` behaviourally tested: 5 payloads that must pass (including the
  all-fields-omitted discover result) and 5 that must fail (empty content, `isError: false`,
  missing `tools`, `instructions: null`, unknown `resultType`). All 10 behaved correctly.
- `scenario.schema.json` `behavior` block tested against 7 cases including the new
  `canned`-requires-`result` conditional and rejection of unknown behaviour keys. All correct.
- Residual-occurrence scan for the wrong module path and for unresolved placeholders.

## 3. Still open — unchanged by this amendment

**GAP-003 remains open and unresolved.** The `2026-07-28` wire format is not publicly available;
the annex is authored, not ratified; `[P]` count is now 52. Phases 2, 3, 5, 6 and 7 stay blocked on
it. AMEND-4 made the provisional status machine-checkable and nothing more.

---

# Pass 2 — AMEND-8…10  (2026-09-04, after Phase 1)

Pass 1 corrected gaps found *before* implementation. Pass 2 corrects three places where the
specification **contradicted itself**, found by the Phase 1 code review (REV-004, REV-005) and the
documentation pass. Two of the three were resolved silently by implementers — correctly, given what
they were reading — which is the signature of a spec defect rather than a code defect.

| # | Was | Now | Files | Code impact |
|---|---|---|---|---|
| AMEND-8 | ADR-002 seed-derived the credential-hash key; `security.md` offered "random **or** seed-derived" and never chose | Per-process `crypto/rand`, **always**; `credhash` removed from the seed tree as a named, reasoned exception | 2 | **CODE CHANGE REQUIRED** (ratifies the change already directed) |
| AMEND-9 | ADR-013 claimed `otlptracehttp` pulls "**not** gRPC" and recorded "no gRPC" as a consequence | Factually corrected with `go mod why` evidence; gRPC admitted as a disclosed transitive cost; transitive enforcement specified | 1 | **CODE CHANGE REQUIRED** (`hack/deps-check` third tier) |
| AMEND-10 | `traceability.md` labelled `MOCK-209` Phase 2, hiding its Phase 1 delivery; two further stale entries | `MOCK-209` and `MOCK-212` labels corrected; DEL-2 status corrected; phase-column semantics defined | 2 | none |
| *(ruling)* | `selfCheck` schema `default: true` vs implementation absent→`false` | **Schema wins.** Confirmed against four independent spec statements | 1 | **CODE CHANGE REQUIRED** |

**No identifier was renumbered or reused.** No new ADR was needed: AMEND-8 amends ADR-002 in place,
AMEND-9 amends ADR-013 in place. Both record the amendment in their front matter.

---

## AMEND-8 — The credential-hash key is not seed-derived

### What was wrong

Two documents gave incompatible instructions, and neither noticed:

| Document | Said |
|---|---|
| `ADR-002` (domain list) | `"credhash"` is a seed-tree sub-domain — i.e. **seed-derived** |
| `security.md §3` | "`crypto/rand` per run, **or** seed-derived" — an unresolved either/or |
| `security.md §5` | "`HMAC-SHA256(**perRunKey**, rawValue)` … so a captured journal is not a rainbow-table target" — states the *goal*, then the key-lifetime row re-offers the either/or |

The `or` is the actual defect. `security.md` never made the choice, so ADR-002's domain list made it
by default, and `internal/instance/instance.go:156` followed ADR-002 — correctly, given what it was
reading. The result: the key is seed-derived in **every** mode.

That voids the stated security property, because the seed is **deliberately public** (`MOCK-704`:
printed at startup, served at `GET /v1/seed`, exposed as `mcpmock_effective_seed` on the metrics
listener, which binds `0.0.0.0:9090` by default). Anyone holding a journal export — a CI artifact,
per `security.md §9` — plus the seed from the CI log can compute `HMAC(k, candidate)` and
dictionary-attack every recorded credential. The keying was decorative.

### The decision — ratified, and strengthened

The direction already given to development (**per-run random, not seed-derived**) is **ratified**.
A security property beats a convenience property, and seed-determinism is a convenience property.

Two points were **strengthened** beyond the original direction:

1. **No escape hatch.** There is deliberately no `deterministicKeys`-style opt-in for this key,
   unlike the AS signing key in `security.md §2.2`. The AS key has one because its *output* (a
   token) can be an **input** to a later assertion or golden. A credential hash is never an input —
   it is evidence written to the journal and read back by assertions. An opt-in would exist only to
   let someone reintroduce the vulnerability for a golden file that should not exist.
2. **Why this costs nothing, stated so it is not re-litigated.** `MOCK-407`'s claim — *"the hub
   minted a distinct upstream credential"* — is an **equality comparison between records within one
   run**. A per-process random key preserves equality and distinctness exactly. Only *cross-run*
   stability is lost, and nothing in `MOCK-407`, `MOCK-601` or `assertions-api.md` needs it. The one
   thing that would is a golden pinning a literal hash — so `credential.hash` is specified as a
   **volatile field**, normalised out of golden comparison alongside `wallTime`, `monoNs`,
   `durationNs`, `seq` and `peer`.

### How the two documents now agree

Both now carry the same rule, each pointing at the other, and each says *why* — so the reasoning
survives the next person who notices that determinism is incomplete:

- **`ADR-002`** — `"credhash"` removed from the sub-domain list, with an inline "do not re-add"
  marker; new section ***Named exceptions to seed-determinism*** carrying the exception table, the
  cost analysis, and the generalised rule: *a seed-derived value may be observed by an attacker but
  must never be relied upon to be unguessable*. Consequences gained a **Negative** entry recording
  that determinism is now deliberately incomplete in exactly one place.
- **`security.md`** — §3 secret-lifecycle row rewritten to "per process, always; never seed-derived;
  no escape hatch"; §5 key-lifetime row likewise; new §5 subsection ***Why this key is the one
  exception to seed-determinism*** explaining the contradiction, the ruling, why the AS key differs,
  the golden-file consequence, and a "do not fix this back" note.

### Code consequence

**`internal/journal.NewCredHasher` must take a per-process `crypto/rand` key, not a
`determinism.Key`.** `determinism.DomainCredHash` should be **deleted**, not left unused — an unused
domain constant is an invitation. Removing it also makes the domain-list test
(`internal/determinism/domains_test.go:35`) the enforcement point for this amendment.
`journalapi/record.go:304-305` already documents the per-process assumption and becomes correct.

---

## AMEND-9 — ADR-013 was factually wrong about gRPC

### What was wrong

ADR-013 admitted `otlptracehttp` on an explicitly stated premise:

> *"pulls `protobuf` (unavoidable for OTLP encoding) but **not** gRPC"* — and recorded
> *"Six runtime modules, **no gRPC**, no cgo"* as a positive consequence.

Both statements are false, and were false when written. Verified:

```
$ go mod why google.golang.org/grpc
github.com/vyrodovalexey/mcp-mock-server/internal/obs
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp/internal/otlpconfig
google.golang.org/grpc
```

**Root cause, read from the module source** —
`otlptracehttp@v1.46.0/internal/otlpconfig/options.go:19-23` imports `google.golang.org/grpc`,
`grpc/backoff`, `grpc/credentials`, `grpc/credentials/insecure` and `grpc/encoding/gzip`.
`otlpconfig` is the configuration struct **shared** between the HTTP and gRPC exporters, so the HTTP
exporter carries gRPC-typed option fields it never uses on the wire. This is an upstream
code-sharing artifact, not a functional need — `otlptracehttp` opens no gRPC connection.

A second, unreported error in the same ADR: **`google/uuid` sits on the "Not taken, deliberately"
list and is present** as an indirect dependency via `otel/sdk/resource`.

### The honest resolution: amend the ADR, keep `otlptracehttp`

Measured, not asserted (`CGO_ENABLED=0 go build -trimpath`, `go tool nm -size` on the linked
binary): gRPC ≈ **513 KB** linked and **66 packages**; protobuf ≈ 785 KB (unavoidable — OTLP is
protobuf-encoded); otel ≈ 467 KB; `grpc-gateway` + `genproto` ≈ 9 KB. Total binary ≈ 17.1 MB.

**Separating the two constraints, because they are not equally affected:**

- **`MOCK-101` (single statically linked binary, no runtime dependencies) — unaffected.** gRPC is
  compiled in and never dialed: no shared library, no sidecar, no listener, no goroutine. The binary
  is ≈ 0.5 MB larger. As the review said, this affects **size and attack surface, not correctness**.
- **`MOCK-107` (mcpmock enters the hub's `go.mod`) — this is the real cost, and it is precisely the
  cost ADR-013 exists to control.** `grpc`, `grpc-gateway/v2`, `genproto/googleapis/{api,rpc}`,
  `cenkalti/backoff/v5`, `google/uuid`, `golang.org/x/{net,text}` all enter every consumer's module
  graph, SBOM, Trivy scan and `govulncheck` surface — carrying the CVE stream of the tree the ADR
  itself called "historically the largest source of CVE churn in the ecosystem".

Options were costed in the ADR. Summary: **(a) keep and disclose — chosen**, because OTLP tracing is
user-mandated, the tree is inert at runtime, and the defect was the false claim rather than the
dependency. (b) Hand-rolling an OTLP/HTTP exporter still needs protobuf and is the already-rejected
"zero dependencies, absolutely" option. (c) Pinning a gRPC-free otel line is **explicitly not
asserted either way** — whether any supported release ships a gRPC-free `otlpconfig` has not been
verified, and this document does not characterise versions it has not read; it is routed as an open
question. (d) Deferring OTLP would remove a delivered, green Phase 1 capability to shrink a
consumer's SBOM — a worse trade than disclosure.

**The deeper lesson recorded in the ADR:** screening criterion 5 ("its own transitive tree is
inspected and listed") was satisfied in *form* and failed in *substance*, because the inspection was
**asserted rather than executed**. That is now mechanical.

### Transitive `deps-check` — in scope, and required

The gate that should have fired could not: `hack/deps-check` validates only *direct* dependencies,
and gRPC is transitive. That is a structural blind spot, so the fix is structural. **Not** a full
transitive allowlist — a 34-root graph churns on every otel or prometheus patch bump, and a gate
that fails on routine upgrades gets disabled. Two mechanisms with different jobs:

| Mechanism | Behaviour |
|---|---|
| **Transitive denylist** | `go list -deps ./...` must contain no module in a small, explicitly-reasoned forbidden set. Seeded with `google.golang.org/grpc`, **allowed only via the single documented path** `otlptracehttp → internal/otlpconfig`, asserted by matching `go mod why` output. A second path, or gRPC from anywhere else, fails the build. |
| **Transitive graph snapshot** | The full sorted module set from `go list -deps ./...` is committed as a golden file. Any addition or removal fails with a diff; the fix is to review and re-bless in the same commit. This makes "a new dependency requires an ADR amendment" mechanical — a reviewer sees the new module in the PR diff instead of never seeing it. |

The snapshot's **first blessing must record today's graph including gRPC**, so the amendment and the
gate agree from the outset. ADR-013 also now writes down the policy call that was living only in
`hack/deps-check/main.go:102` (`google/uuid` is permitted **indirect-only**).

### Also corrected in ADR-013

- "Six runtime modules, no gRPC" → "six *direct* runtime modules"; the gRPC consequence is now a
  disclosed **Negative** with its measured cost.
- The `otlptracehttp` weight column and the `google/uuid` "Not taken" row.
- **Flagged, not fixed:** `golang.org/x/sync` is in the approved runtime set but **absent** from
  `go.mod` and from `go list -deps ./...` — approved-but-unused. Left pre-approved; the snapshot
  will show it if it is ever actually taken.
- The "Forecloses" clause no longer claims gRPC is foreclosed on dependency grounds. It is
  foreclosed by ADR-015 on interface-design grounds — the reason that actually matters, and now the
  only one standing.

### Code consequence

**`hack/deps-check` gains the third tier** (denylist + snapshot). No application code changes.

---

## AMEND-10 — `traceability.md` phase labels

### The `MOCK-209` correction

`MOCK-209` was labelled Phase **2**. `omitResultType`, `omitServerInfoMeta` (DEF-209),
`selfCheck` and `hideFromCapabilities` (DEF-010/011) are implemented and tested in Phase 1, and
`scenarios/happy-path.yaml` lists `MOCK-209` under `coversRequirements`.

But the correct label is **not** simply "1". Checked against the artifacts:

| Criterion | State | Evidence |
|---|---|---|
| 209.1 `resultType` on every result | Phase 1 ✅ | `internal/wire`, self-check `Phase1ResultTypes()` |
| 209.2 `serverInfo` in result `_meta` | Phase 1 ✅ | |
| 209.3 `omitResultType` removes the key | Phase 1 ✅ | `scenario.Switches.OmitResultType`, `snapshot.go` resolver |
| 209.4 `omitServerInfoMeta` removes `_meta.serverInfo` | Phase 1 ✅ | `snapshot.go` resolver |
| **209.5 omission expressible *per method*** | **NOT delivered** | `scenario.schema.json` `#/$defs/switches/…/methods` exposes only `enabled`, `hideFromCapabilities`, `shape`, `ttlMs`, `cacheScope`; `scenario.MethodSwitch` matches. Global-only. |

**Corrected to:** `1 (209.1–209.4 — delivered, DEF-209), 2 (209.5 per-method omission)`.

### The root cause — an undefined column

§2's `Phase` column and §5's *Coverage by phase* had **drifted apart in meaning** and neither was
defined. §5 lists a requirement under the phase where it becomes **fully** covered; §2 was being
read as "where the work happens". Under §5's reading, `MOCK-209` in Phase 2 was *correct* — one
criterion outstanding. The defect was §2 showing bare `2`, which **hid a substantial Phase 1
delivery** from anyone asking "is `omitResultType` built yet?".

Both column semantics are now **defined normatively** at the head of §2, with a standing rule:
*whenever a requirement is partially delivered, §2 must name the criteria.* Without this, the same
confusion regenerates every phase.

### Other stale entries found

| Entry | Was | Now | Evidence |
|---|---|---|---|
| **`MOCK-212`** | `1 (via sleep, 212.1–.5), 2 (full)` — internally contradictory: it claimed *all five* criteria in Phase 1 **and** completion in Phase 2 | `1 (212.1, .3, .4, .5 + 212.2's "no further frames" clause), 2 (breadth), 9 (212.2's fault-sleep clause)` | 212.2 requires *"any in-progress **fault** sleep is aborted"* — the fault engine (C-18) is Phase 9. `MOCK-212` was never completable in Phase 2. §5 updated: `212` moves Phase 2 → 9; cumulative counts for Phases 2–8 each drop by one and reconcile to 75 at Phase 9. **No work moves** — this records when the last criterion becomes verifiable. |
| **`DEL-2`** | "**Cosign step missing from `ci.yml` — must be added**" | ✅ present | `.github/workflows/ci.yml:759-771`: `sigstore/cosign-installer`, `cosign sign --yes` (keyless/OIDC), `cosign attest --predicate sbom.spdx.json --type spdxjson`, on the image digest. Status was stale. |

### Checked and found **correct** — no change made

- **`MOCK-603` "1 (3 of 8), 10 (all)"** — exactly right. `assertions-api.md §3` names eight;
  `assert/` implements three of them (`AssertNoHeader`, `AssertHeaderNotValue`,
  `AssertRequestCount`). The two extra methods (`…AtMost`, `…AtLeast`) are variants of
  `AssertRequestCount`, not additional named assertions.
- `MOCK-203` Phase 1 (all eight criteria are Phase-1-scoped; DEF-005 wired the validator).
- `MOCK-201` / `MOCK-202` `1 (partial), 2`; `MOCK-104` / `MOCK-702` `1 (subset), 4`;
  `MOCK-901` `1 (smoke), 11`.

---

## Ruling — `selfCheck`'s default: the **schema** is right

The implementation resolves absent → `false` (`internal/instance/snapshot.go`, with an honest
comment flagging the deviation and correctly declining to edit the schema). The schema says
`default: true`. **The schema wins**, on four independent statements in the requirement set:

| Authority | Text |
|---|---|
| **PRIN-5** (`requirements-spec.md:39`) | *"a run with an empty fault list and `switches` **at defaults** produces only self-check-passing responses"* — a property of the **default** configuration, unverifiable if the check is off at defaults |
| **MOCK-244.4** (`:500`) | *"The self-check is suppressible **only** by an explicit fault rule, and the suppression is journaled with the rule id"* |
| **MOCK-503.3** (`:819`) | *"suppressed **only** for the specific rule, and the suppression is journaled"* |
| **ADR-017 §5** / `gap-analysis.md:378` (I-8) | *"self-check is **on by default** and suppressed only by an explicit fault rule"* |

Off-by-default means the check is suppressed **for everyone, always, by absence** — with no fault
rule and nothing journaled. That is a direct contradiction of 244.4 and 503.3, not a tuning choice.

**The hot-path objection does not survive contact with the requirement.** `MOCK-901`'s benchmark
conditions already specify **"self-check off"** explicitly (`requirements-spec.md:1053`,
`gap-analysis.md:161`). The benchmark opts out by name, so the default never costs it anything. The
implementation was paying a correctness price to optimise a case that had already opted out.

Secondary evidence: the codebase's own pointer idiom resolves `enabled` (schema `default: true`) as
nil → **true**. `selfCheck` was the sole field where nil → false against a schema default of true.

**`scenario.schema.json` updated** — `default` unchanged at `true`; the `description` now carries
the ruling, its four authorities and the disposal of the hot-path objection, so this is not
re-litigated a third time.

**Code consequence:** `selfCheck(sw *scenario.Switches)` must resolve `nil → true`
(`sw == nil || sw.SelfCheck == nil || *sw.SelfCheck`), matching the `enabled` idiom, and the
deviation comment must be replaced. If the enabled-path cost proves material, the answer is to make
the check cheaper or sample it — **not** to silently disable it.

---

## Consolidated code changes required by pass 2

Specification-only work is complete. These are the **code** changes the amendments now oblige, in
priority order:

| # | Change | File(s) | Driver |
|---|---|---|---|
| **CC-1** | `NewCredHasher` takes a per-process `crypto/rand` key, not a `determinism.Key`. **Delete `DomainCredHash`** rather than leave it unused. | `internal/journal/credhash.go`, `internal/instance/instance.go:156`, `internal/determinism/domains.go:60-62,100`, `internal/determinism/domains_test.go:35` | AMEND-8 (security) |
| **CC-2** | `credential.hash` added to the volatile-field set normalised out of golden comparison. | `assert/` golden path, `test-strategy.md` corpus | AMEND-8 |
| **CC-3** | `selfCheck` resolves `nil → true`; deviation comment removed. | `internal/instance/snapshot.go` | `selfCheck` ruling |
| **CC-4** | `deps-check` third tier: transitive denylist (gRPC, single documented path) + committed transitive graph snapshot, first-blessed with today's graph. | `hack/deps-check/main.go`, new golden file | AMEND-9 |
| **CC-5** | Anti-silent-wiring harness — see the structural recommendation in the pass-2 report. | `internal/instance/`, `journalapi/`, startup | six recurrences |

**CC-1 and CC-3 change observable behaviour** and may move existing goldens: CC-1 makes
`credential.hash` vary per run (hence CC-2, which must land in the same change), and CC-3 turns the
self-check on for scenarios that author no `selfCheck` key. Neither is optional — both close a
contradiction between the specification and the delivered code.

---

## Still open after pass 2

- **GAP-003 remains open and escalated.** Untouched by this pass; no protocol was invented.
- **Is there a supported gRPC-free otel line?** (AMEND-9 option (c)) — unverified by design; needs a
  network check against the otel release notes before it can be answered.
- **No `LICENSE` file exists**, though `Dockerfile` and `Chart.yaml` both declare Apache-2.0. This
  is a **deliverable gap under DEL-1/DEL-2**, flagged here and **not** resolved: authoring
  licensing terms is the copyright holder's decision, not the architect's. Blocks a clean
  open-source release; does not block Phase 2.
