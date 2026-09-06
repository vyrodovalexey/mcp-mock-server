---
id: ADR-003
title: Deterministic ordering — no Go map iteration on any output path
status: accepted
date: 2026-09-04
reversibility: EASY per call site, HARD as a convention once golden files exist
requirements: §0.1, MOCK-226, MOCK-604
---

# ADR-003 — Deterministic ordering

## Context

Go randomises `map` iteration order by design. Any code that builds a JSON object, a list result,
a header set or a log line by ranging over a `map` produces different bytes on different runs.
This defeats §0.1 and makes `MOCK-604` (golden-file journal comparison) unusable — every diff
would be noise.

`encoding/json` already sorts map keys when marshalling `map[string]T`, so *that* path is safe.
The dangerous paths are the ones we write by hand: ordering list results, iterating fault rules,
iterating header sets, building `_meta`, enumerating instances in the control API, and
enumerating capabilities.

There is no linter in `.golangci.yml` that catches this, and none exists that catches it well.

## Decision

1. **`internal/ordered` provides the only permitted containers for anything that reaches output:**
   - `ordered.Map[K constraints.Ordered, V]` — insertion-ordered, with `SortedKeys()` and a
     deterministic `Range`.
   - `ordered.Set[K]` — same.
   - `ordered.Slice[T]` with an explicit `Sort(less)`.
   These are thin, allocation-free wrappers over `[]struct{k,v}` plus an index `map` used only
   for lookup, never for iteration.

2. **Rule:** a `for … range someMap` whose body appends to a response, a header, a log field set
   or a journal record is a defect. Where a plain `map` must be iterated, the site must first
   collect keys and `slices.Sort` them, with a `//nolint`-free comment `// ordered: sorted keys`.

3. **Enforcement is by test, not by lint.** `test/functional/determinism_test.go` runs each
   scenario in the library twice in the same process with the same seed and **byte-compares**
   every response and the redacted journal. It additionally runs once with `GOMAXPROCS=1` and once
   with `GOMAXPROCS=8` and compares across those. A map-iteration leak fails this within a few
   runs. The test is run 20× in CI (`-count=20`) because a single run of a two-key map has a 50%
   chance of matching by luck.

4. **Canonical JSON.** `internal/jsonrpc` provides `Canonical(v any) ([]byte, error)` producing
   RFC 8785-style output (sorted keys, no insignificant whitespace, stable number formatting).
   Used for: request digests (ADR-002), `requestState` plaintext (ADR-010), golden files
   (`MOCK-604`), and `AssertHeaderMatchesBody` (`MOCK-603`). It is **not** used for normal
   responses, because `MOCK-222`/`MOCK-224` require emitting hand-authored key orders and
   deliberately malformed structures verbatim — those are emitted as `json.RawMessage`.

5. **Deliberate disorder is explicit.** `MOCK-226` non-deterministic list ordering is produced by
   `ordering.Shuffle(rng, items)` with `rng` from ADR-002 — never by leaking map randomness.
   Accidental disorder and requested disorder must never share a mechanism.

## Options considered

- **Trust `encoding/json` map sorting everywhere** — rejected: it only covers `map[string]T` at the
  top of a marshal; it does not cover list assembly, which is where `tools/list` ordering lives.
- **Write a custom linter / `go/analysis` pass** — considered and deferred. It is the right answer
  eventually but is a multi-day build with high false-positive rates (most map ranges are
  harmless). Revisit if the determinism test catches more than two leaks.
- **`GODEBUG` / build-tag to disable map randomisation** — no such knob exists; not an option.

## Consequences

**Positive.** Golden diffs are signal. The determinism test is cheap and catches the whole class.

**Negative.** `ordered.Map` is more verbose than `map`, and reviewers must remember the rule.
`ordered.Map` lookups cost one extra indirection; irrelevant off the hot path, and the hot path
(`tools/call`) uses no maps at all.

**Forecloses.** Using third-party JSON libraries that reorder keys or drop duplicates silently.
