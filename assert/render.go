package assert

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// TestingT is the minimal test sink the assertions report through. A real
// *testing.T (indeed any testing.TB) satisfies it, so assert.T(t, view) works
// unchanged; but because the interface is small and exported, the assertions are
// equally usable from the CLI, the control API and a hub harness, where a hard
// *testing.T dependency would make them unusable (design principle §0.3).
//
// The three methods mirror the subset of testing.TB the assertions need:
//
//   - Helper marks the caller a test helper so a failure points at the caller's
//     line, not the assertion's.
//   - Errorf reports a non-fatal failure (used by the *TB Check* bridge).
//   - Fatalf reports a fatal failure and stops the goroutine.
//
// Stability: v0.
type TestingT interface {
	// Helper marks the calling function as a test helper.
	Helper()
	// Errorf reports a formatted non-fatal failure.
	Errorf(format string, args ...any)
	// Fatalf reports a formatted fatal failure and stops execution.
	Fatalf(format string, args ...any)
}

// maxViolationsListed bounds how many offending records a failure message lists
// individually; the remainder are counted (contracts/assertions-api.md §7). The
// bound keeps a message greppable and prevents a pathological journal from
// producing a multi-megabyte failure.
const maxViolationsListed = 10

// redactedValueMax is the number of leading characters of a header value that may
// appear in a message before truncation. A failing CI log must never print a full
// token (contracts/assertions-api.md §3.1, §7), so any value is cut to this many
// characters with a length indication.
const redactedValueMax = 8

// violation is one offending record in a failed assertion, carrying just enough
// to render a compact, diagnostic line (MOCK-603.8): the record's Seq, its
// method, its primitive name when set, and one already-safe detail string.
type violation struct {
	seq    uint64
	method string
	name   string
	// detail is a short, already-redacted description of what was wrong on this
	// record. It must never contain a raw credential value.
	detail string
}

// report is the accumulated outcome of one assertion over a scope: the fixed
// assertion name, a human-readable scope description, the record count inspected,
// the violations found, an optional hint, and a note count for records skipped as
// inapplicable (e.g. stdio records for a header assertion).
type report struct {
	name      string
	scope     string
	inspected int
	skipped   int
	hint      string
	viols     []violation
}

// redactHeaderValue returns a message-safe rendering of a header value: at most
// [redactedValueMax] leading characters, followed by a redaction marker and the
// number of characters withheld. It is the single choke point through which any
// header value reaches a failure message, so token passthrough cannot leak a
// secret into a CI log (contracts/assertions-api.md §3.1).
func redactHeaderValue(v string) string {
	if v == "" {
		return `""`
	}
	// Count runes, not bytes, so a multibyte value reports an honest length and
	// the prefix never splits a rune.
	runes := []rune(v)
	if len(runes) <= redactedValueMax {
		// Even a short value is not echoed in full: a short token is still a
		// token. Show the length only.
		return fmt.Sprintf(`[redacted, %d chars]`, len(runes))
	}
	prefix := string(runes[:redactedValueMax])
	return fmt.Sprintf(`%q [redacted, %d more chars]`, prefix, len(runes)-redactedValueMax)
}

// hasViolations reports whether the report describes a failure.
func (rep report) hasViolations() bool { return len(rep.viols) > 0 }

// render produces the single canonical failure-message shape shared by every
// assertion (contracts/assertions-api.md §7). Violations are sorted by Seq so the
// message is deterministic and diffs cleanly across runs; at most
// [maxViolationsListed] are listed and the remainder counted. The message never
// contains a raw credential because every value that reaches it has already
// passed through [redactHeaderValue].
func (rep report) render() string {
	viols := make([]violation, len(rep.viols))
	copy(viols, rep.viols)
	sort.SliceStable(viols, func(i, j int) bool { return viols[i].seq < viols[j].seq })

	var b strings.Builder
	fmt.Fprintf(&b, "mcpmock/assert: %s failed\n", rep.name)
	fmt.Fprintf(&b, "  scope:      %s (%d records", rep.scope, rep.inspected)
	if rep.skipped > 0 {
		fmt.Fprintf(&b, ", %d skipped", rep.skipped)
	}
	b.WriteString(")\n")
	fmt.Fprintf(&b, "  violations: %d\n", len(viols))

	limit := len(viols)
	if limit > maxViolationsListed {
		limit = maxViolationsListed
	}
	for _, v := range viols[:limit] {
		renderViolationLine(&b, v)
	}
	if len(viols) > limit {
		fmt.Fprintf(&b, "    … and %d more\n", len(viols)-limit)
	}
	if rep.hint != "" {
		fmt.Fprintf(&b, "  hint: %s", rep.hint)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderViolationLine writes one compact violation line: seq, method, optional
// name, and the record's already-safe detail. Only a bounded set of fields is
// rendered — never a full JSON dump (MOCK-603.8).
func renderViolationLine(b *strings.Builder, v violation) {
	b.WriteString("    ")
	fmt.Fprintf(b, "seq=%d", v.seq)
	if v.method != "" {
		fmt.Fprintf(b, "  %s", v.method)
	}
	if v.name != "" {
		fmt.Fprintf(b, "  name=%s", v.name)
	}
	if v.detail != "" {
		fmt.Fprintf(b, "  %s", v.detail)
	}
	b.WriteByte('\n')
}

// describeScope renders a chain of selectors as a compact, readable scope string
// for the message header. A scope with no constraints renders as "all records",
// which is honest: an unconstrained assertion inspects everything.
func describeScope(chain []journalapi.Selector) string {
	parts := make([]string, 0, len(chain))
	for _, s := range chain {
		parts = append(parts, describeSelector(s)...)
	}
	if len(parts) == 0 {
		return "all records"
	}
	return strings.Join(parts, " ")
}

// describeSelector renders the set fields of one selector as key=value tokens.
func describeSelector(s journalapi.Selector) []string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("instance", s.Instance)
	add("method", s.Method)
	add("name", s.Name)
	add("transport", s.Transport)
	add("era", s.Era)
	add("chain", s.ChainID)
	add("trace", s.TraceID)
	add("fault", s.FaultRule)
	if s.StatusCode != nil {
		parts = append(parts, fmt.Sprintf("status=%d", *s.StatusCode))
	}
	return parts
}

// describeSelectorInline renders a single selector compactly for messages that
// name a selector argument (AssertRequestCount). An unconstrained selector reads
// as "any record", which is the correct plain-language meaning.
func describeSelectorInline(s journalapi.Selector) string {
	parts := describeSelector(s)
	if len(parts) == 0 {
		return "any record"
	}
	return strings.Join(parts, " ")
}
