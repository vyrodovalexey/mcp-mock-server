---
id: ADR-002
title: Determinism via hierarchical seed derivation and content-addressed per-request RNG
status: accepted (amended 2026-09-04 — AMEND-8: `credhash` removed from the seed tree; credential hashing is a named exception to seed-determinism)
date: 2026-09-04
reversibility: HARD — changing the derivation changes every golden file and every recorded seed
requirements: §0.1, MOCK-704, MOCK-208, MOCK-221, MOCK-226, MOCK-501, MOCK-407
---

# ADR-002 — Determinism via hierarchical seed derivation

## Context

§0.1 is the first design principle: *same config + same seed ⇒ byte-identical responses*, and
*every source of nondeterminism (ordering, ids, timestamps, jitter) MUST be seeded and
reproducible*. `MOCK-704` requires a single `--seed` flag and that the effective seed be printed
at startup.

The naive implementation — one `*rand.Rand` guarded by a mutex, shared by the process — fails
twice. It is a contended global lock on a 20 000 rps path (`MOCK-901`), and it is **not
deterministic anyway**: the sequence a given request draws from depends on how many other
requests drew before it, which depends on goroutine scheduling.

Deterministic randomness must therefore not depend on execution order at all.

## Decision

A **seed tree** with content-addressed leaves. `internal/determinism` exposes:

```
type Key [32]byte

func Root(seed uint64) Key                       // HMAC-SHA256(seed_be8, "mcpmock/v1/root")
func (k Key) Derive(domain string, parts ...[]byte) Key   // HMAC-SHA256(k, domain || 0x00 || parts...)
func (k Key) RNG() *rand.Rand                    // rand.New(rand.NewChaCha8(k)) — math/rand/v2
```

Derivation path for a request:

```
root        = Root(seed)
instanceKey = root.Derive("instance", []byte(instanceName))
requestKey  = instanceKey.Derive("request",
                  []byte(method),
                  canonicalJSONRPCID,          // raw bytes as received
                  sha256(canonicalBody))       // RFC 8785-style canonical JSON
rng         = requestKey.RNG()
```

Sub-domains used elsewhere, each a stable string constant in `internal/determinism/domains.go`:
`"catalogue"`, `"cursor"`, `"requeststate"`, `"eventid"`, `"sessionid"`, `"subscriptionid"`,
`"jitter"`, `"shape"` (JSON vs SSE, `MOCK-208`), `"ordering"` (`MOCK-226`), `"astoken"`.

> **`"credhash"` was removed from this list by AMEND-8 (2026-09-04).** It is a named exception —
> see *Named exceptions to seed-determinism*, below. Do not re-add it.

### Named exceptions to seed-determinism  (added by AMEND-8, 2026-09-04)

Seed-determinism is a **convenience** property: it buys reproducible debugging and stable golden
files. It is not a security property, and the seed is **deliberately public** — `MOCK-704` requires
it printed at startup, `GET /v1/seed` serves it, and `mcpmock_effective_seed` exposes it on the
metrics listener (`0.0.0.0:9090` by default). Anything derived from the seed is therefore derivable
by anyone who can read a CI log.

That is harmless for every domain listed above, because each produces *mock output* — a catalogue
name, a cursor, an event id. Predicting them is not a capability an attacker gains anything from;
they are already in the response body.

It is **not** harmless for a key whose entire purpose is to make a value non-reversible. The
following domain is therefore excluded from the seed tree by name:

| Excluded | Key source instead | Why the exception exists |
|---|---|---|
| **Journal credential hashing** (`MOCK-407`, `security.md §5`) — the HMAC key that hashes a presented `Authorization` value into `journalapi.CredentialPart.Hash` | **`crypto/rand`, per process, always.** Never seed-derived, and there is deliberately **no** `deterministicKeys`-style escape hatch for this key. | The keyed hash exists so that a journal export — a CI artifact, per `security.md §9` — is not a dictionary-attack target for a credential a hub accidentally sent to the mock. A seed-derived key makes `HMAC(k, candidate)` computable by anyone holding the export and the (published) seed, which voids the only structural protection the journal claims for credentials. A security property beats a convenience property. |

**Why this costs nothing.** `MOCK-407`'s assertion — *"the hub minted a distinct upstream
credential"* — is an **equality** assertion between records **within one run**. A per-process random
key preserves equality and distinctness perfectly; only *cross-run* stability is lost, and nothing
in `MOCK-407`, `MOCK-601` or `assertions-api.md` needs it. The one thing that would need it is a
golden file containing a literal hash — so `credential.hash` is specified as a **volatile field**,
normalised out of golden comparison alongside `wallTime`, `monoNs`, `durationNs`, `seq` and `peer`
(`test-strategy.md`, `assertions-api.md §4`). A hash is evidence, never an input.

**The rule this generalises to.** A seed-derived value may be *observed* by an attacker but must
never be *relied upon to be unguessable*. Any future domain that fails that test is an exception and
must be added to the table above with its reasoning — not silently derived because the domain list
was the convenient place to put it.

Three rules make this hold:

1. **Order-independence.** Two concurrent requests with identical bytes get identical streams.
   Two different requests never share a stream. Scheduling is irrelevant.
2. **Fixed draw order.** Within a request, `rng` is consumed in the pipeline order of
   `architecture.md §5`. New random decisions are **appended** to the end of a stage's draws,
   never inserted, or all downstream golden files shift. Each stage documents its draw count.
   A unit test asserts the per-stage draw count for a fixed request.
3. **No wall-clock in bodies.** `determinism.VirtualClock` derives a `time.Time` from
   `requestKey.Derive("clock")` plus a scenario-configured epoch. `time.Now()` is permitted for
   journal wall timestamps, metrics and logs only — never for a value serialised into a response.

Seed handling: `--seed` is `uint64`. If absent, a cryptographically random seed is generated,
**printed at startup** on stderr as `{"msg":"effective seed","seed":<n>}` and exposed at
`GET /v1/seed` and as the Prometheus gauge `mcpmock_effective_seed`. This satisfies `MOCK-704`
and makes a flaky CI run reproducible by copying one number out of the log.

## Options considered

1. **Single mutex-guarded global `rand.Rand`** — rejected: contended, and not deterministic under
   concurrency (see Context).
2. **Per-connection RNG seeded from a counter** — rejected: connection assignment order is
   nondeterministic under HTTP keep-alive and connection pooling in the hub.
3. **Per-request RNG seeded from a monotonic request counter** — rejected: the counter value a
   request receives depends on arrival interleaving, so replaying the same test can assign
   different counters. This is the subtle trap; it *looks* deterministic in single-threaded tests.
4. **Content-addressed derivation (chosen).** Cost: one HMAC-SHA256 + one SHA-256 of the body per
   request (~1 µs for a 1 KB body on modern hardware), incurred only when a random decision is
   actually needed — `rng` is created lazily via `sync.OnceValue` per request.
5. `crypto/rand` everywhere — rejected: not reproducible, which is the whole point.

## Consequences

**Positive.** Determinism survives concurrency, `GOMAXPROCS` changes, and connection reuse.
Debugging is tractable: seed + request bytes fully determine behaviour. `MOCK-226`
(explicitly *non*-deterministic ordering, to prove the hub does not depend on upstream order) is
implemented as a *different* seeded permutation per call — derived from
`requestKey.Derive("ordering", callOrdinal)` — so it is nondeterministic *from the hub's
viewpoint* while still reproducible for us. That is the only honest way to satisfy both §0.1 and
`MOCK-226`.

**Negative.** Body hashing costs CPU on the hot path. Mitigated by laziness and by the fact that
`MOCK-901`'s trivial-handler benchmark makes no random decisions. Golden files are coupled to the
derivation constants; changing a domain string is a breaking change and must bump the scenario
`apiVersion`.

**Negative (AMEND-8).** Determinism is no longer total: `journalapi.CredentialPart.Hash` varies
between runs of the same seed. This is intentional and specified, but it means "same config + same
seed ⇒ byte-identical responses" (§0.1) holds for **responses**, while the **journal** has one
documented volatile field beyond the already-volatile timing fields. Golden comparison must
normalise it. The alternative — a publicly derivable credential-hash key — was rejected as a
security defect; see *Named exceptions to seed-determinism*.

**Known limits, routed to gap analysis.**
- Count-based fault triggers (`MOCK-501`…`508`, "count") use atomic counters whose assignment
  *is* order-dependent. This is a genuine residual nondeterminism — GAP-007.
- Injected latency (`MOCK-501`) makes *timing* nondeterministic. §0.1's "byte-identical
  responses" is read as applying to response bytes, not to wall-clock — GAP-008.

**Forecloses.** Using an off-the-shelf RNG-based fuzz library that seeds itself.
