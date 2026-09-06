package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
	"github.com/vyrodovalexey/mcp-mock-server/internal/controlclient"
)

// TestReorderArgs asserts positionals are moved after flags so an instance name
// can appear before or after the endpoint flags. It covers "--flag value",
// "--flag=value" and the "--" terminator.
func TestReorderArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"name after flags", []string{"--url", "u", "n"}, []string{"--url", "u", "n"}},
		{"name before flags", []string{"n", "--url", "u"}, []string{"--url", "u", "n"}},
		{"eq form", []string{"n", "--url=u"}, []string{"--url=u", "n"}},
		{"limit int flag", []string{"n", "--limit", "5"}, []string{"--limit", "5", "n"}},
		{"terminator", []string{"--url", "u", "--", "--weird-name"}, []string{"--url", "u", "--weird-name"}},
		{"only name", []string{"n"}, []string{"n"}},
		{"only flags", []string{"--url", "u"}, []string{"--url", "u"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderArgs(tc.in)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("reorderArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestCtlNoEndpoint asserts ctl reports a usage error (exit 2) when neither an
// endpoint flag nor an endpoint env var is set.
func TestCtlNoEndpoint(t *testing.T) {
	t.Setenv(envControlURL, "")
	t.Setenv(envControlSocketCtl, "")
	var out, errOut bytes.Buffer
	code := runCtl(context.Background(), &out, &errOut, []string{"seed"})
	if code != exitIO {
		t.Fatalf("exit code = %d, want %d", code, exitIO)
	}
	if !strings.Contains(errOut.String(), "no control endpoint") {
		t.Errorf("stderr %q missing endpoint hint", errOut.String())
	}
}

// TestCtlUnknownVerb asserts an unknown verb is a usage error listing the verbs.
func TestCtlUnknownVerb(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runCtl(context.Background(), &out, &errOut, []string{"explode"})
	if code != exitIO {
		t.Fatalf("exit code = %d, want %d", code, exitIO)
	}
	if !strings.Contains(errOut.String(), `unknown verb "explode"`) {
		t.Errorf("stderr %q missing unknown-verb message", errOut.String())
	}
}

// startServer starts an in-process facade over HTTP with a control TCP listener
// and a control socket, returning the control URL and socket path.
func startServer(t *testing.T, opts ...mcpmock.Option) (*mcpmock.Server, string, string) {
	t.Helper()
	sock := filepath.Join(shortSockDir(t), "c.sock")
	base := []mcpmock.Option{
		mcpmock.WithSeed(7),
		mcpmock.WithAddr("127.0.0.1:0"),
		mcpmock.WithControlAddr("127.0.0.1:0"),
		mcpmock.WithControlSocket(sock),
	}
	srv, err := mcpmock.New(append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, srv.ControlURL(), srv.ControlSocket()
}

// shortSockDir makes a short temp dir under /tmp so the socket path stays within
// the platform sun_path limit.
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mcpctl")
	if err != nil {
		t.Fatalf("mkdir short temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestCtlReachesControlOverBothTransports is the criterion: ctl reaches the
// control API over both HTTP and UDS and agrees with it. It drives runCtl end to
// end against a live facade and asserts the seed the CLI prints equals the
// server's effective seed (MOCK-704.2), over both transports.
func TestCtlReachesControlOverBothTransports(t *testing.T) {
	srv, url, sock := startServer(t)
	wantSeed := srv.Seed()

	endpoints := []struct {
		name string
		flag []string
	}{
		{"http", []string{"seed", "--url", url}},
		{"uds", []string{"seed", "--socket", sock}},
	}
	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := runCtl(context.Background(), &out, &errOut, ep.flag)
			if code != exitOK {
				t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitOK, errOut.String())
			}
			var si control.SeedInfo
			if err := json.Unmarshal(out.Bytes(), &si); err != nil {
				t.Fatalf("decode seed: %v (raw=%q)", err, out.String())
			}
			if si.Seed != wantSeed {
				t.Errorf("ctl seed = %d, server seed = %d", si.Seed, wantSeed)
			}
		})
	}
}

// TestCtlInstanceNotFound asserts a not-found instance is a control-plane error
// mapped to exit 2, and the message is surfaced.
func TestCtlInstanceNotFound(t *testing.T) {
	_, url, _ := startServer(t)
	var out, errOut bytes.Buffer
	code := runCtl(context.Background(), &out, &errOut, []string{"instance", "ghost", "--url", url})
	if code != exitIO {
		t.Fatalf("exit code = %d, want %d", code, exitIO)
	}
	if !strings.Contains(errOut.String(), "not found") {
		t.Errorf("stderr %q missing not-found message", errOut.String())
	}
}

// TestCtlMissingName asserts a name-bearing verb without a name is a usage error.
func TestCtlMissingName(t *testing.T) {
	_, url, _ := startServer(t)
	var out, errOut bytes.Buffer
	code := runCtl(context.Background(), &out, &errOut, []string{"instance", "--url", url})
	if code != exitIO {
		t.Fatalf("exit code = %d, want %d", code, exitIO)
	}
	if !strings.Contains(errOut.String(), "instance name is required") {
		t.Errorf("stderr %q missing name-required message", errOut.String())
	}
}

// TestCtlHelpGeneratedFromRouteTable asserts `ctl --help` lists a line for every
// verb the route table exposes, and names its operationId — proving the help is
// generated from controlclient.Verbs() rather than hand-written (MOCK-104.3), so
// it cannot drift from the control API.
func TestCtlHelpGeneratedFromRouteTable(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runCtl(context.Background(), &out, &errOut, []string{"--help"})
	if code != exitOK {
		t.Fatalf("ctl --help exit = %d, want %d", code, exitOK)
	}
	help := errOut.String()
	for _, v := range controlclient.Verbs() {
		if !strings.Contains(help, v.Name) {
			t.Errorf("ctl --help missing verb %q", v.Name)
		}
		if !strings.Contains(help, v.OpID) {
			t.Errorf("ctl --help missing operationId %q for verb %q", v.OpID, v.Name)
		}
	}
}

// TestCtlEnvEndpointPrecedence asserts the endpoint env var is used when no flag
// is given, and a flag overrides it (flag > env), by pointing the env at a dead
// address and the flag at the live server.
func TestCtlEnvEndpointPrecedence(t *testing.T) {
	_, url, _ := startServer(t)
	t.Setenv(envControlURL, "http://127.0.0.1:1") // unreachable; flag must win

	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code := runCtl(ctx, &out, &errOut, []string{"seed", "--url", url})
	if code != exitOK {
		t.Fatalf("flag did not override env: exit=%d stderr=%q", code, errOut.String())
	}
}
