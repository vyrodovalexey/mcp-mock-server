//go:build functional

package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/assert"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// mock_601_journal_test.go — journal completeness (MOCK-601) and query/filter
// (MOCK-602), plus the MOCK-105.5 credential-safety scan. MOCK-602 filters are
// exercised through Server.Control().For(name).Journal — ADR-015's in-process
// front end, the SAME implementation the `ctl` CLI and the HTTP control API wrap
// — so the filter semantics are proven functionally while the CLI transport
// parity stays at integration level (test-strategy §1/§4).

// seedJournal drives a mix of methods so the filters have something to select,
// and returns the server + its instance.
func seedJournal(t *testing.T) (*mcpmock.Server, *mcpmock.Instance) {
	t.Helper()
	s, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	if _, err := c.Discover(ctx(t), mcpclient.IntID(1)); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, err := c.ListTools(ctx(t), mcpclient.IntID(2)); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if _, err := c.CallTool(ctx(t), mcpclient.IntID(3), "echo", map[string]any{"message": "one"}); err != nil {
		t.Fatalf("echo 1: %v", err)
	}
	if _, err := c.CallTool(ctx(t), mcpclient.IntID(4), "echo", map[string]any{"message": "two"}); err != nil {
		t.Fatalf("echo 2: %v", err)
	}
	return s, inst
}

// TestMOCK601_JournalCapturesRequests asserts every driven request is captured
// (MOCK-601): four requests ⇒ four records, each with the method it carried.
func TestMOCK601_JournalCapturesRequests(t *testing.T) {
	t.Parallel()
	_, inst := seedJournal(t)
	if got := inst.Journal().Len(); got != 4 {
		t.Fatalf("journal captured %d records, want 4 (MOCK-601)", got)
	}
	methods := map[string]int{}
	inst.Journal().Iter(func(rec journalapi.Record) bool {
		methods[rec.JSONRPC.Method]++
		return true
	})
	if methods["server/discover"] != 1 || methods["tools/list"] != 1 || methods["tools/call"] != 2 {
		t.Fatalf("journal method counts = %v, want discover:1 list:1 call:2", methods)
	}
}

// TestMOCK602_Filters exercises the MOCK-602.4 filter dimensions through the
// in-process control front end: method glob, tool name glob, status code, and
// the limit page bound.
func TestMOCK602_Filters(t *testing.T) {
	t.Parallel()
	s, inst := seedJournal(t)
	ctl := s.Control().For(inst.Name())
	c := ctx(t)

	// Method glob: tools/* selects both echo calls.
	page, err := ctl.Journal(c, journalapi.Query{Selector: journalapi.Selector{Method: "tools/call"}})
	if err != nil {
		t.Fatalf("journal method filter: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("method=tools/call total = %d, want 2", page.Total)
	}

	// Name glob: the tool name is extracted onto the record; echo selects two.
	page, err = ctl.Journal(c, journalapi.Query{Selector: journalapi.Selector{Name: "echo"}})
	if err != nil {
		t.Fatalf("journal name filter: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("name=echo total = %d, want 2", page.Total)
	}

	// Status filter: 200 selects the successful calls.
	ok := 200
	page, err = ctl.Journal(c, journalapi.Query{Selector: journalapi.Selector{StatusCode: &ok}})
	if err != nil {
		t.Fatalf("journal status filter: %v", err)
	}
	if page.Total < 1 {
		t.Fatalf("status=200 total = %d, want >= 1", page.Total)
	}

	// Limit page bound: limit 1 returns one record but reports the full total.
	page, err = ctl.Journal(c, journalapi.Query{Limit: 1})
	if err != nil {
		t.Fatalf("journal limit: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("limit=1 returned %d records, want 1", len(page.Records))
	}
	if page.Total != 4 {
		t.Fatalf("limit=1 total = %d, want 4 (total independent of page)", page.Total)
	}
}

// TestMOCK602_ClearJournal asserts clearing empties the journal (602.6/702.7).
func TestMOCK602_ClearJournal(t *testing.T) {
	t.Parallel()
	s, inst := seedJournal(t)
	ctl := s.Control().For(inst.Name())
	if err := ctl.ClearJournal(ctx(t)); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := inst.Journal().Len(); got != 0 {
		t.Fatalf("journal not empty after clear: %d records", got)
	}
}

// TestMOCK602_NDJSONRoundTrip exports the journal as NDJSON and loads it back
// into a server-less View via assert.FromReader (MOCK-603.4), proving the
// on-the-wire journal contract round-trips.
func TestMOCK602_NDJSONRoundTrip(t *testing.T) {
	t.Parallel()
	s, inst := seedJournal(t)
	ctl := s.Control().For(inst.Name())

	seq, err := ctl.JournalStream(ctx(t), journalapi.Query{})
	if err != nil {
		t.Fatalf("journal stream: %v", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for rec, rErr := range seq {
		if rErr != nil {
			t.Fatalf("stream record: %v", rErr)
		}
		if err := enc.Encode(rec); err != nil {
			t.Fatalf("encode ndjson: %v", err)
		}
	}
	view, err := assert.FromReader(&buf)
	if err != nil {
		t.Fatalf("assert.FromReader: %v", err)
	}
	if view.Len() != 4 {
		t.Fatalf("round-tripped view has %d records, want 4", view.Len())
	}
}

// sentinelCredential is a clearly test-only bearer value the credential-scan test
// sends and then proves never appears in the serialised journal (MOCK-105.5,
// test-strategy §10 "test-only-…" convention).
const sentinelCredential = "test-only-SENTINEL-9f3c1a7e-do-not-log"

// TestMOCK105_CredentialScan sends a request carrying a sentinel Authorization
// bearer value and asserts the raw value appears NOWHERE in the fully-serialised
// journal — the header value is redacted (MOCK-105.5, security.md §5). This is
// the "no credential appears in journal output" sentinel test.
func TestMOCK105_CredentialScan(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)

	r := mcpclient.ToolsListRequest(mcpclient.IntID(1))
	completeCallInfo(r)
	r.Headers = []mcpclient.Header{{Name: "Authorization", Value: "Bearer " + sentinelCredential}}
	if _, err := c.Do(ctx(t), r); err != nil {
		t.Fatalf("do: %v", err)
	}

	// Serialise the ENTIRE journal (every record, every field) and scan it.
	snapshot := inst.Journal().Snapshot()
	blob, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	if strings.Contains(string(blob), sentinelCredential) {
		t.Fatalf("SECURITY: sentinel credential leaked into the journal (MOCK-105.5)\njournal: %s", blob)
	}
	// The Authorization header must still be present but redacted, so the scan
	// is proving redaction, not merely absence of the header.
	if !journalHasRedactedAuthorization(inst.Journal()) {
		t.Fatalf("expected a redacted Authorization header in the journal (security.md §5)")
	}
}

// journalHasRedactedAuthorization reports whether the journal carries an
// Authorization header whose value was redacted (the RedactedHeaderValue prefix).
func journalHasRedactedAuthorization(v journalapi.View) bool {
	var found bool
	v.Iter(func(rec journalapi.Record) bool {
		if rec.HTTP == nil {
			return true
		}
		for _, h := range rec.HTTP.Headers {
			if eqFold(h[0], "Authorization") && strings.HasPrefix(h[1], journalapi.RedactedHeaderValue) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// TestMOCK602_NoStreamGoroutineLeak drains a journal stream fully and confirms a
// second stream still works, a light guard that the streaming path (which spawns
// a bounded exporter goroutine) does not wedge. goleak in the parent module's
// TestMain is the authoritative leak check; this is a functional smoke.
func TestMOCK602_StreamTwice(t *testing.T) {
	t.Parallel()
	s, inst := seedJournal(t)
	ctl := s.Control().For(inst.Name())
	for i := 0; i < 2; i++ {
		seq, err := ctl.JournalStream(context.Background(), journalapi.Query{})
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		var n int
		for _, rErr := range seq {
			if rErr != nil {
				t.Fatalf("stream %d record: %v", i, rErr)
			}
			n++
		}
		if n != 4 {
			t.Fatalf("stream %d yielded %d records, want 4", i, n)
		}
	}
}
