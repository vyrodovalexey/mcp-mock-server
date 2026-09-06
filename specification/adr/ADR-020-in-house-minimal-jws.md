---
id: ADR-020
title: In-house minimal JWS (internal/jwtmini) for the embedded authorization server
status: accepted
date: 2026-09-04
reversibility: EASY — internal package, swappable for a library
requirements: MOCK-405, MOCK-403, MOCK-404, MOCK-406, MOCK-401, MOCK-101
---

# ADR-020 — In-house minimal JWS

## Context

`MOCK-405` asks for an embedded mock authorization server issuing **signed JWTs with configurable
`aud`, `scope`, `exp`, and RFC 9207 `iss` behavior**, so end-to-end auth tests need no external
IdP. `MOCK-403` requires audience validation. `MOCK-406` requires expiry mid-session and scope
escalation.

The obvious move is `github.com/golang-jwt/jwt/v5` or `go-jose`. Both are competent libraries.

But §0.4 — "deliberately non-conformant on demand" — changes the requirement. The mock's purpose
includes producing tokens a correct issuer would refuse to produce:

- `alg: none`;
- a valid signature over a header whose `alg` disagrees with the key type;
- `aud` as a string where the hub expects an array, and vice versa;
- `exp` in the past, `exp` absent, `exp` as a string, `nbf` in the future;
- duplicate JSON keys in the claims;
- an unpadded/mis-padded base64url segment;
- a `kid` that is absent from the published JWKS;
- RFC 9207 `iss` present, absent, or mismatched.

A well-written JWT library exists precisely to make these impossible. Fighting it means
constructing tokens by hand anyway — at which point the library is only used for the half of the
work we did not need.

## Decision

`internal/jwtmini`: ~250 lines over `crypto/ecdsa`, `crypto/rsa`, `crypto/sha256`,
`encoding/base64`, `encoding/json`.

```go
type Header struct{ Alg, Typ, Kid string; Raw json.RawMessage }  // Raw wins if set
type Signer interface{ Sign(signingInput []byte) (sig []byte, alg string, err error) }

func Sign(h Header, claims any, s Signer) (string, error)
func SignRaw(headerJSON, claimsJSON []byte, s Signer) (string, error)   // deliberate malformation
func Parse(tok string) (Header, json.RawMessage, []byte, error)          // no verification
func Verify(tok string, keys KeySet) (Header, json.RawMessage, error)
```

Supported algorithms: **ES256** (default — P-256, small keys, fast generation, keeps startup in
budget) and **RS256** (because some hubs only accept RSA). `none` is supported **only** through
`SignRaw`, never through `Sign`, so it cannot happen by accident.

`SignRaw` takes pre-serialised header and claim bytes. Every deliberate malformation in the list
above is expressible as a byte sequence, and none of them needs library cooperation. Malformations
are declared in the scenario file as a named enum
(`auth.malform: [alg-none, aud-string, exp-past, kid-unknown, dup-claims, bad-padding, …]`), so
they are discoverable and validated rather than free-form.

**Key material.** Keys are generated at startup with `crypto/rand` (ES256 P-256 generation is
microseconds). In deterministic mode (`--seed` with `auth.deterministicKeys: true`) the key is
derived from the ADR-002 seed tree so JWTs are byte-stable for golden files — with a startup
`WARN` and a `security.md` note that a seed-derived signing key is test-only. **No private key is
ever committed to the repository, embedded in the image, or written to a log** (`security.md §3`).

**Verification side.** `MOCK-403` (audience) and `MOCK-404` (scopes) use `Verify` against a key
set that may be the embedded AS's own JWKS or an externally configured JWKS URL. When
verification fails, the failure reason is journaled with the same staged-reason discipline as
ADR-010 (`bad_signature`, `unknown_kid`, `alg_mismatch`, `expired`, `not_yet_valid`,
`aud_mismatch`, `iss_mismatch`, `malformed`) so `MOCK-403`'s "primary detector for hub token
passthrough" produces a specific, inspectable finding rather than a generic 401.

**Endpoints published by the embedded AS** (`MOCK-405`, `MOCK-402`):
`/.well-known/oauth-authorization-server`, `/.well-known/jwks.json`, `/token`
(client_credentials + refresh, configurable), and — on the resource side —
`/.well-known/oauth-protected-resource` (RFC 9728). These are served on the instance's own
listener path prefix so a 200-instance fleet does not need 200 authorization servers; a shared AS
with per-instance `resource` values is the default, with per-instance AS available.

## Options considered

1. **`golang-jwt/jwt/v5`** — rejected: a dependency (ADR-013) that we would bypass for the most
   important half of the work. Its `SigningMethodNone` requires an explicit unsafe constant, and
   arbitrary header/claim byte control is not part of its API.
2. **`go-jose/go-jose`** — rejected: heavier, JWE machinery unused, same bypass problem.
3. **`lestrrat-go/jwx`** — rejected: largest of the three, most transitive deps.
4. **Library for signing + hand-rolled for malformation** — rejected: two token construction paths
   that must agree; the malformed path is the one that matters and it would be the untested one.
5. **In-house minimal JWS (chosen).**

## Consequences

**Positive.** Zero dependency. Every §5 auth malformation is a first-class, named, schema-validated
scenario option rather than an escape hatch. The verification reason taxonomy is ours, so
`MOCK-406`'s "record which credential the hub presented on each request" and `MOCK-403`'s
passthrough detection produce precise journal evidence.

**Negative.** We own cryptographic code. Mitigations, all mandatory:
- We implement **signing and verification of a fixed algorithm set only** — no algorithm agility,
  no key-format parsing beyond PEM, no JWE. The dangerous parts of JWT are the parts we are not
  building.
- `Verify` rejects `alg: none` and enforces that the algorithm matches the key type before any
  signature check (the classic JWT vulnerability class), with a dedicated test.
- Constant-time comparison via `crypto/hmac.Equal` where a MAC is involved; ECDSA/RSA verification
  is delegated to stdlib.
- A test vector suite from RFC 7515 Appendix A (A.2 RS256, A.3 ES256) is embedded and asserted.
- `gosec` is enabled (`.golangci.yml:23`) and this package gets no exclusions.

**Explicit scope limit.** `internal/jwtmini` is **not** a general-purpose JWT library and its
package doc says so. It must never be promoted out of `internal/`.

**Forecloses.** Supporting EdDSA / PS256 / HMAC-signed tokens without extending the package.
`HS256` is deliberately omitted: a symmetric-key JWT would require distributing a shared secret in
the scenario file, which conflicts with "no secrets in source, logs, images or examples".
