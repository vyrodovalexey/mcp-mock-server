package mcpmock

import (
	"io"
	"log/slog"
	"net"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// Option configures a [Server] at construction (contracts/library-api.md §3).
// Options are applied in order and later wins; an option that conflicts with a
// loaded scenario file wins over the file. The interface is deliberately
// unexported-behind-a-func so the option set stays closed: a caller uses the
// With* functions below and cannot define a new Option, which keeps the applied
// configuration auditable and the surface a published contract.
//
// Stability: v0.
type Option interface {
	apply(*options)
}

// optionFunc adapts a function to an [Option]. It is the single Option
// implementation; every With* function returns one.
type optionFunc func(*options)

func (f optionFunc) apply(o *options) { f(o) }

// options is the accumulated, unexported configuration a [Server] is built
// from. It is populated by applying every [Option] in order, then read once by
// New. It holds no process-global state (ADR-007): two Servers built from
// independent option sets share nothing.
type options struct {
	// seed is the effective root seed (MOCK-704); seedSet records whether the
	// caller supplied one, so New can generate a random seed only when absent.
	seed    uint64
	seedSet bool

	// scenarioRoot bounds extends path resolution for NewFromFile (MOCK-703).
	scenarioRoot string
	// skipValidation skips JSON Schema validation on the programmatic paths
	// (WithoutValidation); the file path always validates post-composition.
	skipValidation bool
	// overlays are programmatic scenario overlays applied last (MOCK-703).
	overlays []*scenario.Document

	// addr is the MCP HTTP listener address (":0" for ephemeral); listener, when
	// non-nil, is used instead and disables addr. httpEnabled records whether an
	// HTTP listener should be started at all.
	addr        string
	addrSet     bool
	listener    net.Listener
	httpEnabled bool

	// stdioIn/stdioOut are the explicit stdio streams (ADR-011): the library
	// NEVER defaults them to os.Stdin/os.Stdout. When both are set the stdio
	// transport is enabled for the (single) instance.
	stdioIn      io.Reader
	stdioOut     io.Writer
	stdioEnabled bool

	// observabilityAddr, when set, serves /metrics, /healthz and /readyz.
	observabilityAddr string
	obsEnabled        bool

	// control listener configuration (ADR-015, MOCK-104). controlAddr binds a
	// TCP control listener; controlSocket overrides the unix-socket path;
	// controlDisabled turns the control plane off entirely (--no-control);
	// controlToken is the bearer token; its presence permits a non-loopback bind
	// (MOCK-104.5) and it is enforced per request on a non-loopback TCP control
	// listener (security.md §2.1, REV-001). controlAddrSet /
	// controlSocketSet record whether the caller supplied each, so an explicit
	// override is distinguishable from the default.
	controlAddr      string
	controlAddrSet   bool
	controlSocket    string
	controlSocketSet bool
	controlDisabled  bool
	controlToken     string
	controlTokenSet  bool

	// logger is the facade's lifecycle logger. It defaults to a discard logger
	// (the library is quiet by default and NEVER writes to stdout, ADR-011);
	// cmd/mcpmock passes an stderr logger.
	logger *slog.Logger
	// registerer lets an embedding test supply its own Prometheus registerer
	// (ADR-016 WithRegisterer). When nil a fresh private registry is used.
	registerer *prometheus.Registry

	// journalCfg overrides the per-instance journal configuration; journalSet
	// records that the caller supplied one. journalOff disables journalling
	// fleet-wide (MOCK-901 measurement mode).
	journalCfg journalapi.Config
	journalSet bool
	journalOff bool
}

// defaultOptions returns the zero-configuration baseline every Server starts
// from before options are applied. The defaults encode the library's safe
// posture: no listener bound, no stdio streams (they must be explicit, ADR-011),
// a discard logger (never stdout), and a journal ring sized for many instances
// (see [defaultInstanceJournal]).
func defaultOptions() *options {
	return &options{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		journalCfg: defaultInstanceJournal(),
	}
}

// defaultInstanceJournal is the facade's chosen per-instance journal ring size
// (ADR-007 makes the fleet-wide journal budget a facade concern). The journal
// package defaults to 100 000 records, which preallocates ~3.15 MiB per
// instance; at the MOCK-904 target of ≥200 instances that is ~630 MiB of ring
// preallocation alone, which is a poor default for a library meant to run a
// fleet inside someone else's test binary.
//
// The facade therefore defaults each instance to a 4 096-record ring, bounded
// also by 8 MiB, whichever binds first. Measured at 200 instances the whole
// fleet then costs ~87 MiB, against ~667 MiB for the 100 000-record ring and
// ~66 MiB with journalling off — the 4 096-record ring adds only ~21 MiB of
// journal storage across the fleet while still retaining thousands of recent
// requests, which is ample for the assertion patterns the journal exists to
// serve (a hub test inspects the last handful of requests, not a hundred
// thousand). A caller that wants the full 100 000-record ring passes
// WithJournal(journalapi.Config{Enabled: true, MaxRecords: 100000}).
func defaultInstanceJournal() journalapi.Config {
	return journalapi.Config{
		Enabled:    true,
		MaxRecords: 4096,
		MaxBytes:   8 << 20,
	}
}

// --- identity and determinism ---

// WithSeed sets the effective root seed (MOCK-704). Absent this option New
// generates a cryptographically random seed, so two Servers built without a
// seed differ; with it, two Servers built with the same seed produce
// byte-identical responses (PRIN-1).
func WithSeed(seed uint64) Option {
	return optionFunc(func(o *options) {
		o.seed = seed
		o.seedSet = true
	})
}

// WithScenarioRoot bounds extends path resolution for NewFromFile to dir
// (MOCK-703.7): an extends target resolving outside dir, including via ".." or a
// symlink, is rejected before any file read.
func WithScenarioRoot(dir string) Option {
	return optionFunc(func(o *options) { o.scenarioRoot = dir })
}

// WithoutValidation skips JSON Schema validation on the programmatic
// construction paths, trading the ~40 ms schema slice of the startup budget for
// the caller's assurance that the document is already valid. The file path
// still validates post-composition; this option affects NewFromScenario and the
// WithOverlay merge.
func WithoutValidation() Option {
	return optionFunc(func(o *options) { o.skipValidation = true })
}

// WithOverlay adds programmatic scenario overlays applied last (highest
// precedence) during composition (MOCK-703). Each overlay is merged over the
// base document in order; a nil document is ignored.
func WithOverlay(docs ...*scenario.Document) Option {
	return optionFunc(func(o *options) {
		for _, d := range docs {
			if d != nil {
				o.overlays = append(o.overlays, d)
			}
		}
	})
}

// --- listeners ---

// WithAddr sets the MCP HTTP listener address and enables the HTTP transport.
// Use ":0" or "127.0.0.1:0" for an ephemeral port; [Server.Instances] then
// report their concrete bound URL. It is ignored when [WithListener] is also
// supplied.
func WithAddr(addr string) Option {
	return optionFunc(func(o *options) {
		o.addr = addr
		o.addrSet = true
		o.httpEnabled = true
	})
}

// WithListener supplies a pre-bound net.Listener for the MCP HTTP transport and
// enables it, disabling [WithAddr]. It is the path a test uses to bind an
// ephemeral loopback port itself and hand it in.
func WithListener(l net.Listener) Option {
	return optionFunc(func(o *options) {
		o.listener = l
		o.httpEnabled = true
	})
}

// WithStdio supplies the explicit stdio streams and enables the stdio transport
// for the instance (ADR-011: the library NEVER defaults to os.Stdin/os.Stdout).
// in is the request stream, out receives protocol frames only. Passing
// os.Stdout here from the library form would defeat cmd/mcpmock's stdout hijack;
// cmd/mcpmock passes the captured real fd 1, not os.Stdout.
func WithStdio(in io.Reader, out io.Writer) Option {
	return optionFunc(func(o *options) {
		o.stdioIn = in
		o.stdioOut = out
		o.stdioEnabled = true
	})
}

// WithObservabilityAddr serves /metrics, /healthz and /readyz on addr, on its
// own listener distinct from every MCP listener (observability.md §5).
func WithObservabilityAddr(addr string) Option {
	return optionFunc(func(o *options) {
		o.observabilityAddr = addr
		o.obsEnabled = true
	})
}

// --- control plane (ADR-015, MOCK-104) ---

// WithControlAddr binds the HTTP control API to addr, on a listener distinct
// from every MCP listener (MOCK-104: separate port). Use "127.0.0.1:0" for an
// ephemeral loopback port; [Server.ControlURL] then reports the concrete bound
// URL. Binding a non-loopback address without a token configured is refused at
// startup (MOCK-104.5, security.md §2.1): the control plane reads a journal
// containing full request bodies, so it must not be reachable off-host by
// default.
func WithControlAddr(addr string) Option {
	return optionFunc(func(o *options) {
		o.controlAddr = addr
		o.controlAddrSet = true
	})
}

// WithControlSocket sets the unix-domain-socket path for the control API,
// overriding the AMEND-7 default. The path is used verbatim: no pid is appended
// and no length fallback is applied, and an unusable explicit path is a startup
// error (ADR-015 "The documented path"). Passing "" is an error, not a synonym
// for disable; use [WithoutControl] to disable.
func WithControlSocket(path string) Option {
	return optionFunc(func(o *options) {
		o.controlSocket = path
		o.controlSocketSet = true
	})
}

// WithoutControl starts no control listener at all (--no-control). The
// in-process [Server.Control] still works; only the HTTP and unix-socket front
// ends are suppressed.
func WithoutControl() Option {
	return optionFunc(func(o *options) { o.controlDisabled = true })
}

// WithControlToken configures the bearer token that authorizes control requests
// and permits a non-loopback bind (MOCK-104.5). It is provided for the library
// form; cmd/mcpmock reads the token from MCPMOCK_CONTROL_TOKEN or
// --control-token-file, never a flag value (security.md §2.1). The token both
// permits the non-loopback bind and is enforced per request on that listener: a
// non-loopback TCP control listener requires a matching "Authorization: Bearer"
// header, compared in constant time (security.md §2.1). Loopback and the unix
// socket are served without per-request auth, per the control contract's
// bearerAuth condition.
func WithControlToken(token string) Option {
	return optionFunc(func(o *options) {
		o.controlToken = token
		o.controlTokenSet = true
	})
}

// --- observability (ADR-016) ---

// WithLogger sets the facade's lifecycle logger (ADR-016). It MUST write to
// stderr (or elsewhere) but never stdout in stdio mode, since stdout is the
// protocol frame channel (ADR-011). The default is a discard logger, so the
// library is silent unless a caller opts in.
func WithLogger(l *slog.Logger) Option {
	return optionFunc(func(o *options) {
		if l != nil {
			o.logger = l
		}
	})
}

// WithRegisterer supplies the Prometheus registry backing this Server's metrics
// (ADR-016). When absent the Server creates a fresh private registry — never the
// process-global default registerer (ADR-007) — so two Servers in one process
// never race on duplicate registration.
func WithRegisterer(r *prometheus.Registry) Option {
	return optionFunc(func(o *options) { o.registerer = r })
}

// --- journal (ADR-005) ---

// WithJournal overrides the per-instance journal configuration (ADR-005). It
// replaces the facade's fleet-friendly default (see the package doc's journal
// note); pass journalapi.Config{Enabled: true, MaxRecords: 100000} to restore
// the journal package's own 100 000-record ring.
func WithJournal(cfg journalapi.Config) Option {
	return optionFunc(func(o *options) {
		o.journalCfg = cfg
		o.journalSet = true
		o.journalOff = false
	})
}

// WithoutJournal disables journalling for every instance (MOCK-901 measurement
// mode): the write path becomes a single atomic load and no Record is
// constructed, and each instance costs ~2 KiB instead of a ring preallocation.
func WithoutJournal() Option {
	return optionFunc(func(o *options) {
		o.journalOff = true
		o.journalSet = false
	})
}
