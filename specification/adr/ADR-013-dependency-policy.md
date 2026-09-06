---
id: ADR-013
title: Dependency policy and the approved dependency set
status: accepted (amended 2026-09-04 — AMEND-5: two-tier runtime/test-only policy, `goleak` admitted; AMEND-9: "no gRPC" was factually wrong — corrected, gRPC admitted as a transitive consequence of OTLP, transitive enforcement added)
date: 2026-09-04
reversibility: MEDIUM per dependency; HARD as a policy once consumers depend on the module graph
requirements: MOCK-101, MOCK-105, MOCK-107, MOCK-107.7, §10.2
---

# ADR-013 — Dependency policy

## Context

`MOCK-101`: "a single statically linked Go binary, **no runtime dependencies**". Read literally
that phrase is about *deployment* — no shared libraries, no sidecars, no database, no external
service. It is not a ban on Go modules; a Go module is compiled in, and the result is still one
static binary.

But `MOCK-107` changes the calculus. mcpmock lands in the hub's `go.mod`. Every dependency we take
becomes a dependency the hub's `govulncheck` (`ci.yml:71`), Trivy scan (`ci.yml:644`) and SBOM
(`ci.yml:609`) must carry. §10.2 requires the image to pass Trivy at CRITICAL/HIGH with
`exit-code: 1` (`ci.yml:665`) — a CVE in a transitively-pulled gRPC stack becomes *our* CI
failure.

So the policy is stricter than "static binary" requires.

## Decision

### Screen

A third-party module is admitted only if **all** of the following hold:

1. It implements a requirement we cannot meet with the standard library at reasonable cost.
2. It does not require process-global registration (ADR-007).
3. It does not write to stdout (ADR-011).
4. It has no cgo, and works with `CGO_ENABLED=0`.
5. Its own transitive tree is inspected and listed below — no surprises.
6. It is pinned by version and hash in `go.sum`; upgrades go through `govulncheck` in CI.

### The two tiers  (added by AMEND-5, 2026-09-04)

The policy as originally written implied a test-only tier ("anything test-only is exempt from
1–3") but never enumerated one, while `make deps-check` was specified to fail on "any module
outside the approved list" — and the only list present was the *runtime* set. Taken together those
two statements made `make deps-check` fail on `go.uber.org/goleak`, which `MOCK-107.7` explicitly
mandates. **The missing distinction was the defect.** Dependencies are now explicitly two-tier:

| Tier | Enters a consumer's `go.mod`? | Screen | Enumerated in |
|---|---|---|---|
| **Runtime** | **Yes** — this is the `MOCK-107` cost | All six criteria | *Approved runtime set*, below |
| **Test-only** | **No** — reachable only from `_test.go` files or a build-tagged `tools.go` | Criteria 4–6 only; 1–3 waived | *Approved test-only set*, below |

A test-only module must be unreachable from any non-test file. This is a **mechanical** property,
not a promise: `go list -deps ./...` (which excludes test imports) is the oracle. If a module
appears there, it is runtime, whatever the author intended.

Anything test-only is exempt from 1–3 but must be in a `_test.go`-only import path so it does not
enter consumers' graphs.

### Approved test-only set

| Module | Why | Criteria 4–6 |
|---|---|---|
| `github.com/stretchr/testify` | Assertion ergonomics in `_test.go`. Already admitted by this ADR (see *Not taken*, below). | pure Go, pinned |
| `go.uber.org/goleak` | **`MOCK-107.7` mandates it by name**: "a test that forgets to close leaks nothing (verified with `goleak`)". Detects goroutine leaks after `mcpmock.StartTest(t)` cleanup, and is the verification method for `MOCK-107.7` in `traceability.md`. Pure Go, no transitive dependencies outside stdlib, `CGO_ENABLED=0`-clean. | pure Go, no transitive deps, pinned |
| `go.uber.org/mock` (`mockgen`) | Test doubles; build-tagged `tools.go` tool dependency. Note `mockgen` is **not installed** in the current environment. | tool-only |

**Why admit `goleak` rather than hand-roll leak detection.** The alternative considered was a
~40-line `runtime.NumGoroutine()` delta check with a retry loop. Rejected: it is precisely the kind
of test infrastructure that produces flakes under `-race` and `-count=20` (ADR-003's determinism
suite), it cannot name the leaked stack, and `MOCK-107.7` names `goleak` specifically, so a
substitute would need the requirement changed. The dependency is test-only, has no transitive
dependencies, and never reaches the hub's module graph — the entire cost this ADR exists to
control is not incurred.

### Approved runtime set

| Module | Why | Alternative rejected | Weight |
|---|---|---|---|
| `sigs.k8s.io/yaml` | `MOCK-701` YAML/JSON. Converts YAML→JSON, so **one** JSON Schema validates both inputs and Go structs use `json:` tags only. This is the reason to prefer it over a direct YAML decoder. | `gopkg.in/yaml.v3` (separate tag namespace, schema can't validate YAML-only constructs); `goccy/go-yaml` (larger, faster than we need) | small; pulls `gopkg.in/yaml.v2` |
| `github.com/santhosh-tekuri/jsonschema/v6` | `MOCK-701` scenario validation **and** `MOCK-606` `tools/call` argument validation **and** `MOCK-225` schema edge cases. Critically: it supports a **custom `$ref` loader**, which is how `MOCK-225`'s "network `$ref` to loopback / to a public host" is implemented under our control rather than by uncontrolled egress. Supports draft-07 (`MOCK-225`) and 2020-12. | Hand-rolled validator (weeks of work, and `MOCK-225` demands genuinely correct `oneOf`/`$defs` cost behaviour); `xeipuuv/gojsonschema` (draft-07 only, unmaintained) | small, no transitive deps |
| `github.com/prometheus/client_golang` | `MOCK-105` names Prometheus explicitly. | `expvar` (not Prometheus format); hand-written exposition (histograms done badly are worse than a dependency) | medium; pulls `prometheus/common`, `client_model`, `beorn7/perks`, `cespare/xxhash`, `munnerz/goautoneg`, `prometheus/procfs` |
| `go.opentelemetry.io/otel`, `otel/sdk`, `otel/trace` | User-mandated OTLP tracing. | none — OTel is the requirement | medium |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | **HTTP exporter only.** No gRPC connection is ever opened. | `otlptracegrpc` — still rejected, but **not** on the grounds originally written: see *The gRPC correction* below. | medium; pulls `protobuf` (unavoidable for OTLP encoding) **and, contrary to this ADR's original text, the `google.golang.org/grpc` tree — see AMEND-9** |
| `golang.org/x/sync` (`errgroup`, `semaphore`) | Shutdown orchestration, bounded worker pools. | hand-rolled `sync.WaitGroup` plumbing — genuinely viable; admitted because `x/sync` is effectively stdlib-adjacent and has no transitive deps | tiny |

**Not taken, deliberately:**

| Rejected | Requirement it would have served | Why stdlib instead |
|---|---|---|
| `golang.org/x/crypto` | `MOCK-243` AEAD | `crypto/aes` + `crypto/cipher` + `crypto/hkdf` (Go 1.24+). ADR-010. |
| `golang-jwt/jwt` or `go-jose` | `MOCK-405` signed JWTs | `internal/jwtmini` — ~200 lines over `crypto/ecdsa` + `crypto/rsa` + `encoding/base64`. **The decisive reason is not size:** ADR-020, we must be able to emit *deliberately malformed* tokens, and a correct library refuses to. |
| `spf13/cobra` + `pflag` | `MOCK-104` CLI | stdlib `flag` + a ~60-line subcommand dispatcher. Cobra is fine software; it is 3 modules and a nonzero init cost against a 200 ms startup budget (`MOCK-107`). |
| `chi`/`gin`/`echo`/`fasthttp` | `MOCK-102` HTTP | ADR-012. |
| `google/uuid` | ids | ADR-002 mints ids deterministically; a UUID library would either be nondeterministic or unused. **We take no *direct* dependency on it — but it is present as an INDIRECT dependency via `otel/sdk/resource`, which uses it for the OTel resource instance id. Corrected by AMEND-9: the original table implied absence.** |
| `stretchr/testify` (as a **runtime** dep) | — | **test-only**; see the approved test-only set. Because `.golangci.yml:5` sets `tests: false`, test code is unlinted, but it is still in the test module graph — pinned and reviewed. |
| `go.uber.org/mock` (as a **runtime** dep) | test doubles | **test-only**; build-tagged `tools.go`. |
| A hand-rolled goroutine-leak checker | `MOCK-107.7` | Rejected in favour of `go.uber.org/goleak` (test-only). Reasoning in *The two tiers*, above. |

### The gRPC correction  (added by AMEND-9, 2026-09-04)

**This ADR was factually wrong.** It admitted `otlptracehttp` on the stated basis that it pulls
"`protobuf` … but **not** gRPC", and recorded "no gRPC" as a positive consequence. Both statements
are false for the pinned version, and were false at the time they were written.

**Verified fact.** With `go.opentelemetry.io/otel@v1.46.0`:

```
$ go mod why google.golang.org/grpc
github.com/vyrodovalexey/mcp-mock-server/internal/obs
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp/internal/otlpconfig
google.golang.org/grpc
```

**Root cause, read from the module source.** `otlptracehttp/internal/otlpconfig/options.go:19-23`
imports `google.golang.org/grpc`, `grpc/backoff`, `grpc/credentials`, `grpc/credentials/insecure`
and `grpc/encoding/gzip`. `otlpconfig` is the configuration struct **shared** between the HTTP and
gRPC exporters, so the HTTP exporter carries gRPC-typed option fields it never uses over the wire.
This is a code-sharing artifact upstream, **not** a functional need: `otlptracehttp` opens no gRPC
connection and speaks OTLP over HTTP as documented.

**Measured cost** (`CGO_ENABLED=0 go build -trimpath`, symbol sizes via `go tool nm -size` on the
linked binary):

| Tree | Linked symbol weight | Packages in `go list -deps ./cmd/...` |
|---|---|---|
| `google.golang.org/grpc` | ≈ 513 KB | 66 |
| `google.golang.org/protobuf` | ≈ 785 KB (unavoidable — OTLP is protobuf-encoded) | — |
| `go.opentelemetry.io` | ≈ 467 KB | — |
| `grpc-gateway/v2`, `genproto/googleapis/*` | ≈ 9 KB combined | — |

Total binary: ≈ 17.1 MB.

**What this does and does not cost.**

- **`MOCK-101` (single statically linked binary, no runtime dependencies): unaffected.** gRPC is
  compiled in and never dialed. There is no shared library, no sidecar, no new listener, no new
  goroutine. The binary is ≈ 0.5 MB larger than it would otherwise be. Correctness is untouched.
- **`MOCK-107` (mcpmock lands in the hub's `go.mod`): this is the real cost, and it is the exact
  cost this ADR exists to control.** `grpc`, `grpc-gateway/v2`, `genproto/googleapis/{api,rpc}`,
  `cenkalti/backoff/v5`, `google/uuid` and `golang.org/x/{net,text}` all enter every consumer's
  module graph, SBOM, Trivy scan and `govulncheck` surface. gRPC is, as this ADR itself said, among
  the highest-CVE-churn trees in the ecosystem — and we now carry it while claiming we do not.

**Decision: amend the ADR, keep `otlptracehttp`.** The alternatives were costed:

| Option | Verdict |
|---|---|
| **(a) Keep `otlptracehttp`, amend this ADR to tell the truth** | **Chosen.** OTLP tracing is user-mandated. The gRPC tree is inert at runtime, and ≈ 0.5 MB is a proportionate price for a required capability. The defect was the false claim, not the dependency. |
| (b) Replace with a hand-rolled OTLP/HTTP exporter | Rejected. Still needs `protobuf` (the wire encoding), re-implements retry/backoff/partial-success semantics, and is precisely *Option 1* ("zero third-party dependencies, absolutely") already rejected above as months of work for a worse result. |
| (c) Pin an older/newer gRPC-free otel line | **Not chosen, and deliberately not asserted either way.** Whether any supported otel release ships a gRPC-free `otlpconfig` has **not** been verified — this document makes no claim about versions it has not read. Recorded as an open question (`13-open-questions`, AMEND-9 follow-up); if such a line exists and is supported, revisiting is cheap and strictly better. |
| (d) Defer the OTLP exporter to a later phase | Rejected: `internal/obs` is Phase 1 delivered and green; removing tracing now to reduce a consumer's SBOM is a worse trade than disclosing it. |

**Standing rule this establishes.** A dependency's transitive tree is part of what is admitted.
Criterion 5 ("its own transitive tree is inspected and listed") was satisfied in *form* and failed
in *substance*, because the inspection was asserted rather than executed. Inspection is now
mechanical — see *Enforcement*, below.

### Enforcement

- `make deps-check` is **two-tier** (AMEND-5):
  - `go list -deps ./...` — the runtime graph — must contain nothing outside the *approved runtime
    set*. A test-only module appearing here is a **failure**, because it means a `_test.go`-only
    dependency leaked into production code.
  - `go list -deps -test ./...` minus the runtime graph — the test-only graph — must contain
    nothing outside the *approved test-only set*.
  - Both lists live in one file so the check and this ADR cannot drift; a test asserts the file
    matches the two tables above.

- **Transitive enforcement (added by AMEND-9). `deps-check` gains a third tier — in scope, and
  required.** The gate that should have fired did not, because `hack/deps-check` validates only
  *direct* dependencies against the approved list, and gRPC is transitive. That is a structural
  blind spot, not an oversight, so the answer is to close it structurally rather than to promise
  more careful reading.

  The third tier is deliberately **not** a full allowlist of the transitive graph. A 34-root graph
  churns on every otel or prometheus patch bump, and a gate that fails on every routine upgrade
  gets disabled. Instead, two mechanisms with different jobs:

  | Mechanism | What it does | Why this shape |
  |---|---|---|
  | **Transitive denylist** | `go list -deps ./...` must not contain any module in a small, explicitly-reasoned forbidden set. Seeded with **`google.golang.org/grpc` — allowed *only* via the single documented path `otlptracehttp → internal/otlpconfig`**, asserted by matching `go mod why` output. Any *second* path to gRPC, or gRPC arriving from anywhere else, fails the build. | Catches the class that hurt us — a heavyweight tree entering silently — without failing on benign version drift. The exception is pinned to its justification, so removing OTLP removes the exception. |
  | **Transitive graph snapshot** | The full sorted `go list -deps ./...` module set is committed as a golden file. Any addition or removal fails `deps-check` with a diff, and the fix is to review the diff and re-bless it in the same commit. | Makes "a new dependency requires an ADR amendment" *mechanical*. A reviewer sees the new module in the PR diff instead of never seeing it. Cheap to re-bless, impossible to miss. |

  Both live alongside the existing two tiers in `hack/deps-check`. **The snapshot's first blessing
  must record today's graph including gRPC**, so the amendment and the gate agree from the outset.

- **Policy belongs here, not in the tool.** `hack/deps-check/main.go:102` carries a policy call
  ("`uuid` may only be an INDIRECT dep") that appears in no normative document. AMEND-9 ratifies
  that rule and writes it down: `google/uuid` is permitted **indirect-only** (via `otel/sdk`), and
  a direct import of it is a failure. Any future policy call must be added to this ADR in the same
  change that adds it to the tool.

- `make deps-report` regenerates a table in `docs/dependencies.md` with versions, for review.
- Dependency risk is reported as **versions + a scan recommendation** — `govulncheck ./...`
  (`ci.yml:71`) and Trivy on the image (`ci.yml:644`). No CVE claims are made in this document.
- A new dependency requires an ADR amendment, not just a `go get`.

### Build flags

`CGO_ENABLED=0`, `-trimpath`, `-ldflags="-s -w -X main.version=… -X main.commit=… -X main.date=…"`.
Static by construction; verified in CI with `file bin/mcpmock | grep -q "statically linked"` on
Linux and a `ldd` check that reports "not a dynamic executable".

## Options considered

1. **Zero third-party dependencies, absolutely** — rejected: re-implementing Prometheus exposition,
   OTLP encoding and a JSON Schema validator is months of work and the results would be worse. The
   JSON Schema validator in particular is load-bearing for `MOCK-225`, whose whole point is that
   validation cost and `$ref` behaviour must be *realistic*.
2. **Take whatever is convenient** — rejected: `MOCK-107` externalises the cost onto the hub team.
3. **Vendored dependencies (`vendor/`)** — considered. `.golangci.yml:61` already excludes
   `vendor` from linting, suggesting the convention anticipates it. **Not adopted**: `go.sum` +
   module proxy gives the same reproducibility, and vendoring makes `govulncheck` results noisier.
   Reversible; if the CI environment becomes network-restricted, vendor.
4. **The screened set above (chosen).**

## Consequences

**Positive.** Six *direct* runtime modules, no cgo. `MOCK-101` is satisfied both literally and in
spirit: one statically linked binary, no runtime dependencies, nothing dialed that is not
configured. The two-tier split (AMEND-5) makes "test-only" a checked property rather than an
intention, and lets the project take test-quality dependencies like `goleak` without charging the
hub for them. The transitive snapshot (AMEND-9) makes the *whole* graph a reviewed artifact rather
than an assertion.

**Negative.** `internal/jwtmini` and the `flag`-based CLI are code we own and must test. The
prometheus and otel trees are still the largest part of the graph and the most likely source of a
Trivy finding — accepted, because both are explicitly required.

**Negative (AMEND-9 — corrected; this previously read "no gRPC", which was false).** Choosing OTLP
tracing means **`google.golang.org/grpc` is in the runtime dependency graph**, together with
`grpc-gateway/v2`, `genproto/googleapis/{api,rpc}`, `cenkalti/backoff/v5`, `google/uuid` and
`golang.org/x/{net,text}`. It is inert — no gRPC connection is ever opened — but it is **not free**:
≈ 0.5 MB of linked binary, 66 packages, and full presence in every consumer's `go.mod`, SBOM,
Trivy scan and `govulncheck` surface. This is the `MOCK-107` cost, disclosed rather than denied.
Anyone taking mcpmock as a library inherits the gRPC CVE stream for as long as OTLP tracing is a
requirement.

**Forecloses.** Using any OTel exporter other than OTLP/HTTP without revisiting this ADR. It does
**not** foreclose a gRPC-based control API on dependency grounds any more — gRPC is already
present — but ADR-015 forecloses it on interface-design grounds, which is the reason that actually
matters and now the only one standing.

**Known inaccuracy remaining, flagged not fixed.** `golang.org/x/sync` appears in the approved
runtime set above but is **absent** from `go.mod` and from `go list -deps ./...` — the shutdown and
worker-pool code was written against stdlib `sync` instead. It is approved-but-unused. Left in the
table as pre-approved (taking it later needs no amendment); the transitive snapshot will show it if
it is ever actually taken.
