---
id: ADR-001
title: Single Go module, library-first layout
status: accepted
date: 2026-09-04
reversibility: HARD — the module path and public package set are published contracts (§10.1 semver)
requirements: MOCK-101, MOCK-107, §10.1
---

# ADR-001 — Single Go module, library-first layout

## Context

`MOCK-107` requires mcpmock to be importable as `package mcpmock` directly into the hub's test
suite, not only runnable as a subprocess. `MOCK-101` requires a single statically linked binary.
§10.1 requires semantic versioning of the published module.

These pull in opposite directions. A binary-first layout (`cmd/` + `internal/` only) makes
embedding impossible, because `internal/` is unimportable from outside the module. A
multi-module layout (root, `assert`, `server` as separate modules) gives fine-grained dependency
control but multiplies release toil and creates version-skew bugs between modules that must move
together.

The dependency cost of embedding is real: whatever mcpmock imports lands in the hub's `go.mod`
and in the hub's `govulncheck` and Trivy results.

## Decision

One module, path **`github.com/vyrodovalexey/mcp-mock-server`** `[stated]` — verified from
`git remote -v` (AMEND-1). Four **public** packages, everything else under `internal/`.

The `mcpmock/<x>` column below is a **package label**, not an import path; see
`architecture.md §6` for the label→import-path mapping.

| Package label | Import path | Purpose | Transitive weight |
|---|---|---|---|
| `mcpmock` | `github.com/vyrodovalexey/mcp-mock-server` | Lifecycle facade: construct, start, stop, reach instances, in-process `Control`. | Full server (Prometheus, OTel, YAML, JSON Schema). |
| `mcpmock/scenario` | `github.com/vyrodovalexey/mcp-mock-server/scenario` | Scenario document types + loader + validator. | YAML + JSON Schema only. |
| `mcpmock/journalapi` | `github.com/vyrodovalexey/mcp-mock-server/journalapi` | `Record`, `View`, `Selector`, `Filter` — the journal serialisation contract. | **stdlib only.** |
| `mcpmock/assert` | `github.com/vyrodovalexey/mcp-mock-server/assert` | The eight named assertions (`MOCK-603`) + golden compare. | `journalapi` + stdlib + `testing`. |

`assert` and `journalapi` are stdlib-only by rule (enforced by `make deps-check`). A hub test
that only inspects a captured journal pays nothing.

The binary is `cmd/mcpmock`, a thin `main` over the same public facade. Anything `cmd/mcpmock`
can do, an embedded caller can do.

Version policy: `v0.x` until the wire annex (ADR-019) is ratified, then `v1`. `v0` is stated
explicitly in `README` so consumers do not assume API stability prematurely.

## Options considered

1. **Binary-first, `internal/` only** — rejected: violates `MOCK-107` outright.
2. **Multi-module (`/assert` as its own module)** — rejected for v0: the release coordination cost
   exceeds the benefit, and Go's module graph pruning (`go 1.17+`) already prevents unused
   transitive deps from affecting the consumer's build when the *packages* are not imported. The
   dependency isolation we actually need is achieved by package-level import discipline. Revisit
   at v1 if hub maintainers report `go.sum` bloat.
3. **Single module, everything public** — rejected: freezes internal structure as contract; makes
   the §5 fault vocabulary, which will churn heavily, unrefactorable.

## Consequences

**Positive.** One tag, one changelog, one `govulncheck` surface. Embedded and subprocess modes
cannot diverge because they share the facade. `internal/` stays refactorable through v1.

**Negative.** Importing `mcpmock` (not just `assert`) drags Prometheus and OTel into the hub's
module graph. Mitigated by the package split, and by ADR-016 (OTel is lazily initialised, but
still *linked*). If the hub team objects, the escape hatch is build tags — noted, not taken.

**Forecloses.** Splitting `assert` into its own module later is a breaking import-path change for
consumers unless the path is chosen now to be split-compatible. Therefore `assert` lives at
`MODULE/assert` from day one, which is exactly where a future submodule would live.

**Blocked on.** `gap-analysis.md` GAP-001 — the module path itself is unknown.
