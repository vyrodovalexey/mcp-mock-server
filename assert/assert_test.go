package assert_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/assert"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// sentinelToken is the exact secret embedded in assert/testdata/leaky.ndjson as a
// passed-through header value. Every test that scans a message for a leak checks
// against this value.
const sentinelToken = "Bearer eyJSUPERSECRETtoken0123456789abcdefXYZ"

// recorder is a TestingT double that records Helper/Errorf/Fatalf activity
// without stopping the real test. Fatalf is made non-terminating on purpose so a
// single test can drive a fatal assertion and then inspect its message; the real
// *testing.T behaviour (goroutine exit) is a property of testing.TB, not of the
// assertion logic under test.
type recorder struct {
	helperCalls int
	fatal       bool
	errored     bool
	msg         string
}

func (r *recorder) Helper()                   { r.helperCalls++ }
func (r *recorder) Errorf(f string, a ...any) { r.errored = true; r.msg = fmt.Sprintf(f, a...) }
func (r *recorder) Fatalf(f string, a ...any) { r.fatal = true; r.msg = fmt.Sprintf(f, a...) }

// loadClean and loadLeaky load the committed NDJSON fixtures through the public
// file loader (MOCK-603.4: no running server).
func loadClean(t *testing.T) journalapi.View { t.Helper(); return mustFile(t, "testdata/clean.ndjson") }
func loadLeaky(t *testing.T) journalapi.View { t.Helper(); return mustFile(t, "testdata/leaky.ndjson") }

func mustFile(t *testing.T, path string) journalapi.View {
	t.Helper()
	v, err := assert.FromFile(path)
	if err != nil {
		t.Fatalf("FromFile(%q): %v", path, err)
	}
	return v
}

// --- FromFile / FromReader: work against a file with no server ---------------

func TestFromFileAndReaderAgree(t *testing.T) {
	t.Parallel()
	fromFile := loadClean(t)

	f, err := os.Open("testdata/clean.ndjson")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	fromReader, err := assert.FromReader(f)
	if err != nil {
		t.Fatalf("FromReader: %v", err)
	}
	if fromFile.Len() != fromReader.Len() {
		t.Fatalf("FromFile Len=%d, FromReader Len=%d", fromFile.Len(), fromReader.Len())
	}
	if fromFile.Len() != 5 {
		t.Fatalf("expected 5 records in clean fixture, got %d", fromFile.Len())
	}
}

func TestFromFileMissing(t *testing.T) {
	t.Parallel()
	if _, err := assert.FromFile("testdata/does-not-exist.ndjson"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// --- AssertNoHeader ----------------------------------------------------------

func TestCheckNoHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		view       func(*testing.T) journalapi.View
		header     string
		wantErr    bool
		wantInMsg  []string
		wantNotMsg []string
	}{
		{
			name:    "clean journal passes",
			view:    loadClean,
			header:  "X-Client-Token",
			wantErr: false,
		},
		{
			name:    "forbidden header present fails, case-insensitive",
			view:    loadLeaky,
			header:  "x-client-token", // lower-case query, header stored mixed-case
			wantErr: true,
			// Names the offending Seqs and method; redacts the value; never leaks.
			wantInMsg:  []string{"no-header", "seq=41", "seq=57", "seq=62", "tools/call", "redacted"},
			wantNotMsg: []string{sentinelToken},
		},
		{
			name:    "header absent everywhere passes",
			view:    loadLeaky,
			header:  "X-Not-Present",
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := assert.New(tc.view(t)).CheckNoHeader(tc.header)
			checkErr(t, err, tc.wantErr, tc.wantInMsg, tc.wantNotMsg)
		})
	}
}

// --- AssertHeaderNotValue ----------------------------------------------------

func TestCheckHeaderNotValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		view       func(*testing.T) journalapi.View
		header     string
		value      string
		wantErr    bool
		wantInMsg  []string
		wantNotMsg []string
	}{
		{
			name:    "clean journal passes",
			view:    loadClean,
			header:  "Authorization",
			value:   sentinelToken,
			wantErr: false,
		},
		{
			name:    "token passthrough detected",
			view:    loadLeaky,
			header:  "Authorization", // header stored as "authorization"
			value:   sentinelToken,
			wantErr: true,
			// Reports the matching Seqs; redacts the observed value; never leaks
			// the token even though the caller passed it in.
			wantInMsg:  []string{"header-not-value", "seq=41", "seq=57", "seq=62", "redacted"},
			wantNotMsg: []string{sentinelToken},
		},
		{
			name:    "different value passes",
			view:    loadLeaky,
			header:  "Authorization",
			value:   "Bearer some-other-token-entirely",
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := assert.New(tc.view(t)).CheckHeaderNotValue(tc.header, tc.value)
			checkErr(t, err, tc.wantErr, tc.wantInMsg, tc.wantNotMsg)
		})
	}
}

// --- AssertRequestCount ------------------------------------------------------

func TestCheckRequestCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		sel       journalapi.Selector
		n         int
		wantErr   bool
		wantInMsg []string
	}{
		{
			name:    "exact match passes",
			sel:     journalapi.Selector{Method: "tools/call"},
			n:       3, // clean fixture has 3 tools/call
			wantErr: false,
		},
		{
			name:      "wrong count fails with actual and sample",
			sel:       journalapi.Selector{Method: "tools/call"},
			n:         1,
			wantErr:   true,
			wantInMsg: []string{"request-count", "want exactly 1, got 3", "seq=41", "tools/call"},
		},
		{
			name:    "zero is a legitimate assertion that passes",
			sel:     journalapi.Selector{Method: "resources/read"},
			n:       0,
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := assert.New(loadClean(t)).CheckRequestCount(tc.sel, tc.n)
			checkErr(t, err, tc.wantErr, tc.wantInMsg, nil)
		})
	}
}

func TestCheckRequestCountAtMostAtLeast(t *testing.T) {
	t.Parallel()
	a := assert.New(loadClean(t))
	sel := journalapi.Selector{Method: "tools/call"} // count == 3

	if err := a.CheckRequestCountAtMost(sel, 3); err != nil {
		t.Errorf("AtMost 3 should pass with 3: %v", err)
	}
	if err := a.CheckRequestCountAtMost(sel, 2); err == nil {
		t.Error("AtMost 2 should fail with 3")
	}
	if err := a.CheckRequestCountAtLeast(sel, 3); err != nil {
		t.Errorf("AtLeast 3 should pass with 3: %v", err)
	}
	if err := a.CheckRequestCountAtLeast(sel, 4); err == nil {
		t.Error("AtLeast 4 should fail with 3")
	}
}

// --- Scoping is immutable and narrows -----------------------------------------

func TestScopingImmutableAndNarrows(t *testing.T) {
	t.Parallel()
	base := assert.New(loadClean(t))
	scoped := base.WhereMethod("tools/list")

	// The scoped view sees only tools/list (2 records in clean), the base still
	// sees all: Where* did not mutate the receiver.
	if err := scoped.CheckRequestCount(journalapi.Selector{}, 2); err != nil {
		t.Errorf("scoped count: %v", err)
	}
	if err := base.CheckRequestCount(journalapi.Selector{}, 5); err != nil {
		t.Errorf("base count changed by scoping: %v", err)
	}
}

// --- Fatal vs non-fatal differ -----------------------------------------------

func TestFatalAndNonFatalDiffer(t *testing.T) {
	t.Parallel()

	// Non-fatal: returns an error, does not touch a sink.
	nonFatal := assert.New(loadLeaky(t)).CheckNoHeader("X-Client-Token")
	if nonFatal == nil {
		t.Fatal("non-fatal Check should have returned an error")
	}

	// Fatal: reports through Fatalf (not Errorf) and calls Helper first.
	rec := &recorder{}
	assert.T(rec, loadLeaky(t)).AssertNoHeader("X-Client-Token")
	if !rec.fatal {
		t.Error("fatal Assert should have called Fatalf")
	}
	if rec.errored {
		t.Error("fatal Assert must not call Errorf")
	}
	if rec.helperCalls == 0 {
		t.Error("fatal Assert must call Helper()")
	}
	// The two forms carry the same rendered message.
	if rec.msg != nonFatal.Error() {
		t.Errorf("fatal and non-fatal messages diverged:\nfatal:    %q\nnon-fatal: %q", rec.msg, nonFatal.Error())
	}
}

func TestFatalDoesNotFireOnPass(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	assert.T(rec, loadClean(t)).AssertNoHeader("X-Client-Token")
	if rec.fatal || rec.errored {
		t.Errorf("passing assertion must not report: %q", rec.msg)
	}
	if rec.helperCalls == 0 {
		t.Error("Assert must call Helper() even on the passing path")
	}
}

// --- Helper() is invoked for every fatal assertion ---------------------------

func TestHelperInvokedForEveryAssertion(t *testing.T) {
	t.Parallel()
	sel := journalapi.Selector{Method: "tools/call"}
	cases := []struct {
		name string
		run  func(a *assert.TB)
	}{
		{"AssertNoHeader", func(a *assert.TB) { a.AssertNoHeader("X-None") }},
		{"AssertHeaderNotValue", func(a *assert.TB) { a.AssertHeaderNotValue("X-None", "x") }},
		{"AssertRequestCount", func(a *assert.TB) { a.AssertRequestCount(sel, 3) }},
		{"AssertRequestCountAtMost", func(a *assert.TB) { a.AssertRequestCountAtMost(sel, 3) }},
		{"AssertRequestCountAtLeast", func(a *assert.TB) { a.AssertRequestCountAtLeast(sel, 3) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recorder{}
			tc.run(assert.T(rec, loadClean(t)))
			if rec.helperCalls == 0 {
				t.Errorf("%s did not call Helper()", tc.name)
			}
		})
	}
}

// --- The single highest-value property: no secret in any message -------------

func TestNoSecretInMessage(t *testing.T) {
	t.Parallel()
	view := loadLeaky(t)
	sel := journalapi.Selector{} // every record

	// Every assertion, driven to failure against a journal containing the
	// sentinel secret; every resulting message is scanned. This is the
	// contracts/assertions-api.md §7 enforcement test.
	msgs := []string{}

	collect := func(run func(a *assert.TB)) {
		rec := &recorder{}
		run(assert.T(rec, view))
		if rec.msg != "" {
			msgs = append(msgs, rec.msg)
		}
	}
	collect(func(a *assert.TB) { a.AssertNoHeader("X-Client-Token") })
	collect(func(a *assert.TB) { a.AssertNoHeader("Authorization") })
	collect(func(a *assert.TB) { a.AssertHeaderNotValue("Authorization", sentinelToken) })
	collect(func(a *assert.TB) { a.AssertHeaderNotValue("X-Client-Token", sentinelToken) })
	collect(func(a *assert.TB) { a.AssertRequestCount(sel, 999) })

	// Also scan the non-fatal error strings.
	na := assert.New(view)
	for _, e := range []error{
		na.CheckNoHeader("X-Client-Token"),
		na.CheckHeaderNotValue("Authorization", sentinelToken),
	} {
		if e != nil {
			msgs = append(msgs, e.Error())
		}
	}

	if len(msgs) < 5 {
		t.Fatalf("expected several failure messages to scan, got %d", len(msgs))
	}
	for i, m := range msgs {
		if strings.Contains(m, sentinelToken) {
			t.Errorf("message %d leaked the sentinel secret:\n%s", i, m)
		}
		// The full token is 43 chars; assert no long verbatim run of it leaks
		// beyond the permitted 8-char prefix.
		if strings.Contains(m, "eyJSUPERSECRET") {
			t.Errorf("message %d leaked a secret substring beyond the redaction bound:\n%s", i, m)
		}
	}
}

// --- Compact rendering: bounded, not a JSON dump -----------------------------

func TestMessageIsCompact(t *testing.T) {
	t.Parallel()
	err := assert.New(loadLeaky(t)).CheckNoHeader("X-Client-Token")
	if err == nil {
		t.Fatal("expected failure")
	}
	msg := err.Error()
	// Bounded: a compact rendering, not a full JSON dump of three records.
	if len(msg) > 600 {
		t.Errorf("message too long (%d bytes), likely not compact:\n%s", len(msg), msg)
	}
	// Content check: names the assertion, a Seq and the method (603.2, 603.8).
	for _, want := range []string{"mcpmock/assert:", "seq=", "tools/call"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	// It must not contain a raw body/header dump marker.
	if strings.Contains(msg, "\"headers\"") || strings.Contains(msg, "schemaVersion") {
		t.Errorf("message appears to contain a JSON dump:\n%s", msg)
	}
}

// checkErr centralises the error-shape assertions for the table tests.
func checkErr(t *testing.T, err error, wantErr bool, wantInMsg, wantNotMsg []string) {
	t.Helper()
	if wantErr && err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !wantErr {
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		return
	}
	msg := err.Error()
	for _, w := range wantInMsg {
		if !strings.Contains(msg, w) {
			t.Errorf("message missing %q:\n%s", w, msg)
		}
	}
	for _, w := range wantNotMsg {
		if strings.Contains(msg, w) {
			t.Errorf("message leaked %q:\n%s", w, msg)
		}
	}
}
