package journalapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	japi "github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

func TestNDJSONWriteRead(t *testing.T) {
	t.Parallel()
	in := []japi.Record{sampleRecord(1), sampleRecord(2), sampleRecord(3)}

	var buf bytes.Buffer
	w := japi.NewWriter(&buf)
	if err := w.WriteAll(in); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// One JSON object per line.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(in) {
		t.Fatalf("got %d NDJSON lines, want %d", len(lines), len(in))
	}
	for i, ln := range lines {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("line %d is not valid JSON: %s", i, ln)
		}
	}

	out, err := japi.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("read %d records, want %d", len(out), len(in))
	}
}

// TestNDJSONByteIdentical asserts a record's NDJSON encoding is byte-identical
// to re-encoding what was read back (MOCK-601.2 fidelity across the file
// boundary): header ordering, duplicates and casing all survive.
func TestNDJSONByteIdentical(t *testing.T) {
	t.Parallel()
	in := sampleRecord(41)

	var first bytes.Buffer
	w := japi.NewWriter(&first)
	if err := w.Write(in); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Read it back, then re-encode; the bytes must match.
	rd := japi.NewReader(bytes.NewReader(first.Bytes()))
	got, err := rd.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var second bytes.Buffer
	w2 := japi.NewWriter(&second)
	if err := w2.Write(got); err != nil {
		t.Fatalf("re-write: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("re-close: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("NDJSON not byte-identical across round-trip:\n first: %s\nsecond: %s",
			first.Bytes(), second.Bytes())
	}
}

// TestReadViewFromFileNoServer is the MOCK-603.4 case: a View supporting the
// full Selector surface, built from an NDJSON stream with no server present.
func TestReadViewFromFileNoServer(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := japi.NewWriter(&buf)
	if err := w.WriteAll(records()); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := japi.ReadView(&buf)
	if err != nil {
		t.Fatalf("ReadView: %v", err)
	}
	if v.Len() != 3 {
		t.Fatalf("View.Len = %d, want 3", v.Len())
	}
	// The full selector surface must work against the file-loaded view.
	if n := v.Filter(japi.Selector{Method: "tools/*"}).Len(); n != 2 {
		t.Errorf("method glob on file view = %d, want 2", n)
	}
	if cs := japi.Correlations(v); len(cs) != 1 || cs[0].ChainID != "chain-1" {
		t.Errorf("Correlations on file view = %+v, want one chain-1", cs)
	}
}

func TestReaderSkipsBlankLines(t *testing.T) {
	t.Parallel()
	rec, _ := json.Marshal(sampleRecord(1))
	stream := "\n" + string(rec) + "\n\n"
	out, err := japi.NewReader(strings.NewReader(stream)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("read %d records, want 1 (blank lines skipped)", len(out))
	}
}

func TestReaderMalformedLine(t *testing.T) {
	t.Parallel()
	rd := japi.NewReader(strings.NewReader("{not json}\n"))
	_, err := rd.Read()
	if err == nil {
		t.Fatal("expected an error on malformed NDJSON line")
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("malformed line reported as EOF")
	}
}

func TestReaderEOF(t *testing.T) {
	t.Parallel()
	rd := japi.NewReader(strings.NewReader(""))
	_, err := rd.Read()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream: got %v, want io.EOF", err)
	}
}

func TestWriterClosedIsError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := japi.NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Write(sampleRecord(1)); !errors.Is(err, japi.ErrClosed) {
		t.Fatalf("Write after Close: got %v, want ErrClosed", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close should be idempotent, got %v", err)
	}
}

// TestNDJSONNoHTMLEscaping guards that <, > and & survive verbatim — HTML
// escaping would corrupt captured bodies and the redaction marker.
func TestNDJSONNoHTMLEscaping(t *testing.T) {
	t.Parallel()
	rec := japi.Record{
		SchemaVersion: japi.SchemaVersion,
		Seq:           1,
		Response:      &japi.ResponsePart{Body: []byte(`{"html":"<a>&</a>"}`)},
	}
	var buf bytes.Buffer
	w := japi.NewWriter(&buf)
	if err := w.Write(rec); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if strings.Contains(buf.String(), `\u003c`) || strings.Contains(buf.String(), `\u0026`) {
		t.Fatalf("HTML escaping corrupted the body: %s", buf.String())
	}
}
