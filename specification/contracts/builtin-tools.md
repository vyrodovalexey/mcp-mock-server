---
title: Built-in tool behaviours — `echo`, `sleep`, `fail` (and `canned`)
status: draft
version: 0.1.0
updated: 2026-09-04
normative-source: specification/requirements.md §8; requirements-spec.md MOCK-202, MOCK-203, MOCK-212
implements: AMEND-2 (closes task-breakdown gap G-2), AMEND-6 (closes G-3)
provisional: contains [P] items dependent on GAP-003 wire ratification — see §7
---

# Built-in tool behaviours

## 0. Why this document exists

`implementation-plan.md` Phase 1 names three built-in `tools/call` behaviours — `echo`, `sleep`,
`fail` — and `requirements.md §8` states the non-goal "no real tool side effects beyond
echo/sleep/fail". **Neither defined them.** `scenario.schema.json` carried a `behavior.kind` enum
with the right four values and almost no semantics: no argument shape, no duration unit, no error
shape, no determinism statement.

That is not a small omission. Three implementers would produce three different `echo` content
shapes, all defensible, and every golden fixture in Phase 1 would be built on whichever one
happened to be written first. Golden fixtures are the Phase 1 regression contract, so the cost of
guessing here is paid in every later phase.

This document is the definition. It is the oracle for `TASK-018` (handlers) and `TASK-027`
(functional suite and goldens).

### Provenance summary

| Label | Meaning |
|---|---|
| `[R]` | Restated from `requirements.md` / `requirements-spec.md`, cited. |
| `[D]` | Derived — forced by an `[R]` item, by ADR-002/003 determinism, or by JSON-RPC 2.0. |
| `[P]` | **Proposed here.** Requires ratification with GAP-003. Every `[P]` is listed in §7. |

---

## 1. Common frame

### 1.1 Behaviours vs tools

Two distinct concepts, easily conflated:

- A **behaviour** (`behavior.kind`) is a property of a catalogue item. *Any* tool in the catalogue
  may be configured with any of the four kinds. This is the primary mechanism.
- A **built-in tool** is a catalogue entry that mcpmock provides by default so that a hub test has
  something to call without authoring a catalogue. Phase 1 ships exactly three, named `echo`,
  `sleep` and `fail`, each wired to the behaviour of the same name with default options.

`[D]` The default catalogue in Phase 1 contains these three tools **in this order** —
`echo`, `sleep`, `fail` — because ADR-003 requires deterministic ordering and `tools/list` golden
fixtures depend on it. They are suppressed when the scenario declares its own `catalogue`, so they
never pollute an authored fixture.

### 1.2 The result envelope

`[P-39]` All three behaviours produce a `tools/call` result with `resultType: "toolResult"`
(wire annex 4.2 `[P-21]`) and this shape:

```jsonc
{
  "content": [ /* array of content blocks, never empty */ ],
  "structuredContent": { /* optional, object */ },
  "isError": false,                    // present only when true — see §4.2
  "_meta": { "serverInfo": { /* annex 4.4 */ } },
  "resultType": "toolResult"
}
```

`[P-40]` A **content block** in Phase 1 is only ever `{"type": "text", "text": <string>}`.
Phase 1 emits no image, audio or resource blocks. `MOCK-505`'s deliberately malformed blocks are
Phase 9 and bypass this document entirely.

### 1.3 Argument handling, uniformly

`[D]` `params.arguments` is an object or absent. Absent is equivalent to `{}`.
If `params.arguments` is present and **not** an object, the call fails with `-32602` and
`data.reason: "arguments_not_object"` — for all four kinds, before any behaviour runs.

`[D]` Phase 1 does **not** validate `arguments` against the tool's `inputSchema`. Argument
validation is `MOCK-606`, Phase 3. The `inputSchema` values below are therefore **advertised**
through `tools/list` and are meaningful to the client, but are not enforced by the server in
Phase 1. This is stated explicitly because a test author will otherwise reasonably expect a
schema violation to be rejected, and in Phase 1 it will not be.

### 1.4 Determinism (ADR-002, ADR-003)

`[D]` The governing rule is ADR-002's: **no wall-clock value ever appears in a response body.**
All three behaviours are pure functions of (arguments, scenario config, seed). Consequently:

| Property | Guarantee |
|---|---|
| Same seed, same request, twice | **Byte-identical** response bodies, including key order (canonical JSON, ADR-003). |
| `echo` | Contains no clock and no randomness at all — deterministic by construction, independent of seed. |
| `sleep` | The *delay* is real wall-clock time and therefore varies. The *response body* contains no timing value, so it stays byte-identical. Any timestamp emitted in a result comes from `determinism.VirtualClock`, not from `time.Now()`. |
| `fail` | Message and code are configuration, not generated — deterministic. |
| Journal `elapsedNs` | Real, wall-clock, and therefore **not** byte-stable. It is redacted from golden comparison by `MOCK-604`'s redaction rules. Golden fixtures compare response bodies, not journal timings. |

`[D]` This is the resolution of the apparent conflict between `sleep` and the virtual clock:
**`sleep` delays real time and reports virtual time.** The virtual clock is not advanced by a
`sleep` behaviour in Phase 1 — advancing it would make the emitted timestamps depend on how long
the request happened to take, which is precisely what ADR-002 forbids.

---

## 2. `echo`

### 2.1 Purpose

The zero-surprise tool. A hub test calls it to prove the round trip works and to confirm that what
it sent is what arrived. It is the most-used fixture in the suite, so its shape is load-bearing.

### 2.2 `inputSchema` (advertised)

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "description": "Echoes its arguments back. Any JSON object is accepted.",
  "properties": {
    "message": {
      "type": "string",
      "description": "Conventional field for the common case. Not required."
    }
  },
  "additionalProperties": true
}
```

`[P-41]` `additionalProperties: true` is deliberate: `echo` must be able to carry an arbitrary
payload so a test can prove that arbitrary JSON survives the round trip.

### 2.3 `outputSchema` (advertised)

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["echoed"],
  "properties": {
    "echoed": {
      "description": "The arguments object received, verbatim — or arguments[key] when behavior.echo.key is set.",
      "type": ["object", "array", "string", "number", "boolean", "null"]
    }
  },
  "additionalProperties": false
}
```

### 2.4 What is echoed, exactly

`[P-42]` `echo` returns **the entire `params.arguments` object**, verbatim, with no field
selection, no coercion and no filtering — unless `behavior.echo.key` is set, in which case it
returns `arguments[<key>]` alone.

Two output channels, both populated by default:

| Channel | Content |
|---|---|
| `content[0]` | `{"type":"text","text": <canonical JSON encoding of the echoed value, prefixed by `behavior.echo.prefix`>}` |
| `structuredContent` | `{"echoed": <the echoed value, as JSON, not stringified>}` — omitted when `behavior.echo.structured: false` |

`content` is never empty. When `arguments` is absent or `{}`, `content[0].text` is `"{}"`.

### 2.5 Non-string arguments — the explicit answer

`[P-43]` **Non-string arguments are not coerced, not rejected, and not flattened.** They pass
through as JSON.

- In `structuredContent.echoed` they retain their JSON types: a number stays a number, `null` stays
  `null`, a nested object stays a nested object.
- In `content[0].text` they are rendered as **canonical JSON** (ADR-003: object keys sorted, no
  insignificant whitespace, `\uXXXX` escaping only where required by JSON). The text block is a
  *string containing JSON*, not a prose rendering.

Worked example:

```jsonc
// request params.arguments
{ "b": 2, "a": "x", "n": null, "deep": { "z": [1, true] } }

// content[0].text  — canonical JSON, keys sorted, as a string
"{\"a\":\"x\",\"b\":2,\"deep\":{\"z\":[1,true]},\"n\":null}"

// structuredContent
{ "echoed": { "a": "x", "b": 2, "deep": { "z": [1, true] }, "n": null } }
```

`[D]` Key sorting in the text block is what makes `echo` golden-stable regardless of the order the
client serialised its arguments in. This is the reason canonical JSON is used rather than
`encoding/json`'s default.

`[P-44]` If `behavior.echo.key` is set and the key is **absent** from `arguments`, the call fails
with `-32602`, `message: "mcpmock: echo key not present in arguments"`, and
`data: {"missing": ["<key>"]}`. It does not echo `null`, because a test cannot then distinguish
"absent" from "explicitly null".

---

## 3. `sleep`

### 3.1 Purpose

Provides a controllable, cancellable delay so the hub's timeout, cancellation and concurrency
behaviour can be tested. It is the Phase 1 vehicle for `MOCK-212` (stream closure is cancellation).

### 3.2 Units — the explicit answer

`[R]` **All durations are integer MILLISECONDS.** Every field and argument is named with the `Ms`
suffix precisely so this cannot be misread: `sleepMs`, `maxSleepMs`, `durationMs`. There is no
seconds-valued field anywhere in this behaviour, and no floating-point duration. A non-integer
`durationMs` is a `-32602`.

The existing `scenario.schema.json` field `behavior.sleepMs` already carried the `Ms` suffix, so
milliseconds is a restatement of what the schema implied, not a new choice.

### 3.3 Duration resolution and the maximum

`[P-45]` The **effective delay** is resolved in this order:

1. `arguments.durationMs`, if present **and** `behavior.allowClientDuration` is `true` (default).
2. otherwise `behavior.sleepMs` (default `0`).

Then the bound is applied. `behavior.maxSleepMs` defaults to **30 000 ms (30 s)** `[P-46]` — chosen
to sit under the default Go test timeout of 10 minutes with wide margin, while still being long
enough to exercise a hub's own timeout, which is typically 1–30 s.

`[P-47]` Behaviour on exceeding the maximum is selected by `behavior.onExceedMaxSleep`:

| Mode | Behaviour |
|---|---|
| `reject` (**default**) | The call fails with `-32602`, `message: "mcpmock: requested sleep exceeds maxSleepMs"`, `data: {"requestedMs": <n>, "maxSleepMs": <m>}`. **No delay is incurred** — the rejection is immediate. |
| `clamp` | The delay is `maxSleepMs`; the result carries `_meta.clamped: true` and `_meta.requestedMs: <n>` so the caller can tell it was clamped. |

`reject` is the default because silent clamping makes a hub's timeout test pass for the wrong
reason: the test believes it waited 60 s and it waited 30 s. Failing loudly is the safer default;
`clamp` remains available for tests that deliberately probe the bound.

`[D]` A negative `durationMs` is `-32602` (`data.reason: "negative_duration"`). Zero is legal and
means "return immediately" — it is the default and is the fast path used by most fixtures.

### 3.4 Cancellation — `context.Context` and `MOCK-212`

`[R MOCK-212]` This is the behaviour's main reason to exist, and it is normative.

`[D]` The delay MUST be implemented as a `select` on a timer **and** `ctx.Done()`. A bare
`time.Sleep` is a specification violation, because it cannot observe cancellation and would break
`MOCK-212.1`'s 50 ms bound.

```
select {
case <-timer.C:          // completed  -> emit the result below
case <-ctx.Done():       // cancelled  -> emit nothing
}
```

On cancellation:

| Consequence | Rule | Source |
|---|---|---|
| Response frames | **None.** No result, no error object. The request is abandoned, not answered. | `[R MOCK-212.2]` |
| Timing | The `ctx.Done()` branch is taken within 50 ms of the closure. | `[R MOCK-212.1]` |
| Journal | A record exists with `cancelled: true` and real `elapsedNs` from receipt to cancellation observation (±10 ms). | `[R MOCK-212.3]` |
| Metric | `mcpmock_requests_total{outcome="cancelled"}` increments. | `[R MOCK-212.5]` |
| Timer | Stopped and released on both branches — no goroutine and no timer outlives the request. Verified by `goleak` (`MOCK-107.7`, ADR-013 as amended). | `[D]` |

`[R MOCK-212.4]` Both transports trigger this: an HTTP client disconnect, and on stdio a
`notifications/cancelled` or stdin EOF. The behaviour code is transport-agnostic — it only ever
sees `ctx`.

`[D]` **Interaction with `MOCK-212.2`'s "in-progress fault sleep is aborted".** A `sleep`
*behaviour* and a latency *fault* (Phase 9) are different mechanisms that both delay. Both observe
the same request `ctx` and both abort on cancellation; when both are configured, the delays are
sequential, the fault delay applying first (pipeline stage order, ADR-006), and cancellation during
either aborts the whole request.

### 3.5 `inputSchema` (advertised)

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "description": "Delays for a bounded number of milliseconds, observing cancellation.",
  "properties": {
    "durationMs": {
      "type": "integer",
      "minimum": 0,
      "description": "Delay in MILLISECONDS. Bounded by behavior.maxSleepMs (default 30000). Ignored when behavior.allowClientDuration is false."
    }
  },
  "additionalProperties": false
}
```

### 3.6 `outputSchema` (advertised)

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["sleptMs"],
  "properties": {
    "sleptMs": {
      "type": "integer",
      "minimum": 0,
      "description": "The effective delay after bounding. This is the REQUESTED/CONFIGURED value, not a measurement — see determinism note."
    },
    "clamped": {
      "type": "boolean",
      "description": "Present and true only when onExceedMaxSleep=clamp reduced the delay."
    }
  },
  "additionalProperties": false
}
```

`[D]` **`sleptMs` is the intended duration, never a measured one.** Emitting a measured elapsed
time would put a wall-clock-derived value in a response body, violating ADR-002 and making every
`sleep` golden fixture unstable. The measurement lives in the journal's `elapsedNs`, which is where
timing evidence belongs.

`content[0]` is `{"type":"text","text":"slept 250ms"}` — `[P-48]` the canonical form is
`"slept " + <sleptMs> + "ms"`, chosen so the text block is deterministic and trivially assertable.

---

## 4. `fail`

### 4.1 The two failure shapes, and which is default

`[P-49]` This is the decision the specification previously left open. MCP distinguishes two kinds
of failure, and a mock that cannot produce both is not useful for testing a hub's error handling:

| Mode | JSON-RPC shape | Means | HTTP status |
|---|---|---|---|
| `toolError` (**default**) | A **successful** response: `{"result": {"content":[…], "isError": true, …}}` | "The call reached the tool; the tool ran and failed." A domain failure. | `200` |
| `protocolError` | An **error** response: `{"error": {"code":…, "message":…, "data":…}}` | "The call itself failed." The tool did not run. | per the wire annex §6 status table |

**Default is `toolError`** `[P-49]`. Rationale: `isError: true` is the MCP-native way for a tool to
report failure, it is by far the more common case in real servers, and it is the case hub authors
most often get wrong — a client that treats `isError: true` as success will silently accept
failures. Making it the default means the easy-to-write fixture exercises the easy-to-miss bug.
`protocolError` is the deliberate, opt-in case.

### 4.2 `toolError` shape

```jsonc
{
  "content": [ { "type": "text", "text": "<behavior.fail.message>" } ],
  "isError": true,
  "_meta": { "serverInfo": { /* … */ } },
  "resultType": "toolResult"
}
```

`[P-50]` `isError` is **emitted only when `true`**. A successful result omits the key entirely
rather than carrying `"isError": false`, consistent with `MOCK-201.3`'s absent-not-null rule for
omitted fields. `structuredContent` is not emitted in `toolError` mode.

### 4.3 `protocolError` shape

```jsonc
{
  "jsonrpc": "2.0",
  "id": <echoed request id, raw>,
  "error": {
    "code": <behavior.fail.code, default -32603>,
    "message": "<behavior.fail.message>",
    "data": <behavior.fail.data, omitted entirely when unset>
  }
}
```

`[P-51]` The default code is **`-32603` Internal error**, which is the honest JSON-RPC code for
"the server failed for its own reasons". Any integer is permitted, including codes outside the
JSON-RPC reserved range — `MOCK-505` (Phase 9) depends on being able to emit arbitrary and retired
codes, and `fail` is the Phase 1 seam that capability grows from. Phase 1 does **not** validate the
code, deliberately.

`[D]` The `id` is echoed using the raw-id preservation rule of `internal/jsonrpc` (`TASK-009`): a
string id stays a string, a number id keeps its exact lexical form. `fail` does not normalise ids.

### 4.4 Who selects the mode

`[P-52]` **The scenario selects, not the caller** — by default.

`behavior.fail.allowClientOverride` defaults to **`false`**, and while it is `false`,
`arguments.mode`, `arguments.code` and `arguments.message` are **ignored** (not rejected — ignored,
so that a shared fixture can be called by tests that do not know about the option).

Rationale: mcpmock's contract is that the *scenario* determines server behaviour. If any caller
could flip a tool into `protocolError`, two tests sharing a scenario could interfere, and a golden
fixture would depend on the caller rather than on the configuration. Tests that genuinely need
per-call selection set `allowClientOverride: true` explicitly and accept that the tool's behaviour
is then caller-determined.

When `allowClientOverride` is `true`, precedence is `arguments` → `behavior.fail` → defaults, per
field independently.

### 4.5 `inputSchema` (advertised)

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "description": "Always fails. Shape is scenario-controlled unless behavior.fail.allowClientOverride is true.",
  "properties": {
    "mode":    { "enum": ["toolError", "protocolError"], "description": "Honoured only when behavior.fail.allowClientOverride is true." },
    "code":    { "type": "integer", "description": "protocolError only; honoured only when allowClientOverride is true." },
    "message": { "type": "string",  "description": "Honoured only when allowClientOverride is true." }
  },
  "additionalProperties": false
}
```

### 4.6 `outputSchema` (advertised)

`[D]` **`fail` advertises no `outputSchema`.** In `protocolError` mode there is no result to
describe, and in `toolError` mode the payload is an unstructured text block. Advertising an
`outputSchema` that only sometimes applies would be worse than advertising none.

---

## 5. `canned` — completeness note

`canned` was already in the enum and is unchanged by this amendment. It emits `behavior.result`
verbatim, with `isError` as configured. It is the escape hatch for any shape the three built-ins
cannot produce. `behavior.result` is **required** when `kind: canned` — now enforced by the schema
(`allOf`/`if`/`then`), which it previously was not.

---

## 6. Acceptance criteria added

These are the testable criteria `TASK-018` and `TASK-027` were missing. They attach to existing
requirement ids; no new requirement id is created.

| # | Criterion | Level |
|---|---|---|
| 202.10 | `echo` with `{"b":2,"a":"x"}` returns `content[0].text` equal to the canonical JSON `{"a":"x","b":2}` — keys sorted — and `structuredContent.echoed` deep-equal to the arguments with JSON types preserved. `[P]` | F |
| 202.11 | `echo` with absent `arguments` returns `content[0].text == "{}"` and does not error. `[P]` | F |
| 202.12 | `echo` with `behavior.echo.key` naming an absent key returns `-32602` with `data.missing`. `[P]` | F |
| 202.13 | `sleep` with `durationMs: 0` returns within 50 ms; with `durationMs: 250` returns after ≥ 250 ms; both produce byte-identical bodies at the same seed. `[P]` | F |
| 202.14 | `sleep` with `durationMs` above `maxSleepMs` and `onExceedMaxSleep: reject` returns `-32602` **immediately** (< 50 ms — proving no delay was incurred) with `data.requestedMs` and `data.maxSleepMs`. `[P]` | F |
| 202.15 | `sleep` with `onExceedMaxSleep: clamp` delays `maxSleepMs` and sets `_meta.clamped: true`. `[P]` | F |
| 202.16 | `sleep` in flight, client disconnects: `ctx` cancels within 50 ms, **no frame is written**, journal shows `cancelled: true`, `mcpmock_requests_total{outcome="cancelled"}` increments, and `goleak` reports no leaked goroutine or timer. `[R MOCK-212]` | F |
| 202.17 | `fail` with default config returns HTTP `200` and a **result** with `isError: true`; the key `isError` is absent on a successful `echo` result. `[P]` | F |
| 202.18 | `fail` with `mode: protocolError` returns a JSON-RPC `error` with code `-32603` by default and the request id echoed with its original lexical form. `[P]` | F |
| 202.19 | `fail` with `allowClientOverride: false` (default) **ignores** `arguments.mode` and still returns `toolError`. `[P]` | F |
| 202.20 | All three built-in tools appear in `tools/list` in the fixed order `echo`, `sleep`, `fail`, each with the `inputSchema` above; they are absent when the scenario declares its own catalogue. `[P]` | F |

---

## 7. `[P]` register for this document

All items below are **provisional**, are consequences of GAP-003 being unratified, and are recorded
in `gap-analysis.md` GAP-003's blast-radius list. They are numbered continuing the wire annex's
sequence so the two documents share one namespace.

| Id | Item | Blast radius if ratification differs |
|---|---|---|
| `P-39` | `tools/call` result envelope shape | Every Phase 1 `tools/call` golden |
| `P-40` | Text-only content blocks in Phase 1 | Content-block construction, one helper |
| `P-41` | `echo` accepts arbitrary properties | `echo` `inputSchema` only |
| `P-42` | `echo` returns the whole `arguments` object, in two channels | `echo` goldens |
| `P-43` | Non-string arguments pass through as canonical JSON, uncoerced | `echo` goldens |
| `P-44` | Absent `echo.key` is `-32602`, not `null` | One error path |
| `P-45` | Duration precedence: `arguments.durationMs` over `behavior.sleepMs` | One resolution function |
| `P-46` | `maxSleepMs` default 30 000 | One default value |
| `P-47` | `reject` (not `clamp`) is the default on exceeding the maximum | One default + one error path |
| `P-48` | `sleep` text block form `"slept <n>ms"` | `sleep` goldens |
| `P-49` | `toolError` is the default `fail` mode | `fail` goldens; hub error-handling tests |
| `P-50` | `isError` omitted when false | Every `tools/call` golden |
| `P-51` | `-32603` is the default `protocolError` code | One default value |
| `P-52` | Scenario selects the failure mode; caller cannot, by default | `fail` configuration surface |

**None of these is load-bearing in the way wire annex `P-06` (`_meta` location) or `P-13`
(sentinel format) are.** They are contained inside `tools/call` result construction and their
ratification cost is regenerating Phase 1 goldens — hours, not weeks. This is a deliberate
consequence of ADR-019: the shapes are data, and the goldens are generated.
