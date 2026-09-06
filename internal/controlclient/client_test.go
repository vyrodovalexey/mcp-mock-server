package controlclient_test

import (
	"context"
	"errors"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
	"github.com/vyrodovalexey/mcp-mock-server/internal/controlclient"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// stubBackend is a minimal control.Backend for driving the real control.Handler
// in tests, so the client is exercised against the genuine server route table,
// not a hand-rolled mock of the wire.
type stubBackend struct {
	instances []control.InstanceInfo
	seed      control.SeedInfo
	health    control.Health
	cleared   bool
}

func (b *stubBackend) Instances(context.Context) ([]control.InstanceInfo, error) {
	return b.instances, nil
}

func (b *stubBackend) Instance(_ context.Context, name string) (control.InstanceInfo, error) {
	for _, in := range b.instances {
		if in.Name == name {
			return in, nil
		}
	}
	return control.InstanceInfo{}, control.ErrNotFound
}

func (b *stubBackend) Seed(context.Context) (control.SeedInfo, error) { return b.seed, nil }
func (b *stubBackend) Health(context.Context) (control.Health, error) { return b.health, nil }
func (b *stubBackend) For(name string) control.InstanceBackend {
	return &stubInstance{b: b, name: name}
}

// stubInstance is the per-instance half of stubBackend.
type stubInstance struct {
	b    *stubBackend
	name string
}

func (s *stubInstance) Journal(context.Context, journalapi.Query) (journalapi.Page, error) {
	return journalapi.Page{Total: 1}, nil
}

func (s *stubInstance) JournalStream(context.Context, journalapi.Query) (iter.Seq2[journalapi.Record, error], error) {
	return nil, control.ErrUnsupported
}

func (s *stubInstance) Correlations(context.Context, journalapi.Query) ([]journalapi.Correlation, error) {
	return nil, nil
}

func (s *stubInstance) ClearJournal(context.Context) error {
	s.b.cleared = true
	return nil
}

// shortSockDir makes a short-pathed temp dir under /tmp so the socket path stays
// within the platform sun_path limit (104 bytes on macOS), which the default
// t.TempDir() path can exceed.
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mcpcc")
	if err != nil {
		t.Fatalf("mkdir short temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newBackend returns a stub with one instance named "a".
func newBackend() *stubBackend {
	return &stubBackend{
		instances: []control.InstanceInfo{{Name: "a", MountPath: "/mcp", Era: "modern"}},
		seed:      control.SeedInfo{Seed: 42, Source: "flag"},
		health:    control.Health{Status: "ok", Instances: 1, Seed: 42},
	}
}

// endpointCase names a transport the client-vs-server agreement test runs over.
type endpointCase struct {
	name  string
	build func(t *testing.T, h http.Handler) (*controlclient.Client, func())
}

// tcpEndpoint serves h over an httptest TCP server and returns a --url client.
func tcpEndpoint(t *testing.T, h http.Handler) (*controlclient.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	c, err := controlclient.New(controlclient.Endpoint{URL: srv.URL})
	if err != nil {
		srv.Close()
		t.Fatalf("New(url): %v", err)
	}
	return c, srv.Close
}

// udsEndpoint serves h over a unix socket and returns a --socket client, so the
// same operations are exercised over both transports (criterion: ctl reaches the
// control API over both HTTP and UDS and agrees).
func udsEndpoint(t *testing.T, h http.Handler) (*controlclient.Client, func()) {
	t.Helper()
	sock := filepath.Join(shortSockDir(t), "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	c, err := controlclient.New(controlclient.Endpoint{Socket: sock})
	if err != nil {
		t.Fatalf("New(socket): %v", err)
	}
	return c, func() { _ = srv.Close() }
}

// TestClientAgreesAcrossTransports drives the real control.Handler over TCP and
// UDS and asserts the client decodes the same values on both — the ADR-015
// "same operations, different transport" property, seen from the client side.
func TestClientAgreesAcrossTransports(t *testing.T) {
	endpoints := []endpointCase{
		{"http", tcpEndpoint},
		{"uds", udsEndpoint},
	}
	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			be := newBackend()
			h := control.NewHandler(be, nil)
			c, closeFn := ep.build(t, h)
			defer closeFn()
			ctx := context.Background()

			seed, err := c.Seed(ctx)
			if err != nil || seed.Seed != 42 || seed.Source != "flag" {
				t.Fatalf("Seed = %+v, err = %v", seed, err)
			}
			health, err := c.Health(ctx)
			if err != nil || health.Status != "ok" || health.Seed != 42 {
				t.Fatalf("Health = %+v, err = %v", health, err)
			}
			list, err := c.Instances(ctx)
			if err != nil || len(list) != 1 || list[0].Name != "a" {
				t.Fatalf("Instances = %+v, err = %v", list, err)
			}
			one, err := c.Instance(ctx, "a")
			if err != nil || one.Name != "a" {
				t.Fatalf("Instance = %+v, err = %v", one, err)
			}
			page, err := c.Journal(ctx, "a", journalapi.Query{})
			if err != nil || page.Total != 1 {
				t.Fatalf("Journal = %+v, err = %v", page, err)
			}
			if err := c.ClearJournal(ctx, "a"); err != nil {
				t.Fatalf("ClearJournal: %v", err)
			}
			if !be.cleared {
				t.Fatal("ClearJournal did not reach the backend")
			}
		})
	}
}

// TestNotFoundMapsToSentinel asserts a 404 envelope decodes to a wrapped
// control.ErrNotFound, so a caller branches with errors.Is regardless of
// transport.
func TestNotFoundMapsToSentinel(t *testing.T) {
	h := control.NewHandler(newBackend(), nil)
	c, closeFn := tcpEndpoint(t, h)
	defer closeFn()

	_, err := c.Instance(context.Background(), "missing")
	if !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestNewEndpointValidation asserts New rejects an empty or ambiguous endpoint.
func TestNewEndpointValidation(t *testing.T) {
	cases := []struct {
		name string
		ep   controlclient.Endpoint
	}{
		{"none", controlclient.Endpoint{}},
		{"both", controlclient.Endpoint{URL: "http://x", Socket: "/s"}},
		{"bad-scheme", controlclient.Endpoint{URL: "ftp://x"}},
		{"no-host", controlclient.Endpoint{URL: "http://"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := controlclient.New(tc.ep); err == nil {
				t.Fatalf("New(%+v) = nil error, want error", tc.ep)
			}
		})
	}
}
