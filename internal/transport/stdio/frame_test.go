package stdio_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/stdio"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// TestFramingWritesSingleLinePerResponse asserts the framing contract: each
// response is exactly one newline-terminated line and contains no embedded
// newline, so a peer's line reader recovers exactly one message per read.
func TestFramingWritesSingleLinePerResponse(t *testing.T) {
	inServer, inClient := io.Pipe()
	outClient, outServer := io.Pipe()

	cfg := stdio.Config{In: inServer, Out: outServer, Instance: newInstance(1, "inst", false), Pipeline: newPipeline(0)}
	tr, err := stdio.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tr.Serve(ctx) }()

	go func() {
		w := bufio.NewWriter(inClient)
		for i := 0; i < 4; i++ {
			_, _ = w.Write(rawRequest(strconv.Itoa(i), wire.MethodDiscover))
			_, _ = w.Write([]byte{'\n'})
		}
		_ = w.Flush()
	}()

	br := bufio.NewReader(outClient)
	for i := 0; i < 4; i++ {
		line, err := br.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		body := bytes.TrimRight(line, "\n")
		if bytes.Contains(body, []byte{'\n'}) {
			t.Fatalf("frame %d contains an embedded newline: %q", i, body)
		}
		if len(body) == 0 {
			t.Fatalf("frame %d is empty", i)
		}
	}
	_ = inClient.Close()
	<-done
}

// TestOversizedFrameRejected asserts a frame exceeding MaxLineBytes is rejected
// as ErrLineTooLong and ends the read loop rather than growing an unbounded
// buffer (the memory-DoS boundary).
func TestOversizedFrameRejected(t *testing.T) {
	// An unterminated 4 KiB stream against a 1 KiB line bound.
	big := bytes.Repeat([]byte("A"), 4096)
	in := bytes.NewReader(big)
	out := &syncBuffer{}

	cfg := stdio.Config{
		In: in, Out: out,
		Instance: newInstance(1, "inst", false), Pipeline: newPipeline(0),
		MaxLineBytes: 1024,
	}
	tr, err := stdio.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- tr.Serve(context.Background()) }()

	select {
	case serveErr := <-errCh:
		// Serve surfaces the overflow (a non-EOF read error). It must not panic
		// and must not hang.
		if serveErr == nil {
			t.Fatal("expected a non-nil error for an oversized unterminated frame")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve hung on an oversized frame instead of bounding it")
	}
}

// TestStdioHTTPByteEquality is acceptance criterion 5 / MOCK-102 102.3-102.4:
// the response bytes for a given payload over stdio are byte-identical to those
// produced through the engine's buffered (HTTP) sink at the same seed, for
// tools/list and tools/call. Both go through the same engine encoder; the test
// proves the stdio transport adds nothing but the trailing newline framing.
func TestStdioHTTPByteEquality(t *testing.T) {
	cases := []struct {
		name   string
		method string
	}{
		{"tools/call", wire.MethodToolsCall},
		{"tools/list", wire.MethodToolsList},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawRequest("1", tc.method)

			// HTTP-equivalent path: drive the pipeline with a BufferedSink, the
			// same sink httpx uses (emit.go). Same seed via the same instance.
			httpInst := newInstance(42, "inst", false)
			httpPipe := newPipeline(0)
			buffered := engine.NewBufferedSink()
			ex := &engine.Exchange{
				Ctx: context.Background(), Instance: httpInst,
				Transport: engine.KindHTTP, Peer: "http", Raw: raw,
			}
			if err := httpPipe.Handle(context.Background(), ex, buffered); err != nil {
				t.Fatalf("http Handle: %v", err)
			}
			httpBytes := buffered.Bytes()

			// stdio path: same seed, same method, read the framed line back and
			// strip the trailing newline.
			inServer, inClient := io.Pipe()
			outClient, outServer := io.Pipe()
			cfg := stdio.Config{
				In: inServer, Out: outServer,
				Instance: newInstance(42, "inst", false), Pipeline: newPipeline(0),
			}
			tr, err := stdio.New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tr.Serve(ctx) }()
			go func() {
				_, _ = inClient.Write(raw)
				_, _ = inClient.Write([]byte{'\n'})
			}()

			line, err := bufio.NewReader(outClient).ReadBytes('\n')
			if err != nil {
				t.Fatalf("read stdio frame: %v", err)
			}
			stdioBytes := bytes.TrimRight(line, "\n")

			if !bytes.Equal(httpBytes, stdioBytes) {
				t.Fatalf("byte mismatch:\n http  = %q\n stdio = %q", httpBytes, stdioBytes)
			}
			_ = inClient.Close()
			<-done
		})
	}
}
