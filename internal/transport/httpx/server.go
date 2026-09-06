package httpx

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
)

// Default tuning constants (ADR-012 §3). WriteTimeout MUST be zero for streams;
// per-frame deadlines are set with http.ResponseController instead.
// DefaultPath is the MCP endpoint mount path when a scenario configures none
// (MOCK-102.2). The facade (TASK-022) applies it; the transport itself routes on
// whatever path a [Mount] carries.
const DefaultPath = "/mcp"

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultMaxHeaderBytes    = 1 << 16 // 64 KiB
	defaultMaxBodyBytes      = 1 << 20 // 1 MiB; a malformed giant body cannot OOM us
	defaultKeepAliveTick     = 10 * time.Millisecond
)

// Config configures a [Server]. The zero value is not usable; supply at least
// Addr (or Listener) and one Mount.
type Config struct {
	// Addr is the TCP address to listen on (e.g. ":8080" or "127.0.0.1:0" for an
	// ephemeral test port). Ignored when Listener is set.
	Addr string
	// Listener, when non-nil, is used instead of binding Addr — the path
	// StartTest uses to bind an ephemeral loopback port and hand it in.
	Listener net.Listener
	// Mounts are the initial instance mounts (MOCK-103). More may be added at
	// runtime with [Server.Mount].
	Mounts []*Mount
	// MaxBodyBytes bounds the request body read; zero uses the default. A body
	// over the bound is rejected rather than buffered, so a malformed or hostile
	// giant body cannot exhaust memory (MOCK-901 allocation discipline).
	MaxBodyBytes int64
	// KeepAliveTick is the shared timer wheel tick; zero uses the default 10 ms.
	KeepAliveTick time.Duration
}

// Server is the streamable-http MCP listener. It is a thin, era-blind adapter
// (architecture.md §6.1 rule 3): it decodes an HTTP POST into a transport-neutral
// [engine.Exchange], drives it through the mounted instance's pipeline, and
// writes the buffered response back — interpreting no wire content itself.
//
// # Concurrency model (architecture.md §7.1, ADR-012)
//
// A non-streaming request spawns ZERO extra goroutines: it is handled entirely
// on net/http's connection goroutine. The process-wide extras are one shared
// timer wheel goroutine (never per stream) plus net/http's own accept and
// connection goroutines. There is no per-request time.Ticker and no per-stream
// ticker, because MOCK-903's ≥20 000 concurrent streams make per-stream timers a
// wake-up storm; the shared [timerWheel] replaces them all.
//
// # Per-stream cost
//
// For an SSE stream the design targets ADR-012 §3's envelope: net/http's read
// goroutine + our handler goroutine, two ~8 KiB stacks, the conn's two bufio
// buffers, one buffered frame channel (cap 16, ≈ 256 B) and a small stream
// record — ≈ 25 KiB/stream user space, ≈ 500 MiB at 20 000. Phase 1 emits only
// buffered JSON; the SSE framing exists so Phase 2 fills content into a correct,
// already-measured envelope. The benchmark reports the measured figure.
//
// # Shutdown contract (MOCK-508)
//
// [Server.Shutdown] stops accepting, waits (bounded by the passed context) for
// in-flight requests to drain, stops the timer wheel, and returns. It does not
// hang on an open stream past the context deadline: when the deadline fires it
// returns net/http's shutdown error and the caller may force [Server.Close].
type Server struct {
	router  *Router
	wheel   *timerWheel
	http    *http.Server
	ln      net.Listener
	maxBody int64
}

// New constructs a Server from cfg without binding or starting anything, so
// construction is I/O-free and goroutine-free (the facade's no-I/O-in-New
// contract, MOCK-107). Call [Server.Serve] to bind and accept.
func New(cfg Config) (*Server, error) {
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	tick := cfg.KeepAliveTick
	if tick <= 0 {
		tick = defaultKeepAliveTick
	}
	s := &Server{
		router:  newRouter(cfg.Mounts),
		wheel:   newTimerWheel(tick),
		maxBody: maxBody,
	}
	s.http = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
		WriteTimeout:      0, // MUST be 0 for streams (ADR-012 §3)
		MaxHeaderBytes:    defaultMaxHeaderBytes,
		ConnContext:       wrapConn,
	}
	if cfg.Listener != nil {
		s.ln = headListener{Listener: cfg.Listener}
	} else {
		var lc net.ListenConfig
		ln, err := lc.Listen(context.Background(), "tcp", cfg.Addr)
		if err != nil {
			return nil, err
		}
		s.ln = headListener{Listener: ln}
	}
	return s, nil
}

// Addr returns the address the server is listening on, resolving an ephemeral
// ":0" port to the concrete bound port. It is safe to call after [New].
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Mount adds or replaces an instance mount at runtime (MOCK-103/MOCK-702).
func (s *Server) Mount(m *Mount) { s.router.Mount(m) }

// Unmount removes the mount at path, reporting whether one was present.
func (s *Server) Unmount(path string) bool { return s.router.Unmount(path) }

// Mounts returns the current mounts, sorted by path, for introspection and
// tests. It is not for the request path.
func (s *Server) Mounts() []*Mount { return s.router.Mounts() }

// Serve starts the shared timer wheel and serves until Shutdown/Close. It
// returns http.ErrServerClosed on a clean shutdown, which the caller treats as
// success. It blocks; run it in the caller's own goroutine.
func (s *Server) Serve() error {
	s.wheel.start()
	return s.http.Serve(s.ln)
}

// Shutdown gracefully drains in-flight requests, bounded by ctx, then stops the
// timer wheel (MOCK-508). It never hangs past ctx: http.Server.Shutdown returns
// ctx.Err() when the deadline fires before connections drain.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	s.wheel.stop(ctx)
	return err
}

// Close forcibly closes the listener and all connections immediately and stops
// the wheel. It is the escape hatch when a graceful [Server.Shutdown] exceeds
// its deadline.
func (s *Server) Close() error {
	err := s.http.Close()
	s.wheel.stop(context.Background())
	return err
}

// ServeHTTP is the single request entry point. It resolves the mount, enforces
// the MOCK-207 method policy, decodes the body into an Exchange, and drives the
// pipeline. It adds no goroutine (acceptance criterion 3).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mount, ok := s.router.match(r.URL.Path)
	if !ok {
		s.writeNotFound(w)
		return
	}
	// MOCK-207: only POST is a valid MCP request; GET and DELETE (and every
	// other method) are 405. The Allow header names the one permitted method.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeStatus(w, http.StatusMethodNotAllowed, "method not allowed on MCP endpoint")
		return
	}

	// MOCK-207: Mcp-Session-Id and Last-Event-ID are IGNORED and no session id
	// is minted. We deliberately do not read them into any decision and never
	// set a session response header. This method documents the intent; the code
	// path simply never consults those headers, which is the whole point.
	s.assertStateless(r)

	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody+1))
	if err != nil || int64(len(body)) > s.maxBody {
		// A read error or an over-limit body: reply with a well-formed status,
		// never a panic. The pipeline is not entered for a body we could not
		// read whole.
		s.writeStatus(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable")
		return
	}

	ex := s.buildExchange(r, mount, body)
	sink := newHTTPJSONSink(w)
	// ex.Ctx IS r.Context(): client disconnect cancels it, the engine observes
	// the cancellation at stages 8-9, closes the sink ClientGone and journals
	// the elapsed time (MOCK-212). We pass r.Context() explicitly (it is the
	// same value stored in ex.Ctx) so the one threaded context is visible to
	// the compiler and contextcheck; the pipeline never recreates it.
	if err := mount.Pipeline.Handle(r.Context(), ex, sink); err != nil {
		// An infrastructure error (a sink write failure) is the only thing
		// Handle returns as an error; a wire error is already in the sink. If
		// nothing was written yet, surface a 500; otherwise the client already
		// has a partial response and there is nothing correct left to do.
		if !sink.written {
			s.writeStatus(w, http.StatusInternalServerError, "internal error")
		}
	}
}

// assertStateless is the documented no-op that encodes MOCK-207: modern mode is
// sessionless, so the transport neither reads Mcp-Session-Id / Last-Event-ID to
// make a decision nor mints a session id. It exists so the statelessness is a
// named, testable contract rather than an accident of omission. It intentionally
// does nothing with the header values.
func (s *Server) assertStateless(_ *http.Request) {
	// Intentionally empty: the session and event-id headers are ignored, and no
	// Mcp-Session-Id is ever written to the response. See MOCK-207.
}

// buildExchange assembles the transport-neutral Exchange. Headers are captured
// in wire order with original casing and duplicates from the raw request head
// (MOCK-601.2); r.Header would have canonicalised them. ex.Ctx is r.Context()
// verbatim (MOCK-212).
func (s *Server) buildExchange(r *http.Request, mount *Mount, body []byte) *engine.Exchange {
	headers := wireHeaders(r.Context())
	if headers == nil {
		headers = fallbackHeaders(r)
	}
	return &engine.Exchange{
		Ctx:       r.Context(),
		Instance:  mount.Instance,
		Transport: engine.KindHTTP,
		Peer:      r.RemoteAddr,
		HTTP: &engine.HTTPContext{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.RawQuery,
			Headers: headers,
		},
		Raw: body,
	}
}

// fallbackHeaders builds a header list from the (canonicalised) http.Header when
// the raw head was not captured — e.g. under HTTP/2, where original casing is
// not a wire concept. Order within a name is preserved; cross-name order is not
// recoverable here, which is acceptable because the wire-fidelity property is an
// HTTP/1.1 concern exercised through the raw-socket path.
func fallbackHeaders(r *http.Request) [][2]string {
	out := make([][2]string, 0, len(r.Header))
	for name, vals := range r.Header {
		for _, v := range vals {
			out = append(out, [2]string{name, v})
		}
	}
	return out
}

// writeNotFound answers an unmounted path with a well-formed error instead of a
// bare 404 page or a panic (acceptance criterion 2).
func (s *Server) writeNotFound(w http.ResponseWriter) {
	s.writeStatus(w, http.StatusNotFound, "no MCP endpoint at this path")
}

// writeStatus writes a small JSON error object with the given status. It is the
// single place the transport frames its own (non-wire) HTTP errors, so a 404 /
// 405 / 413 all share one shape. This is transport framing, not MCP wire
// content, so no internal/wire vocabulary is involved.
func (s *Server) writeStatus(w http.ResponseWriter, status int, msg string) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)
	// A fixed, allocation-light body; the message is a constant caller string.
	_, _ = w.Write([]byte(`{"error":`))
	_, _ = w.Write(quoteJSON(msg))
	_, _ = w.Write([]byte(`}`))
}

// quoteJSON returns msg as a JSON string literal (including quotes). msg is
// always a constant ASCII caller string, so this simple quoter is sufficient and
// avoids pulling encoding/json onto the error path.
func quoteJSON(msg string) []byte {
	out := make([]byte, 0, len(msg)+2)
	out = append(out, '"')
	out = append(out, msg...)
	out = append(out, '"')
	return out
}
