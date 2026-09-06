---
title: Public Go Library API — package mcpmock
status: draft
version: 0.1.0
updated: 2026-09-04
stability: v0 — may change until ADR-019 is ratified
requirements: MOCK-107, MOCK-104, MOCK-702, MOCK-704
---

# Contract: `package mcpmock`

This is a **published contract** (ADR-001). It is deliberately small: lifecycle, reach an
instance, get a journal, mutate at runtime. Everything else is reachable through `Control`.

Design rules this API obeys:
- No process globals are touched (ADR-007). Two `Server`s coexist in one test binary.
- No `init()` work; `New` does not bind listeners, `Start` does (`MOCK-107`).
- `context.Context` is the first parameter of anything that can block.
- Errors are wrapped sentinels, inspectable with `errors.Is`.

---

## 1. Lifecycle

```go
// Package mcpmock provides a programmable MCP server emulator usable as a test
// harness, either as an in-process library or as a standalone binary.
package mcpmock

// Server is a running (or startable) set of logical MCP server instances sharing
// one process, one seed tree and one observability bundle.
//
// A Server is safe for concurrent use. Two Servers may run simultaneously in one
// process; they share no state.
type Server struct{ /* unexported */ }

// New constructs a Server from options. It performs no I/O, binds no listener and
// starts no goroutine. It is safe to call New and never call Start.
func New(opts ...Option) (*Server, error)

// NewFromFile loads and composes a scenario (MOCK-701, MOCK-703), validates it
// against the embedded JSON Schema, and constructs a Server.
func NewFromFile(path string, opts ...Option) (*Server, error)

// NewFromScenario constructs a Server from an already-decoded scenario document.
// The scenario is validated unless WithoutValidation was supplied.
func NewFromScenario(s *scenario.Document, opts ...Option) (*Server, error)

// Start binds listeners and begins serving. It returns once every listener is
// accepting, or on the first bind error. The provided context governs startup
// only; use Close or Shutdown to stop.
func (s *Server) Start(ctx context.Context) error

// Shutdown stops accepting, closes open streams according to the configured
// shutdown mode, and waits for in-flight requests up to the context deadline.
func (s *Server) Shutdown(ctx context.Context) error

// Close is Shutdown with a short default deadline, suitable for t.Cleanup.
func (s *Server) Close() error
```

### Test convenience

```go
// StartTest constructs and starts a Server bound to ephemeral loopback ports,
// registers Close with tb.Cleanup, and fails the test on any startup error.
//
// It is the intended entry point for hub test suites (MOCK-107).
func StartTest(tb testing.TB, opts ...Option) *Server
```

`StartTest` deliberately takes `testing.TB`, not `*testing.T`, so it works from benchmarks and
fuzz targets. It does **not** import `testing` into the non-test build — `assert` and `StartTest`
live in files that are always compiled, which is acceptable because `testing` is stdlib and
importing it outside a test only costs a small binary increase. (If that proves objectionable,
`StartTest` moves to `mcpmock/mcpmocktest`; recorded as a known future split.)

---

## 2. Reaching instances

```go
// Instances returns every logical instance in stable configuration order.
func (s *Server) Instances() []*Instance

// Instance returns the named instance.
func (s *Server) Instance(name string) (*Instance, bool)

// ControlURL is the base URL of the HTTP control API, or "" if disabled.
func (s *Server) ControlURL() string

// ControlSocket is the path of the unix-socket control listener, or "".
func (s *Server) ControlSocket() string

// Control returns an in-process implementation of the control operations.
// It performs no HTTP round trip (MOCK-104, ADR-015).
func (s *Server) Control() Control

// Seed returns the effective root seed (MOCK-704).
func (s *Server) Seed() uint64

// Registerer returns the Prometheus registerer backing this Server's metrics.
func (s *Server) Registerer() prometheus.Registerer
```

```go
// Instance is one logical MCP server: its own scenario, catalogue, journal,
// fault set, authorization configuration and seed subtree.
type Instance struct{ /* unexported */ }

func (i *Instance) Name() string

// URL is the base URL a client should POST to for this instance, including
// scheme, host, port and mount path. Empty for stdio-only instances.
func (i *Instance) URL() string

// Journal returns a read-only view over this instance's request journal.
// The view is a live handle; Filter returns a derived view without copying.
func (i *Instance) Journal() journalapi.View

// Control returns control operations scoped to this instance.
func (i *Instance) Control() InstanceControl

// Generation returns the current configuration snapshot generation (ADR-014),
// which is also recorded on every journal record.
func (i *Instance) Generation() uint64
```

---

## 3. Options

```go
type Option interface{ /* unexported; implemented by functions below */ }

// --- identity and determinism ---
func WithSeed(seed uint64) Option                  // MOCK-704
func WithScenarioRoot(dir string) Option           // bounds `extends` path resolution
func WithoutValidation() Option                    // skip JSON Schema validation (startup budget)
func WithOverlay(docs ...*scenario.Document) Option // programmatic overlay, applied last (MOCK-703)

// --- listeners ---
func WithAddr(addr string) Option                  // MCP listener address; ":0" for ephemeral
func WithListener(l net.Listener) Option           // supply your own; disables WithAddr
func WithTLS(cfg *tls.Config) Option               // MOCK-106
func WithStdio(in io.Reader, out io.Writer) Option // ADR-011: never defaults to os.Stdin/os.Stdout
func WithControlAddr(addr string) Option
func WithControlSocket(path string) Option
func WithControlToken(token string) Option
func WithoutControl() Option
func WithObservabilityAddr(addr string) Option     // /metrics, /healthz, /readyz

// --- observability (ADR-016) ---
func WithLogger(l *slog.Logger) Option             // must write to stderr in stdio mode
func WithRegisterer(r prometheus.Registerer) Option
func WithTracerProvider(tp trace.TracerProvider) Option
func WithGlobalOTel() Option                       // opt in to otel.SetTracerProvider; cmd/ only

// --- journal (ADR-005) ---
func WithJournal(cfg journalapi.Config) Option
func WithoutJournal() Option                       // MOCK-901 measurement mode

// --- safety ---
func WithAllowProcessExit() Option                 // required for MOCK-508 restart faults in-process
func WithSafeMode(on bool) Option                  // ADR-018 hostile-corpus gate
```

Options are applied in order; later wins. An option that conflicts with the scenario file wins
over the file, and the effective configuration is logged at `INFO` at startup.

---

## 4. Control

`Control` is the single operation set shared by the HTTP API, the CLI and in-process callers
(ADR-015). It is **consumer-only**: callers use it, they do not implement it, and methods may be
added in minor versions. This is stated in the doc comment.

```go
type Control interface {
    Instances(ctx context.Context) ([]InstanceInfo, error)
    Instance(ctx context.Context, name string) (InstanceInfo, error)
    AddInstance(ctx context.Context, spec scenario.InstanceSpec) (InstanceInfo, error)
    RemoveInstance(ctx context.Context, name string) error
    Seed(ctx context.Context) (uint64, error)
    Health(ctx context.Context) (Health, error)
    For(name string) InstanceControl
}

type InstanceControl interface {
    // MOCK-702 mutation
    SetEra(ctx context.Context, era string) error
    PatchCatalogue(ctx context.Context, patches []scenario.CataloguePatch) error
    RegenerateCatalogue(ctx context.Context, p scenario.CatalogueParams) error
    Notify(ctx context.Context, n scenario.NotificationSpec) error
    RotateCredentials(ctx context.Context, c scenario.CredentialSpec) error
    CloseStreams(ctx context.Context, sel StreamSelector, mode CloseMode) error
    InvalidateSession(ctx context.Context, sessionID string) error   // MOCK-305

    // Faults (MOCK-501..508)
    Faults(ctx context.Context) ([]FaultStatus, error)
    SetFaults(ctx context.Context, rules []scenario.FaultRule) error
    PatchFault(ctx context.Context, ruleID string, p scenario.FaultPatch) error
    ArmFault(ctx context.Context, ruleID string, times int) error

    // Journal (MOCK-602, MOCK-605)
    Journal(ctx context.Context, q journalapi.Query) (journalapi.Page, error)
    JournalStream(ctx context.Context, q journalapi.Query) (iter.Seq2[journalapi.Record, error], error)
    Correlations(ctx context.Context, q journalapi.Query) ([]journalapi.Correlation, error)
    ClearJournal(ctx context.Context) error

    // MRTR / legacy client-bound calls (MOCK-302)
    Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}
```

---

## 5. Error semantics

All errors returned by this package wrap one of:

| Sentinel | Meaning | Typical caller action |
|---|---|---|
| `ErrValidation` | Scenario failed schema or semantic validation. Wrapped value implements `ValidationError` with `Pointer()`, `File()`, `Line()`, `Keyword()`. | Fix the scenario. Non-retryable. |
| `ErrNotFound` | Named instance / fault rule / session does not exist. | Non-retryable. |
| `ErrAlreadyExists` | Instance name or mount path collides. | Non-retryable. |
| `ErrConflict` | Mutation rejected because of current state (e.g. `listener: own` limit exceeded). | Non-retryable. |
| `ErrRateLimited` | Control mutation rate exceeded (ADR-014). Wrapped value exposes `RetryAfter()`. | **Retryable** after the stated delay. |
| `ErrNotStarted` / `ErrClosed` | Lifecycle misuse. | Non-retryable; programming error. |
| `ErrUnsupported` | Operation is not available in this mode (e.g. process exit without `WithAllowProcessExit`, ADR-009). | Non-retryable. |
| `ErrControlAuth` | Control API rejected the credential. | Non-retryable. |
| `ErrTransport` | Underlying I/O failure on an HTTP or socket control client. | **Retryable.** |

```go
type ValidationError interface {
    error
    Pointer() string   // JSON Pointer into the composed document
    File() string      // source file, after composition provenance tracking
    Line() int
    Keyword() string   // failing JSON Schema keyword, or "" for semantic rules
}
```

`Start` returns a `*net.OpError`-wrapping error on bind failure, so `errors.As` to
`*net.OpError` works. Nothing in this package returns a bare `errors.New` string that callers
must match on.

---

## 6. Concurrency and lifetime guarantees

| Guarantee | Statement |
|---|---|
| `Server` methods | Safe for concurrent use after `New`. |
| `Instance` methods | Safe for concurrent use. |
| `journalapi.View` | Safe for concurrent use; `Filter` returns a new view and does not mutate the receiver. |
| Records returned by a `View` | **Copies.** A record handed to the caller is never mutated by the ring (ADR-005's seqlock retry happens before return). |
| `Control` | Safe for concurrent use; mutations are serialised per instance. |
| After `Close` | All methods return `ErrClosed`. Previously obtained `Record` values remain valid. |
| Goroutine leaks | `Close` returns only after every goroutine the `Server` started has exited. Asserted with `goleak` in `TestNoGoroutineLeak`. |

---

## 7. Minimal usage example

```go
func TestHubDoesNotForwardClientToken(t *testing.T) {
    mock := mcpmock.StartTest(t,
        mcpmock.WithSeed(42),
        mcpmock.WithScenarioRoot("testdata"),
    )
    up := mock.Instance("upstream-a")

    hub := startHubUnderTest(t, hubConfig{Upstream: up.URL()})
    hub.CallTool(t, "search", map[string]any{"q": "hello"})

    j := assert.T(t, up.Journal())
    j.AssertNoHeader("X-Client-Token")
    j.AssertHeaderNotValue("Authorization", "Bearer "+clientToken)
    j.AssertNoSessionHeaders()
    j.AssertHeaderMatchesBody()
}
```

---

## 8. Compatibility policy

- `v0.x`: any release may change this API. Changes are listed in `CHANGELOG.md` with a migration
  note. The module is `v0` deliberately until ADR-019 is ratified.
- From `v1`: additive only for structs and options; `Control` remains consumer-only and may gain
  methods; removals require a major version.
- `journalapi.Record` is the most sensitive type here, because golden files (`MOCK-604`) encode
  it. Its JSON representation is versioned by a `schemaVersion` field from day one.
