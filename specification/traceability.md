---
title: mcpmock — Traceability Matrix
status: draft
version: 0.2.0
updated: 2026-09-04
amended: 2026-09-04 — AMEND-10 (MOCK-209 and MOCK-212 phase labels; DEL-2 status; phase-column semantics defined)
rule: every MOCK-nnn appears exactly once in §2; no gaps in either direction
---

# Traceability

## 1. Component catalog

| ID | Component | Package | Responsibility | ADRs |
|---|---|---|---|---|
| **C-01** | Public facade | `mcpmock` | Lifecycle, options, instance access, in-process `Control` | ADR-001, ADR-007, ADR-015 |
| **C-02** | Scenario types & loader | `scenario` | Config types, `extends` composition, JSON Schema validation | ADR-008 |
| **C-03** | Journal contract | `journalapi` | `Record`, `View`, `Selector`, `Query`, `Correlation` — stdlib-only | ADR-001, ADR-005 |
| **C-04** | Assertions | `assert` | The eight named assertions, golden compare, correlation helpers | ADR-001, ADR-017 |
| **C-05** | CLI | `cmd/mcpmock` | `serve`, `validate`, `ctl`, `gen`, `corpus`; the only place process globals are touched | ADR-011, ADR-013, ADR-015 |
| **C-06** | Determinism kernel | `internal/determinism` | Seed tree, ChaCha8 DRNG, virtual clock, deterministic id minting | ADR-002 |
| **C-07** | Ordered containers | `internal/ordered` | Deterministic iteration; canonical JSON support | ADR-003 |
| **C-08** | JSON-RPC core | `internal/jsonrpc` | Envelope, raw ids, error codes, canonical encoder | ADR-003 |
| **C-09** | Wire types | `internal/wire` | 2026-07-28 + legacy shapes, generated from the annex | ADR-019 |
| **C-10** | Engine | `internal/engine` | Pipeline stages, `Exchange`, `ResponseSink`, `Initiator`, handler registry, single error encoder | ADR-006, ADR-014 |
| **C-11** | Era arbiter | `internal/era` | Modern/legacy/dual detection, probe behaviours | ADR-019 |
| **C-12** | Modern handlers | `internal/modern` | 2026-07-28 methods, `_meta`, header mirroring, `resultType` | ADR-019 |
| **C-13** | Legacy handlers | `internal/legacy` | Handshake, sessions, GET SSE, `Last-Event-ID` replay, server-initiated requests | ADR-006, ADR-019 |
| **C-14** | MRTR | `internal/mrtr` | `InputRequiredResult`, rounds, re-asking, `requestState` AEAD | ADR-010 |
| **C-15** | Catalogue | `internal/catalogue` | Virtual generation, overlays, drift, scope views, edge cases | ADR-004 |
| **C-16** | Paging | `internal/paging` | Opaque AEAD cursors, per-page ttl/scope, page faults | ADR-004, ADR-010 |
| **C-17** | Journal store | `internal/journal` | Sharded ring, seqlock, overflow, query, NDJSON, redaction | ADR-005 |
| **C-18** | Fault engine | `internal/fault` | Selector × trigger × action, ordered rules, bloom fast path | ADR-009 |
| **C-19** | Authorization | `internal/authz` | Modes, PRM document, challenges, audience and scope checks | ADR-020 |
| **C-20** | Mock AS | `internal/authsrv` | Metadata, JWKS, `/token`, RFC 9207 | ADR-020 |
| **C-21** | Minimal JWS | `internal/jwtmini` | ES256/RS256 sign+verify, deliberate malformation | ADR-020 |
| **C-22** | stdio transport | `internal/transport/stdio` | Framing, mux, stdout hijack integration | ADR-006, ADR-011 |
| **C-23** | HTTP transport | `internal/transport/httpx` | Prefix router, JSON/SSE sinks, TLS/mTLS, hijack faults | ADR-006, ADR-012, ADR-007 |
| **C-24** | Control plane | `internal/control` | HTTP + UDS front-ends over `Control` | ADR-015 |
| **C-25** | Instance & registry | `internal/instance` | `*Instance`, `Snapshot`, COW mutation, route table | ADR-007, ADR-014 |
| **C-26** | Timer wheel | `internal/sched` | Keep-alives, timed notifications, drift, expiry sweeps | ADR-012 |
| **C-27** | Observability | `internal/obs` | slog handler, Prometheus registry, OTel wiring, metric handles | ADR-016 |
| **C-28** | Config loader | `internal/config` | Compose, provenance, embedded schema, semantic rules | ADR-008 |
| **C-29** | Certificates | `internal/certs` | Ephemeral CA and leaf generation | — |
| **C-30** | Corpus | `internal/corpus` | Encoded hostile payloads, manifest, gates | ADR-018 |
| **C-31** | Independent client | `test/mcpclient` | Test-only oracle; imports nothing internal | ADR-017 |

---

## 2. Requirement → component → contract → verification

Verification levels: `U` unit · `F` functional · `I` integration · `E` e2e · `P` performance.
Phase from `implementation-plan.md`.

> **What the `Phase` column means (defined by AMEND-10, 2026-09-04).** This column lists **every
> phase in which the requirement receives work**, with the covered acceptance criteria named when
> coverage is partial. It is *not* "the phase at which the requirement is done" — that is §5
> *Coverage by phase*, which lists a requirement only under the phase where it becomes **fully**
> covered.
>
> These two columns were never defined and had drifted apart in meaning, which is the root cause of
> the `MOCK-209` discrepancy AMEND-10 corrects: §5 correctly showed `MOCK-209` completing in Phase 2
> (one criterion outstanding), while §2 showed only `2` and so **hid the substantial Phase 1
> delivery**. A reader checking "is `omitResultType` built yet?" got the wrong answer from §2.
> Whenever a requirement is partially delivered, §2 **must** name the criteria.

### §1 Deployment and interfaces

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| MOCK-101 | C-05, C-27 | ADR-013 | `deployment.md §2`, §8 | I, E | 1 |
| MOCK-102 | C-22, C-23, C-10 | ADR-006, ADR-011 | `contracts/wire-2026-07-28.md §1` | F, I | 1 |
| MOCK-103 | C-25, C-23 | ADR-007 | `scenario.schema.json#/$defs/fleetSpec` | F, I | 4 |
| MOCK-104 | C-24, C-05, C-01 | ADR-015 | `control-api.openapi.yaml`, `library-api.md §4`, **ADR-015 §"The documented path" (104.4, 104.5 — AMEND-7)** | I | 1 (subset), 4 (full) |
| MOCK-105 | C-27, C-22 | ADR-011, ADR-016 | `observability.md §2`, §4 | F, I | 1 |
| MOCK-106 | C-23, C-29 | ADR-007 | `scenario.schema.json#/$defs/transport`, `security.md §4` | I | 4 |
| MOCK-107 | C-01, C-03, C-04, C-25 | ADR-001, ADR-007, **ADR-013** | `library-api.md`; **107.7 verified with `go.uber.org/goleak`, admitted test-only by ADR-013 (AMEND-5)** | U, E, P | 1 |

### §2 Modern era

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| MOCK-201 | C-12, C-09 | ADR-019 | annex §5; **`wire-2026-07-28.schema.json#/$defs/discoverResult` (201.4 self-check — AMEND-4)** | F | 1 (partial), 2 |
| MOCK-202 | C-12, C-10 | ADR-019 | annex §4; `switches.methods`; **`builtin-tools.md` (echo/sleep/fail, 202.10–202.20 — AMEND-2)**; **`wire-2026-07-28.schema.json#/$defs/toolListResult`,`#/$defs/toolCallResult`** | F | 1 (partial), 2 |
| MOCK-203 | C-12, C-10 | ADR-019 | annex §2; **`requirements-spec.md` §"`switches.validateMeta` — the three modes" (203.7, 203.8 — AMEND-6)**; **`wire-2026-07-28.schema.json#/$defs/metaEnvelope`,`#/$defs/metaMissingErrorData`** | F | 1 |
| MOCK-204 | C-12, C-08 | ADR-003, ADR-019 | annex §3 | U, F | 2 |
| MOCK-205 | C-12, C-11 | ADR-019 | annex §6 | F | 2 |
| MOCK-206 | C-12, C-15 | ADR-019 | annex §6 | F | 2 |
| MOCK-207 | C-23, C-12 | ADR-019 | annex §1 | F | 2 |
| MOCK-208 | C-23, C-10, C-06 | ADR-002, ADR-006, ADR-012 | annex §1; `$defs/shape` | F | 2 |
| MOCK-209 | C-12, C-09 | ADR-019 | annex §4; `scenario.schema.json#/$defs/switches` (`omitResultType`, `omitServerInfoMeta`) | F | **1 (209.1–209.4 — delivered, DEF-209), 2 (209.5 per-method omission)** |
| MOCK-210 | C-10, C-26 | ADR-012 | annex §2.10 | F, P | 2 |
| MOCK-211 | C-10, C-12 | ADR-019 | annex §2.8 | F | 2 |
| MOCK-212 | C-10, C-23, C-17 | ADR-006 | `library-api.md §6`; **`builtin-tools.md §3.4` — the `sleep` behaviour is the Phase 1 vehicle for cancellation (202.16)** | F | **1 (via `sleep`: 212.1, 212.3, 212.4, 212.5, and 212.2's "no further frames" clause), 2 (breadth across Phase 2 methods), 9 (212.2's "in-progress fault sleep is aborted" clause — needs the fault engine, C-18)** — *corrected by AMEND-10: the previous label claimed all five criteria in Phase 1 and simultaneously "2 (full)", which cannot both hold; 212.2 is not completable before Phase 9* |
| MOCK-221 | C-15, C-06 | ADR-002, ADR-004 | `$defs/generated` | U, F, P | 3 |
| MOCK-222 | C-15, C-02 | ADR-004, ADR-008 | `$defs/authoredItem` | F | 1 (partial), 3 |
| MOCK-223 | C-15, C-12 | ADR-004 | `$defs/edgeCases.names` | U, F | 3 |
| MOCK-224 | C-15 | ADR-008 | `$defs/edgeCases.headerAnnotations` | U, F | 3 |
| MOCK-225 | C-15, C-28 | ADR-013, ADR-018 | `$defs/edgeCases.schemas`; `security.md §7` | F, P | 3 |
| MOCK-226 | C-15, C-06, C-07 | ADR-002, ADR-003 | `catalogue.ordering` | F | 3 |
| MOCK-227 | C-15, C-25, C-26 | ADR-004, ADR-014 | `$defs/catalogue.drift`; control API `catalogue` | F, I | 3 |
| MOCK-228 | C-15, C-19 | ADR-004 | `requiredScopes` | F, P | 3 |
| MOCK-231 | C-16, C-14 | ADR-004, ADR-010 | `$defs/paging` | U, F | 2 |
| MOCK-232 | C-16, C-12 | ADR-019 | `$defs/paging.perPage` | F | 2 |
| MOCK-233 | C-15, C-25, C-26 | ADR-014 | `drift.emitNotification: false` | F | 3 |
| MOCK-241 | C-14 | ADR-010, ADR-019 | annex §7; `$defs/mrtr` | F | 5 |
| MOCK-242 | C-14, C-09 | ADR-019 | annex §7.3; `mrtr.inputRequests` | F | 5 |
| MOCK-243 | C-14, C-06 | **ADR-010** | `security.md §6`; annex §7.5 | U, F | 5 |
| MOCK-244 | C-14, C-10 | ADR-017 §5 | annex §7.1 | U, F | 5 |
| MOCK-245 | C-14, C-12 | ADR-019 | `switches.nonConformant` | F | 5 |
| MOCK-246 | C-14 | ADR-010 | `mrtr.reask` | F | 5 |
| MOCK-247 | C-14, C-17, C-04 | ADR-010 | `assertions-api.md §3.9` | F, U | 5 |
| MOCK-251 | C-10, C-23, C-22 | ADR-006 | annex §8 | F | 6 |
| MOCK-252 | C-10 | ADR-019 | `subscriptions.acceptedTypes` | F | 6 |
| MOCK-253 | C-26, C-24, C-15 | ADR-012 | control API `notifications` | F, I | 6 |
| MOCK-254 | C-23, C-10 | ADR-006, ADR-012 | control API `streams:close` | F, I | 6 |
| MOCK-255 | C-26, C-23 | **ADR-012** | `subscriptions.sse` | F, P | 6 |
| MOCK-256 | C-22, C-10 | ADR-006 | annex §8.8 | F, P | 6 |
| MOCK-257 | C-10 | — | `switches.nonConformant` | F | 6 |

### §3 Legacy era

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| MOCK-301 | C-13, C-23, C-06 | ADR-006, ADR-019 | `wire-legacy-2025.md`; `$defs/sessions` | F | 7 |
| MOCK-302 | C-13, C-10 (`Initiator`) | ADR-006 | `library-api.md` `InstanceControl.Call` | F, I | 7 |
| MOCK-303 | C-11, C-25 | ADR-014, ADR-019 | annex §9 era detection | F, I | 7 |
| MOCK-304 | C-11, C-23 | — | `$defs/sessions.probe` | F | 7 |
| MOCK-305 | C-13, C-24 | ADR-014 | control API `sessions:invalidate` | I | 7 |
| MOCK-306 | C-13, C-10 | — | `$defs/errorSpec` | F | 7 |

### §4 Authorization

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| MOCK-401 | C-19 | ADR-020 | `$defs/auth.mode` | F | 8 |
| MOCK-402 | C-19, C-23 | ADR-020 | `security.md §2.2` | F | 8 |
| MOCK-403 | C-19, C-21 | ADR-020 | `$defs/auth.resource` | F | 8 |
| MOCK-404 | C-19, C-15 | ADR-020 | `requiredScopes`, `auth.scopeMatch` | F | 8 |
| MOCK-405 | C-20, C-21 | **ADR-020** | `$defs/auth.embeddedServer` | I, E | 8 |
| MOCK-406 | C-19, C-20, C-24 | ADR-020 | control API `credentials:rotate` | F | 8 |
| MOCK-407 | C-17, C-19 | ADR-005 | `security.md §5`; `data-model.md §3` | F | 8 |

### §5 Faults

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| §5 preamble | C-18 | **ADR-009** | `$defs/faultRule` | F | 9 |
| MOCK-501 | C-18, C-26, C-06 | ADR-002, ADR-009 | `$defs/latencyAction` | F, P | 9 |
| MOCK-502 | C-18, C-23, C-29 | ADR-009, ADR-012 | `$defs/transportAction` | **I** | 9 |
| MOCK-503 | C-18, C-10 | ADR-006, ADR-009 | `$defs/protocolAction` | F | 9 |
| MOCK-504 | C-18, C-10 | ADR-005, ADR-009 | `$defs/sizeAction` | F, P | 9 |
| MOCK-505 | C-18, C-08 | ADR-009 | `$defs/errorSpec`; annex §6 | F | 9 |
| MOCK-506 | C-18, C-15 | ADR-009, ADR-018 | `$defs/contentAction` | F | 9 |
| MOCK-507 | C-30, C-18 | **ADR-018** | `$defs/corpusAction`; `security.md §1` | U, F, E | 9 |
| MOCK-508 | C-18, C-05, C-01 | ADR-009 | `$defs/restartAction` | **I, E** | 9 |

### §6 Journal and assertions

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| MOCK-601 | C-17, C-03 | **ADR-005** | `data-model.md §3` | F | 1 |
| MOCK-602 | C-17, C-24 | ADR-005, ADR-015 | control API `journal` | I | 1 |
| MOCK-603 | C-04, C-03 | ADR-001, ADR-017 | **`assertions-api.md`** | U | 1 (3 of 8), 10 (all) |
| MOCK-604 | C-04, C-07 | ADR-003, ADR-017 | `assertions-api.md §4` | U | 10 |
| MOCK-605 | C-14, C-17, C-04 | ADR-010 | `Correlation` in both contracts | I, U | 5 |
| MOCK-606 | C-15, C-17 | ADR-013 | `data-model.md` `ValidationPart` | F, P | 3 |

### §7 Configuration and control

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| MOCK-701 | C-02, C-28, C-05 | **ADR-008** | **`scenario.schema.json`** | U | 1 |
| MOCK-702 | C-24, C-25 | ADR-014, ADR-015 | `control-api.openapi.yaml` | I, P | 1 (subset), 4 (full) |
| MOCK-703 | C-28, C-02 | ADR-008 | `$defs/fleetSpec`, `extends` | U | 1 |
| MOCK-704 | C-06, C-05, C-27 | **ADR-002** | `observability.md §4.3` | U, F | 1 |
| MOCK-705 | `scenarios/` | ADR-008 | `docs/scenarios.md` | F | 11 |
| MOCK-706 | C-31 (extended) | ADR-017 | — | I | 12 (optional) |

### §9 Performance

| REQ | Components | ADRs | Contract | Level | Phase |
|---|---|---|---|---|---|
| MOCK-901 | C-23, C-10, C-27 | ADR-012, ADR-014, ADR-016 | `requirements-spec.md §9` conditions | **P** | 1 (smoke), 11 (gated) |
| MOCK-902 | C-17 | **ADR-005** | same | **P** | 10, 11 |
| MOCK-903 | C-23, C-26 | **ADR-012** | same; `deployment.md §5` | **P** | 6, 11 |
| MOCK-904 | C-25, C-15 | ADR-004, ADR-007 | same | **P** | 4, 11 |

### §10 Deliverables

| DEL | Components | Level | Phase | Status |
|---|---|---|---|---|
| DEL-1 module + CLI, semver | C-01…C-05 | E | 11 | planned |
| DEL-2 image + SBOM + **Cosign** | `deployment.md §2`, §7.6 | E | 11 | ✅ **Cosign present — status corrected by AMEND-10.** `ci.yml:759-771`: `sigstore/cosign-installer`, `cosign sign --yes` (keyless/OIDC) and `cosign attest --predicate sbom.spdx.json --type spdxjson`, on the image digest. The previous "must be added" was stale. |
| DEL-3 scenario library + JSON Schema | `scenarios/`, C-02 | F | 11 | planned |
| DEL-4 assertion helpers | C-04 | E | 10 | planned |
| DEL-5 hub conformance suite | — | — | — | ⚪ **BLOCKED — GAP-004**, external |
| DEL-6 documentation + hub mapping table | docs | — | 11 | partial; mapping table blocked with DEL-5 |

---

## 3. Bidirectional completeness check

### 3.1 Requirements with no component — **none**

All 81 numbered `MOCK-nnn` requirements plus the §5 preamble appear exactly once in §2 above.

Count check: §1 = 7, §2 = 12, §2.1 = 8, §2.2 = 3, §2.3 = 7, §2.4 = 7, §3 = 6, §4 = 7,
§5 = 8 (+preamble), §6 = 6, §7 = 6, §9 = 4. **Total 81 + 1 preamble + 6 deliverables.**

### 3.2 Components with no requirement — **none**

| Component | Justifying requirement(s) |
|---|---|
| C-06 determinism | MOCK-704, §0.1 |
| C-07 ordered | §0.1 via MOCK-704, MOCK-604 |
| C-08 jsonrpc | MOCK-204, MOCK-503, MOCK-505 |
| C-09 wire | all of §2/§3 |
| C-11 era | MOCK-303, MOCK-304 |
| C-26 timer wheel | MOCK-210, MOCK-227, MOCK-253, MOCK-255 |
| C-28 config | MOCK-701, MOCK-703 |
| C-29 certs | MOCK-106, MOCK-502 |
| C-30 corpus | MOCK-507 |
| C-31 test client | ADR-017 (test infrastructure; justified by the correctness of §2/§3 rather than by a single requirement — **flagged as the one component whose justification is architectural, not requirement-driven**) |

### 3.3 ADRs with no requirement — **none**

ADR-001↔MOCK-107/101 · ADR-002↔MOCK-704 · ADR-003↔§0.1/MOCK-604 · ADR-004↔MOCK-221/904 ·
ADR-005↔MOCK-601/902 · ADR-006↔MOCK-102 · ADR-007↔MOCK-103/107 · ADR-008↔MOCK-701/703 ·
ADR-009↔§5 · ADR-010↔MOCK-243 · ADR-011↔MOCK-102/105 · ADR-012↔MOCK-901/903 ·
ADR-013↔MOCK-101/107 · ADR-014↔MOCK-702 · ADR-015↔MOCK-104 · ADR-016↔MOCK-105 ·
ADR-017↔§10.1 correctness · ADR-018↔MOCK-507 · ADR-019↔§2/§3 · ADR-020↔MOCK-405.

### 3.4 Contracts with no requirement — **none**

`library-api.md`↔MOCK-107 · `assertions-api.md`↔MOCK-603 ·
`control-api.openapi.yaml`↔MOCK-104/702 · `scenario.schema.json`↔MOCK-701/§10.3 ·
`wire-2026-07-28.md`↔§2 (ADR-019) · **`wire-2026-07-28.schema.json`↔MOCK-201.4/202/203 (AMEND-4)** ·
**`builtin-tools.md`↔MOCK-202/212/§8 (AMEND-2)**.

---

## 4. §11 coverage matrix — reproduced with verification status

`requirements.md` §11 maps hub areas to mock capabilities. **The `HUB-nnn` document is not in this
repository (GAP-004), so every hub-side reference below is `[unverified — external document]` and
no `HUB-nnn` semantics are invented anywhere in this specification.** The mock-side column is
fully traced by §2 above.

| Hub area | Hub reqs | Mock capabilities | Mock side traced? |
|---|---|---|---|
| Statelessness / no sessions | HUB-103…109, 501 `[unverified]` | MOCK-207, 301, 903 | ✅ |
| Per-request `_meta` | HUB-121…128 `[unverified]` | MOCK-203, 205, 206, 603 | ✅ |
| Header mirroring | HUB-141…148 `[unverified]` | MOCK-204, 223, 224, 603 | ✅ |
| Aggregation & namespacing | HUB-161…169 `[unverified]` | MOCK-221…228, 103, 904 | ✅ |
| Caching | HUB-181…186 `[unverified]` | MOCK-231…233 | ✅ |
| MRTR | HUB-201…209 `[unverified]` | MOCK-241…247, 605 | ✅ |
| Subscriptions | HUB-221…229 `[unverified]` | MOCK-251…257 | ✅ |
| Cancellation & timeouts | HUB-241…244 `[unverified]` | MOCK-210, 212, 501, 502 | ✅ |
| Authorization | HUB-301…310 `[unverified]` | MOCK-401…407 | ✅ |
| Security / untrusted content | HUB-401…410 `[unverified]` | MOCK-225, 505, 506, 507 | ✅ |
| Degraded operation | HUB-167, 504 `[unverified]` | MOCK-501, 502, 508 | ✅ |
| Era bridging | HUB-701…724 `[unverified]` | MOCK-301…306 | ✅ |
| Extensions | HUB-801…804 `[unverified]` | MOCK-201 (`extensions`), scenario library | ✅ |

`MOCK-245` cites "per HUB-206" in `requirements.md`. That requirement's content is unknown; the
mock-side behaviour (refuse in conformant mode, emit under a named non-conformant switch) is
implemented from `MOCK-245`'s own text alone.

---

## 5. Coverage by phase

| Phase | Requirements fully covered | Cumulative |
|---|---|---|
| 1 | 101, 102, 105, 107, 203, 601, 602, 701, 703, 704 | 10 / 81 |
| 2 | 201, 202, 204, 205, 206, 207, 208, 209, 210, 211, 231, 232 | 22 |
| 3 | 221, 222, 223, 224, 225, 226, 227, 228, 233, 606 | 32 |
| 4 | 103, 104, 106, 702, 904 | 37 |
| 5 | 241, 242, 243, 244, 245, 246, 247, 605 | 45 |
| 6 | 251, 252, 253, 254, 255, 256, 257, 903 | 53 |
| 7 | 301, 302, 303, 304, 305, 306 | 59 |
| 8 | 401, 402, 403, 404, 405, 406, 407 | 66 |
| 9 | 501, 502, 503, 504, 505, 506, 507, 508, **212** (+§5 preamble) | 75 |
| 10 | 603, 604, 902 | 78 |
| 11 | 705, 901 | 80 |
| 12 (optional) | 706 | 81 |

Phases 1–11 deliver **80 of 81** numbered requirements; `MOCK-706` (a SHOULD) is the only
deliberate deferral, per GAP-012.

**AMEND-10 changes to this table.** `MOCK-212` moved from Phase 2 to **Phase 9**. This moves no
work: `212.1`, `212.3`, `212.4`, `212.5` and half of `212.2` are already delivered in Phase 1 (see
§2). It records that `212.2`'s second clause — *"any in-progress **fault** sleep is aborted"* —
cannot be verified until the fault engine (C-18) exists in Phase 9, so Phase 2 was never the point
at which `MOCK-212` became fully covered. Cumulative counts for Phases 2–8 each drop by one and
reconcile back to 75 at Phase 9; the totals are unchanged.

`MOCK-209` **stays in Phase 2 here, and that is correct** — `209.5` (per-method omission) is
outstanding. Only §2's label was wrong; it hid the Phase 1 delivery of `209.1`–`209.4`.
