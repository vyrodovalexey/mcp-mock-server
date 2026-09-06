package determinism

import (
	"encoding/binary"
	"time"
)

// VirtualClock produces the deterministic time.Time that mcpmock serializes
// into response bodies in place of the wall clock. ADR-002 rule 3 is the
// governing rule: time.Now() must never reach a value that appears in a
// response, because that value would then differ run to run and break the
// byte-identity guarantee (§0.1). Journal wall timestamps, metrics and log
// lines are exempt — they are evidence about the run, not part of the modeled
// response — and they legitimately use time.Now() elsewhere in the module.
//
// A VirtualClock is immutable and is a pure function of the request key and the
// scenario epoch it was constructed with. The same (requestKey, epoch) always
// yields the same instant, on every process and every GOMAXPROCS setting. It
// holds no state that advances: in particular a sleep behavior delays real
// time but does NOT advance this clock (builtin-tools.md §1.4), because
// advancing it would make the emitted instant depend on how long the request
// happened to take.
//
// VirtualClock never reads time.Now(). A source-level AST scan (TASK-026)
// asserts this file contains no time.Now reference; the code here must keep it
// true.
type VirtualClock struct {
	// instant is the fully resolved deterministic time, computed once at
	// construction. Storing the resolved value rather than the key keeps Now
	// allocation-free and makes the "no time.Now" property trivially auditable.
	instant time.Time
}

// NewVirtualClock derives a deterministic clock for a single request from that
// request's key and the scenario-configured epoch. The derived instant is
// epoch plus a bounded, key-dependent offset, so two different requests see
// different-but-reproducible instants and the same request always sees the same
// one.
//
// The offset is derived from requestKey.Derive(DomainClock): the first eight
// big-endian bytes of that child key are read as an unsigned count of
// nanoseconds and reduced modulo maxClockOffset, then added to epoch. Bounding
// the offset keeps the emitted instant within a day of the configured epoch so
// scenario authors get a plausible, stable timestamp rather than an arbitrary
// point in the far future. epoch is used exactly as supplied, including its
// monotonic-clock reading being stripped, so the result is a pure wall value.
func NewVirtualClock(requestKey Key, epoch time.Time) VirtualClock {
	clockKey := requestKey.Derive(DomainClock)
	raw := binary.BigEndian.Uint64(clockKey[:8])
	offset := time.Duration(raw % uint64(maxClockOffset))
	// Round(0) strips any monotonic reading so the stored instant is a pure,
	// reproducible wall time with no hidden process-relative component.
	return VirtualClock{instant: epoch.Add(offset).Round(0)}
}

// maxClockOffset bounds the key-derived offset added to the scenario epoch. One
// day gives a wide, stable spread of plausible timestamps while keeping the
// emitted instant anchored near the author's chosen epoch. It is a derivation
// constant: changing it changes every emitted virtual timestamp and is
// therefore a breaking change on the same footing as a domain string.
const maxClockOffset = 24 * time.Hour

// Now returns this clock's deterministic instant. It is named Now to read
// naturally at call sites that would otherwise reach for time.Now(), but it
// consults no wall clock: every call on a given VirtualClock returns the same
// time.Time. This is the method a response-building handler calls instead of
// time.Now().
func (c VirtualClock) Now() time.Time {
	return c.instant
}
