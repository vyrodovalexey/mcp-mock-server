package control

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveSocketPath exercises the AMEND-7 override precedence and the
// length-fallback behaviour: an explicit override wins verbatim, the env var is
// next, and the default embeds the pid under XDG_RUNTIME_DIR or /tmp.
func TestResolveSocketPath(t *testing.T) {
	const pid = 4242
	tests := []struct {
		name     string
		flag     SocketOverride
		env      map[string]string
		wantPath string
		wantFell bool
		wantErr  bool
	}{
		{
			name:     "explicit flag wins verbatim",
			flag:     SocketOverride{Path: "/run/custom.sock", Set: true},
			env:      map[string]string{EnvControlSocket: "/ignored.sock", "XDG_RUNTIME_DIR": "/run/user/1000"},
			wantPath: "/run/custom.sock",
		},
		{
			name:     "env var when no flag",
			env:      map[string]string{EnvControlSocket: "/run/env.sock"},
			wantPath: "/run/env.sock",
		},
		{
			name:     "default under XDG_RUNTIME_DIR",
			env:      map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
			wantPath: "/run/user/1000/mcpmock-4242.sock",
		},
		{
			name:     "default falls back to /tmp when XDG absent",
			env:      map[string]string{},
			wantPath: "/tmp/mcpmock-4242.sock",
		},
		{
			name:     "relative XDG is ignored, falls to /tmp",
			env:      map[string]string{"XDG_RUNTIME_DIR": "relative/dir"},
			wantPath: "/tmp/mcpmock-4242.sock",
		},
		{
			name:    "empty explicit override is an error",
			flag:    SocketOverride{Path: "", Set: true},
			wantErr: true,
		},
		{
			name:    "over-long explicit override is an error",
			flag:    SocketOverride{Path: "/" + strings.Repeat("x", sunPathMax()+10), Set: true},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := func(k string) string { return tt.env[k] }
			path, fell, err := ResolveSocketPath(tt.flag, env, pid)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", path)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
			if fell != tt.wantFell {
				t.Errorf("fellBack = %v, want %v", fell, tt.wantFell)
			}
		})
	}
}

// TestResolveDefaultLengthFallback asserts that an XDG path that would overflow
// the platform limit falls back to /tmp and reports fellBack (AMEND-7).
func TestResolveDefaultLengthFallback(t *testing.T) {
	longDir := "/" + strings.Repeat("d", sunPathMax())
	env := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return longDir
		}
		return ""
	}
	path, fell, err := ResolveSocketPath(SocketOverride{}, env, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fell {
		t.Errorf("expected fellBack=true for an over-long XDG dir")
	}
	if filepath.Dir(path) != tmpSocketDir {
		t.Errorf("expected fallback into %q, got %q", tmpSocketDir, path)
	}
}

// shortSockDir returns a short-lived directory whose path is short enough that
// a socket under it stays within the platform sun_path limit (~104 bytes on
// macOS), which t.TempDir()'s long paths can exceed. It is cleaned up with the
// test.
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mcpctl")
	if err != nil {
		t.Fatalf("mkdir short temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestListenUDSMode asserts the socket is created 0600 owner-only (AMEND-7),
// verified after bind and before any accept could widen it.
func TestListenUDSMode(t *testing.T) {
	path := filepath.Join(shortSockDir(t), "ctl.sock")
	ln, err := ListenUDS(path)
	if err != nil {
		t.Fatalf("ListenUDS: %v", err)
	}
	defer func() { _ = ln.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != socketMode {
		t.Errorf("socket mode = %#o, want %#o", perm, socketMode)
	}
}

// TestListenUDSMissingParent asserts a missing parent directory is a startup
// error naming the path, not a silent directory creation (AMEND-7).
func TestListenUDSMissingParent(t *testing.T) {
	path := filepath.Join(shortSockDir(t), "nope", "ctl.sock")
	_, err := ListenUDS(path)
	if err == nil {
		t.Fatal("expected error for missing parent directory")
	}
	if !strings.Contains(err.Error(), "parent") {
		t.Errorf("error should name the missing parent: %v", err)
	}
}

// TestListenUDSProbeBeforeUnlink is the load-bearing AMEND-7 test: a LIVE socket
// is never stolen, and a STALE socket file is cleaned up and rebound.
func TestListenUDSProbeBeforeUnlink(t *testing.T) {
	t.Run("live socket is not stolen", func(t *testing.T) {
		path := filepath.Join(shortSockDir(t), "live.sock")
		live, err := ListenUDS(path)
		if err != nil {
			t.Fatalf("first bind: %v", err)
		}
		defer func() { _ = live.Close() }()
		// Accept in the background so a probe dial succeeds (a live owner).
		go func() {
			for {
				c, aerr := live.Accept()
				if aerr != nil {
					return
				}
				_ = c.Close()
			}
		}()

		_, err = ListenUDS(path)
		if err == nil {
			t.Fatal("expected a collision error binding over a live socket")
		}
		if !strings.Contains(err.Error(), "live process") {
			t.Errorf("collision error should mention the live process: %v", err)
		}
		// The live listener must still be usable — it was not stolen.
		if _, derr := net.Dial("unix", path); derr != nil {
			t.Errorf("live socket should still be dialable: %v", derr)
		}
	})

	t.Run("stale socket is cleaned up and rebound", func(t *testing.T) {
		path := filepath.Join(shortSockDir(t), "stale.sock")
		// Create a stale socket file: bind then close, leaving the file behind
		// with nothing listening (simulating a SIGKILLed prior process).
		first, err := ListenUDS(path)
		if err != nil {
			t.Fatalf("first bind: %v", err)
		}
		// Close the listener but leave the file (net closes+unlinks on Close, so
		// recreate a bare stale file to model an unclean exit).
		_ = first.Close()
		staleFile(t, path)

		ln, err := ListenUDS(path)
		if err != nil {
			t.Fatalf("expected stale socket to be cleaned and rebound, got: %v", err)
		}
		defer func() { _ = ln.Close() }()
	})
}

// staleFile leaves a non-socket regular file at path to model a stale socket
// that no process is listening on, so probeAlive's dial is refused.
func staleFile(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, socketMode)
	if err != nil {
		t.Fatalf("create stale file: %v", err)
	}
	_ = f.Close()
}

// TestIsAddrInUse sanity-checks the EADDRINUSE classifier used to decide whether
// to probe.
func TestIsAddrInUse(t *testing.T) {
	if isAddrInUse(nil) {
		t.Error("nil error is not in-use")
	}
	if !isAddrInUse(errors.New("bind: address already in use")) {
		t.Error("expected 'address already in use' to classify as in-use")
	}
}
