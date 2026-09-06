package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// readHeaderTimeout bounds the control listener's header read, matching the
// project's other listeners (security.md §8 slowloris mitigation).
const readHeaderTimeout = 10 * time.Second

// Server runs the control [Handler] over a TCP listener and/or a unix-domain
// socket (ADR-015: both may run simultaneously; in stdio mode only the socket
// exists). It is the lifecycle object the facade owns; it starts no goroutine
// until [Server.Serve] and unlinks its socket on [Server.Shutdown]. It holds no
// process-global state (ADR-007), so two facades' control servers do not collide.
type Server struct {
	handler *Handler

	tcpLn   net.Listener
	unixLn  net.Listener
	sock    string
	tcpSrv  *http.Server
	unixSrv *http.Server

	mu      sync.Mutex
	errCh   chan error
	started bool
}

// Config configures a control [Server]. At least one of TCPListener or UnixSocket
// should be set; a Server with neither serves nothing.
type Config struct {
	// Handler is the built control handler serving the routes.
	Handler *Handler
	// TCPListener, when non-nil, is served over TCP (HTTP mode). The caller
	// binds it (so an ephemeral ":0" resolves before Serve) and CheckBind-guards
	// its address.
	TCPListener net.Listener
	// UnixListener, when non-nil, is served over the unix socket at SocketPath.
	// The caller binds it via ListenUDS.
	UnixListener net.Listener
	// SocketPath is the path UnixListener is bound to, unlinked on shutdown.
	SocketPath string
	// Token is the control bearer token. When TCPRequiresAuth is set it is the
	// value the TCP listener's per-request auth compares against in constant
	// time (security.md §2.1). It is never applied to the unix-socket listener,
	// whose 0600 permissions are its access control.
	Token string
	// TCPRequiresAuth requires a valid bearer token on every TCP request. It is
	// set by the facade exactly when the TCP listener is non-loopback — the
	// reachable, adversarial surface the contract (bearerAuth: "Required
	// whenever the listener is not loopback and not a unix socket") and
	// security.md §2.1 mandate authentication for. A loopback TCP listener is
	// served without auth, matching the contract.
	TCPRequiresAuth bool
}

// New constructs a control Server from cfg, binding nothing further (the
// listeners are already bound). It starts no goroutine. The TCP listener serves
// an auth-wrapped handler when cfg.TCPRequiresAuth is set (a non-loopback bind);
// the unix-socket listener always serves the plain handler.
func New(cfg Config) *Server {
	tcpHandler := http.Handler(cfg.Handler)
	if cfg.TCPRequiresAuth {
		tcpHandler = requireBearer(cfg.Token, cfg.Handler)
	}
	return &Server{
		handler: cfg.Handler,
		tcpLn:   cfg.TCPListener,
		unixLn:  cfg.UnixListener,
		sock:    cfg.SocketPath,
		tcpSrv: &http.Server{
			Handler:           tcpHandler,
			ReadHeaderTimeout: readHeaderTimeout,
		},
		unixSrv: &http.Server{
			Handler:           cfg.Handler,
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}
}

// TCPAddr returns the base URL of the TCP control listener, or "" when none.
func (s *Server) TCPAddr() string {
	if s.tcpLn == nil {
		return ""
	}
	return "http://" + s.tcpLn.Addr().String()
}

// SocketPath returns the unix socket path, or "" when none.
func (s *Server) SocketPath() string { return s.sock }

// Serve begins serving on every configured listener. It returns immediately; the
// serving runs in background goroutines that Shutdown joins. Calling Serve twice
// is a no-op after the first.
func (s *Server) Serve() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	n := 0
	if s.tcpLn != nil {
		n++
	}
	if s.unixLn != nil {
		n++
	}
	s.errCh = make(chan error, n)
	if s.tcpLn != nil {
		go s.serveOne(s.tcpSrv, s.tcpLn)
	}
	if s.unixLn != nil {
		go s.serveOne(s.unixSrv, s.unixLn)
	}
}

// serveOne serves srv on one listener, forwarding a terminal error (other than
// the clean shutdown sentinel) to errCh. The TCP and unix listeners are served
// by distinct http.Servers so the TCP path can carry per-request auth while the
// socket path does not.
func (s *Server) serveOne(srv *http.Server, ln net.Listener) {
	err := srv.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.errCh <- err
		return
	}
	s.errCh <- nil
}

// Shutdown stops accepting, drains in-flight requests bounded by ctx, joins the
// serving goroutines, and unlinks the unix socket (AMEND-7 cleanup: the same
// graceful path that flushes the journal). It is idempotent.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil
	}
	var err error
	if e := s.tcpSrv.Shutdown(ctx); e != nil {
		err = e
	}
	if e := s.unixSrv.Shutdown(ctx); e != nil && err == nil {
		err = e
	}
	for range cap(s.errCh) {
		if e := <-s.errCh; e != nil && err == nil {
			err = e
		}
	}
	if s.sock != "" {
		if rmErr := os.Remove(s.sock); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("control: unlink socket %q: %w", s.sock, rmErr)
		}
	}
	s.started = false
	return err
}
