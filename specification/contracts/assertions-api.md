---
title: Assertion Helper API — package mcpmock/assert
status: draft
version: 0.1.0
updated: 2026-09-04
requirements: MOCK-603, MOCK-604, MOCK-605, MOCK-601, MOCK-602
---

# Contract: `package assert`

`MOCK-603` names eight assertions. This contract specifies those eight plus the machinery they
need, and their **failure semantics** — which matter more than their signatures, because an
assertion whose failure message does not identify the offending request wastes the debugging time
it was meant to save.

**Dependency rule (ADR-001, enforced by `make deps-check`):** this package imports only
`mcpmock/journalapi`, the standard library, and `testing`. It must work against a journal read
from a file, with no server running.

---

## 1. Entry points

```go
// Package assert provides assertions over an mcpmock request journal.
package assert

// Asserts is the non-fatal form. Every method returns an error describing all
// violations found, or nil. Usable outside a test.
type Asserts struct{ /* unexported */ }

func New(v journalapi.View) *Asserts

// T binds a View to a testing.TB. Assert* methods call tb.Helper() and
// tb.Fatalf on violation; Check* methods return an error.
func T(tb testing.TB, v journalapi.View) *TB

// FromFile loads an NDJSON journal export (MOCK-602) with no running server.
func FromFile(path string) (journalapi.View, error)
func FromReader(r io.Reader) (journalapi.View, error)
```

Both `*Asserts` and `*TB` expose the same method names; `*TB` fails the test, `*Asserts` returns
errors. A single generated file keeps them in sync (`TestAssertParity` asserts the method sets
match), so the two forms cannot drift.

---

## 2. Scoping

Assertions apply to a **subset** of the journal. Scoping is an immutable chain.

```go
func (a *TB) Where(s Selector) *TB
func (a *TB) WhereMethod(pattern string) *TB     // glob
func (a *TB) WhereName(pattern string) *TB       // primitive name, glob
func (a *TB) WhereInstance(name string) *TB
func (a *TB) WhereSince(t time.Time) *TB
func (a *TB) WhereChain(chainID string) *TB      // MOCK-605
func (a *TB) Not() *TB                           // inverts the next assertion's sense

type Selector struct {
    Instance   string
    Method     string    // glob
    Name       string    // glob
    Transport  string    // "http" | "stdio"
    Era        string
    Since, Until time.Time
    ChainID    string
    TraceID    string
    FaultRule  string
    StatusCode *int
}
```

`Where*` never mutates the receiver. An empty scope is **not** an automatic pass: every assertion
declares its empty-scope behaviour in §3, because "asserted nothing and passed" is the most
dangerous failure mode in a test-harness API.

```go
// RequireNonEmpty fails if the current scope selects zero records. Call it
// before a batch of assertions when an empty journal would be a test bug.
func (a *TB) RequireNonEmpty() *TB
```

---

## 3. The eight named assertions (`MOCK-603`)

### 3.1 `AssertNoHeader(name string)`

```go
func (a *TB) AssertNoHeader(name string) *TB
func (a *Asserts) CheckNoHeader(name string) error
```

Fails if **any** record in scope carries an HTTP header whose name matches `name`
**case-insensitively**. Matching is against the recorded wire-order header list (`MOCK-601.2`),
not a canonicalised map, so `x-client-token` and `X-Client-Token` are both caught.

- Empty scope: **passes** (vacuously true), but emits `tb.Logf` noting zero records inspected.
- Failure message: `no-header "X-Client-Token": found on 3 request(s): seq=41 tools/call, seq=57 tools/call, seq=62 tools/list (first value: "Bearer eyJ…" [redacted, 9 more chars])`. Header **values are redacted** in the message to the first 8 characters — a failing CI log must not print a token.
- stdio records are skipped (no headers); the count of skipped records is reported.

### 3.2 `AssertHeaderNotValue(name, value string)`

```go
func (a *TB) AssertHeaderNotValue(name, value string) *TB
```

Fails if any record carries header `name` (case-insensitive) with a value **equal** to `value`
(exact, case-sensitive comparison, since token values are case-sensitive). Comparison is
constant-time to avoid the assertion itself becoming a timing oracle in a shared CI.

- The intended use is token-passthrough detection: `AssertHeaderNotValue("Authorization", "Bearer "+clientToken)`.
- Failure message redacts as in 3.1 and reports the matching `Seq` list.
- Companion: `AssertHeaderValueHashNot(name string, hash string)` compares against the journal's
  `credential.hash` when the raw value is unavailable to the test (`MOCK-407`).

### 3.3 `AssertHeaderMatchesBody()`

```go
func (a *TB) AssertHeaderMatchesBody() *TB
```

Mirrored-header integrity over **all** records in scope (`MOCK-204`, `MOCK-603`). For each record
it checks:

| Header | Compared to | Rule |
|---|---|---|
| `MCP-Protocol-Version` | `_meta.protocolVersion` | exact string |
| `Mcp-Method` | JSON-RPC `method` | exact string |
| `Mcp-Name` | primitive name in `params` | exact, or Base64-sentinel decode then exact |
| `Mcp-Param-*` | corresponding `params` field | typed: integers numerically, booleans by keyword, strings byte-wise |

- Reports **every** mismatch, not the first (`MOCK-603.6`).
- A header absent while the body has the field is reported as `missing`, the converse as `extra`;
  both are configurable to be tolerated: `AssertHeaderMatchesBody(assert.AllowMissing())`.
- The sentinel decoder used here is `journalapi`'s, which is the **independent** implementation
  (ADR-017) — not the server's encoder. This is deliberate: the assertion is an oracle.
- Empty scope: passes with a log note.
- Depends on ADR-019 ratification for the exact `Mcp-Param-*` typing rules.

### 3.4 `AssertMetaField(path string, m Matcher)`

```go
func (a *TB) AssertMetaField(path string, m Matcher) *TB

type Matcher interface {
    Match(v json.RawMessage) (bool, string)   // ok, explanation-on-failure
}

func Equal(want any) Matcher
func EqualJSON(want string) Matcher
func Contains(sub string) Matcher
func MatchesRegexp(re string) Matcher
func OneOf(vals ...any) Matcher
func Absent() Matcher
func Present() Matcher
func SetEquals(vals ...string) Matcher     // order-insensitive; for clientCapabilities
func Custom(name string, f func(json.RawMessage) (bool, string)) Matcher
```

`path` is a **JSON Pointer** into the decoded `_meta` object (`/protocolVersion`,
`/clientCapabilities/sampling`, `/io.modelcontextprotocol~1logLevel` — note `~1` escaping for `/`
in keys). JSON Pointer is chosen over dotted paths precisely because MCP `_meta` keys contain
dots and slashes.

- Applies to **every** record in scope; fails listing each non-matching `Seq` with the matcher's
  explanation.
- Empty scope passes with a log note.

### 3.5 `AssertTraceContextPropagated(traceID string)`

```go
func (a *TB) AssertTraceContextPropagated(traceID string) *TB
func (a *TB) AssertTraceContextContinuous() *TB    // no explicit id: all records share one trace
```

Fails if any record in scope lacks a valid W3C `traceparent`, or carries one whose `trace-id`
differs from `traceID`.

- Reads `Record.Correlation.TraceID`, which is populated **unconditionally** by the pipeline
  regardless of whether tracing export is enabled (ADR-016) — so this works in a plain unit test
  with no collector.
- A **malformed** `traceparent` is a distinct, reported failure class (`invalid_traceparent`)
  with the raw value included; a hub emitting a malformed header is exactly the defect this
  catches, and reporting it as merely "missing" would hide it.
- `AssertTraceContextContinuous` additionally reports the set of distinct trace ids found, which
  is the useful output when a hub splits a trace it should have continued.

### 3.6 `AssertCapabilitiesNotWidened(declared Capabilities)`

```go
type Capabilities map[string]json.RawMessage    // path -> value, flattened by JSON Pointer

func (a *TB) AssertCapabilitiesNotWidened(declared Capabilities) *TB
```

Fails if any record's `_meta.clientCapabilities` contains a capability **path** absent from
`declared`, or a value that is a strict superset of the declared value.

Widening rules:
- A path present in the request but not in `declared` ⇒ widened.
- A boolean `false`→`true` ⇒ widened.
- An array gaining elements ⇒ widened (set comparison).
- An object gaining keys ⇒ widened (recursive).
- Narrowing is **not** a failure (a hub may legitimately advertise less).

Failure message names each widened JSON Pointer, the declared value and the observed value, with
the offending `Seq` list. This is the assertion most likely to surface a real hub defect, so its
message quality is explicitly part of the contract.

### 3.7 `AssertNoSessionHeaders()`

```go
func (a *TB) AssertNoSessionHeaders() *TB
```

Modern-mode cleanliness (`MOCK-207`). Fails if any record in scope carries `Mcp-Session-Id` or
`Last-Event-ID` on the **request**, or if any recorded **response** carries `Mcp-Session-Id`.

- Automatically scoped to `era == "modern"` records unless the caller has scoped otherwise; if
  the scope contains legacy-era records, the assertion **fails with a message saying so** rather
  than silently ignoring them. Silently skipping records is how an assertion lies.

### 3.8 `AssertRequestCount(sel Selector, n int)`

```go
func (a *TB) AssertRequestCount(sel Selector, n int) *TB
func (a *TB) AssertRequestCountAtMost(sel Selector, n int) *TB
func (a *TB) AssertRequestCountAtLeast(sel Selector, n int) *TB
```

Counts records matching `sel` **within the current scope** and compares. The primary use is
de-duplication and cache-hit verification (`MOCK-231`/`232`/`233`).

- Failure message includes the actual count, the selector rendered readably, and — when the actual
  count is small — the matching `Seq`/method list, so "3 instead of 1" is immediately diagnosable.
- `n == 0` is a legitimate and common assertion; it does not trigger the empty-scope note.

### 3.9 `AssertRetryIDsDistinct()`

```go
func (a *TB) AssertRetryIDsDistinct() *TB
```

MRTR id discipline (`MOCK-247`). Groups records by `Record.MRTR.ChainID` (`MOCK-605`) and fails if
any chain contains two records whose JSON-RPC ids are byte-equal.

- Ids are compared as **raw JSON**, so `1`, `"1"` and `1.0` are distinct — which is correct,
  because JSON-RPC treats them as distinct id values and a hub that changes the type is doing
  something worth knowing about. A type-only difference is reported as a distinct warning class.
- Chains with a rejected `requestState` are included, with their rejection reason in the message.
- Empty scope (no MRTR chains): passes with a log note. `RequireNonEmpty` is the guard.

---

## 4. Golden-file comparison (`MOCK-604`)

```go
func (a *TB) AssertGolden(name string, opts ...GoldenOption) *TB

func WithRedaction(rules ...RedactRule) GoldenOption
func WithGoldenDir(dir string) GoldenOption          // default "testdata/golden"
func WithRecordFields(fields ...string) GoldenOption // project to a subset

type RedactRule struct {
    Pointer string    // JSON Pointer into the record, supports "*" for array wildcards
    Mode    RedactMode // Drop | Replace | Normalize
    Const   string
    Fn      func(json.RawMessage) json.RawMessage
}

func DefaultRedactions() []RedactRule
```

`DefaultRedactions()` covers `/wallTime`, `/monoNs`, `/durationNs`, `/peer`, `/credential/hash`,
`/response/frames/*/eventId`, `/mrtr/requestState`, `/session/id`, `/subscription/id`.

- On mismatch: a **unified diff** of the redacted, canonically-encoded (ADR-003) journals, capped
  at 200 lines with a pointer to the full artifact written to `t.TempDir()`.
- Update: `MCPMOCK_UPDATE_GOLDEN=1` rewrites the file and emits a `WARN` naming it. There is
  deliberately no `-update` flag, because flags propagate through `go test ./...` and update
  everything.
- Golden files carry a header line with `schemaVersion` and the mcpmock version that wrote them; a
  version mismatch produces a clear message rather than a confusing diff.

---

## 5. Correlation helpers (`MOCK-605`)

```go
func (a *TB) Chains() []journalapi.Correlation
func (a *TB) AssertChainRounds(chainID string, n int) *TB
func (a *TB) AssertChainCompleted(chainID string) *TB
func (a *TB) AssertNoRejectedState() *TB        // no requestState verification failures
func (a *TB) AssertStateRejection(reason string, n int) *TB   // exactly n rejections of a kind
```

`AssertStateRejection` accepts the ADR-010 reason vocabulary: `malformed`, `tampered`,
`cross_instance`, `expired`, `cross_principal`, `cross_request`, `replayed`.

---

## 6. Argument-integrity helpers (`MOCK-606`)

```go
func (a *TB) AssertArgumentsValid() *TB                      // all tools/call args passed schema validation
func (a *TB) AssertArgumentsEqual(sel Selector, want any) *TB // byte-compare canonicalised args
func (a *TB) AssertNoArgumentMutation(sent map[string]any) *TB
```

`AssertNoArgumentMutation` is the direct expression of `MOCK-606`'s purpose: it canonicalises what
the test sent and compares to what was recorded, reporting a JSON diff of the difference.

---

## 7. Failure-message contract

Every failure message follows one shape, because grep-ability across a CI log matters:

```
mcpmock/assert: <assertion-name> failed
  scope:      instance=upstream-a method=tools/* (17 records)
  violations: 3
    seq=41  tools/call  name=search   <specific detail>
    seq=57  tools/call  name=search   <specific detail>
    seq=62  tools/list                <specific detail>
  hint: <one line pointing at the likely cause or the relevant MOCK-nnn>
```

Rules:
- At most 10 violations are listed; the remainder are counted.
- **No raw credential, token or `Authorization` value ever appears**, in any message, in any mode.
  Values are truncated to 8 characters with a length indication. Enforced by a test that runs
  every assertion against a journal containing a sentinel secret and scans the message.
- The `hint` cites the relevant requirement id when one applies.
- Messages are deterministic (violations sorted by `Seq`) so they diff cleanly across runs.
