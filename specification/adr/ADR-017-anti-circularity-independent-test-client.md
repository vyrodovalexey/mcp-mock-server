---
id: ADR-017
title: Anti-circularity — an independent test client that shares no code with the server
status: accepted
date: 2026-09-04
reversibility: EASY — it is test-only code
requirements: §10.1, all of §2/§3 (correctness of the emulator itself)
---

# ADR-017 — The recursion problem

## Context

mcpmock is a **test oracle**. Hub tests will trust its responses and its journal. If mcpmock is
wrong, the hub team debugs a phantom.

The obvious way to test it is circular: use mcpmock's own encoder to produce a response, then use
mcpmock's own decoder to check it. That test passes even when both are wrong in the same way —
which is the normal failure mode, because they were written by the same person from the same
misreading of the same sentence.

This is not a hypothetical risk here, because the primary protocol revision (`2026-07-28`) is
**not publicly specified** (GAP-003). There is no external conformance suite to fall back on. The
only oracle available is `requirements.md` plus the wire annex we author (ADR-019).

## Decision

Four independent mechanisms, each breaking a different circularity.

### 1. `test/mcpclient` — an independent client

A minimal MCP client written **from the wire annex, not from the server code**, using only the
standard library. Rules, enforced by `make deps-check`:

- It may import **nothing** from `internal/`. Not `internal/jsonrpc`, not `internal/wire`, not
  even the error-code constants — it declares its own.
- It parses SSE by hand (`bufio.Scanner` over `data:`/`event:`/`id:`/`:` lines), not with our sink.
- It builds `_meta` and `Mcp-*` headers from the annex tables, not from our constructors.
- It is written by whoever did **not** write `internal/modern`, and the review of it is a review
  against the annex text.

Every functional test drives the server through this client. When the two disagree, either the
server, the client, or the annex is wrong — and finding out which is exactly the work that has
value.

### 2. Byte-level golden fixtures authored from the requirement text

`test/golden/` holds `*.request` / `*.response` pairs written **by hand** from `requirements.md`
sentences, with the requirement id in the filename
(e.g. `MOCK-204_header_mismatch_base64_sentinel.txt`). They are reviewed as *readings of the
requirement*, not as outputs of the code. Regenerating a golden from the server is a
process violation; the `make golden-update` target deliberately does not exist — updates are
manual edits with a reviewer.

This is the primary defence and the reason `MOCK-604`'s redaction machinery is built early: our
own tests use it.

### 3. Round-trip and metamorphic properties

Properties that are true independently of any implementation, testable with `testing/quick`-style
generation:

- `sentinelDecode(sentinelEncode(n)) == n` for all names, including non-ASCII (`MOCK-223`).
- Pagination: concatenating all pages, deduplicated, equals the full list — **except** when a
  page-fault is armed, where the specific corruption is asserted (`MOCK-231`).
- Determinism: same seed + same bytes ⇒ same bytes out (ADR-003 §3).
- AEAD: `Open(Seal(p)) == p`; any single-bit flip in the token fails `Open` (`MOCK-243`).
- Catalogue: `IndexOf(At(i).Name) == i` for all `i` (`MOCK-221`).
- Journal: for a serialised workload, records appear in `Seq` order matching send order.

Metamorphic relations are especially valuable where absolute correctness is unknowable: e.g.
"enabling a latency fault changes timing but not response bytes" directly tests ADR-002's
determinism claim.

### 4. Cross-implementation checks where they exist

For the **legacy** eras (`2025-11-25`, `2025-06-18`, `2025-03-26`), public third-party MCP client
SDKs exist. `test/integration` includes an **optional, network-gated** job that drives mcpmock's
legacy endpoint with one such SDK. Value: it catches misreadings of the legacy handshake that our
own client would share.

For the modern era no such cross-check is possible; this is stated as a **residual risk** in
`test-strategy.md` and in GAP-003, not papered over.

### 5. The mock's self-check (`MOCK-244`)

`MOCK-244` requires the mock to **reject its own responses** that contain neither `requestState`
nor `inputRequests`. Generalised: a `switches.selfCheck` mode (default **on** in tests, **off** in
performance runs) validates every outgoing response against the wire annex's JSON Schema before
emitting, and panics — loudly, in tests — on violation. It is explicitly disabled by the fault
engine when a fault deliberately produces a non-conformant response, and the journal records that
self-check was suppressed and by which rule. That last detail matters: otherwise "self-check off"
would silently mask real bugs during fault tests.

## Options considered

1. **Test the server against itself** — rejected, this is the failure being avoided.
2. **Only golden files** — insufficient: goldens do not cover the state machines (MRTR rounds,
   sessions, subscriptions).
3. **Only an independent client** — insufficient: the client can share a misreading with the
   server if one person writes both, hence the authorship rule and the goldens-from-text rule.
4. **Formal specification (TLA+ / Alloy) of the MRTR and session state machines** — considered.
   Genuinely useful for `MOCK-241`…`247` and `MOCK-301`/`305`, which are the two real state
   machines. **Deferred**, not rejected: recorded as an option in `test-strategy.md §7` to revisit
   if MRTR defects cluster.

## Consequences

**Positive.** A whole class of "confidently wrong" defects is structurally prevented. The
independent client is also directly reusable as the `MOCK-706` record/replay client and as example
code in the documentation.

**Negative.** Real duplicated effort: a second SSE parser, a second `_meta` builder, a second
header encoder. Roughly 1500 lines of test-only code. This is the price of the tool being
trustworthy, and it is cheap relative to a hub team chasing a phantom for a week.

The authorship rule ("written by whoever did not write the server side") is a process constraint
that a single-developer project cannot satisfy. Where that is the case, the mitigation is to
write the client **first**, from the annex, before the server handler exists — recorded in
`implementation-plan.md` as an explicit task ordering, not left to good intentions.

**Forecloses.** Sharing wire types between test client and server, which will be tempting every
time the annex changes. `make deps-check` makes the temptation fail the build.
