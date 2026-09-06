package mcpmock

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vyrodovalexey/mcp-mock-server/internal/config"
	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/stdio"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// closeTimeout is the short default deadline [Server.Close] uses for a
// t.Cleanup-friendly shutdown. It is long enough to drain a well-behaved
// in-flight request but short enough that a stuck handler does not hang a test
// suite past its own timeout.
const closeTimeout = 5 * time.Second

// Server is a running (or startable) set of logical MCP server instances
// sharing one process, one seed tree and one observability bundle
// (contracts/library-api.md §1). It is the lifecycle facade of ADR-001.
//
// A Server touches no process-global state (ADR-007): its registry, metrics,
// logger and listeners are all owned instance state. Two Servers may therefore
// run simultaneously in one test binary — a parallel hub test that starts a
// fresh fleet per subtest works — because they share nothing.
//
// New performs no I/O, binds no listener and starts no goroutine; Start binds
// listeners and begins serving; Shutdown or Close stops. A Server is safe for
// concurrent use after New. Construct one with [New], [NewFromFile] or
// [NewFromScenario].
type Server struct {
	// obs is the per-Server observability bundle: private registry, logger and
	// tracer, none of them process-global (ADR-007).
	bundle *obs.Bundle
	// logger is the facade's lifecycle logger (never stdout, ADR-011).
	logger loggerIface
	// seed is the effective root seed (MOCK-704).
	seed uint64
	// registry maps mount path to *instance.Instance for the HTTP router.
	registry *instance.Registry
	// instances are the logical instances in stable configuration order
	// (index == id), the order [Server.Instances] returns.
	instances []*Instance

	// httpSrv is the shared HTTP listener when the HTTP transport is enabled,
	// else nil (ADR-007 §3: one listener, prefix routing).
	httpSrv *httpx.Server
	// httpErr receives the Serve goroutine's terminal error.
	httpErr chan error

	// stdioTransport is the single stdio transport when enabled, else nil.
	stdioTransport *stdio.Transport
	// stdioCancel cancels the stdio Serve loop; stdioDone closes when Serve
	// returns. The serve context itself is a local in Start (never stored on
	// the Server), derived-but-detached from the startup context so Serve
	// outlives Start yet contextcheck sees an inherited context.
	stdioCancel context.CancelFunc
	stdioDone   chan struct{}

	// obsSrv is the observability HTTP server when enabled, else nil.
	obsSrv    *http.Server
	obsLn     net.Listener
	obsHealth *obs.HealthState

	// ctlSrv is the control-plane server (HTTP and/or unix socket) when the
	// control plane is enabled, else nil (ADR-015, MOCK-104). ctlURL/ctlSocket
	// cache its resolved endpoints for [Server.ControlURL]/[Server.ControlSocket].
	ctlSrv    *control.Server
	ctlURL    string
	ctlSocket string

	// seedSource records where the effective seed came from ("flag" or
	// "random"), reported by GET /v1/seed (MOCK-704.2).
	seedSource string
	// startedAt is the construction time, used for the control health uptime.
	startedAt time.Time

	// mu guards the lifecycle state transitions (started/closed) so concurrent
	// Start/Shutdown/Close calls are serialized; it is not on any request path.
	mu      sync.Mutex
	started bool
	closed  bool
}

// loggerIface is the tiny slice of *slog.Logger the facade uses for lifecycle
// records. Naming it keeps the struct field decoupled from a concrete type in
// the rare places a nil logger must be tolerated.
type loggerIface interface {
	Info(msg string, args ...any)
}

// Instance is one logical MCP server: its own scenario, catalogue, journal and
// seed subtree (contracts/library-api.md §2). It is a thin facade over the
// internal instance, exposing only the published surface. Its methods are safe
// for concurrent use.
type Instance struct {
	inst *instance.Instance
	// server is the owning Server, so [Instance.Control] can reach the
	// per-instance control operations without a package global (ADR-007).
	server *Server
	// url is the base URL a client POSTs to for this instance, or "" for a
	// stdio-only instance. It is filled in once the HTTP listener binds.
	url string
}

// New constructs a Server from a default (empty) scenario plus options. It
// performs no I/O, binds no listener and starts no goroutine, so it is safe to
// call New and never call Start (contracts/library-api.md §1, MOCK-107.6).
//
// With no scenario supplied the Server has a single default instance mounted at
// the default MCP path, which is enough for a lifecycle or two-servers test; a
// real scenario comes through [NewFromFile] or [NewFromScenario].
func New(opts ...Option) (*Server, error) {
	doc := defaultScenarioDocument()
	return newServer(&doc, opts...)
}

// NewFromFile loads and composes the scenario at path (MOCK-701, MOCK-703),
// validates it against the embedded JSON Schema, and constructs a Server. A
// scenario root supplied with [WithScenarioRoot] bounds extends resolution;
// absent one, the file's own directory is the root.
//
// A validation failure is returned wrapped in [ErrValidation]; an I/O failure
// (missing or unreadable file) is returned as a plain wrapped error, so a
// caller distinguishes the two with errors.Is.
func NewFromFile(path string, opts ...Option) (*Server, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt.apply(o)
	}
	root, err := scenarioRootFor(o.scenarioRoot, path)
	if err != nil {
		return nil, err
	}
	doc, err := config.ComposeFile(root, path)
	if err != nil {
		return nil, wrapValidation(err)
	}
	return newServerFromDoc(&doc, o)
}

// NewFromScenario constructs a Server from an already-decoded scenario document.
// The scenario is validated unless [WithoutValidation] was supplied. Overlays
// added with [WithOverlay] are merged over s in order before construction.
func NewFromScenario(s *scenario.Document, opts ...Option) (*Server, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil scenario document", ErrValidation)
	}
	return newServer(s, opts...)
}

// newServer applies opts, composes overlays over base, validates, and builds.
func newServer(base *scenario.Document, opts ...Option) (*Server, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt.apply(o)
	}
	doc, err := composeOverlays(base, o)
	if err != nil {
		return nil, err
	}
	return newServerFromDoc(doc, o)
}

// composeOverlays merges any WithOverlay documents over base and validates the
// result unless validation was skipped. It returns the effective document to
// build instances from.
func composeOverlays(base *scenario.Document, o *options) (*scenario.Document, error) {
	if len(o.overlays) == 0 {
		if !o.skipValidation {
			if err := validateDoc(base); err != nil {
				return nil, err
			}
		}
		return base, nil
	}
	// Compose base + overlays as JSON trees so absent-vs-zero merge semantics
	// hold (config.Compose owns the merge algebra, MOCK-703).
	baseJSON, err := marshalDoc(base)
	if err != nil {
		return nil, err
	}
	overlayJSON := make([][]byte, 0, len(o.overlays))
	for _, ov := range o.overlays {
		b, mErr := marshalDoc(ov)
		if mErr != nil {
			return nil, mErr
		}
		overlayJSON = append(overlayJSON, b)
	}
	composed, err := config.Compose("<overlay>", baseJSON, overlayJSON...)
	if err != nil {
		return nil, wrapValidation(err)
	}
	return &composed, nil
}

// newServerFromDoc is the shared construction body: it resolves the seed, builds
// the observability bundle, constructs every instance and wires the transports
// WITHOUT binding a listener or starting a goroutine (that is Start's job).
func newServerFromDoc(doc *scenario.Document, o *options) (*Server, error) {
	seed := effectiveSeed(o)
	bundle, err := obs.New(obs.Config{Registry: o.registerer})
	if err != nil {
		return nil, fmt.Errorf("mcpmock: observability init: %w", err)
	}
	bundle.Metrics().SetEffectiveSeed(seed)

	specs, err := instanceSpecs(doc)
	if err != nil {
		return nil, err
	}

	s := &Server{
		bundle:     bundle,
		logger:     o.logger,
		seed:       seed,
		seedSource: seedSourceFor(o),
		startedAt:  time.Now(),
		registry:   instance.NewRegistry(),
	}
	root := determinism.Root(seed)
	for i, is := range specs {
		if err := s.addInstance(i, is, root, o); err != nil {
			return nil, err
		}
	}
	bundle.Metrics().SetInstances(len(s.instances))

	if err := s.buildTransports(o); err != nil {
		return nil, err
	}
	return s, nil
}

// instanceSpec pairs a resolved instance name/mount path with its validated
// scenario spec, so newServerFromDoc builds each instance uniformly whether the
// document is a single Scenario or a multi-instance Fleet.
type instanceSpec struct {
	name      string
	mountPath string
	spec      scenario.InstanceSpec
}

// addInstance constructs one instance from is, registers it and mounts it. It
// refuses a duplicate name or mount path with [ErrAlreadyExists] rather than
// silently overwriting, because a collision is a configuration error (ADR-007).
func (s *Server) addInstance(id int, is instanceSpec, root determinism.Key, o *options) error {
	metrics := s.bundle.NewInstanceMetrics(is.name)
	inst := instance.New(instance.Config{
		Name:          is.name,
		MountPath:     is.mountPath,
		ID:            id,
		Root:          root,
		Spec:          applyJournalOptions(is.spec, o),
		Metrics:       metrics,
		MetaValidator: metaValidator(is.spec, metrics),
	})
	if !s.registry.Add(inst) {
		return fmt.Errorf("%w: mount path %q", ErrAlreadyExists, inst.MountPath())
	}
	s.instances = append(s.instances, &Instance{inst: inst, server: s})
	return nil
}

// metaValidator builds the stage-4 _meta validator (MOCK-203, AMEND-6) for an
// instance from its scenario switches.validateMeta mode, recording the
// mcpmock_meta_validation_total metric through the instance's pre-resolved
// metric handles. This is the composition-root wiring that closes the
// cross-task seam: internal/instance holds only the narrower engine.Metrics
// surface and must not import internal/modern, and internal/modern must not
// import internal/obs, so the facade — which legitimately imports all three —
// is the one place that can bind the concrete *obs.InstanceMetrics recorder to
// the modern validator and inject the result into the instance (architecture.md
// §6.1 layering).
//
// An absent switches.validateMeta yields the empty mode string, which
// modern.NewMetaValidator folds to "strict" — the schema default and the
// conformant-server behavior MOCK-203 requires. rec may be nil (no metrics
// configured), which disables metric recording without affecting the journal
// outcome.
func metaValidator(spec scenario.InstanceSpec, rec modern.MetaMetricRecorder) engine.MetaValidator {
	var mode string
	if sw := spec.Switches; sw != nil && sw.ValidateMeta != nil {
		mode = *sw.ValidateMeta
	}
	return modern.NewMetaValidator(mode, rec)
}

// buildTransports constructs the HTTP and/or stdio transports around the built
// instances, binding the listener but starting no serving goroutine. A Server
// with neither transport enabled is a valid, in-process-only Server (its
// journals are still reachable), which is what the fastest embedded start uses.
func (s *Server) buildTransports(o *options) error {
	if o.httpEnabled {
		if err := s.buildHTTP(o); err != nil {
			return err
		}
	}
	if o.stdioEnabled {
		if err := s.buildStdio(o); err != nil {
			return err
		}
	}
	if o.obsEnabled {
		if err := s.buildObs(o); err != nil {
			return err
		}
	}
	if !o.controlDisabled {
		if err := s.buildControl(o); err != nil {
			return err
		}
	}
	return nil
}

// buildHTTP builds the shared HTTP listener and mounts every instance's
// pipeline behind the prefix router (ADR-007 §3). New binds the listener (so an
// ephemeral ":0" resolves to a concrete port and URL) but starts no goroutine.
func (s *Server) buildHTTP(o *options) error {
	mounts := make([]*httpx.Mount, 0, len(s.instances))
	for _, in := range s.instances {
		mounts = append(mounts, &httpx.Mount{
			Path:     mountPathFor(in.inst),
			Instance: in.inst,
			Pipeline: buildPipeline(s.selfCheckLogger(in.inst.Name())),
		})
	}
	cfg := httpx.Config{Mounts: mounts}
	if o.listener != nil {
		cfg.Listener = o.listener
	} else {
		cfg.Addr = o.addr
	}
	srv, err := httpx.New(cfg)
	if err != nil {
		return fmt.Errorf("mcpmock: http listener: %w", err)
	}
	s.httpSrv = srv
	base := httpBaseURL(srv.Addr())
	for _, in := range s.instances {
		in.url = base + mountPathFor(in.inst)
	}
	return nil
}

// buildStdio builds the single stdio transport (Phase 1 stdio is
// single-instance). It uses the explicit streams from WithStdio and never
// os.Stdout (ADR-011), preserving cmd/mcpmock's hijack seam.
func (s *Server) buildStdio(o *options) error {
	if len(s.instances) == 0 {
		return fmt.Errorf("%w: stdio requires an instance", ErrUnsupported)
	}
	in := s.instances[0].inst
	tr, err := stdio.New(stdio.Config{
		In:       o.stdioIn,
		Out:      o.stdioOut,
		Instance: in,
		Pipeline: buildPipeline(s.selfCheckLogger(in.Name())),
	})
	if err != nil {
		return fmt.Errorf("mcpmock: stdio transport: %w", err)
	}
	s.stdioTransport = tr
	s.stdioDone = make(chan struct{})
	return nil
}

// buildObs binds the observability listener (metrics/health) but starts no
// serving goroutine; Start attaches the handler and serves it.
func (s *Server) buildObs(o *options) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", o.observabilityAddr)
	if err != nil {
		return fmt.Errorf("mcpmock: observability listener: %w", err)
	}
	s.obsLn = ln
	s.obsHealth = obs.NewHealthState()
	s.obsSrv = &http.Server{
		Handler:           s.bundle.Handler(s.obsHealth),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return nil
}

// buildControl binds the control-plane listeners (ADR-015, MOCK-104): a TCP
// listener on a separate port from every MCP listener, and/or a unix-domain
// socket. It binds but starts no serving goroutine; Start serves and Shutdown
// unlinks the socket. A non-loopback TCP bind without a token is refused here,
// at construction, before anything accepts (MOCK-104.5).
//
// Listener selection: a TCP listener is bound when the caller set a control
// address or when the HTTP transport is enabled (loopback default); a unix
// socket is bound when the caller set a socket path or when the stdio transport
// is enabled (ADR-011: stdin/stdout belong to the protocol, so stdio mode reaches
// control only through the socket).
func (s *Server) buildControl(o *options) error {
	wantTCP := o.controlAddrSet || o.httpEnabled
	wantUDS := o.controlSocketSet || o.stdioEnabled
	if !wantTCP && !wantUDS {
		return nil
	}
	cfg := control.Config{Handler: control.NewHandler(s.Control(), control.OpenAPISpec)}
	if wantTCP {
		ln, addr, err := s.bindControlTCP(o)
		if err != nil {
			return err
		}
		cfg.TCPListener = ln
		// A non-loopback TCP listener is the reachable, adversarial surface, so
		// every request on it must present the bearer token (security.md §2.1;
		// CheckBind already refused this bind unless a token was configured).
		// Loopback and the unix socket are served without per-request auth, per
		// the contract's bearerAuth condition. REV-001.
		if control.RequiresAuth(addr) {
			cfg.TCPRequiresAuth = true
			cfg.Token = o.controlToken
		}
		s.ctlURL = "http://" + ln.Addr().String()
	}
	if wantUDS {
		ln, path, err := bindControlUDS(o)
		if err != nil {
			return err
		}
		cfg.UnixListener = ln
		cfg.SocketPath = path
		s.ctlSocket = path
	}
	s.ctlSrv = control.New(cfg)
	return nil
}

// bindControlTCP resolves the control TCP address (an explicit one, else an
// ephemeral loopback port), refuses an unsafe non-loopback bind (MOCK-104.5),
// and binds the listener.
func (s *Server) bindControlTCP(o *options) (net.Listener, string, error) {
	addr := o.controlAddr
	if !o.controlAddrSet {
		addr = "127.0.0.1:0"
	}
	if err := control.CheckBind(addr, o.controlTokenSet && o.controlToken != ""); err != nil {
		return nil, "", fmt.Errorf("mcpmock: control bind: %w", err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("mcpmock: control listener: %w", err)
	}
	return ln, addr, nil
}

// bindControlUDS resolves the control socket path (AMEND-7 precedence and
// fallbacks) and binds it with the probe-before-unlink lifecycle and 0600 mode.
func bindControlUDS(o *options) (net.Listener, string, error) {
	override := control.SocketOverride{Path: o.controlSocket, Set: o.controlSocketSet}
	path, _, err := control.ResolveSocketPath(override, os.Getenv, os.Getpid())
	if err != nil {
		return nil, "", fmt.Errorf("mcpmock: control socket path: %w", err)
	}
	ln, err := control.ListenUDS(path)
	if err != nil {
		return nil, "", fmt.Errorf("mcpmock: control socket: %w", err)
	}
	return ln, path, nil
}

// seedSourceFor reports where the effective seed came from for GET /v1/seed
// (MOCK-704.2): "flag" when the caller supplied one, else "random".
func seedSourceFor(o *options) string {
	if o.seedSet {
		return seedSourceFlag
	}
	return seedSourceRandom
}

// buildPipeline builds an engine registry with the three Phase 1 handlers and
// wraps it in a pipeline. Each instance gets its own pipeline; the handlers are
// stateless, so this is cheap and shares no mutable state.
//
// log is the per-instance ERROR logger the MOCK-201.4 self-check names the schema
// path through on a rejection (switches.selfCheck). It is bound here — the facade
// is the one layer that legitimately imports internal/obs and internal/modern
// (architecture.md §6.1), so it is where a *slog.Logger reaches the modern
// self-check without modern importing obs. A nil logger disables only the log
// line; the -32603 rejection and its journalled reason are unaffected.
func buildPipeline(log modern.SelfCheckLogger) *engine.Pipeline {
	reg := engine.NewRegistry()
	modern.RegisterHandlersWithLogger(reg, modern.BuiltinConfig{}, log)
	return engine.NewPipeline(reg)
}

// selfCheckLogger returns the per-instance ERROR logger the MOCK-201.4 self-check
// names the schema path through, or nil when the Server has no observability
// bundle. A nil bundle is the in-process-only / no-obs configuration; the
// self-check then still rejects a malformed result with -32603 and a journalled
// reason, only without the additional ERROR log line. The returned *slog.Logger
// satisfies modern.SelfCheckLogger, so the modern package never imports
// internal/obs (architecture.md §6.1).
func (s *Server) selfCheckLogger(name string) modern.SelfCheckLogger {
	if s.bundle == nil {
		return nil
	}
	return s.bundle.InstanceLogger(name)
}

// Start binds listeners and begins serving, returning once every listener is
// accepting or on the first bind error (contracts/library-api.md §1). The
// context governs startup only; use Shutdown or Close to stop. Calling Start
// twice, or after Close, returns a lifecycle sentinel.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.started {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if s.httpSrv != nil {
		s.httpErr = make(chan error, 1)
		go func() {
			err := s.httpSrv.Serve()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.httpErr <- err
				return
			}
			s.httpErr <- nil
		}()
	}
	if s.stdioTransport != nil {
		// The stdio Serve loop must outlive Start (the startup context governs
		// startup only), so its context is derived from ctx but detached from
		// ctx's cancellation with context.WithoutCancel, then given its own
		// cancel used at shutdown. This inherits ctx's values (contextcheck is
		// satisfied) without tying Serve's lifetime to the startup deadline,
		// and the context is a Start-local, never stored on the Server.
		serveCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		s.stdioCancel = cancel
		go func() {
			defer close(s.stdioDone)
			_ = s.stdioTransport.Serve(serveCtx)
		}()
	}
	if s.obsSrv != nil {
		go func() { _ = s.obsSrv.Serve(s.obsLn) }()
		s.obsHealth.SetReady(true)
	}
	if s.ctlSrv != nil {
		s.ctlSrv.Serve()
	}

	s.started = true
	// The control socket path is announced on the lifecycle logger at startup
	// (AMEND-7 "Announcement": structured-log key controlSocket on stderr).
	s.logger.Info("started",
		"seed", s.seed,
		"instances", len(s.instances),
		"controlUrl", s.ctlURL,
		"controlSocket", s.ctlSocket,
	)
	return nil
}

// Shutdown stops accepting, closes open streams and waits for in-flight
// requests up to the context deadline (contracts/library-api.md §1). It returns
// only after every goroutine the Server started has exited (MOCK-107.7), so a
// host test suite never inherits a leaked goroutine.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownLocked(ctx)
}

// shutdownLocked is the shared teardown, callers holding mu. It is idempotent:
// a second call after a completed shutdown is a no-op returning nil.
func (s *Server) shutdownLocked(ctx context.Context) error {
	if s.closed {
		return nil
	}
	var firstErr error
	firstErr = keepFirst(firstErr, s.shutdownHTTP(ctx))
	s.shutdownStdio()
	firstErr = keepFirst(firstErr, s.shutdownObs(ctx))
	if s.ctlSrv != nil {
		firstErr = keepFirst(firstErr, s.ctlSrv.Shutdown(ctx))
	}
	firstErr = keepFirst(firstErr, s.bundle.Shutdown(ctx))

	s.closed = true
	s.started = false
	return firstErr
}

// keepFirst returns first when it is non-nil, else err, so a teardown sequence
// keeps the first failure without a repeated nil-guard at every step.
func keepFirst(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

// shutdownHTTP stops the MCP HTTP server and joins its Serve goroutine, returning
// the first error observed. It is a no-op when the HTTP transport is disabled.
func (s *Server) shutdownHTTP(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	firstErr := s.httpSrv.Shutdown(ctx)
	return keepFirst(firstErr, <-s.httpErr)
}

// shutdownStdio cancels the stdio Serve loop and waits for it to exit. It is a
// no-op when the stdio transport is disabled.
func (s *Server) shutdownStdio() {
	if s.stdioTransport == nil {
		return
	}
	s.stdioCancel()
	<-s.stdioDone
}

// shutdownObs marks the observability listener not-ready and stops it, returning
// its shutdown error. It is a no-op when observability is disabled.
func (s *Server) shutdownObs(ctx context.Context) error {
	if s.obsSrv == nil {
		return nil
	}
	if s.obsHealth != nil {
		s.obsHealth.SetReady(false)
	}
	return s.obsSrv.Shutdown(ctx)
}

// Close is Shutdown with a short default deadline, suitable for t.Cleanup
// (contracts/library-api.md §1). It is safe to call more than once.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownLocked(ctx)
}

// Instances returns every logical instance in stable configuration order
// (contracts/library-api.md §2).
func (s *Server) Instances() []*Instance {
	out := make([]*Instance, len(s.instances))
	copy(out, s.instances)
	return out
}

// Instance returns the named instance and whether it exists
// (contracts/library-api.md §2).
func (s *Server) Instance(name string) (*Instance, bool) {
	for _, in := range s.instances {
		if in.inst.Name() == name {
			return in, true
		}
	}
	return nil, false
}

// Seed returns the effective root seed (MOCK-704, contracts/library-api.md §2).
func (s *Server) Seed() uint64 { return s.seed }

// Registerer returns the Prometheus registerer backing this Server's metrics
// (contracts/library-api.md §2). It is the Server's private registry, never the
// process-global default (ADR-007).
func (s *Server) Registerer() prometheus.Registerer {
	return s.bundle.Metrics().Registry()
}

// ControlURL is the base URL of the HTTP control API, or "" when no TCP control
// listener is running (ADR-015, MOCK-104). The control API binds a port distinct
// from every MCP listener; in HTTP mode a loopback control listener runs by
// default, and [WithControlAddr] sets the address. [WithoutControl] suppresses
// it. The in-process [Server.Control] works regardless.
func (s *Server) ControlURL() string { return s.ctlURL }

// ControlSocket is the path of the unix-socket control listener, or "" when none
// is running (ADR-015, AMEND-7). In stdio mode a 0600 owner-only socket is
// created at the documented path (or the [WithControlSocket] override) and its
// path is announced on the lifecycle logger at startup.
func (s *Server) ControlSocket() string { return s.ctlSocket }

// ObservabilityURL is the base URL of the observability listener serving
// /metrics, /healthz and /readyz (observability.md §5), or "" when no
// observability listener is running ([WithObservabilityAddr] absent). It is the
// concrete bound address, so an ephemeral ":0" resolves to a real port, letting
// cmd/mcpmock announce the metrics endpoint and a test discover it — the same
// pattern as [Server.ControlURL]. The listener is distinct from every MCP
// listener.
func (s *Server) ObservabilityURL() string {
	if s.obsLn == nil {
		return ""
	}
	return httpBaseURL(s.obsLn.Addr())
}

// Control returns control operations scoped to this instance (ADR-015,
// contracts/library-api.md §2), a convenience for [Server.Control]().For(name).
func (i *Instance) Control() InstanceControl {
	return &instanceController{s: i.server, name: i.inst.Name()}
}

// Name returns the instance name (contracts/library-api.md §2).
func (i *Instance) Name() string { return i.inst.Name() }

// URL is the base URL a client POSTs to for this instance, including scheme,
// host, port and mount path (contracts/library-api.md §2). It is empty for a
// stdio-only instance.
func (i *Instance) URL() string { return i.url }

// Journal returns a read-only view over this instance's request journal
// (contracts/library-api.md §2). Records it yields are copies (ADR-005); an
// instance with journalling disabled yields an empty view.
func (i *Instance) Journal() journalapi.View {
	ring, _ := i.inst.Journal()
	if ring == nil {
		return journalapi.NewView(nil)
	}
	return ring.View()
}

// Generation returns the current configuration snapshot generation (ADR-014),
// which is also recorded on every journal record.
func (i *Instance) Generation() uint64 { return i.inst.Snapshot().Gen() }

// effectiveSeed returns the caller-supplied seed, or a cryptographically random
// one when none was supplied (MOCK-704.1: two runs without --seed differ).
func effectiveSeed(o *options) uint64 {
	if o.seedSet {
		return o.seed
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic and unheard of on supported
		// platforms; fall back to a fixed value rather than panicking in a
		// library. A caller wanting determinism passes WithSeed.
		return 0
	}
	return binary.BigEndian.Uint64(b[:])
}

// applyJournalOptions folds the facade's journal option into the instance
// spec's journal block, so WithJournal/WithoutJournal override the scenario's
// journal configuration (options win over the file, §3). It replaces the spec's
// journal wholesale because the option is a complete configuration, not a patch.
func applyJournalOptions(spec scenario.InstanceSpec, o *options) scenario.InstanceSpec {
	if o.journalOff {
		off := false
		spec.Journal = &scenario.Journal{Enabled: &off}
		return spec
	}
	if o.journalSet {
		spec.Journal = journalToScenario(o.journalCfg)
		return spec
	}
	if spec.Journal == nil {
		// No scenario journal and no explicit option: apply the facade's
		// fleet-friendly default (see defaultInstanceJournal).
		spec.Journal = journalToScenario(o.journalCfg)
	}
	return spec
}

// journalToScenario maps a journalapi.Config back onto a scenario.Journal so the
// instance's own journalConfig mapping consumes it uniformly.
func journalToScenario(cfg journalapi.Config) *scenario.Journal {
	enabled := cfg.Enabled
	j := &scenario.Journal{Enabled: &enabled}
	if cfg.MaxRecords > 0 {
		v := cfg.MaxRecords
		j.MaxRecords = &v
	}
	if cfg.MaxBytes > 0 {
		v := int(cfg.MaxBytes)
		j.MaxBytes = &v
	}
	if cfg.Mode != "" {
		m := string(cfg.Mode)
		j.Bodies = &m
	}
	return j
}
