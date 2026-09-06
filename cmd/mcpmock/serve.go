package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// envControlAuth is the name of the environment variable the control bearer
// token is read from when no --control-token-file is given (security.md §2.1).
// The token itself is never accepted as a flag value, so it cannot leak into a
// process listing; only this variable name is a compile-time constant.
const envControlAuth = "MCPMOCK_CONTROL_TOKEN"

// envMetricsListen is the environment fallback for --metrics-listen: it names
// only an endpoint, not behavior, so it does not compromise reproducibility
// (deployment.md §3, MOCK-704). Precedence is flag > env > default.
const envMetricsListen = "MCPMOCK_METRICS_LISTEN"

// defaultMetricsListen is the observability listener's default bind address
// (observability.md §38, ADR-016: default ":9090"). It binds every interface,
// not loopback, deliberately: a kubelet liveness/readiness probe reaches the pod
// from outside its network namespace, so a 127.0.0.1 bind would make /healthz
// and /readyz unreachable in a container (deployment.md §4, the TASK-031 probes
// and TASK-034 deploy depend on it). Port 9090 is unprivileged, so the
// distroless non-root image (uid 65532, TASK-030) can bind it without
// capabilities. An operator who wants loopback-only passes
// --metrics-listen 127.0.0.1:9090 explicitly.
const defaultMetricsListen = ":9090"

// shutdownGrace bounds the graceful drain a signal-triggered shutdown is given
// before it gives up on in-flight requests (MOCK-212). It is generous for a
// loopback mock and short enough that a stuck handler cannot wedge the process.
const shutdownGrace = 10 * time.Second

// serveFlags holds the parsed `serve` flags. Precedence for the control socket
// is flag > env > default and is resolved inside the facade (AMEND-7); the CLI
// only forwards an explicit --control-socket.
type serveFlags struct {
	transport     string
	path          string
	seed          string
	listen        string
	metricsListen string
	controlListen string
	controlSocket string
	noControl     bool
	logLevel      string
	scenarioRoot  string
	noValidate    bool
	tokenFile     string
}

// runServe implements `mcpmock serve`. It parses flags, resolves the effective
// seed, installs the ADR-011 stdout hijack in stdio mode (before any other
// initialisation, criterion 6), constructs and starts the facade, prints the
// effective seed on stderr (MOCK-704.2), and blocks until ctx is canceled
// (SIGINT/SIGTERM) — then shuts down gracefully and restores the hijack.
//
// It returns exitOK on a clean shutdown, exitValidation when the scenario is
// invalid, and exitIO on any other startup failure or a bad invocation.
func runServe(errOut io.Writer, args []string) int {
	// Parse and construct the session BEFORE creating the signal context, so the
	// facade's context-free constructors never run inside a ctx-carrying scope.
	sess, transports, code, ok := prepareServe(errOut, args)
	if !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	return sess.serve(ctx, transports)
}

// prepareServe parses flags and builds a session WITHOUT holding a context, so
// the facade's context-free constructors never sit in a ctx-carrying scope
// (contextcheck). runServe uses it before creating the signal context; the tests
// use it to obtain a session they then serve under a cancellable context.
func prepareServe(errOut io.Writer, args []string) (*session, transportSet, int, bool) {
	sf, code, ok := parseServeFlags(errOut, args)
	if !ok {
		return nil, transportSet{}, code, false
	}
	transports, err := parseTransports(sf.transport)
	if err != nil {
		fmt.Fprintf(errOut, "mcpmock serve: %v\n", err)
		return nil, transportSet{}, exitIO, false
	}
	level, err := parseLogLevel(sf.logLevel)
	if err != nil {
		fmt.Fprintf(errOut, "mcpmock serve: %v\n", err)
		return nil, transportSet{}, exitIO, false
	}
	seed, seedSet, err := parseSeed(sf.seed)
	if err != nil {
		fmt.Fprintf(errOut, "mcpmock serve: %v\n", err)
		return nil, transportSet{}, exitIO, false
	}
	if !seedSet {
		seed = randomSeed()
	}
	sess, code, ok := buildSession(errOut, sf, transports, level, seed)
	if !ok {
		return nil, transportSet{}, code, false
	}
	return sess, transports, exitOK, true
}

// transportSet records which transports serve enables. At least one must be on.
type transportSet struct {
	stdio bool
	http  bool
}

// parseServeFlags parses the serve flag set. It returns the flags, an exit code,
// and whether parsing succeeded; on a parse error or --help it has already
// written to errOut.
func parseServeFlags(errOut io.Writer, args []string) (serveFlags, int, bool) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var sf serveFlags
	fs.StringVar(&sf.transport, "transport", "http", "comma-separated transports: stdio,http")
	fs.StringVar(&sf.path, "path", "", "scenario file to load (empty = built-in default scenario)")
	fs.StringVar(&sf.seed, "seed", "", "root seed (uint64); absent = cryptographically random")
	fs.StringVar(&sf.listen, "listen", "127.0.0.1:0", "MCP HTTP listen address")
	fs.StringVar(&sf.metricsListen, "metrics-listen", "",
		"observability listen address for /metrics,/healthz,/readyz "+
			"(flag > "+envMetricsListen+" > default "+defaultMetricsListen+")")
	fs.StringVar(&sf.controlListen, "control-listen", "", "control API TCP listen address (loopback by default)")
	fs.StringVar(&sf.controlSocket, "control-socket", "", "control API unix socket path (overrides the default)")
	fs.BoolVar(&sf.noControl, "no-control", false, "disable the control API front ends")
	fs.StringVar(&sf.logLevel, "log-level", "info", "log level: debug,info,warn,error")
	fs.StringVar(&sf.scenarioRoot, "scenario-root", "", "bound extends resolution to this directory")
	fs.BoolVar(&sf.noValidate, "no-validate", false, "skip scenario schema validation")
	fs.StringVar(&sf.tokenFile, "control-token-file", "", "file holding the control bearer token (never a flag value)")
	fs.Usage = func() { serveUsage(errOut, fs) }

	if err := fs.Parse(args); err != nil {
		return serveFlags{}, exitIO, false
	}
	return sf, exitOK, true
}

// parseTransports parses the --transport comma list into a set, rejecting an
// unknown transport and an empty set.
func parseTransports(raw string) (transportSet, error) {
	var ts transportSet
	for _, tok := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(tok)) {
		case "":
			// tolerate stray commas / whitespace
		case "stdio":
			ts.stdio = true
		case "http":
			ts.http = true
		default:
			return transportSet{}, fmt.Errorf("unknown transport %q (want stdio and/or http)", tok)
		}
	}
	if !ts.stdio && !ts.http {
		return transportSet{}, fmt.Errorf("no transport selected (want stdio and/or http)")
	}
	return ts, nil
}

// session is a constructed-but-not-yet-running serve session: the built facade,
// the optional stdout hijack, and the lifecycle logger. It is assembled without
// a context (construction binds listeners with its own background scope, like
// the facade's own New), and then run under the process context.
type session struct {
	srv    *mcpmock.Server
	hj     *hijackHandle
	logger *slog.Logger
	errOut io.Writer
}

// serve runs an already-built session until ctx is canceled, then tears it down.
// Construction happened in buildSession (context-free); this is the sole
// context-bound half, so the facade's context-free constructors never sit in a
// ctx-carrying call chain (what contextcheck wants).
func (s *session) serve(ctx context.Context, ts transportSet) int {
	// A detached cleanup context inherits ctx's values but not its cancellation,
	// so the deferred teardown still runs after a signal cancels ctx.
	cleanupCtx := context.WithoutCancel(ctx)
	if s.hj != nil {
		defer s.hj.restore(cleanupCtx, s.errOut)
	}
	defer s.close(cleanupCtx)
	return s.run(ctx, ts)
}

// buildSession constructs the observability bundle, installs the hijack in stdio
// mode (before the facade, criterion 6), assembles the options and builds the
// facade. It holds NO context on purpose: construction is not a cancellable
// operation and the facade's constructors bind their listeners with their own
// background scope, so keeping ctx out of this scope is what contextcheck wants
// (there is nothing to propagate). A failure after the hijack is installed
// restores it here using a background context.
func buildSession(
	errOut io.Writer, sf serveFlags, ts transportSet, level slog.Level, seed uint64,
) (*session, int, bool) {
	// The lifecycle logger and every diagnostic go to stderr — NEVER stdout,
	// which in stdio mode is the protocol frame channel (ADR-011).
	bundle, err := obs.New(obs.Config{LogWriter: errOut, LogLevel: level})
	if err != nil {
		fmt.Fprintf(errOut, "mcpmock serve: observability init: %v\n", err)
		return nil, exitIO, false
	}
	logger := bundle.Logger()

	var hj *hijackHandle
	if ts.stdio {
		hj, err = installHijack(logger, bundle.Metrics())
		if err != nil {
			fmt.Fprintf(errOut, "mcpmock serve: install stdout hijack: %v\n", err)
			return nil, exitIO, false
		}
	}

	opts, err := buildOptions(sf, ts, seed, logger, hj)
	if err != nil {
		fmt.Fprintf(errOut, "mcpmock serve: %v\n", err)
		restoreOnFail(hj, errOut)
		return nil, exitIO, false
	}
	srv, err := newServer(sf, opts)
	if err != nil {
		restoreOnFail(hj, errOut)
		return nil, serveConstructError(errOut, err), false
	}
	return &session{srv: srv, hj: hj, logger: logger, errOut: errOut}, exitOK, true
}

// restoreOnFail restores the hijack when session construction fails after it was
// installed, so a failed stdio startup does not leave os.Stdout hijacked. It runs
// in a context-free construction scope, so it uses a background context for the
// bounded drain join (there is no parent context to inherit here).
func restoreOnFail(hj *hijackHandle, errOut io.Writer) {
	if hj != nil {
		hj.restore(context.Background(), errOut)
	}
}

// run prints the effective seed, starts the facade under ctx, waits for ctx to
// be canceled (SIGINT/SIGTERM), then drains gracefully (MOCK-212). The shutdown
// context inherits ctx's values via context.WithoutCancel but has its own
// deadline, since ctx is already canceled by the time we drain.
func (s *session) run(ctx context.Context, ts transportSet) int {
	// Print the effective seed on stderr as a structured record BEFORE Start so
	// a failing run is reproducible even if a listener bind then fails
	// (MOCK-704.2).
	s.logger.Info("effective seed", slog.Uint64("seed", s.srv.Seed()))

	if err := s.srv.Start(ctx); err != nil {
		fmt.Fprintf(s.errOut, "mcpmock serve: start: %v\n", err)
		return exitIO
	}
	announce(s.logger, s.srv, ts)

	<-ctx.Done()
	s.logger.Info("shutting down", slog.String(obs.FieldEvent, "shutdown"))
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(s.errOut, "mcpmock serve: shutdown: %v\n", err)
		return exitIO
	}
	return exitOK
}

// close tears the facade down on the deferred cleanup path (a normal shutdown
// has already run in run(), so this is a no-op then). It propagates the supplied
// cleanup context, bounded by a short deadline, and is idempotent.
func (s *session) close(ctx context.Context) {
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownGrace)
	defer cancel()
	_ = s.srv.Shutdown(shutdownCtx)
}

// buildOptions assembles the facade options from the parsed flags. It reads the
// control token from a file or the environment — never a flag value — so the
// token never appears in a process listing or in help text (security.md §2.1).
func buildOptions(
	sf serveFlags, ts transportSet, seed uint64, logger *slog.Logger, hj *hijackHandle,
) ([]mcpmock.Option, error) {
	opts := []mcpmock.Option{
		mcpmock.WithSeed(seed),
		mcpmock.WithLogger(logger),
	}
	if sf.scenarioRoot != "" {
		opts = append(opts, mcpmock.WithScenarioRoot(sf.scenarioRoot))
	}
	if sf.noValidate {
		opts = append(opts, mcpmock.WithoutValidation())
	}
	if ts.http {
		opts = append(opts, mcpmock.WithAddr(sf.listen))
	}
	if ts.stdio {
		// The hijack's captured fd 1 is the protocol writer; os.Stdin is the
		// request stream. os.Stdout is deliberately NOT passed (it is now the
		// drain pipe); ProtoOut is the real terminal/pipe fd 1.
		opts = append(opts, mcpmock.WithStdio(os.Stdin, hj.protoOut()))
	}
	// The observability listener (/metrics,/healthz,/readyz) is enabled in every
	// transport mode, including stdio: it is a separate HTTP listener that never
	// touches stdout (ADR-011), so it satisfies MOCK-105 without disturbing the
	// stdio protocol channel. Its logs already go to the stderr bundle logger.
	opts = append(opts, mcpmock.WithObservabilityAddr(resolveMetricsListen(sf)))

	controlOpts, err := controlOptions(sf)
	if err != nil {
		return nil, err
	}
	return append(opts, controlOpts...), nil
}

// controlOptions builds the control-plane options: disable, socket override, and
// the token (from file or env). Reading the token here keeps it out of the flag
// surface entirely.
func controlOptions(sf serveFlags) ([]mcpmock.Option, error) {
	if sf.noControl {
		return []mcpmock.Option{mcpmock.WithoutControl()}, nil
	}
	var opts []mcpmock.Option
	if sf.controlListen != "" {
		opts = append(opts, mcpmock.WithControlAddr(sf.controlListen))
	}
	if sf.controlSocket != "" {
		opts = append(opts, mcpmock.WithControlSocket(sf.controlSocket))
	}
	token, err := readControlToken(sf.tokenFile)
	if err != nil {
		return nil, err
	}
	if token != "" {
		opts = append(opts, mcpmock.WithControlToken(token))
	}
	return opts, nil
}

// resolveMetricsListen resolves the observability listener address with
// flag > env > default precedence (deployment.md §3): an explicit
// --metrics-listen wins, else MCPMOCK_METRICS_LISTEN, else the container-safe
// default (see defaultMetricsListen). It always returns a non-empty address, so
// the observability listener is always enabled — the CLI has no "off" switch for
// it, because MOCK-105 requires the process-mode binary to expose /metrics.
func resolveMetricsListen(sf serveFlags) string {
	if addr := firstSet(sf.metricsListen, os.Getenv(envMetricsListen)); addr != "" {
		return addr
	}
	return defaultMetricsListen
}

// readControlToken reads the control bearer token from tokenFile when set, else
// from MCPMOCK_CONTROL_TOKEN. It is never taken from a flag value, so it cannot
// leak into a process listing or shell history (security.md §2.1). A trailing
// newline is trimmed. The token itself is never logged or echoed.
func readControlToken(tokenFile string) (string, error) {
	if tokenFile != "" {
		// filepath.Clean normalises the operator-supplied path and satisfies
		// gosec G304 without suppression: the path is a deliberate operator
		// input, not request-derived.
		b, err := os.ReadFile(filepath.Clean(tokenFile))
		if err != nil {
			return "", fmt.Errorf("read control token file: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return strings.TrimRight(os.Getenv(envControlAuth), "\r\n"), nil
}

// newServer constructs the facade from the scenario file (or the built-in
// default when --path is empty) plus opts.
func newServer(sf serveFlags, opts []mcpmock.Option) (*mcpmock.Server, error) {
	if sf.path == "" {
		return mcpmock.New(opts...)
	}
	return mcpmock.NewFromFile(sf.path, opts...)
}

// serveConstructError maps a facade construction error to an exit code: a
// validation failure is exit 1 (MOCK-701), any other failure is exit 2.
func serveConstructError(errOut io.Writer, err error) int {
	fmt.Fprintf(errOut, "mcpmock serve: %v\n", err)
	if isValidationError(err) {
		return exitValidation
	}
	return exitIO
}

// announce logs the bound endpoints once the server is serving, so an operator
// sees the MCP URL, control URL and control socket. The control socket path is
// also announced by the facade itself (AMEND-7); this is the CLI-level summary.
func announce(logger *slog.Logger, srv *mcpmock.Server, ts transportSet) {
	attrs := []any{slog.String(obs.FieldEvent, obs.EventStartup)}
	if ts.http {
		if insts := srv.Instances(); len(insts) > 0 {
			attrs = append(attrs, slog.String("mcpUrl", insts[0].URL()))
		}
	}
	if ts.stdio {
		attrs = append(attrs, slog.Bool("stdio", true))
	}
	if u := srv.ObservabilityURL(); u != "" {
		attrs = append(attrs, slog.String("metricsUrl", u))
	}
	if u := srv.ControlURL(); u != "" {
		attrs = append(attrs, slog.String("controlUrl", u))
	}
	if s := srv.ControlSocket(); s != "" {
		attrs = append(attrs, slog.String("controlSocket", s))
	}
	logger.Info("serving", attrs...)
}

// serveUsage prints the serve subcommand's help.
func serveUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage: mcpmock serve [flags]")
	fmt.Fprintln(w, "\nRun the mock MCP server. In stdio mode stdout carries protocol frames only;")
	fmt.Fprintln(w, "the effective seed and all diagnostics go to stderr.")
	fmt.Fprintln(w, "\nExit codes: 0 clean shutdown, 1 scenario validation error, 2 startup/usage error.")
	fmt.Fprintln(w, "\nFlags:")
	fs.PrintDefaults()
}
