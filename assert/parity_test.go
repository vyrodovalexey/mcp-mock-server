package assert_test

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/assert"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// TestAssertParity asserts that the fatal (*TB Assert*) and non-fatal
// (*Asserts Check*) forms expose the same set of assertions, so the two forms
// cannot drift (contracts/assertions-api.md §1). It pairs each Assert<X> on *TB
// with a Check<X> on *Asserts and fails if either side has an unpaired member.
func TestAssertParity(t *testing.T) {
	t.Parallel()

	assertNames := methodStems(reflect.TypeOf(&assert.TB{}), "Assert")
	checkNames := methodStems(reflect.TypeOf(&assert.Asserts{}), "Check")

	for stem := range assertNames {
		if !checkNames[stem] {
			t.Errorf("Assert%s on *TB has no Check%s counterpart on *Asserts", stem, stem)
		}
	}
	for stem := range checkNames {
		if !assertNames[stem] {
			t.Errorf("Check%s on *Asserts has no Assert%s counterpart on *TB", stem, stem)
		}
	}

	// Sanity: the three Phase 1 assertions (plus the two count variants) are
	// present, so the parity check is not vacuously satisfied by an empty set.
	for _, want := range []string{"NoHeader", "HeaderNotValue", "RequestCount"} {
		if !assertNames[want] {
			t.Errorf("expected Assert%s to exist", want)
		}
	}
}

// methodStems returns the set of method names on t that start with prefix, with
// the prefix stripped (e.g. "AssertNoHeader" -> "NoHeader").
func methodStems(t reflect.Type, prefix string) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumMethod(); i++ {
		name := t.Method(i).Name
		if strings.HasPrefix(name, prefix) {
			out[strings.TrimPrefix(name, prefix)] = true
		}
	}
	return out
}

// TestSignaturesMatchContract pins the exported signatures to
// contracts/assertions-api.md §1–§3 at compile time (TASK-025 criterion 6). If a
// signature drifts, this file stops compiling, which is the intended alarm.
func TestSignaturesMatchContract(t *testing.T) {
	t.Parallel()

	// Constructors and loaders.
	var _ func(journalapi.View) *assert.Asserts = assert.New
	var _ func(assert.TestingT, journalapi.View) *assert.TB = assert.T
	var _ func(string) (journalapi.View, error) = assert.FromFile
	var _ func(io.Reader) (journalapi.View, error) = assert.FromReader

	// Fatal forms on *TB.
	var tb *assert.TB
	var _ func(string) *assert.TB = tb.AssertNoHeader
	var _ func(string, string) *assert.TB = tb.AssertHeaderNotValue
	var _ func(journalapi.Selector, int) *assert.TB = tb.AssertRequestCount
	var _ func(journalapi.Selector, int) *assert.TB = tb.AssertRequestCountAtMost
	var _ func(journalapi.Selector, int) *assert.TB = tb.AssertRequestCountAtLeast

	// Non-fatal forms on *Asserts.
	var a *assert.Asserts
	var _ func(string) error = a.CheckNoHeader
	var _ func(string, string) error = a.CheckHeaderNotValue
	var _ func(journalapi.Selector, int) error = a.CheckRequestCount
	var _ func(journalapi.Selector, int) error = a.CheckRequestCountAtMost
	var _ func(journalapi.Selector, int) error = a.CheckRequestCountAtLeast
}

// TestRealTestingTSatisfiesInterface confirms that a real *testing.T satisfies
// TestingT, so assert.T(t, view) compiles unchanged in an ordinary test even
// though T's parameter is the minimal interface, not testing.TB.
func TestRealTestingTSatisfiesInterface(t *testing.T) {
	t.Parallel()
	var _ assert.TestingT = t
}
