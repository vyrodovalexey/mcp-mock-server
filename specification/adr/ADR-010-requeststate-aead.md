---
id: ADR-010
title: requestState is an AES-256-GCM AEAD token with HKDF-derived keys and counter nonces
status: accepted
date: 2026-09-04
reversibility: MEDIUM — token format is versioned and instance-scoped; no cross-version persistence
requirements: MOCK-243, MOCK-244, MOCK-246, MOCK-247, MOCK-605, MOCK-101
---

# ADR-010 — `requestState` AEAD construction

## Context

`MOCK-243`:

> MUST mint `requestState` as an AEAD-protected blob binding principal, request digest and TTL,
> and MUST verify it on retry: **tampered, expired, cross-principal and cross-request** state MUST
> be rejected with a **recorded, inspectable reason**.

The requirement names AEAD but not the construction. Four distinct rejection classes must be
distinguishable in the journal, which means verification must be staged, not a single boolean.

`MOCK-605` additionally requires grouping an initial request with its MRTR retries "via the
mock-side `requestState` identity" — so the token must carry a stable chain id that survives
across rounds while the rest of the payload changes.

`MOCK-101` ("no runtime dependencies") and ADR-013's dependency screen push hard toward stdlib.

## Decision

### Construction

**AES-256-GCM**, from `crypto/aes` + `crypto/cipher`. Keys derived with **HKDF-SHA256** from
`crypto/hkdf` (standard library as of Go 1.24; the target toolchain is Go 1.27.1 — **verify at
implementation time**, GAP-021). Zero third-party cryptography.

```
IKM        = instance root secret (32 bytes)
key        = HKDF-SHA256(IKM, salt = instanceID, info = "mcpmock/requestState/v1")   -> 32 bytes
noncePrefix= HKDF-SHA256(IKM, salt = instanceID, info = "mcpmock/requestState/nonce/v1")[0:4]
nonce      = noncePrefix (4B) || counter (8B, big-endian, atomic.Uint64)             -> 12 bytes
AAD        = "mcpmock/rs/v1" || instanceID || protocolVersion || method
plaintext  = canonical JSON of Claims (ADR-003 Canonical)
token      = "mrs1." || base64url_nopad( nonce || ciphertext||tag )
```

`Claims`:

```json
{
  "v":    1,
  "chain":"<16-byte random-per-chain id, hex>",   // MOCK-605 correlation identity, stable across rounds
  "inst": "upstream-a",
  "prin": "<sha256-hmac of the principal identity>",
  "rdig": "<sha256 of canonical initial request params>",
  "meth": "tools/call",
  "name": "search",
  "round": 1,
  "iat":  1767225600000,
  "exp":  1767225660000,
  "keys": ["confirm","creds"]                      // inputRequests keys outstanding
}
```

### Why a counter nonce

AES-GCM's catastrophic failure mode is nonce reuse. A 12-byte random nonce has a birthday bound
around 2^32 messages per key, which a 20 000 rps mock reaches in ~2.5 days of continuous MRTR
traffic — uncomfortably close. A `prefix || counter` nonce is reuse-free by construction, needs no
entropy at mint time (faster, and works in deterministic mode without consuming the DRNG), and the
counter is per-instance so instances cannot collide.

The counter resets on process restart, which *would* reuse nonces if the key survived a restart.
It does not: `IKM` is regenerated per process (`crypto/rand`) unless the scenario pins it. When
the scenario **does** pin it (deterministic mode, so golden files are stable), the nonce prefix is
additionally mixed with a per-process boot id, and a warning is logged that pinned-key mode is
test-only. This is documented in `security.md §6` and is not a real-world exposure — the tokens
protect nothing valuable — but the construction should still be correct, because engineers copy
patterns.

### Verification and rejection reasons (`MOCK-243`)

Staged, so each failure class is distinguishable:

| Stage | Failure | `reason` recorded | Wire response |
|---|---|---|---|
| 1 | Missing `mrs1.` prefix / bad base64 / short | `malformed` | `-32602` |
| 2 | AEAD `Open` fails | `tampered` | `-32602` |
| 3 | `inst` ≠ this instance | `cross_instance` | `-32602` |
| 4 | `exp` < now | `expired` | `-32602` |
| 5 | `prin` ≠ current principal hash | `cross_principal` | `-32602` |
| 6 | `rdig` ≠ digest of the retry's request | `cross_request` | `-32602` |
| 7 | `chain` seen with a `round` ≤ recorded round | `replayed` | `-32602` |
| — | ok | `accepted` | continue |

Note stages 3–6 run **after** a successful `Open`, so they distinguish "someone forged this" from
"this is our token used wrongly" — exactly the distinction `MOCK-243` asks for. All eight outcomes
appear in the journal (`Record.MRTR.VerifyReason`), in the metric
`mcpmock_requeststate_verify_total{result}`, and in the error response's `data.reason` when
`switches.exposeStateReason` is on (default on — this is a test tool; hiding the reason helps
nobody).

Replay detection (stage 7) uses a per-instance bounded LRU of `chain -> highestRound`, default
capacity 65 536, with an explicit metric when eviction causes a missed replay detection.

### Cross-request binding and re-asking (`MOCK-246`)

`rdig` binds the *initial* request's canonical params, not the retry's — a retry legitimately adds
`inputResponses`. `MOCK-243`'s "cross-request" rejection therefore means: the retry's `method`,
`name` and *original* params (echoed by the client) do not match what the token was minted for.
The wire annex (ADR-019) must state precisely which fields the client echoes; **this is a
specification gap**, GAP-010.

Re-asking (`MOCK-246`) mints a **new** token with the same `chain`, `round+1`, refreshed `exp`.

### TTL

Configurable `mrtr.stateTTL` (default 60 s), plus `mrtr.stateTTLOverride` values of `0`, negative
and huge for testing the hub's handling. In deterministic mode `iat`/`exp` come from
`determinism.VirtualClock`.

## Options considered

1. **XChaCha20-Poly1305** (`golang.org/x/crypto/chacha20poly1305`) — genuinely attractive: 24-byte
   nonces make random nonces safe with no counter bookkeeping, and it is constant-time without
   AES-NI. **Rejected** solely on the dependency screen (ADR-013): it adds `golang.org/x/crypto` to
   every consumer of `MOCK-107`, and AES-GCM with a counter nonce is equally safe here. Recorded as
   the runner-up; if a counter-management bug appears, switch — the token is versioned (`mrs1.`)
   precisely so this is a one-line format bump.
2. **HMAC-SHA256 MAC over plaintext JSON (no encryption)** — rejected: `MOCK-243` says AEAD, and an
   unencrypted state blob would let a hub author "helpfully" parse it, which would hide exactly the
   bugs this mechanism exists to find.
3. **JWT (JWE) via a library** — rejected: dependency, and JWE's algorithm agility is a liability
   for a token nobody else consumes.
4. **Opaque server-side handle (map lookup, no crypto)** — rejected: `MOCK-243` explicitly requires
   the *tampered* case to be detectable, which a random handle cannot express (a tampered handle is
   simply "not found"). It also defeats testing a hub that persists state across mock restarts.
5. **AES-256-GCM + HKDF + counter nonce (chosen).**

## Consequences

**Positive.** Zero crypto dependencies. All six `MOCK-243` failure modes are precisely
distinguishable and journaled. `chain` gives `MOCK-605` its correlation key for free. Token is
~180 bytes base64 — small enough to travel in every result.

**Negative.** Counter state is per-process and must never be reset while a key is live; the pinned-
key deterministic mode needed a boot-id mix to stay correct, which is subtle and must be covered
by a unit test asserting nonce uniqueness across a simulated restart. A bounded replay LRU means
replay detection is best-effort under adversarial load — accepted, metered.

**Forecloses.** Stateless replay detection (would require a wider window or persistence), and
sharing `requestState` across instances (deliberate: `MOCK-243` wants cross-instance reuse
rejected).
