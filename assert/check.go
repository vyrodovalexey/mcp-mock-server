package assert

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// Asserts is the non-fatal assertion form over a [journalapi.View]. Every Check*
// method returns an error describing all violations found, or nil. It is usable
// outside a test — from the CLI, the control API or a hub harness — because it
// reports through a returned error rather than a testing sink.
//
// An Asserts is immutable. Where* methods return a new Asserts with a narrowed
// scope and never mutate the receiver, so a base Asserts can be reused to build
// several independent scoped assertions.
//
// Stability: v0.
type Asserts struct {
	view  journalapi.View
	chain []journalapi.Selector
}

// New returns a non-fatal [Asserts] over v (contracts/assertions-api.md §1).
func New(v journalapi.View) *Asserts {
	return &Asserts{view: v}
}

// FromFile loads an NDJSON journal export (MOCK-602) into a [journalapi.View]
// with no running server, so assertions run against a recorded journal
// (MOCK-603.4). The returned error names the path on failure.
func FromFile(path string) (journalapi.View, error) {
	f, err := os.Open(path) //nolint:gosec // path is a test-supplied journal export, not user input.
	if err != nil {
		return nil, fmt.Errorf("mcpmock/assert: open journal %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	v, err := journalapi.ReadView(f)
	if err != nil {
		return nil, fmt.Errorf("mcpmock/assert: read journal %q: %w", path, err)
	}
	return v, nil
}

// FromReader loads an NDJSON journal stream into a [journalapi.View] with no
// running server (MOCK-603.4).
func FromReader(r io.Reader) (journalapi.View, error) {
	v, err := journalapi.ReadView(r)
	if err != nil {
		return nil, fmt.Errorf("mcpmock/assert: read journal: %w", err)
	}
	return v, nil
}

// scoped returns the [journalapi.View] narrowed by every selector in the chain,
// applied in order. Because [journalapi.Selector.Matches] is a pure conjunction,
// the applied order does not affect the resulting record set.
func (a *Asserts) scoped() journalapi.View {
	v := a.view
	for _, s := range a.chain {
		v = v.Filter(s)
	}
	return v
}

// withSelector returns a copy of a with s appended to the scope chain. It never
// mutates the receiver (contracts/assertions-api.md §2).
func (a *Asserts) withSelector(s journalapi.Selector) *Asserts {
	next := make([]journalapi.Selector, len(a.chain)+1)
	copy(next, a.chain)
	next[len(a.chain)] = s
	return &Asserts{view: a.view, chain: next}
}

// Where narrows the scope by an explicit [journalapi.Selector].
func (a *Asserts) Where(s journalapi.Selector) *Asserts { return a.withSelector(s) }

// WhereMethod narrows the scope to records whose JSON-RPC method matches the glob
// pattern (path.Match semantics, e.g. "tools/*").
func (a *Asserts) WhereMethod(pattern string) *Asserts {
	return a.withSelector(journalapi.Selector{Method: pattern})
}

// WhereInstance narrows the scope to records recorded by the named instance.
func (a *Asserts) WhereInstance(name string) *Asserts {
	return a.withSelector(journalapi.Selector{Instance: name})
}

// CheckNoHeader is the non-fatal form of [TB.AssertNoHeader]. It returns an error
// if any record in scope carries an HTTP header whose name matches name
// case-insensitively, or nil otherwise.
func (a *Asserts) CheckNoHeader(name string) error {
	return a.evaluate(noHeaderReport(a.scoped(), a.chain, name))
}

// CheckHeaderNotValue is the non-fatal form of [TB.AssertHeaderNotValue]. It
// returns an error if any record carries header name (case-insensitive) with a
// value equal to value (exact, case-sensitive, constant-time).
func (a *Asserts) CheckHeaderNotValue(name, value string) error {
	return a.evaluate(headerNotValueReport(a.scoped(), a.chain, name, value))
}

// CheckRequestCount is the non-fatal form of [TB.AssertRequestCount]. It returns
// an error unless exactly n records match sel within the current scope.
func (a *Asserts) CheckRequestCount(sel journalapi.Selector, n int) error {
	return a.evaluate(requestCountReport(a.scoped(), a.chain, sel, n, countExact))
}

// CheckRequestCountAtMost is the non-fatal form of [TB.AssertRequestCountAtMost].
func (a *Asserts) CheckRequestCountAtMost(sel journalapi.Selector, n int) error {
	return a.evaluate(requestCountReport(a.scoped(), a.chain, sel, n, countAtMost))
}

// CheckRequestCountAtLeast is the non-fatal form of
// [TB.AssertRequestCountAtLeast].
func (a *Asserts) CheckRequestCountAtLeast(sel journalapi.Selector, n int) error {
	return a.evaluate(requestCountReport(a.scoped(), a.chain, sel, n, countAtLeast))
}

// evaluate turns a report into a returned error (the non-fatal contract): nil
// when there are no violations, otherwise an error carrying the rendered message.
func (a *Asserts) evaluate(rep report) error {
	if !rep.hasViolations() {
		return nil
	}
	return errors.New(rep.render())
}

// headerMatches reports whether headerName matches want case-insensitively,
// without allocating a lowercased copy of either operand for every comparison
// beyond what strings.EqualFold does internally.
func headerMatches(headerName, want string) bool {
	return strings.EqualFold(headerName, want)
}

// valueEqualsConstantTime reports whether a == b using a constant-time
// comparison, so the assertion cannot become a timing oracle for a token in a
// shared CI (contracts/assertions-api.md §3.2). Length is compared first, which
// leaks only the length, never the contents.
func valueEqualsConstantTime(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// noHeaderReport builds the report for AssertNoHeader/CheckNoHeader. It inspects
// the recorded wire-order header list (MOCK-601.2), so both x-client-token and
// X-Client-Token are caught. stdio records have no headers and are counted as
// skipped rather than silently ignored.
func noHeaderReport(v journalapi.View, chain []journalapi.Selector, name string) report {
	rep := report{
		name:  "no-header",
		scope: describeScope(chain),
		hint:  "MOCK-603: a header on an outbound request is token/context passthrough",
	}
	v.Iter(func(r journalapi.Record) bool {
		rep.inspected++
		if r.HTTP == nil {
			rep.skipped++
			return true
		}
		if hdr, val, ok := firstHeader(r.HTTP.Headers, name); ok {
			rep.viols = append(rep.viols, violation{
				seq:    r.Seq,
				method: r.JSONRPC.Method,
				name:   r.JSONRPC.Name,
				detail: fmt.Sprintf("header %q present (value: %s)", hdr, redactHeaderValue(val)),
			})
		}
		return true
	})
	return rep
}

// headerNotValueReport builds the report for AssertHeaderNotValue. A record
// violates when it carries header name (case-insensitive) whose value equals the
// forbidden value exactly (case-sensitive, constant-time). The forbidden value is
// never echoed; only the redacted observed value appears.
func headerNotValueReport(v journalapi.View, chain []journalapi.Selector, name, value string) report {
	rep := report{
		name:  "header-not-value",
		scope: describeScope(chain),
		hint:  "MOCK-603: forbidden header value observed — likely token passthrough",
	}
	v.Iter(func(r journalapi.Record) bool {
		rep.inspected++
		if r.HTTP == nil {
			rep.skipped++
			return true
		}
		for _, kv := range r.HTTP.Headers {
			if headerMatches(kv[0], name) && valueEqualsConstantTime(kv[1], value) {
				rep.viols = append(rep.viols, violation{
					seq:    r.Seq,
					method: r.JSONRPC.Method,
					name:   r.JSONRPC.Name,
					detail: fmt.Sprintf("header %q matches forbidden value (%s)", kv[0], redactHeaderValue(kv[1])),
				})
				break
			}
		}
		return true
	})
	return rep
}

// countMode selects the comparison AssertRequestCount and its variants apply.
type countMode int

const (
	// countExact requires the observed count to equal n.
	countExact countMode = iota
	// countAtMost requires the observed count to be <= n.
	countAtMost
	// countAtLeast requires the observed count to be >= n.
	countAtLeast
)

// requestCountReport builds the report for AssertRequestCount and its at-most /
// at-least variants. It counts records matching sel within the already-scoped
// view and compares per mode. A count assertion has at most one violation — the
// mismatch itself — whose detail carries the actual count and, when small, the
// matching records, so "3 instead of 1" is immediately diagnosable
// (contracts/assertions-api.md §3.8).
func requestCountReport(
	v journalapi.View, chain []journalapi.Selector,
	sel journalapi.Selector, n int, mode countMode,
) report {
	matched := v.Filter(sel)
	got := matched.Len()
	rep := report{
		name:      countName(mode),
		scope:     describeScope(chain),
		inspected: v.Len(),
		hint:      "MOCK-231/232/233: de-duplication and cache-hit verification",
	}
	if countSatisfied(mode, got, n) {
		return rep
	}
	rep.viols = append(rep.viols, violation{
		detail: fmt.Sprintf("selector %q: want %s %d, got %d%s",
			describeSelectorInline(sel), countRelation(mode), n, got, countSample(matched, got)),
	})
	return rep
}

// countSatisfied reports whether an observed count got satisfies mode against n.
func countSatisfied(mode countMode, got, n int) bool {
	switch mode {
	case countAtMost:
		return got <= n
	case countAtLeast:
		return got >= n
	case countExact:
		return got == n
	default:
		return got == n
	}
}

// countName returns the assertion name for a count mode, matching the failure
// shape's leading identifier.
func countName(mode countMode) string {
	switch mode {
	case countAtMost:
		return "request-count-at-most"
	case countAtLeast:
		return "request-count-at-least"
	case countExact:
		return "request-count"
	default:
		return "request-count"
	}
}

// countRelation renders the human-readable comparison word for a mode.
func countRelation(mode countMode) string {
	switch mode {
	case countAtMost:
		return "at most"
	case countAtLeast:
		return "at least"
	case countExact:
		return "exactly"
	default:
		return "exactly"
	}
}

// countSample renders up to a few matching records inline when the actual count
// is small, so a count mismatch is immediately diagnosable. It returns the empty
// string when the count is large enough that a list would bloat the message.
func countSample(v journalapi.View, got int) string {
	if got == 0 || got > maxViolationsListed {
		return ""
	}
	var b strings.Builder
	v.Iter(func(r journalapi.Record) bool {
		fmt.Fprintf(&b, "\n    seq=%d  %s", r.Seq, r.JSONRPC.Method)
		if r.JSONRPC.Name != "" {
			fmt.Fprintf(&b, "  name=%s", r.JSONRPC.Name)
		}
		return true
	})
	return b.String()
}

// firstHeader returns the first header whose name matches want case-insensitively,
// preserving the original casing of the matched name for the message.
func firstHeader(headers [][2]string, want string) (name, value string, ok bool) {
	for _, kv := range headers {
		if headerMatches(kv[0], want) {
			return kv[0], kv[1], true
		}
	}
	return "", "", false
}
