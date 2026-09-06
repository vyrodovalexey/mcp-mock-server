---
title: mcpmock — Test Strategy
status: draft
version: 0.1.0
updated: 2026-09-04
adr: ADR-017 (anti-circularity), ADR-003 (determinism testing)
ci: .github/workflows/ci.yml
---

# Test Strategy

## 0. The recursion problem, stated plainly

**mcpmock is a test tool. Its own tests cannot be circular.**

The trap is specific and easy to fall into: use mcpmock's encoder to produce a response, then use
mcpmock's decoder to assert the response is correct. That test passes when both are wrong in the
same way — which is the *normal* case, because one person wrote both from one reading of one
sentence.

The trap is worse here than usual, for a reason worth stating: the primary protocol revision
(`2026-07-28`) is **not publicly specified** (ADR-019, GAP-003). There is no external conformance
suite to fall back on and no reference implementation to differential-test against. The only
oracles available are:

1. `requirements.md`, read by a human;
2. the wire annex we author from it, reviewed by a human;
3. properties that are true independently of any implementation.

Everything below is built on those three.

### 0.1 The five anti-circularity mechanisms

| # | Mechanism | Breaks which circularity | Cost |
|---|---|---|---|
| 1 | **`test/mcpclient`** — an independent MCP client, stdlib-only, importing nothing from `internal/`, written from the annex | Server encoder ↔ server decoder | ~1500 lines duplicated |
| 2 | **Golden fixtures authored from requirement text**, named by `MOCK-nnn`, never regenerated from the code | Implementation defining its own correctness | Manual maintenance |
| 3 | **Round-trip and metamorphic properties** | Both of the above sharing a misreading | Cheap; high value |
| 4 | **Cross-implementation check on the legacy eras** with a third-party MCP SDK | A misreading shared by author and reviewer | Optional, network-gated |
| 5 | **Self-check** (`MOCK-244` generalised): outgoing responses validated against the annex schema | Handler drifting from the annex | Runtime cost; off in perf runs |

**Enforced rules:**
- `make deps-check` fails if `test/mcpclient` imports any `internal/` package or any third-party
  module. It re-declares its own error codes, header names and sentinel codec.
- `make golden-update` **does not exist**. Golden files are edited by hand with a reviewer.
  `MCPMOCK_UPDATE_GOLDEN=1` exists for the *assertion library's* goldens used by hub tests, not
  for mcpmock's own wire fixtures — these are different directories and the distinction is
  documented.
- Where one person writes both sides, the ordering rule applies: **write the client first**, from
  the annex, before the corresponding server handler exists. This is a task-ordering constraint in
  `implementation-plan.md`, not a good intention.

### 0.2 Residual risk, stated

If the wire annex is a misreading of the real `2026-07-28` specification, **every mechanism above
will agree with the misreading.** No amount of testing fixes that; only ratification does
(ADR-019). This is why GAP-003 blocks Phase 2 rather than being a documentation note.

---

## 1. Levels, and what each proves

CI build tags are already fixed by the existing pipeline (`ci.yml:126`, `:222`, `:281`) and are
adopted unchanged.

| Level | Tag / path | Runs against | Proves | Budget |
|---|---|---|---|---|
| **Unit** | `./...`, no tag | pure functions, single structs | Algorithms: seed derivation, ordering, ring buffer, AEAD, glob selectors, merge semantics, catalogue generation, sentinel codec, JWS, assertion logic | < 60 s, `-race` |
| **Functional** | `-tags=functional ./test/functional/...` | an in-process `mcpmock.Server` driven by `test/mcpclient` | Protocol behaviour end to end without a network hop: every §2/§3 requirement, most of §5/§6 | < 5 min, `-race` |
| **Integration** | `-tags=integration ./test/integration/...` | a **real process**, real sockets, TLS/mTLS, control API over HTTP and unix socket | Things in-process tests structurally cannot reach: TLS, connection resets, accept refusal, process exit, unix socket control, CLI parity | < 10 min |
| **E2E** | `-tags=e2e ./test/e2e/...` | container image on Kubernetes `docker-desktop` / `mcpmock-test` | Deliverable reality: image, Helm chart, manifests, NetworkPolicy, rolling restart, embedded external-module import | < 15 min |
| **Performance** | `./test/perf/` (k6 + Go benchmarks) | a real process, isolated CPUs | §9 targets | separate job, tag-gated |

**Important boundary:** functional tests use the library API in-process. That is fast and
`-race`-clean, but it means anything about *sockets* is not proven there. `MOCK-502`, `MOCK-106`
and `MOCK-508` are therefore **integration-level requirements**, and the traceability matrix says
so rather than letting a fast in-process test create false confidence.

`.golangci.yml:5` sets `run.tests: false`, so test files are not linted. That is a deliberate
pre-existing convention; test code quality is maintained by review, and `testpackage`
(`.golangci.yml:48`) still applies to non-test files' package structure.

---

## 2. Unit level

Responsible for everything expressible as a pure function. This is where determinism, crypto and
data structures are actually proven.

| Package | Key tests |
|---|---|
| `determinism` | Derivation stability across runs/processes/architectures; `Key.Derive` domain separation (different domains never collide); draw-count-per-stage assertions (ADR-002 rule 2); `VirtualClock` reproducibility |
| `ordered` | Iteration order stability; sorted-key correctness; no map leakage |
| `jsonrpc` | Canonical encoding (RFC 8785-style) golden vectors; raw-id preservation (`1` vs `"1"` vs `1.0`) |
| `journal` | Ring wrap-around; seqlock torn-read retry under a hammering concurrent writer; `Seq` totality; drop accounting; overflow policies incl. block-timeout degradation; `-race` at 8 writers |
| `mrtr` (AEAD) | `Open(Seal(p)) == p`; **every single-bit flip** in a sample token fails `Open`; nonce uniqueness across a simulated restart; each of the eight rejection reasons; TTL boundary |
| `catalogue` | `IndexOf(At(i).Name) == i` for all `i`; golden items at `i ∈ {0,1,2499,4999}`; overlay replacement; drift application; scope filtering; memo bound |
| `paging` | Concatenated pages = full list, exactly once; cursor opacity; cross-instance rejection; overlap/skip faults produce the *exact* configured corruption |
| `fault` | Selector algebra truth table; glob compilation; trigger semantics; composition order; `stopPropagation`; bloom fast path |
| `config` | Every ADR-008 merge-table row (table-driven); cycle detection; `null` deletion; keyed-list merge; path-traversal rejection; schema/struct agreement (`make schema-check`) |
| `jwtmini` | RFC 7515 A.2/A.3 test vectors; `alg: none` rejected on verify; alg↔key-type mismatch rejected **before** signature check; every named malformation produces the intended bytes |
| `authz` | Audience matching (string and array); scope matching; challenge header construction (single, well-formed) |
| `assert` | Each of the eight assertions: positive, negative, message content, **and a secret-leak scan** |
| `sentinel codec` | Round-trip over ASCII, non-ASCII, astral, 128-char, 129-char, and names literally matching the sentinel pattern |

### 2.1 Property tests

Run with `testing/quick` or a small in-repo generator; deterministic seeds recorded on failure.

- `sentinelDecode(sentinelEncode(n)) == n` for all `n`.
- Pagination concatenation identity.
- Journal: for a serialised workload, `Seq` order equals send order.
- AEAD: any mutation of the token fails; `Open` never returns wrong plaintext.
- Catalogue: name↔index bijection.
- Merge: `merge(a, {}) == a`; `merge(a, a) == a` (idempotence); associativity where defined.

---

## 3. Functional level

Driven **only** through `test/mcpclient`. One test file per requirement group, named
`mock_2xx_test.go` etc., with the requirement id in each test name so a failure names the
requirement:

```
TestMOCK204_HeaderMismatch_NumericComparison
TestMOCK243_RequestState_CrossPrincipalRejected
TestMOCK256_Stdio_InterleavedSubscriptions
```

This naming is not cosmetic — it is what makes the per-requirement verdict reporting in
`implementation-plan.md` mechanical rather than a judgement call.

### 3.1 The determinism suite (PRIN-1)

The load-bearing test of the whole product.

```
for each scenario in scenarios/:
    run A: fresh Server, seed=S, GOMAXPROCS=1, fixed request script
    run B: fresh Server, seed=S, GOMAXPROCS=1, same script
    run C: fresh Server, seed=S, GOMAXPROCS=8, same script
    run D: separate process, seed=S, same script
    assert bytes(A.responses) == bytes(B) == bytes(C) == bytes(D)
    assert redact(A.journal) == redact(B) == redact(C) == redact(D)
run with -count=20
```

Runs at `-count=20` because a two-key map has a 50% chance of matching by luck in one run.
Redaction removes only wall-clock, monotonic, duration and peer fields.

**Known exclusions**, asserted separately rather than hidden: scenarios using count-based fault
triggers under concurrency (GAP-007) run only at `GOMAXPROCS=1` unless `determinism.strict` is set;
latency-injecting scenarios compare bytes but not timing (GAP-008).

### 3.2 Coverage of §2/§3

Every `MOCK-2xx`/`MOCK-3xx` acceptance criterion in `requirements-spec.md` maps to at least one
functional test. Non-conformant switches are tested in **pairs**: conformant behaviour, then the
named switch, then a diff showing exactly what changed. A switch with no paired test is a switch
nobody can trust.

### 3.3 Journal and assertion coverage

For each of the eight assertions: a scenario that satisfies it, a scenario that violates it, and a
check that the failure message names the offending `Seq` and contains no secret.

---

## 4. Integration level

Real process, real sockets. Everything the in-process form cannot prove.

| Area | Tests |
|---|---|
| TLS/mTLS (`MOCK-106`) | Each `clientAuth` mode; missing client cert rejected at handshake; client-cert evidence in the journal; min-version enforcement; per-instance policy via SNI |
| Transport faults (`MOCK-502`) | Client observes `ECONNRESET`, half-close, truncation at the exact byte, `ECONNREFUSED` on accept refusal, each TLS handshake failure mode |
| Restart (`MOCK-508`) | stdio process exit with in-flight requests; journal flushed before exit; `hangShutdown` holds streams past SIGTERM |
| Control API (`MOCK-104`, `MOCK-702`) | Every route; error catalogue conformance; non-loopback bind refused without a token; unix socket in stdio mode; **CLI parity** (`mcpmock ctl` vs HTTP for every operation) |
| Embedded AS (`MOCK-405`) | Full token flow with no external IdP and no egress; every malformation; JWKS rotation |
| Journal export | NDJSON streaming; `?follow=true`; `assert.FromFile` round trip |
| Startup budget | p95 over 50 real process starts (`MOCK-107.1`) |
| Cross-check (optional, network-gated) | A third-party MCP client SDK against the **legacy** endpoint (ADR-017 §4) |

The existing `integration-tests` job (`ci.yml:149`) provisions Vault and Keycloak for a different
project. **mcpmock needs neither** — `MOCK-405` exists precisely so end-to-end auth tests have no
external dependency. Those service blocks are removed (`deployment.md §7`), which also makes the
job faster and less flaky.

---

## 5. E2E level

On `docker-desktop`, namespace `mcpmock-test` — the only authorised environment.

| Test | Proves |
|---|---|
| Image starts, no shell present, runs as 65532 with read-only rootfs | `MOCK-101.3/.4` |
| `helm lint` / `template` / `install --wait` reaches Ready | `MOCK-101.5` |
| Plain manifests apply and reach Ready | `MOCK-101.6` |
| Deny-all-egress NetworkPolicy, mock still serves | `MOCK-101.8` |
| Helm **refuses** to render hostile mode without a NetworkPolicy | `MOCK-507.7` |
| Rolling restart behind a Service while a client is connected | `MOCK-508.2` |
| **External-module import**: a throwaway module in `test/e2e/embed/` with its own `go.mod` requires `mcpmock` via a `replace`, imports `mcpmock` + `assert`, and runs a test | `MOCK-107.3` — the only test that proves the module is genuinely importable |
| Scrape `/metrics`, validate with `promtool` | `MOCK-105.1` |

Cleanup is unconditional (`t.Cleanup` + `helm uninstall` + namespace delete) and every e2e test
verifies its namespace is `mcpmock-test` before acting.

---

## 6. Performance level

k6 for HTTP/SSE load; Go benchmarks for internals. Conditions are fixed in
`requirements-spec.md §9` and repeated in every report so a number is never quoted without them.

| Test | Target | Tool |
|---|---|---|
| `perf/tools_call_nojournal.js` | `MOCK-901` ≥ 20 000 rps, p99 < 25 ms | k6 |
| `perf/tools_call_journal.js` | `MOCK-902` ≥ 5000 rps, p99 < 50 ms, bounded RSS | k6 |
| `perf/sse_streams.js` | `MOCK-903` 20 000 streams, 5 min, 0 drops, RSS ≤ 1.5 GiB | k6 |
| `perf/fleet_startup_test.go` | `MOCK-904` 200 instances, `MOCK-107` p95 < 200 ms, RSS ≤ 512 MiB | Go |
| `BenchmarkJournalWrite` | allocation-free write; < 200 ns | Go |
| `BenchmarkRequestPipeline` | ≤ 12 allocs/request | Go |
| `BenchmarkMetricRecord` | < 50 ns | Go |
| `BenchmarkCatalogueAt` | O(1), one alloc | Go |
| `perf/goroutine_budget_test.go` | `≈ 2×streams + constant`, no per-stream timer | Go |

**The early-falsification rule.** `MOCK-901` and `MOCK-903` rest on estimates in ADR-012, not
measurements. A rough throughput smoke test is scheduled in **Phase 1**, not at the end, so that
if stdlib `net/http` cannot reach the numbers we learn it while the architecture is still cheap to
change. This is deliberate and is called out in the implementation plan.

Perf jobs run on tags and on demand, never on every PR (they are noisy on shared runners), and
publish results as artifacts.

---

## 7. Formal methods — considered, deferred

The two genuine state machines are the MRTR chain (`MOCK-241`…`247`) and the legacy session
(`MOCK-301`, `MOCK-305`). Both have a plausible TLA+ / Alloy model that would prove absence of
states like "chain accepted after expiry" or "session valid after DELETE".

**Deferred, not rejected.** Revisit if defects cluster there. The interim substitute is
`stateDiagram-v2` models in `data-model.md §5` plus exhaustive table-driven transition tests
generated from those diagrams.

---

## 8. Coverage policy

The existing pipeline uploads coverage from four levels and merges them in SonarCloud
(`ci.yml:338`–`:364`). That structure is kept.

| Package group | Line coverage floor | Rationale |
|---|---|---|
| `internal/determinism`, `internal/ordered`, `internal/journal`, `internal/mrtr`, `internal/jwtmini`, `internal/paging` | **90%** | Correctness here is load-bearing for everything else and is cheaply testable. |
| `internal/catalogue`, `internal/fault`, `internal/config`, `internal/authz` | 80% | |
| `internal/modern`, `internal/legacy`, `internal/transport` | 70% (functional-level coverage counts) | Much of this is wire assembly, better covered by golden fixtures than by line coverage. |
| `assert`, `journalapi`, `scenario`, `mcpmock` | 85% | Public contracts. |
| `cmd/` | 50% | Thin. |

Coverage is a floor, not a goal. A high number here does not mean the tool is correct — §0 does.

---

## 9. Definition of done

A requirement is **done** when all of the following hold. Anything short of all of them is
reported as `PARTIAL` or `NOT-IMPLEMENTED`, honestly.

1. Every acceptance criterion in `requirements-spec.md` for that `MOCK-nnn` has a passing test at
   the level stated in `traceability.md`.
2. The test name contains the requirement id.
3. `golangci-lint run` passes with the repository's existing `.golangci.yml`, no new exclusions.
4. `go test -race` passes at unit and functional level.
5. `govulncheck ./...` reports nothing actionable.
6. The behaviour is reachable from a **named scenario key or fault id** — not only from Go code.
7. New wire behaviour is recorded in the annex with a `[R]`/`[D]`/`[P]` label.
8. New nondeterminism, if any, is either seeded or documented in the known-exceptions list.
9. The scenario JSON Schema covers any new configuration, with `additionalProperties: false`.
10. Docs updated: fault catalogue, scenario reference, or `CHANGELOG.md` as applicable.

**Phase-level done** additionally requires: the determinism suite green at `-count=20`, the
`MOCK-107` startup gate green, and the e2e Helm install green on `docker-desktop`/`mcpmock-test`.

---

## 10. Test data and environments

| Need | Source |
|---|---|
| Scenarios | `scenarios/` (`MOCK-705`), composed from `base.yaml` |
| Golden wire fixtures | `test/golden/`, hand-authored, named by `MOCK-nnn` |
| Certificates | Generated at runtime by `internal/certs`. **No PEM is committed** (`security.md §3`); `make secrets-check` enforces it |
| Tokens | Minted by the embedded AS, or fixture strings clearly marked `test-only-…` |
| Hostile corpus | `internal/corpus`, gated (ADR-018). Excluded from the default CI sweep |
| Kubernetes | `docker-desktop` context, `mcpmock-test` namespace, **only** |
| Load generation | k6 (present in the environment) |
| Mocks/doubles | `go.uber.org/mock` (`mockgen`) — **not currently installed**; `make tools` installs it. Interfaces exist only at genuine abstraction boundaries (`ResponseSink`, `Initiator`, `Clock`, `Catalogue`, `Control`) |
