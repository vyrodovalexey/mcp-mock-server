package assert

import (
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// TB binds a [journalapi.View] to a [TestingT] (contracts/assertions-api.md §1).
// Its Assert* methods call [TestingT.Helper] then [TestingT.Fatalf] on violation,
// so a failure stops the test and reports the caller's line; the matching Check*
// methods on [Asserts] return an error instead.
//
// A TB is immutable. Where* methods return a new TB with a narrowed scope and
// never mutate the receiver, so a base TB can be reused for several independent
// scoped assertions.
//
// Stability: v0.
type TB struct {
	t TestingT
	a *Asserts
}

// T binds v to tb for fatal assertions (contracts/assertions-api.md §1). tb is
// the minimal [TestingT] interface, which any *testing.T satisfies, so
// assert.T(t, view) works in a normal test while the same call also works from a
// non-test caller that supplies its own [TestingT].
func T(tb TestingT, v journalapi.View) *TB {
	return &TB{t: tb, a: New(v)}
}

// with returns a copy of the receiver whose non-fatal delegate is a2, preserving
// the bound [TestingT]. It is the shared tail of every Where* method.
func (a *TB) with(a2 *Asserts) *TB {
	return &TB{t: a.t, a: a2}
}

// Where narrows the scope by an explicit [journalapi.Selector]. It never mutates
// the receiver (contracts/assertions-api.md §2).
func (a *TB) Where(s journalapi.Selector) *TB { return a.with(a.a.Where(s)) }

// WhereMethod narrows the scope to records whose JSON-RPC method matches the glob
// pattern (path.Match semantics, e.g. "tools/*").
func (a *TB) WhereMethod(pattern string) *TB { return a.with(a.a.WhereMethod(pattern)) }

// WhereInstance narrows the scope to records recorded by the named instance.
func (a *TB) WhereInstance(name string) *TB { return a.with(a.a.WhereInstance(name)) }

// AssertNoHeader fails the test if any record in scope carries an HTTP header
// whose name matches name case-insensitively (contracts/assertions-api.md §3.1).
// Matching is against the recorded wire-order header list (MOCK-601.2), so both
// x-client-token and X-Client-Token are caught. stdio records carry no headers
// and are counted as skipped. Header values in the failure message are redacted,
// so a failing CI log never prints a token.
//
// It returns the receiver so calls can be chained after a Where*.
func (a *TB) AssertNoHeader(name string) *TB {
	a.t.Helper()
	a.fail(noHeaderReport(a.a.scoped(), a.a.chain, name))
	return a
}

// AssertHeaderNotValue fails the test if any record carries header name
// (case-insensitive) with a value equal to value — the token-passthrough check
// (contracts/assertions-api.md §3.2). Comparison is exact, case-sensitive and
// constant-time so the assertion is not a timing oracle. The forbidden value is
// never echoed; the message redacts the observed value.
func (a *TB) AssertHeaderNotValue(name, value string) *TB {
	a.t.Helper()
	a.fail(headerNotValueReport(a.a.scoped(), a.a.chain, name, value))
	return a
}

// AssertRequestCount fails the test unless exactly n records match sel within the
// current scope (contracts/assertions-api.md §3.8). n == 0 is a legitimate
// assertion. The failure message names the actual count and, when small, the
// matching records, so "3 instead of 1" is immediately diagnosable.
func (a *TB) AssertRequestCount(sel journalapi.Selector, n int) *TB {
	a.t.Helper()
	a.fail(requestCountReport(a.a.scoped(), a.a.chain, sel, n, countExact))
	return a
}

// AssertRequestCountAtMost fails the test unless at most n records match sel
// within the current scope.
func (a *TB) AssertRequestCountAtMost(sel journalapi.Selector, n int) *TB {
	a.t.Helper()
	a.fail(requestCountReport(a.a.scoped(), a.a.chain, sel, n, countAtMost))
	return a
}

// AssertRequestCountAtLeast fails the test unless at least n records match sel
// within the current scope.
func (a *TB) AssertRequestCountAtLeast(sel journalapi.Selector, n int) *TB {
	a.t.Helper()
	a.fail(requestCountReport(a.a.scoped(), a.a.chain, sel, n, countAtLeast))
	return a
}

// fail reports a failed report through the bound sink. It is the single place the
// fatal form calls Fatalf, so Helper() has already been invoked by the exported
// method and the reported line is the caller's.
func (a *TB) fail(rep report) {
	if !rep.hasViolations() {
		return
	}
	a.t.Fatalf("%s", rep.render())
}
