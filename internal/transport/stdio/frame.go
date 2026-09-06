package stdio

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// frame.go owns the newline-delimited JSON-RPC framing for the stdio transport
// and the single-writer serialization that makes MOCK-256 interleaving safe.
//
// Framing contract (ADR-006, wire clause 1.9 [P-04]): one JSON-RPC message per
// line, UTF-8, no embedded raw newline inside a message. A read yields exactly
// the bytes of one message with the terminating '\n' stripped; a write emits the
// message bytes followed by exactly one '\n', in a single Write call so a frame
// is never split across a newline boundary — a partially written frame
// interleaved with another request's frame is a silent, catastrophic protocol
// corruption, and the single-Write discipline is what forecloses it.

// defaultMaxLineBytes bounds a single inbound request line so a pathological or
// hostile peer cannot exhaust memory by sending an unterminated stream. A line
// longer than this is reported as [ErrLineTooLong] and the reader stops, rather
// than growing an unbounded buffer.
const defaultMaxLineBytes = 16 << 20 // 16 MiB

// ErrLineTooLong reports that an inbound frame exceeded the configured maximum
// line length before a newline was seen. It is returned wrapped so a caller can
// match it with errors.Is.
var ErrLineTooLong = errors.New("stdio: inbound frame exceeds maximum line length")

// frameReader reads newline-delimited JSON-RPC messages from the supplied
// reader. It is used by the single reader goroutine only and holds no lock: the
// two-goroutine model (architecture.md §7.1) gives stdin exactly one reader.
type frameReader struct {
	br      *bufio.Reader
	maxLine int
}

// newFrameReader wraps r with a bounded line reader. A maxLine of zero selects
// [defaultMaxLineBytes].
func newFrameReader(r io.Reader, maxLine int) *frameReader {
	if maxLine <= 0 {
		maxLine = defaultMaxLineBytes
	}
	return &frameReader{
		br:      bufio.NewReaderSize(r, 64<<10),
		maxLine: maxLine,
	}
}

// readFrame reads one message, returning its bytes without the terminating
// newline. It returns io.EOF (possibly with the trailing partial bytes) when the
// stream ends, and [ErrLineTooLong] when a message exceeds the bound. Blank
// lines (a bare newline) are skipped so a peer that pads with newlines does not
// produce empty parse errors; an empty return with a nil error never happens.
//
// The returned slice is freshly allocated per call, so a worker may retain it
// (the engine keeps ex.Raw for the journal and the body hash) without the reader
// overwriting it on the next read.
func (fr *frameReader) readFrame() ([]byte, error) {
	for {
		line, err := fr.readLine()
		if err != nil {
			return line, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			// A blank framing line carries no message; skip it rather than
			// handing the engine empty bytes that would only yield a parse
			// error. A genuinely malformed (non-empty) frame is still passed
			// through to the engine, which answers -32700 without panic.
			continue
		}
		return line, nil
	}
}

// readLine reads a single newline-delimited line, bounded by maxLine, returning
// the bytes without the newline. On overflow it returns the bytes read so far
// wrapped in [ErrLineTooLong]; on stream end it returns any partial bytes with
// io.EOF.
func (fr *frameReader) readLine() ([]byte, error) {
	var buf []byte
	for {
		b, err := fr.br.ReadByte()
		if err != nil {
			return buf, err
		}
		if b == '\n' {
			return buf, nil
		}
		buf = append(buf, b)
		if len(buf) > fr.maxLine {
			return buf, fmt.Errorf("%w: %d bytes", ErrLineTooLong, len(buf))
		}
	}
}

// writeFrame writes message followed by exactly one newline in a SINGLE Write
// call, so no other frame can interleave between the payload and its terminator.
// It is the only function in the package that writes protocol bytes, and it is
// called only by the single writer goroutine (see transport.go): those two facts
// together are the frame-level serialization guarantee MOCK-256 requires.
//
// The message must not itself contain a newline; the engine's byte-stable
// encoder (jsonrpc.Response.Encode) never emits one, so a well-formed frame is
// always a single line. writeFrame does not validate this — validating on the
// hot path would cost a scan — but the contract is stated so a future frame
// source honors it.
func writeFrame(w io.Writer, message []byte) error {
	// One allocation and one Write: assemble payload+'\n' so the write is
	// atomic at the io.Writer boundary. Splitting into two Writes would let the
	// os.File short-write or another goroutine's write (there is none, by
	// construction) interpose between the bytes and the newline.
	buf := make([]byte, 0, len(message)+1)
	buf = append(buf, message...)
	buf = append(buf, '\n')
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("stdio: write frame: %w", err)
	}
	return nil
}
