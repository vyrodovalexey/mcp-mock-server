package journalapi

import (
	"path"
	"time"
)

// Selector is an AND-combinable filter over journal records (MOCK-602.4). A
// zero Selector matches every record; each set field narrows the match, and all
// set fields must hold for a record to be selected.
//
// Method and Name are shell-style glob patterns (see [Selector.Matches]); the
// remaining string fields are exact matches. StatusCode is a pointer so that
// "status 0" (unset) is distinguishable from "no status filter".
//
// Stability: v0. Fields are additive; a new filter dimension is a new field, so
// existing callers are unaffected.
type Selector struct {
	// Instance matches [Record.Instance] exactly when non-empty.
	Instance string
	// Method matches [JSONRPCPart.Method] as a glob when non-empty.
	Method string
	// Name matches [JSONRPCPart.Name] as a glob when non-empty.
	Name string
	// Transport matches [Record.Transport] exactly when non-empty.
	Transport string
	// Era matches [Record.Era] exactly when non-empty.
	Era string
	// Since selects records at or after this wall-clock time when non-zero.
	Since time.Time
	// Until selects records strictly before this wall-clock time when non-zero.
	Until time.Time
	// ChainID matches [MRTRPart.ChainID] exactly when non-empty (MOCK-605).
	ChainID string
	// TraceID matches [CorrelationPart.TraceID] exactly when non-empty.
	TraceID string
	// FaultRule selects records in which a fault with this rule id fired, when
	// non-empty.
	FaultRule string
	// StatusCode selects records whose response status equals *StatusCode when
	// non-nil.
	StatusCode *int
}

// Empty reports whether the selector constrains nothing, i.e. matches every
// record. It is the honest test behind "an empty scope is not an automatic
// pass" in the assertion contract.
func (s Selector) Empty() bool {
	return s.Instance == "" && s.Method == "" && s.Name == "" &&
		s.Transport == "" && s.Era == "" && s.Since.IsZero() && s.Until.IsZero() &&
		s.ChainID == "" && s.TraceID == "" && s.FaultRule == "" && s.StatusCode == nil
}

// Matches reports whether r satisfies every set field of the selector. Glob
// fields use [path.Match] semantics ("tools/*", "*"); a malformed pattern never
// matches and never panics. Matching is a pure conjunction of independent
// predicates, which is why [View.Filter] composes order-independently.
func (s Selector) Matches(r Record) bool {
	return s.matchesIdentity(r) &&
		s.matchesProtocol(r) &&
		s.matchesTime(r) &&
		s.matchesCorrelation(r) &&
		s.matchesOutcome(r)
}

// matchesIdentity applies the instance and transport predicates.
func (s Selector) matchesIdentity(r Record) bool {
	if s.Instance != "" && r.Instance != s.Instance {
		return false
	}
	if s.Transport != "" && string(r.Transport) != s.Transport {
		return false
	}
	return true
}

// matchesProtocol applies the method-glob, name-glob and era predicates.
func (s Selector) matchesProtocol(r Record) bool {
	return globMatch(s.Method, r.JSONRPC.Method) &&
		globMatch(s.Name, r.JSONRPC.Name) &&
		(s.Era == "" || r.Era == s.Era)
}

// matchesCorrelation applies the chain-id and trace-id predicates.
func (s Selector) matchesCorrelation(r Record) bool {
	if s.ChainID != "" && (r.MRTR == nil || r.MRTR.ChainID != s.ChainID) {
		return false
	}
	if s.TraceID != "" && r.Correlation.TraceID != s.TraceID {
		return false
	}
	return true
}

// matchesOutcome applies the fault-rule and status-code predicates.
func (s Selector) matchesOutcome(r Record) bool {
	if s.FaultRule != "" && !hasFault(r, s.FaultRule) {
		return false
	}
	if s.StatusCode != nil && !matchesStatus(r, *s.StatusCode) {
		return false
	}
	return true
}

// matchesTime applies the Since/Until window against the record wall time.
func (s Selector) matchesTime(r Record) bool {
	if !s.Since.IsZero() && r.WallTime.Before(s.Since) {
		return false
	}
	if !s.Until.IsZero() && !r.WallTime.Before(s.Until) {
		return false
	}
	return true
}

// And returns a selector list equivalent to applying s and then other. It is
// the compositional primitive behind [View.Filter]: applying two selectors in
// either order selects the same records because matching is a pure conjunction.
func (s Selector) And(other Selector) []Selector {
	return []Selector{s, other}
}

// globMatch reports whether value matches pattern. An empty pattern always
// matches (the field is unconstrained). A malformed pattern matches nothing.
func globMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	ok, err := path.Match(pattern, value)
	if err != nil {
		return false
	}
	return ok
}

// hasFault reports whether any fault with ruleID fired for r.
func hasFault(r Record, ruleID string) bool {
	for _, f := range r.Faults {
		if f.RuleID == ruleID {
			return true
		}
	}
	return false
}

// matchesStatus reports whether r's recorded status equals code. The response
// status is authoritative; the HTTP status is a fallback for records that never
// produced a JSON-RPC response part.
func matchesStatus(r Record, code int) bool {
	if r.Response != nil && r.Response.Status == code {
		return true
	}
	if r.Response == nil && r.HTTP != nil && r.HTTP.Status == code {
		return true
	}
	return false
}
