package engine

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// unknownLabel is the stable String() fallback for an out-of-range enum value
// ([Kind], [StageID], [CloseReason]). It is a single constant so the three
// String methods never panic and never diverge on the fallback text.
const unknownLabel = "unknown"

// Kind identifies the transport an [Exchange] arrived on. It is deliberately a
// small enumeration and NOT an HTTP-specific type: the abstraction must not
// assume HTTP (ADR-006), so a handler that branches on Kind is a review smell —
// the whole point is that handlers do not.
type Kind uint8

const (
	// KindStdio is the newline-delimited stdio transport: everything is
	// multiplexed on one duplex channel (MOCK-256).
	KindStdio Kind = iota
	// KindHTTP is the streamable-http transport (request/response, and SSE in
	// Phase 2).
	KindHTTP
)

// String returns the journalapi transport tag for the kind, so the engine
// records a record's transport without a wire literal or a second mapping. It
// never panics on an out-of-range value.
func (k Kind) String() string {
	switch k {
	case KindStdio:
		return string(journalapi.TransportStdio)
	case KindHTTP:
		return string(journalapi.TransportHTTP)
	default:
		return unknownLabel
	}
}

// HTTPContext carries the HTTP-transport slice of an inbound exchange, or is nil
// for a stdio exchange. Headers are a wire-order, duplicate- and
// casing-preserving list — never an http.Header — because MOCK-601.2 depends on
// recording exactly what was sent, before Go's header canonicalisation can
// intervene. A handler must not read HTTPContext to make a protocol decision;
// it exists for journalling and for the transport's own use.
type HTTPContext struct {
	// Method is the HTTP method (e.g. "POST").
	Method string
	// Path is the request path the transport matched.
	Path string
	// Query is the raw query string without the leading '?'.
	Query string
	// Headers are the request headers in wire order, original casing and
	// duplicates preserved.
	Headers [][2]string
}

// Instance is the engine's view of the *Instance that owns a request (ADR-014).
// It is a CONSUMER-DEFINED interface: internal/instance (TASK-015) depends on
// internal/engine, so the engine cannot import it; instead the engine names the
// minimal surface it needs and internal/instance's *Instance satisfies it. This
// is the "interfaces defined by the consumer" house rule and is what keeps the
// dependency arrow pointing instance → engine (architecture.md §6.1).
//
// Everything the pipeline needs from an instance is read-only and cheap: a
// name, the atomic snapshot load, the instance seed subtree, the journal ring
// and capturer, and the pre-resolved metric handles. None of it starts a
// goroutine, matching the zero-goroutines-at-rest invariant (architecture.md
// §7.1).
type Instance interface {
	// Name is the instance name; it participates in the seed derivation and is
	// recorded on every journal record.
	Name() string
	// LoadSnapshot returns the current immutable configuration snapshot. The
	// pipeline calls it EXACTLY ONCE, at stage 1, and carries the result on the
	// [Exchange] so a mid-request mutation cannot be observed (ADR-014).
	LoadSnapshot() Snapshot
	// InstanceKey is the instance seed subtree, root.Derive(DomainInstance,
	// name) per ADR-002. Stage 1 derives the per-request key from it.
	InstanceKey() determinism.Key
	// Journal returns the storage ring and the capturer used at stage 9. Both
	// may be nil when the instance has no journal, in which case stage 9 is a
	// no-op.
	Journal() (*journal.Ring, *journal.Capturer)
	// Metrics returns the pre-resolved per-instance metric handles, or nil when
	// metrics are not configured. The pipeline records through them without
	// ever calling WithLabelValues (observability.md §2.3).
	Metrics() Metrics
	// Epoch is the scenario epoch the virtual clock is anchored to (ADR-002
	// rule 3); a handler serializing a timestamp derives it from here.
	Epoch() time.Time
	// Start is the instance-start reference for the monotonic journal timestamp
	// (MOCK-601.1 MonoNs): stage 9 records time.Since(Start()) so elapsed-time
	// claims survive wall-clock adjustments (REV-002). The returned time carries
	// a monotonic reading, so the subtraction is a true monotonic delta.
	Start() time.Time
}

// Snapshot is the engine's view of an instance's immutable configuration
// snapshot (ADR-014). Like [Instance] it is consumer-defined so internal/engine
// need not import internal/instance. It is deeply immutable after publication;
// the pipeline and handlers only read it.
//
// The MetaValidator method is the stage-4 seam: TASK-017 supplies a validator
// through the snapshot, and the pipeline invokes it at stage 4. Phase 1's
// snapshot returns a permissive default validator (see [AcceptAllMeta]).
type Snapshot interface {
	// Gen is the snapshot generation, monotonically increasing per mutation and
	// journalled on every record (ADR-014), so a test can partition its journal
	// by generation and know which requests saw which configuration.
	Gen() uint64
	// Era is the resolved protocol era string recorded at stage 2 ("modern" in
	// Phase 1).
	Era() string
	// MetaValidator returns the stage-4 _meta validator. It is never nil: a
	// snapshot with no configured validator returns [AcceptAllMeta]. TASK-017's
	// snapshot returns a validator honoring switches.validateMeta.
	MetaValidator() MetaValidator
	// MethodEnabled reports whether the named method is enabled
	// (switches.methods.<name>.enabled, MOCK-202.2). A disabled method is
	// dispatched to a -32601 without reaching its handler.
	MethodEnabled(method string) bool
	// JournalGeneration reports whether journalling is on for this snapshot; it
	// lets stage 9 skip capture setup cheaply when the instance disabled the
	// journal in configuration (distinct from the ring's runtime enabled flag).
	JournalEnabled() bool
}

// Exchange is the transport-neutral inbound representation of one request
// (ADR-006). A transport decodes received bytes into an Exchange and drives it
// through [Pipeline.Handle]; a handler receives the Exchange and never a
// transport-specific type, which is what lets one handler tree serve both
// transports (MOCK-102).
//
// The zero value is not usable; a transport constructs an Exchange with at least
// Ctx, Instance and Transport set. Fields marked "stage N" are populated by the
// pipeline as it runs and are not set by the transport.
type Exchange struct {
	// Ctx is the request context, created once at the transport boundary and
	// threaded through every stage and handler (architecture.md §7.3). Client
	// disconnect / stdin EOF cancels it; the pipeline observes the cancellation
	// at stages 8-9 (MOCK-212). It is never recreated mid-pipeline.
	Ctx context.Context
	// Instance is the owning instance (ADR-014).
	Instance Instance
	// Transport tags the originating transport for journalling and for a
	// transport that needs to know its own kind; handlers do not branch on it.
	Transport Kind
	// Peer is the remote address string, recorded on the journal record.
	Peer string
	// HTTP is the HTTP-transport slice, or nil for stdio.
	HTTP *HTTPContext
	// Raw is the exact received request bytes (MOCK-601.1), retained for the
	// journal and for the ADR-002 body hash.
	Raw []byte

	// Snapshot is the immutable configuration captured once at stage 1
	// (ADR-014). Stages 2-9 and every handler read THIS, never a fresh Load, so
	// a concurrent mutation cannot appear half-applied in this request.
	Snapshot Snapshot
	// Request is the decoded JSON-RPC envelope (stage 1). Its id is preserved
	// byte-for-byte (MOCK-203) and echoed on the response.
	Request *jsonrpc.Request
	// Rand is the lazy per-request RNG accessor (ADR-002): the first call
	// derives the content-addressed request key (canonical body hash + HMAC) and
	// builds the ChaCha8 stream, later calls return the same *rand.Rand. A
	// request that draws nothing pays NEITHER the body hash NOR the RNG
	// construction — the MOCK-901 no-random-decision path (ADR-002 option 4).
	// Draws happen in a single fixed order on this one goroutine (ADR-002
	// rule 2); the accessor is safe to share but the draws must not race.
	Rand func() *rand.Rand
	// Clock is the lazy deterministic virtual clock accessor for this request
	// (ADR-002 rule 3). Like Rand it derives the request key on first call, so a
	// handler that serializes no timestamp pays nothing. A handler serializing a
	// timestamp calls Clock().Now(), never time.Now().
	Clock func() determinism.VirtualClock
}

// startKey derives the per-request key from the instance key and the request
// content, using the exact ADR-002 derivation path: method, the raw JSON-RPC id
// bytes as received, and sha256 of the canonical request body. It is the single
// place the engine computes a request key, so the draw-order contract stays
// auditable. It never consults arrival order, so two goroutines handling
// identical bytes derive the identical key.
func startKey(instanceKey determinism.Key, method string, rawID []byte, bodyHash []byte) determinism.Key {
	return instanceKey.Derive(
		determinism.DomainRequest,
		[]byte(method),
		rawID,
		bodyHash,
	)
}
