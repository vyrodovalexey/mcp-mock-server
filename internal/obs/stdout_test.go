package obs_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// TestMain installs goleak so a test that leaks a goroutine (e.g. a tracer
// exporter that never shuts down) fails loudly. goleak is the ADR-013 test-only
// dependency mandated for exactly this (MOCK-107.7).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestNoStdoutWrites is the single most important safety test for this package
// (ADR-011): in stdio transport mode stdout is the MCP frame channel, so a
// single stray write corrupts the protocol. It redirects the real os.Stdout to
// a pipe, exercises the entire observability surface — logger, metrics endpoint,
// tracer with a black-hole collector, health endpoints — and asserts that not
// one byte reached stdout.
//
// It must not run in parallel: it mutates the process-global os.Stdout.
func TestNoStdoutWrites(t *testing.T) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- data
	}()

	restore := func() {
		os.Stdout = origStdout
		_ = w.Close()
	}

	// Logs go to an explicit stderr-like buffer, never stdout.
	var logBuf bytes.Buffer
	b, err := obs.New(obs.Config{
		LogWriter: &logBuf,
		LogLevel:  slog.LevelDebug,
		Tracing: obs.TracingConfig{
			Enabled:       true,
			Endpoint:      "http://127.0.0.1:1",
			Insecure:      true,
			ExportTimeout: 100 * time.Millisecond,
		},
	})
	if err != nil {
		restore()
		t.Fatalf("new: %v", err)
	}

	// Exercise every path that a third-party lib might sneak a stdout write onto.
	b.Logger().Info("mcpmock started", obs.FieldEvent, obs.EventStartup)
	b.InstanceLogger("inst").Debug("request completed", obs.FieldEvent, obs.EventRequestCompleted)
	im := b.NewInstanceMetrics("inst")
	im.RecordRequest("http", "modern", "tools/call", "ok", 0.01)

	handler := b.Handler(obs.NewHealthState())
	for _, path := range []string{obs.PathMetrics, obs.PathHealthz, obs.PathReadyz} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}

	_, span := b.Tracer().Tracer().Start(context.Background(), obs.SpanRequest)
	span.End()
	shCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = b.Shutdown(shCtx)
	cancel()

	restore()
	data := <-captured

	if len(data) != 0 {
		t.Fatalf("observability package wrote %d bytes to stdout: %q", len(data), data)
	}
	if logBuf.Len() == 0 {
		t.Fatal("expected log output on the explicit writer, got none")
	}
}
