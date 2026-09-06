package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// stdioTransport speaks newline-delimited JSON-RPC over a reader/writer pair
// (MOCK-102 stdio). Per annex 1.9 [P-04]: one JSON-RPC message per line, UTF-8,
// no embedded raw newlines. The client writes exactly the request bytes it was
// given followed by a single '\n', and reads back one line as the response.
//
// This transport shares no framing code with the server's stdio transport
// (ADR-017): it reads and writes lines with the standard library alone.
type stdioTransport struct {
	w  io.Writer
	br *bufio.Reader
	// mu serialises write-then-read so a single transport used sequentially
	// cannot interleave a partial write with another goroutine's read. It does
	// not make concurrent Do calls meaningful — stdio is a single ordered
	// channel — but it prevents corruption if the caller misuses it.
	mu sync.Mutex
	// closer, when set, is closed by close().
	closer io.Closer
	// maxLine bounds a single response line so a pathological server cannot
	// exhaust memory in a test.
	maxLine int
}

// StdioConfig configures a stdio [Client]. Reader and Writer are the two ends
// of the pipe to the server: the client writes requests to Writer and reads
// responses from Reader. In-process tests typically wire these to an
// io.Pipe pair driven by the server under test.
type StdioConfig struct {
	// Reader is the stream the client reads responses from (the server's stdout).
	Reader io.Reader
	// Writer is the stream the client writes requests to (the server's stdin).
	Writer io.Writer
	// Closer, if set, is closed when the Client is closed (e.g. to signal EOF
	// to the server and trigger MOCK-212 cancellation on stdin EOF).
	Closer io.Closer
	// MaxLineBytes bounds one response line. Zero selects a 16 MiB default.
	MaxLineBytes int
}

func newStdioTransport(cfg StdioConfig) (*stdioTransport, error) {
	if cfg.Reader == nil || cfg.Writer == nil {
		return nil, errors.New("mcpclient: stdio requires both Reader and Writer")
	}
	maxLine := cfg.MaxLineBytes
	if maxLine <= 0 {
		maxLine = 16 << 20
	}
	return &stdioTransport{
		w:       cfg.Writer,
		br:      bufio.NewReaderSize(cfg.Reader, 64<<10),
		closer:  cfg.Closer,
		maxLine: maxLine,
	}, nil
}

// roundTrip writes payload as one line and reads one response line. If payload
// already contains a trailing newline it is not doubled. A request whose id is
// absent (a notification) still expects the caller to know no response will
// come; roundTrip nonetheless attempts a bounded read and reports gotResponse
// according to whether a line arrived before ctx expired.
//
// Context cancellation is honoured cooperatively: the blocking read runs in a
// helper goroutine and roundTrip returns as soon as ctx is done, so a cancelled
// test does not hang. The helper goroutine is bounded by maxLine and ends when
// the read completes or the underlying reader is closed.
func (t *stdioTransport) roundTrip(ctx context.Context, payload []byte, _ []Header) ([]byte, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.writeLine(payload); err != nil {
		return nil, false, err
	}

	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		line, err := t.readLine()
		ch <- readResult{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		// The read goroutine is left to drain into the buffered channel; it
		// ends when the reader yields or is closed by the caller. We do not
		// leak it into a goleak assertion because the test owns the pipe and
		// closes it, which unblocks the read.
		return nil, false, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			if errors.Is(r.err, io.EOF) && len(r.line) == 0 {
				// Clean EOF with no data: no response produced.
				return nil, false, nil
			}
			if len(r.line) == 0 {
				return nil, false, r.err
			}
			// Partial line then error: surface the bytes AND the error so the
			// caller can assert on a truncated/ malformed response.
			return r.line, true, r.err
		}
		return r.line, true, nil
	}
}

// writeLine writes payload followed by exactly one newline, unless payload
// already ends in one.
func (t *stdioTransport) writeLine(payload []byte) error {
	if _, err := t.w.Write(payload); err != nil {
		return fmt.Errorf("stdio write: %w", err)
	}
	if !bytes.HasSuffix(payload, []byte{'\n'}) {
		if _, err := t.w.Write([]byte{'\n'}); err != nil {
			return fmt.Errorf("stdio write newline: %w", err)
		}
	}
	return nil
}

// readLine reads a single newline-delimited message, bounded by maxLine. The
// returned bytes exclude the terminating newline.
func (t *stdioTransport) readLine() ([]byte, error) {
	var buf []byte
	for {
		b, err := t.br.ReadByte()
		if err != nil {
			return buf, err
		}
		if b == '\n' {
			return buf, nil
		}
		buf = append(buf, b)
		if len(buf) > t.maxLine {
			return buf, fmt.Errorf("stdio response line exceeds %d bytes", t.maxLine)
		}
	}
}

// close closes the optional closer, signalling EOF to the server.
func (t *stdioTransport) close() error {
	if t.closer == nil {
		return nil
	}
	return t.closer.Close()
}
