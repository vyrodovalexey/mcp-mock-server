---
title: Fault Catalogue — the complete §5 action vocabulary
status: draft
version: 0.1.0
updated: 2026-09-04
requirements: §5 preamble, MOCK-501…MOCK-508
adr: ADR-009
schema: contracts/scenario.schema.json#/$defs/faultRule
---

# Fault Catalogue

Every fault is `selector × trigger × action` (ADR-009). This document is the **action** vocabulary.
Selector and trigger are identical for every row, which is the point of the model.

**Universal properties** (verified by the §5-preamble matrix test):
- Addressable by `when.{instance, method, name, era, phase, mrtrRound, principal}`.
- Triggered by `probability` (seeded, per rule id), `count`, or control-API `armed`.
- Journaled with the rule id; increments `mcpmock_faults_injected_total{rule,kind}`.
- Composable in list order; `stopPropagation` halts.

**Legend.** `phase` = where it is evaluated. `req` = extra requirement.
`suspicious` = `mcpmock validate` emits a warning when combined with certain other rules.

---

## 501 — Latency  `kind: latency`  ·  phase `response` | `stream`

| Action | Config | Notes |
|---|---|---|
| Fixed delay | `mode: fixed, fixedMs` | ±5 ms accuracy |
| Distribution | `mode: distribution, distribution:{kind: normal\|lognormal\|exponential, meanMs, stddevMs}` | seeded; mean within ±10% over 10 000 samples |
| Percentile | `mode: percentile, percentiles:[{p, ms}]` | inverse-CDF, linear interpolation; p50/p90/p99 within ±15% |
| Slow first byte | `slowFirstByteMs` | delays only the first byte |
| Slow SSE events | `slowSseEventMs` | per-event delay |
| Stall after N events | `stallAfterEvents: N` | stops writing, does **not** close |

All waits `select` on `ctx.Done()` and abort on cancellation (`MOCK-212`).

---

## 502 — Transport  `kind: transport`  ·  phase `connection` | `stream`

| Action | Config | Extra requirement |
|---|---|---|
| Connection reset | `kind: reset` | `transport.http.forceHTTP1: true` (hijack is unavailable under HTTP/2) |
| Half-close | `kind: halfClose` | same |
| Truncate mid-JSON | `kind: truncate, truncateAtBytes` | — |
| TCP accept refusal | `kind: acceptRefusal, durationMs` | `listener: own` |
| TLS handshake failure | `kind: tlsHandshakeFailure, tlsFailureMode: wrongCert\|expiredCert\|unknownCA\|alert` | `listener: own` |
| **DNS failure** | — | 🔴 **not implementable as a server-side fault — GAP-009.** Delivered instead as `mcpmock gen endpoint --unresolvable` (RFC 6761 `.invalid` host), with an optional `mcpmock dnsfail` responder as a decision item. |

Connection-phase faults are journaled at `phase: connection` even though no HTTP request exists.

---

## 503 — Protocol  `kind: protocol`  ·  phase `response`

All emitted via `FrameRawBytes` (ADR-006) so the typed path is never compromised. Each suppresses
the self-check for that response only, and the suppression is journaled by rule id.

| Action | Config |
|---|---|
| Invalid JSON | `kind: invalidJson, invalidJsonKind: truncated\|extraComma\|nan\|duplicateKeys\|bom\|controlChars` |
| JSON-RPC id mismatch | `kind: idMismatch` |
| Duplicate id | `kind: duplicateId` |
| `null` id | `kind: nullId` |
| Missing `jsonrpc` | `kind: missingJsonrpc` |
| Response to a notification | `kind: responseToNotification` |
| Unsolicited response | `kind: unsolicitedResponse` |
| Unknown method | `kind: unknownMethod` |
| Wrong `resultType` | `kind: wrongResultType, resultTypeValue` |
| Unknown `resultType` | `kind: unknownResultType, resultTypeValue` |
| `input_required` on an unsupported method | `kind: inputRequiredOnUnsupportedMethod` |
| Arbitrary bytes | `rawBytesBase64` — the escape hatch |

---

## 504 — Size  `kind: size`  ·  phase `response` | `stream`

| Action | Config | Notes |
|---|---|---|
| Oversized result | `kind: oversizedResult, bytes` | **streamed**, never fully buffered by mcpmock |
| Oversized SSE event | `kind: oversizedSseEvent, bytes` | same |
| Many content blocks | `kind: manyContentBlocks, blocks` | ≥ 100 000 supported |
| Deep `structuredContent` | `kind: deepStructuredContent, depth` | ≥ 10 000 supported |

**Validation rule:** `journal.bodies: full` combined with `bytes > journal.maxBytes/16` is a
**scenario error**, not a warning (ADR-005) — it would evict the entire journal.

---

## 505 — Errors  `kind: error`  ·  phase `response`

| Group | Codes |
|---|---|
| JSON-RPC standard | `-32700`, `-32600`, `-32601`, `-32602`, `-32603` |
| MCP | `-32020` HeaderMismatch, `-32021` MissingRequiredClientCapability, `-32022` UnsupportedProtocolVersion |
| Retired | `-32002`, `-32042` |
| Reserved-range undefined | any of `-32768…-32000` not listed above, **including both boundaries** |
| Outside the reserved range | application codes, positive codes, `0` |

`message` may be absent; `data` may be any JSON type including a scalar. `httpStatus` is
independently settable, so code/status disagreement is testable.

---

## 506 — Content  `kind: content`  ·  phase `response`

| Action | Config | Notes |
|---|---|---|
| `isError: true` result | `kind: isError` | — |
| `structuredContent` violating `outputSchema` | `kind: schemaViolation, violation: wrongType\|missingRequired\|extraProperty\|wrongEnum` | the declared schema is unchanged; only the content violates it |
| Resource links to foreign schemes | `kind: foreignUriScheme, schemes: [file, smb, data, gopher, …]` | emitted as data |
| Hostile icon URLs | `kind: hostileIconUrl, iconUrls: [javascript:…, file:…, …]` | **mcpmock never dereferences any URL it emits** (`security.md §7`) |

Actions carrying active content are gated by `hostile.enabled` (ADR-018 §7).

---

## 507 — Prompt injection  `kind: corpus`  ·  phase `response`

| Config | Meaning |
|---|---|
| `categories: [...]` / `ids: [...]` | corpus selection |
| `targets: [description, annotations, instructions, title]` | where the payload is injected |
| `wrap: true` (default) | emit inside `<<MCPMOCK-UNTRUSTED id=…>>…<</MCPMOCK-UNTRUSTED>>` |

**Three gates, all required** (ADR-018): `spec.hostile.enabled: true`, this rule, and **not**
`--safe-mode` (which is the container image default). Failing any gate emits
`[[mcpmock:corpus:<id>:withheld:<reason>]]` and a `WARN`.

**When armed:** `mcpmock_hostile_mode{instance} = 1`, `X-Mcpmock-Hostile: 1` on responses, a
startup `WARN`, `corpusIds` on every affected journal record, and `hostile: true` in
`GET /v1/instances`. The Helm chart **refuses to render** hostile mode without
`networkPolicy.enabled: true`.

Corpus content ownership is 🔴 **GAP-015**.

---

## 508 — Restart  `kind: restart`  ·  phase `response` | control API

| Action | Config | Notes |
|---|---|---|
| Process exit | `kind: exit, delayMs, exitCode, flushJournalTo` | in-flight requests journaled (and optionally flushed to a file) before exit |
| Hanging shutdown | `kind: hangShutdown` | never closes streams after SIGTERM; the pod dies at `terminationGracePeriodSeconds` |
| Rolling restart (HTTP) | not a fault rule | a deployment operation; covered by the Helm e2e test |

**`exit` is refused in embedded-library mode** unless `WithAllowProcessExit()` was passed
(ADR-009) — otherwise a hub unit test would kill its own test binary.

---

## Page faults  `kind: page`  ·  phase `response`  (supporting `MOCK-231`)

Expressed through `paging.perPage` rather than the fault list where the corruption is
page-specific, and through a fault rule where it should be probabilistic:

| Action | Effect |
|---|---|
| `overlapPrevious: k` | page N+1 repeats the last `k` items of page N |
| `skipItems: k` | page N+1 skips `k` items |
| `error: {...}` | the page returns the configured error |
| cursor expiry | driven by `paging.cursorTTLSeconds` |

---

## Suspicious combinations

`mcpmock validate` emits **warnings** (exit 0 — §0.4 permits nonsense deliberately) for:

| Combination | Why it is flagged |
|---|---|
| `truncate` + `oversizedResult` | the truncation point is likely reached before the size is |
| `reset` + `latency.stallAfterEvents` | the reset makes the stall unobservable |
| `corpus` + `wrap: false` + `hostile.allowExternalRefs` | maximum blast radius; deserves a second look |
| `restart.exit` + `probability` | a probabilistic process exit makes a test suite nondeterministic in a way seeding cannot fix |
| any `count` trigger without `determinism.strict` under a concurrent load scenario | GAP-007 |
