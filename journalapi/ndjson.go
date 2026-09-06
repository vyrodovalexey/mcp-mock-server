package journalapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrClosed is returned by [Writer.Write] and [Writer.Flush] after [Writer.Close].
var ErrClosed = errors.New("journalapi: writer closed")

// Writer streams [Record] values as newline-delimited JSON (NDJSON): one JSON
// object per line, in the order written (MOCK-602). It disables HTML escaping so
// characters such as <, > and & survive byte-identically, which matters for
// bodies and header values captured verbatim.
//
// Writer is not safe for concurrent use; a caller serializes writes. It holds no
// package-level state.
//
// Stability: v0.
type Writer struct {
	bw     *bufio.Writer
	enc    *json.Encoder
	closed bool
}

// NewWriter returns a [Writer] emitting NDJSON to w.
func NewWriter(w io.Writer) *Writer {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	return &Writer{bw: bw, enc: enc}
}

// Write emits one record followed by a newline. json.Encoder.Encode already
// appends the newline, so the output is valid NDJSON.
func (w *Writer) Write(r Record) error {
	if w.closed {
		return ErrClosed
	}
	if err := w.enc.Encode(r); err != nil {
		return fmt.Errorf("journalapi: encode record seq=%d: %w", r.Seq, err)
	}
	return nil
}

// WriteAll emits every record in order, stopping at the first error.
func (w *Writer) WriteAll(records []Record) error {
	for _, r := range records {
		if err := w.Write(r); err != nil {
			return err
		}
	}
	return nil
}

// Flush flushes buffered output to the underlying writer.
func (w *Writer) Flush() error {
	if w.closed {
		return ErrClosed
	}
	if err := w.bw.Flush(); err != nil {
		return fmt.Errorf("journalapi: flush: %w", err)
	}
	return nil
}

// Close flushes and marks the writer closed. It does not close the underlying
// writer, which the caller owns. Close is idempotent.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	err := w.bw.Flush()
	w.closed = true
	if err != nil {
		return fmt.Errorf("journalapi: flush on close: %w", err)
	}
	return nil
}

// Reader parses an NDJSON journal stream one [Record] at a time. Blank lines are
// tolerated and skipped. It holds no package-level state.
//
// Stability: v0.
type Reader struct {
	sc  *bufio.Scanner
	err error
}

// maxNDJSONLine bounds a single NDJSON line so a hostile or corrupt file cannot
// force unbounded buffering. Journal bodies can be large, so the ceiling is
// generous but finite.
const maxNDJSONLine = 64 << 20 // 64 MiB

// NewReader returns a [Reader] over r. Lines up to 64 MiB are supported; a
// longer line yields an error from [Reader.Read].
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxNDJSONLine)
	return &Reader{sc: sc}
}

// Read returns the next record. It returns [io.EOF] when the stream is
// exhausted. A malformed line yields a wrapped decode error naming the line.
func (r *Reader) Read() (Record, error) {
	if r.err != nil {
		return Record{}, r.err
	}
	for r.sc.Scan() {
		line := r.sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			r.err = fmt.Errorf("journalapi: decode NDJSON line: %w", err)
			return Record{}, r.err
		}
		return rec, nil
	}
	if err := r.sc.Err(); err != nil {
		r.err = fmt.Errorf("journalapi: scan NDJSON: %w", err)
		return Record{}, r.err
	}
	r.err = io.EOF
	return Record{}, io.EOF
}

// ReadAll reads every remaining record into a slice.
func (r *Reader) ReadAll() ([]Record, error) {
	var out []Record
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
}

// ReadView reads an entire NDJSON stream and returns a [View] over it, sorted
// into [Record.Seq] order. This is the entry point that makes assertions against
// a journal file, with no running server, possible (MOCK-602, MOCK-603.4).
func ReadView(r io.Reader) (View, error) {
	records, err := NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	return NewView(records), nil
}

// trimSpace returns b without leading and trailing ASCII whitespace. It avoids
// pulling in strings/bytes helpers for a one-line need and never allocates.
func trimSpace(b []byte) []byte {
	i := 0
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	j := len(b)
	for j > i && isSpace(b[j-1]) {
		j--
	}
	return b[i:j]
}

// isSpace reports whether c is an ASCII whitespace byte.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
