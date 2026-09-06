package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// This file implements the control unix-domain-socket lifecycle exactly as
// AMEND-7 specifies it in ADR-015 "The documented path": default path, override
// precedence, 0600 permissions applied before accept, the platform sun_path
// length limit with a /tmp fallback, and — the load-bearing part — probe before
// unlink. An unconditional unlink silently steals a healthy process's socket,
// which under ADR-007's multi-instance model is a real scenario, so a stale
// socket is only removed after a dial proves nothing is listening.

// socketMode is the owner-only permission the control socket is created with
// (AMEND-7). It is applied by an explicit chmod after net.Listen and before the
// listener accepts, so a permissive umask cannot widen it.
const socketMode os.FileMode = 0o600

// sunPathMax is the platform limit on a unix socket path (sockaddr_un.sun_path):
// 104 bytes on macOS/BSD, 108 on Linux (AMEND-7). A computed default longer than
// this falls back to /tmp; an explicit override longer than it is a startup
// error.
func sunPathMax() int {
	if runtime.GOOS == "linux" {
		return 108
	}
	return 104
}

// EnvControlSocket is the environment variable that overrides the default
// control socket path, below an explicit --control-socket flag (AMEND-7).
const EnvControlSocket = "MCPMOCK_CONTROL_SOCKET"

// SocketOverride carries an explicit socket path choice, distinguishing "no
// override" (use the default) from "override to this path". An empty explicit
// path (--control-socket "") is an error, never a synonym for disable (AMEND-7).
type SocketOverride struct {
	// Path is the explicit override path.
	Path string
	// Set reports whether an override was supplied at all.
	Set bool
}

// ResolveSocketPath computes the control socket path per AMEND-7's precedence:
// an explicit override (flag) wins, else MCPMOCK_CONTROL_SOCKET, else the
// documented default ${XDG_RUNTIME_DIR}/mcpmock-${pid}.sock or
// /tmp/mcpmock-${pid}.sock. An explicit override is used verbatim — no pid
// appended, no length fallback — and an unusable one is an error. The computed
// default that exceeds the platform limit falls back to /tmp, and reports
// fellBack=true so the caller can log the fallback at WARN. pid is the process
// id to embed; env reads the environment (os.Getenv in production).
func ResolveSocketPath(flag SocketOverride, env func(string) string, pid int) (path string, fellBack bool, err error) {
	if flag.Set {
		return resolveExplicit(flag.Path)
	}
	if v := env(EnvControlSocket); v != "" {
		return resolveExplicit(v)
	}
	return resolveDefault(env, pid)
}

// tmpSocketDir is the AMEND-7 fallback directory for the control socket when the
// XDG_RUNTIME_DIR default would exceed the platform sun_path limit.
const tmpSocketDir = "/tmp"

// resolveExplicit validates an explicit override path: non-empty and within the
// platform length limit, used verbatim.
func resolveExplicit(p string) (path string, fellBack bool, err error) {
	if p == "" {
		return "", false, errors.New(`control: --control-socket "" is not a valid path (use --no-control to disable)`)
	}
	if len(p) > sunPathMax() {
		return "", false, fmt.Errorf("control: explicit socket path %q exceeds the %d-byte limit", p, sunPathMax())
	}
	return p, false, nil
}

// resolveDefault computes the documented default path and applies the /tmp
// length fallback (AMEND-7). fellBack is true only when an absolute
// XDG_RUNTIME_DIR was present but its computed path exceeded the platform limit,
// forcing the /tmp fallback — the case AMEND-7 asks be logged at WARN. When
// XDG_RUNTIME_DIR is absent or relative, /tmp is simply the documented default
// (not a fallback). If /tmp also exceeds the limit, it is a fatal error.
func resolveDefault(env func(string) string, pid int) (path string, fellBack bool, err error) {
	name := "mcpmock-" + strconv.Itoa(pid) + ".sock"
	fellBack = false
	if dir := env("XDG_RUNTIME_DIR"); filepath.IsAbs(dir) {
		p := filepath.Join(dir, name)
		if len(p) <= sunPathMax() {
			return p, false, nil
		}
		// The XDG path was present but too long: the /tmp use below is a
		// genuine fallback worth logging.
		fellBack = true
	}
	tmp := tmpSocketDir + "/" + name
	if len(tmp) > sunPathMax() {
		return "", false, fmt.Errorf("control: default socket path %q exceeds the %d-byte limit", tmp, sunPathMax())
	}
	return tmp, fellBack, nil
}

// ListenUDS binds a unix-domain-socket listener at path, applying the AMEND-7
// lifecycle: the parent directory must pre-exist (mcpmock never creates it); on
// EADDRINUSE the existing socket is probed by dialing it and only unlinked if
// the dial is refused (stale); the socket is chmod'd to 0600 before it accepts.
// It returns the listener; the caller unlinks path on graceful shutdown.
func ListenUDS(path string) (net.Listener, error) {
	if err := requireParentDir(path); err != nil {
		return nil, err
	}
	ln, err := unixListen(path)
	if err != nil {
		ln, err = handleBindInUse(path, err)
		if err != nil {
			return nil, err
		}
	}
	if cerr := os.Chmod(path, socketMode); cerr != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("control: chmod socket %q to %#o: %w", path, socketMode, cerr)
	}
	return ln, nil
}

// unixListen binds a unix listener at path through a net.ListenConfig, satisfying
// the noctx lint rule that forbids the bare net.Listen. The socket lifetime is
// governed by the returned listener's Close, not by a context, so a background
// context is the correct scope here.
func unixListen(path string) (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(context.Background(), "unix", path)
}

// requireParentDir verifies the socket's parent directory already exists,
// returning a startup error naming the path when it does not (AMEND-7:
// "Missing parent ⇒ startup error naming the path"). mcpmock never creates the
// directory.
func requireParentDir(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("control: socket parent directory %q does not exist: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("control: socket parent %q is not a directory", dir)
	}
	return nil
}

// handleBindInUse implements probe-before-unlink (AMEND-7). It is entered only
// after the bind failed; if that failure was EADDRINUSE it dials the existing
// socket. A successful dial means a live process owns it ⇒ a collision error. A
// refused dial means the socket is stale ⇒ unlink and retry the bind exactly
// once; a second failure is fatal. A non-EADDRINUSE failure is returned as-is.
func handleBindInUse(path string, listenErr error) (net.Listener, error) {
	if !errors.Is(listenErr, os.ErrExist) && !isAddrInUse(listenErr) {
		return nil, fmt.Errorf("control: listen unix %q: %w", path, listenErr)
	}
	if probeAlive(path) {
		return nil, fmt.Errorf("control: socket %q is in use by a live process; refusing to steal it", path)
	}
	// Stale socket: nothing is listening. Unlink and retry the bind once.
	if rmErr := os.Remove(path); rmErr != nil {
		return nil, fmt.Errorf("control: removing stale socket %q: %w", path, rmErr)
	}
	ln, err := unixListen(path)
	if err != nil {
		return nil, fmt.Errorf("control: re-listen unix %q after unlinking stale socket: %w", path, err)
	}
	return ln, nil
}

// probeAlive reports whether a live process is listening on the socket at path,
// by dialing it. A successful dial (immediately closed) means alive; a refused
// or errored dial means stale. The dial is bounded so a hung peer cannot stall
// startup.
func probeAlive(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// probeTimeout bounds the stale-socket probe dial.
const probeTimeout = 250 * time.Millisecond

// isAddrInUse reports whether err is an "address already in use" bind failure.
// A stale socket file makes net.Listen("unix", …) fail with a wrapped
// EADDRINUSE; matching on the message keeps this free of a syscall import and
// portable across the two supported platforms. It is acceptable because
// probeAlive is the actual safety mechanism: even a misclassification here only
// decides whether we probe, and probing a genuinely free path simply fails the
// dial and treats it as stale.
func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "in use")
}
