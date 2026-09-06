---
title: mcpmock — Gap Analysis
status: draft
version: 0.1.0
updated: 2026-09-04
normative-source: specification/requirements.md
---

# Gap Analysis

Every place `requirements.md` is **ambiguous**, **silent**, **internally inconsistent** or
**untestable**, with a proposed resolution.

**Flags:**
- 🔴 **DECISION-REQUIRED** — needs the requester's decision. Listed first. These block work.
- 🟡 **RESOLVED-BY-PROPOSAL** — this specification proposes an answer; proceed unless contradicted.
- ⚪ **EXTERNAL** — depends on something outside this repository.
- ✅ **RESOLVED** — answered with evidence; retained for the record, no longer blocking.

**Blocking column** states which implementation phase cannot start.

---

## Summary

| Flag | Count | IDs |
|---|---|---|
| 🔴 DECISION-REQUIRED | **7** | GAP-003, 005, 006, 009, 012, 015, 016 |
| ✅ RESOLVED | **2** | GAP-001 (module path, AMEND-1), GAP-002 (Go version) |
| 🟡 RESOLVED-BY-PROPOSAL | 11 | GAP-007, 008, 010, 011, 013, 014, 017, 019, 020, 021, 022 |
| ⚪ EXTERNAL | 2 | GAP-004, 018 |

**Phase 1 is no longer blocked.** ✅ GAP-001 and GAP-002 were its only blockers and both are
resolved (2026-09-04, AMEND-1). 🔴 GAP-003 still blocks Phase 2 onward and **remains open** — the
`2026-07-28` wire annex is authored, not ratified. AMEND-4 added a machine-checkable schema for the
Phase 1 subset; that makes the *provisional* status testable, and does **not** resolve GAP-003. The
rest block their own phases and are listed against them in `implementation-plan.md`.

---

# 🔴 Decisions required

## GAP-001 — Go module path ✅ **RESOLVED 2026-09-04** (AMEND-1)
**No longer blocks Phase 1.**

**Resolution.** The module path is **`github.com/vyrodovalexey/mcp-mock-server`**.

**Evidence.** `git remote -v` → `git@github.com:vyrodovalexey/mcp-mock-server.git`. This is direct
evidence and supersedes the inference recorded below.

**Withdrawn inference.** This gap previously recommended `github.com/vyrodovalexey/mcpmock`,
inferred from `ci.yml:183`. **That inference was wrong.** `ci.yml:183` references the container
image `vyrodovalexey/keycloak-test:26.5` — a third-party test image, evidence of the GitHub *owner*
only, and never evidence of a module path. The error is recorded rather than deleted because it
shows the failure mode: an owner name harvested from an image tag was allowed to stand in for a
repository name.

**Note.** The module path is the repository name (`mcp-mock-server`); the root Go **package** name
remains `mcpmock`, and the binary remains `mcpmock`. See `architecture.md §6` for the
package-label→import-path mapping. Every `mcpmock/<x>` in this specification is a package label,
not an import path.

---

## GAP-002 — Go version conflict between the toolchain and CI ✅ **RESOLVED 2026-09-04**
**No longer blocks Phase 1.** Toolchain **Go 1.27.1**; `go.mod` declares `go 1.27.0`;
`ci.yml` `GO_VERSION` → `1.27.1` and `GOLANGCI_LINT_VERSION` → `v2.13.2`. The original analysis is
retained below.

**Evidence.** Local toolchain reports `go1.27.1`. `ci.yml:17` pins `GO_VERSION: '1.26.4'`. The
two disagree, and `go.mod`'s `go` directive must pick one.

This matters beyond tidiness: ADR-010 depends on **`crypto/hkdf` being in the standard library**
(added in Go 1.24), and ADR-013's "zero third-party crypto" rests on it. It is available in both
1.26 and 1.27, so the ADR holds either way — but the version must be fixed before anyone writes
the import.

**Recommendation.** `go.mod`: `go 1.27.0`. `ci.yml:17`: `GO_VERSION: '1.27.1'`. Also raise
`GOLANGCI_LINT_VERSION` from `v2.12.2` (`ci.yml:18`) to `v2.13.2`, which is the stated target.
Confirm, or state the minimum Go version consumers must have.

---

## GAP-003 — The `2026-07-28` wire format is not specified anywhere available
**Blocks: Phases 2, 3, 5, 6, 7. The single largest gap in the engagement.**

**Evidence.** `requirements.md` names, without defining: revision `2026-07-28`; error codes
`-32020` / `-32021` / `-32022`; the method `server/discover`; `resultType`; MRTR /
`InputRequiredResult` / `inputRequests` / `inputResponses` / `requestState`;
`subscriptions/listen` and `notifications/subscriptions/acknowledged`; the `_meta` key set; the
`Mcp-Param-*` mirroring rules; and the "Base64 sentinel". For each it gives names and behaviour
but no schema, no types, no nesting and no example.

I could not confirm that these correspond to any publicly published MCP specification. **No claim
is made either way** — the specification text is simply not in this repository, and I will not
assert what it does or does not contain.

**Consequence for an implementer.** `MOCK-201` cannot be written from `MOCK-201`. There are
**52 distinct wire-level details** an implementer must invent: `[P-01]`…`[P-38]` in
`contracts/wire-2026-07-28.md`, plus `[P-39]`…`[P-52]` in `contracts/builtin-tools.md`
(the `tools/call` result envelope and the `echo`/`sleep`/`fail` shapes, added by AMEND-2). Inventing
them silently produces a mock that tests the hub against a fiction, with no record of what was
assumed.

**AMEND-4 does not narrow this gap.** `contracts/wire-2026-07-28.schema.json` now makes the Phase 1
subset machine-checkable, which means the *provisional* status is now enforced by a schema that
carries its own warning, rather than asserted in prose. Self-check proves mcpmock is consistent
with its own annex; it proves nothing about interoperability. Ratification remains required.

**The four most consequential, which change the hub's own code:**

| ID | Question | Proposal |
|---|---|---|
| `P-06` | Is `_meta` at the top level of the JSON-RPC object, or inside `params`? | **inside `params`** |
| `P-13` | The Base64 sentinel format for `Mcp-Name` | **`=?B?<base64url-nopad>?=`** |
| `P-21` | The `resultType` value set | a per-method table (see annex §4.2) |
| `P-37` | What the MRTR retry echoes — this *defines* `MOCK-243`'s "cross-request" rejection | same method, same params, plus `inputResponses` + `requestState` |

**Recommendation.** One of:
1. Supply the `2026-07-28` specification, and the annex is rewritten to match; **or**
2. Ratify `contracts/wire-2026-07-28.md`'s `[P]` items as the definition mcpmock emulates.

Until then: `apiVersion` stays `v1alpha1`, the module stays `v0`, and mcpmock's documentation must
**not** claim MCP conformance — only that it emulates the revision as described in
`requirements.md` plus the annex. See ADR-019.

---

## GAP-005 — `MOCK-903` and `MOCK-904` are jointly impossible as written
**Blocks: Phase 6.**

**Evidence.** `MOCK-903`: "≥20 000 concurrent open SSE streams **per instance**."
`MOCK-904`: "≥200 logical server instances in one process."

Read literally and jointly: 20 000 × 200 = **4 000 000 concurrent streams in one process**. At
ADR-012's measured-per-stream cost (~25 KiB user space) that is ~100 GiB of memory and ~8 million
goroutines. It is not achievable on any reasonable host and is almost certainly not intended.

**Recommendation.** Restate `MOCK-903` as **"≥20 000 concurrent SSE streams per process,
distributed across instances in any way"**, and note that the 200-instance and 20 000-stream
targets are measured in separate performance profiles, not simultaneously. `deployment.md §5`
already sizes them as separate profiles (`many-streams`, `large-fleet`).

Confirm, or state the intended simultaneous target.

---

## GAP-006 — §9 performance targets have no stated measurement conditions
**Blocks: Phase 11 acceptance (but the conditions must be agreed in Phase 1 so the perf harness is built right).**

**Evidence.** `MOCK-901` says "≥20 000 `tools/call` requests/s per instance on 4 vCPU with a
trivial handler and journaling disabled". Unspecified: TLS or plaintext; HTTP/1.1 or HTTP/2;
keep-alive or not; connection count; payload size; log level; percentile target; measurement
duration; whether the load generator shares the CPUs. Each of these moves the result by a factor
of two or more. TLS alone typically costs 30–60% of throughput.

`MOCK-902` and `MOCK-903` are similarly unconditioned.

**Recommendation.** Adopt the conditions written into `requirements-spec.md §9`:
Linux, 4 vCPU, `GOMAXPROCS=4`, **HTTP/1.1 plaintext with keep-alive**, ~500-byte request bodies,
log level `INFO` (per-request logging off), tracing off, self-check off, load generator on
separate CPUs, 60 s steady state after 30 s warm-up, error rate < 0.1%, **p99 < 25 ms**.
A TLS variant is measured and **reported without a threshold**.

Confirm, or state the intended conditions — particularly whether TLS is in or out.

---

## GAP-009 — `MOCK-502` "DNS failure simulation" is not implementable as written
**Blocks: Phase 9.**

**Evidence.** `MOCK-502` lists "DNS failure simulation" among transport faults. mcpmock is a
**server**. It performs no name resolution on the path being tested; the hub resolves mcpmock's
hostname, using the hub's resolver, before mcpmock is involved at all. A server cannot fail its
own DNS lookup because it does not make one.

**Recommendation.** Reinterpret as *"the hub must be able to be pointed at a name that fails to
resolve"*, delivered as two helpers:
- **(a)** `mcpmock gen endpoint --unresolvable` emits an endpoint URL using an RFC 6761
  `.invalid` hostname, guaranteed never to resolve, for the hub's upstream configuration. Trivial,
  zero risk.
- **(b)** an optional `mcpmock dnsfail` subcommand serving a local UDP/TCP DNS responder that
  returns `SERVFAIL` / `NXDOMAIN` / timeout for a configured zone, which the test points the hub's
  resolver at. This is real work (~400 lines) and adds a DNS server to the product's surface.

Recommend **(a) only** for the delivered scope, with (b) recorded as optional. Needs a decision:
is (a) sufficient?

---

## GAP-012 — `MOCK-706` (record/replay proxy) roughly doubles the protocol surface
**Blocks: nothing in phases 1–11; it is a scope decision.**

**Evidence.** `MOCK-706` is a **SHOULD**. Implementing it makes mcpmock an MCP **client** as well
as a server: it must construct conformant requests, parse real servers' SSE, handle their
sessions, follow their MRTR flows and negotiate their eras. That is a second full implementation
of §2 and §3 from the other side.

**Compounding factor:** it must be built against the *real* protocol, which per GAP-003 we do not
have. Recording against a real MCP server would in fact be a *good* way to discover the real wire
format — which is an argument for doing it, but only after GAP-003 is resolved, since we would not
know how to interpret what we recorded.

**Partial mitigation already in the design:** ADR-017 requires `test/mcpclient`, an independent
client, for anti-circularity reasons. That client is ~60% of what `MOCK-706` needs.

**Recommendation.** Defer to a final optional phase (Phase 12). Confirm that deferring a SHOULD
is acceptable, or raise its priority and accept the cost elsewhere.

---

## GAP-015 — Who authors and approves the hostile corpus (`MOCK-507`)?
**Blocks: Phase 9 (`MOCK-507` only).**

**Evidence.** `MOCK-507` requires "a bundled corpus" of prompt-injection payloads with
"instruction-like text and hidden Unicode". `requirements.md` does not say where the content comes
from, who reviews it, or under what licence it is redistributed.

This is not an architectural question. It carries legal exposure (redistribution of third-party
attack corpora), security-review exposure (shipping attack payloads in a signed container image),
and an editorial question (what counts as a representative corpus).

ADR-018 specifies the **containment**: encoded at rest, plaintext manifest with `sourceRef` and
`license` per entry, hash-verified in CI, three gates, delimiters, NetworkPolicy requirement,
loud signalling. That machinery is ready to hold whatever content is approved.

**Recommendation.** Name an owner and a review path. Concretely:
- Start with a **small, wholly original corpus** (~20 entries) written in-house, so provenance and
  licence are unambiguous.
- Require security sign-off on the manifest before the first release that ships it.
- Do **not** vendor a third-party corpus without a licence review.

Needs a decision: who signs off, and is an in-house corpus acceptable for v0?

---

## GAP-016 — `MOCK-225` "network `$ref` to a public host" is deliberate egress from a test pod
**Blocks: Phase 3.**

**Evidence.** `MOCK-225` requires schemas with "network `$ref` (to a loopback address, **to a
public host**)". Because mcpmock resolves `$ref`s for `MOCK-606` argument validation, this is a
configuration-driven outbound fetch — a textbook SSRF primitive — from inside the test namespace.

**Design already in place** (`security.md §7`): loopback allowed by default and served by an
embedded in-process `$ref` host; public hosts require `hostile.allowExternalRefs: true` **and** an
explicit host allowlist; disabled entirely under `--safe-mode` (the image default); 2 s timeout,
no redirects, 1 MiB cap, JSON content types only; excluded from the CI sweep.

**Recommendation.** Confirm that (a) external `$ref` fetching is genuinely wanted rather than
"emit a schema containing an external `$ref` but never fetch it", and (b) the allowlist +
opt-in + never-in-CI posture is acceptable.

If the intent is only *to emit* such a schema and observe how the **hub** handles it — which is
the more likely reading of `MOCK-225` — then mcpmock never needs to fetch anything and this gap
closes entirely. **That is my recommended reading**, and it is materially safer.

---

# ⚪ External dependencies

## GAP-004 — The `HUB-nnn` requirements document is not in this repository
**Blocks: §10.5 (conformance suite) and §10.6 (mapping table). Not on the phase critical path.**

**Evidence.** `requirements.md` §10.5 requires a "conformance test suite for the hub … running the
acceptance criteria of **the hub requirements document**", and §11's coverage matrix cites
`HUB-103…109`, `HUB-121…128`, `HUB-141…148`, `HUB-161…169`, `HUB-181…186`, `HUB-201…209`,
`HUB-221…229`, `HUB-241…244`, `HUB-301…310`, `HUB-401…410`, `HUB-167`, `HUB-504`, `HUB-701…724`,
`HUB-801…804`. That document is **not present**. `MOCK-245` even cites a specific behaviour
"per HUB-206" whose content is unknown.

**Position taken.** No `HUB-nnn` semantics are invented anywhere in this specification. Not one.

**Delivered instead:** the extension points. `MOCK-603` assertion helpers, `MOCK-705` scenario
library, an importable module (`MOCK-107`), and a documented harness shape. The hub team can
write the suite against these. A structured but **empty** mapping table is delivered for them to
populate.

**To close:** supply the document, or accept that DEL-5 and the DEL-6 mapping table are owned by
the hub team.

## GAP-018 — Where do `MOCK-604` golden files live?
**Not blocking.**

`MOCK-604` requires snapshot comparison "so a hub behavior change surfaces as a reviewable diff".
The diff is of **hub** behaviour, so the goldens belong in the **hub's** repository, reviewed by
hub reviewers. mcpmock ships only the comparison and redaction library.

**Recommendation.** Confirm. This also affects who owns the `schemaVersion` compatibility burden
on `journalapi.Record`.

---

# 🟡 Resolved by proposal

## GAP-007 — Count-based fault triggers are nondeterministic under concurrency
Conflicts with §0.1. "First 3 matching requests" depends on which requests arrive first, which
depends on scheduling. Atomic counters make it consistent, not deterministic.
**Proposal:** ship `determinism.strict` (default `false`), which routes count evaluation through
the journal's global `Seq`, making "first 3" mean "the 3 lowest-`Seq` matching requests" —
deterministic, at the cost of a small serialisation window. Journal always records which rules
fired, so behaviour is *observable* even when not predictable. Documented as a known exception to
§0.1. **Open sub-question: should `strict` be the default?** Recommend no (it costs throughput and
most tests do not need it).

## GAP-008 — §0.1 "byte-identical responses" vs `MOCK-501` latency injection
Injected latency makes *timing* nondeterministic by design.
**Proposal:** read §0.1 as applying to **response bytes**, not wall-clock. Journal timestamps and
durations are redacted in golden comparison by default. Stated explicitly so nobody discovers it
during a flaky test.

## GAP-010 — `MOCK-243` "cross-request" needs the retry's shape defined
Cannot decide whether a retry is "the same request" without knowing what the retry echoes.
**Proposal:** annex `[P-37]` — the retry is the same method with the same params plus
`inputResponses` and `requestState`; `rdig` binds the **initial** params. Folded into GAP-003.

## GAP-011 — `MOCK-203` does not say how the error identifies the missing field
**Proposal:** `error.data.missing: ["protocolVersion"]` (annex `[P-11]`). Without it a hub test
cannot distinguish which field it forgot, which is the whole point of the requirement.

## GAP-013 — `MOCK-226` names only two orderings
`deterministic` and `shuffled`. **Proposal:** add `reversed` and `byName` as conveniences; they
cost nothing and cover common hub-ordering tests. Marked `[assumed]`.

## GAP-014 — Scope and `cacheScope` semantics unstated
`MOCK-404` does not define scope matching (exact / prefix / hierarchical); `MOCK-228` does not
define the emitted `cacheScope` for a scope-filtered list.
**Proposal:** `scopeMatch: exact` by default with `prefix` available; `cacheScope` defaults to
`"private"` for scope-filtered lists (which is what `MOCK-228` is testing the hub for), and is
overridable to any string.

## GAP-017 — `MOCK-902` `overflow: block` can deadlock the system under test
An unbounded block turns journal pressure into a hang in the **hub**, and the hub team would
correctly report it as a mock bug.
**Proposal:** `block` is bounded by `journal.blockTimeoutMs` (default 100 ms), then degrades to
drop-oldest with a **distinct** counter so the degradation is visible. A third policy, `error`
(reject with `503`), is added for tests that must never lose evidence.

## GAP-019 — `MOCK-303` dual-era needs a total, deterministic discrimination rule
`requirements.md` says dual-era must serve both concurrently but does not say how to decide which
a given request is.
**Proposal:** the ordered rule in `contracts/wire-2026-07-28.md §9`, with a configurable
`dualDefault` (default `modern`) so the rule is **total** — every input resolves. Folded into
GAP-003 for ratification.

## GAP-020 — `MOCK-305` does not say what happens to in-flight requests on session invalidation
**Proposal:** `sessions.inFlightOnInvalidate: complete | fail` (default `complete`), with the
choice journaled. Both behaviours are interesting to a hub, so make it configurable rather than
guessing.

## GAP-021 — `crypto/hkdf` availability must be verified before ADR-010 is implemented
ADR-010 and ADR-013's "zero third-party crypto" depend on `crypto/hkdf` being in the standard
library (added Go 1.24). The target toolchain is 1.27.1, so it should be present.
**Proposal:** verify with a one-line compile check as the first task of the ADR-010
implementation. If absent, the fallback is `golang.org/x/crypto/hkdf` (a small, well-scoped
addition) or a 15-line in-house HKDF over `crypto/hmac` — HKDF is simple enough to implement
correctly, unlike AEAD.

## GAP-022 — `MOCK-247` "MUST assert" is odd phrasing for a server
A server does not "assert"; a test does.
**Proposal:** mcpmock **records** both ids and exposes `retryIdsDistinct` per chain; the
*assertion* is `assert.AssertRetryIDsDistinct()` (`MOCK-603`); and an optional
`mrtr.enforceDistinctRetryIds` makes the server reject a reused id, for tests that want the server
to police it. All three, so no reading is lost.

---

# Internal inconsistencies found in `requirements.md`

| # | Inconsistency | Resolution |
|---|---|---|
| I-1 | §0.1 demands byte-identical determinism; §5 demands probabilistic and count-based faults, and `MOCK-226` demands *non*-deterministic ordering | ADR-002: content-addressed seeding makes probability and ordering deterministic-for-us while nondeterministic-for-the-hub. Count triggers remain a genuine exception — GAP-007. |
| I-2 | `MOCK-903` "per instance" × `MOCK-904` "200 instances" | GAP-005 |
| I-3 | `MOCK-105` requires logs on stderr; `MOCK-102` requires stdout for stdio framing — a hard conflict if any code writes to stdout | ADR-011: stdout hijack + leak counter. Resolved structurally. |
| I-4 | `MOCK-101` "no runtime dependencies" vs `MOCK-105` requiring Prometheus and the user requiring OTel | ADR-013: "no runtime dependencies" is about deployment (no sidecars, no services, static binary), not about Go modules. Six screened **direct** modules, no cgo. **Corrected by AMEND-9:** this row previously read "no gRPC", which was false — `google.golang.org/grpc` is in the runtime graph transitively via `otlptracehttp`. It is inert (never dialed) so `MOCK-101` still holds, but it is disclosed, not denied. See ADR-013 *The gRPC correction*. |
| I-5 | `MOCK-601` requires recording "all HTTP headers"; the constraint "credentials hashed" requires *not* recording the `Authorization` value | `security.md §5`: the header's presence, casing and position are preserved; the value is replaced with a hash marker. Wire fidelity is broken exactly once, deliberately, in favour of not leaking. |
| I-6 | `MOCK-704` requires every pseudo-random decision to be seeded; `MOCK-405`/`MOCK-106` need cryptographic keys, which must **not** be seed-derived in any real context | ADR-020 / `security.md §3`: `crypto/rand` by default; seed-derived only under an explicit `deterministicKeys` flag that logs a `WARN` and is documented as producing a publicly derivable key. |
| I-7 | §10.2 requires a Cosign signature "built in the **existing** pipeline"; the existing pipeline has no Cosign step (`ci.yml` generates an SBOM at `:609` but never signs) | `deployment.md §7.6`: the step must be added. §10.2 is currently **unmet by the existing workflow**. |
| I-8 | `MOCK-244` requires the mock to reject **its own** invalid responses, i.e. self-validation; §5 requires the mock to emit deliberately invalid responses | ADR-017 §5: self-check is on by default and suppressed **only** by an explicit fault rule, with the suppression journaled by rule id. |

---

# Untestable-as-written summary

| Requirement | Problem | Proposed testable form |
|---|---|---|
| `MOCK-107` | "under 200 ms" — no workload, no percentile | p95 < 200 ms over 50 runs on the reference scenario (200 instances × 5000 tools) |
| `MOCK-201`, `204`, `209` | field names without types or shapes | resolved by the ratified annex (GAP-003) |
| `MOCK-225` | "composition-keyword bombs sized to a configurable validation cost" — no cost unit | `estimatedValidationOps`, verified within ±30% by measuring validation wall time |
| `MOCK-247` | "MUST assert" | GAP-022 |
| `MOCK-301` | nine behaviours in one bullet | split into 301.1–301.11 in `requirements-spec.md` |
| `MOCK-501` | "per-percentile injection" undefined | inverse-CDF from `{p, latency}` points; measured p50/p90/p99 within ±15% |
| `MOCK-502` | "DNS failure simulation" | GAP-009 |
| `MOCK-903` | "per instance" | GAP-005 |
| `MOCK-901`/`902`/`903` | no measurement conditions | GAP-006 |
| §10.5, §11 | depend on an absent document | GAP-004 |
