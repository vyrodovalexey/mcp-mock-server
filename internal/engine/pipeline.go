package engine

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// StageID identifies one of the nine pipeline stages (architecture.md §5). The
// values are the stage numbers, so their order is the execution order and the
// ADR-002 draw order. They are exported so a test can assert the sequence and so
// an instrumented pipeline can report which stage it is in.
type StageID uint8

const (
	// StageDecode (1) parses the envelope, extracts params/_meta, and captures
	// the immutable snapshot and the per-request RNG (ADR-002 / ADR-014).
	StageDecode StageID = iota + 1
	// StageEraResolve (2) resolves the protocol era (modern-only in Phase 1).
	StageEraResolve
	// StageAuthorize (3) authorizes the request (`none` in Phase 1).
	StageAuthorize
	// StageValidate (4) validates _meta — the seam TASK-017 fills.
	StageValidate
	// StageFaultPre (5) is the request-phase fault hook (Phase 1 no-op).
	StageFaultPre
	// StageDispatch (6) runs the method handler.
	StageDispatch
	// StageFaultPost (7) is the response-phase fault hook (Phase 1 no-op).
	StageFaultPost
	// StageEncode (8) encodes the result or error and emits it through the sink.
	StageEncode
	// StageJournal (9) commits the journal record, including a cancellation
	// record with elapsed time (MOCK-212).
	StageJournal
)

// String returns a stable lowercase stage name for logs and test diagnostics.
func (s StageID) String() string {
	switch s {
	case StageDecode:
		return "decode"
	case StageEraResolve:
		return "eraResolve"
	case StageAuthorize:
		return "authorize"
	case StageValidate:
		return "validate"
	case StageFaultPre:
		return "faultPre"
	case StageDispatch:
		return "dispatch"
	case StageFaultPost:
		return "faultPost"
	case StageEncode:
		return "encode"
	case StageJournal:
		return "journal"
	default:
		return unknownLabel
	}
}

// stageObserver is an optional hook the pipeline calls as it enters each stage.
// It is nil in production and set only by tests, so it costs a single nil check
// per stage on the hot path and nothing else. It is deliberately unexported and
// wired through [Pipeline.withObserver] to keep it out of the public surface.
type stageObserver func(StageID)

// Pipeline runs the nine-stage request pipeline for one instance's engine
// (architecture.md §5). It is immutable after construction and safe for
// concurrent use by many requests: it holds only the read-only [Registry] and
// the optional test observer, and every per-request mutable value lives on the
// [Exchange] passed to [Pipeline.Handle]. It owns no goroutine and adds none per
// request (architecture.md §7.1).
type Pipeline struct {
	registry *Registry
	observe  stageObserver
}

// NewPipeline returns a pipeline dispatching through registry. The registry is
// built once at instance construction (TASK-015/018) and is not mutated
// afterward.
func NewPipeline(registry *Registry) *Pipeline {
	return &Pipeline{registry: registry}
}

// enter announces stage s to the observer when one is installed. It is a single
// nil check on the production hot path.
func (p *Pipeline) enter(s StageID) {
	if p.observe != nil {
		p.observe(s)
	}
}

// Handle runs ex through all nine stages in order and writes the response into
// sink, returning a non-nil error only for an infrastructure failure the
// transport must surface (a sink write error). A wire-level failure — a parse
// error, a rejected _meta, a method-not-found, a handler fault — is NOT returned
// as an error: it is encoded into an error response and emitted through the
// sink, because that is the correct MCP behavior and the transport should send
// it, not treat it as its own failure.
//
// Handle is the entry point every transport calls. ctx is ex.Ctx passed
// explicitly so the compiler and contextcheck see one context threaded through;
// the pipeline never recreates it. The sink is always closed exactly once,
// through a defer, so a mid-pipeline return still closes it (MOCK-212 /
// MOCK-254).
func (p *Pipeline) Handle(ctx context.Context, ex *Exchange, sink Sink) error {
	start := time.Now()
	st := &pipelineState{start: start}
	if m := metricsOf(ex); m != nil {
		m.IncInFlight()
		defer m.DecInFlight()
	}

	// Stage 1 must run first: it captures the snapshot and derives the request
	// key. A decode fault short-circuits to encode+journal so a malformed
	// request still gets a well-formed error response and a complete record
	// (MOCK-601.6).
	if f := p.stageDecode(ctx, ex, st); f != nil {
		return p.finishError(ctx, ex, sink, st, f)
	}
	if f := p.stageEraResolve(ctx, ex, st); f != nil {
		return p.finishError(ctx, ex, sink, st, f)
	}
	p.stageAuthorize(ctx, ex, st)
	if f := p.stageValidate(ctx, ex, st); f != nil {
		return p.finishError(ctx, ex, sink, st, f)
	}
	if f := p.stageFaultPre(ctx, ex); f != nil {
		return p.finishError(ctx, ex, sink, st, f)
	}
	result, f := p.stageDispatch(ctx, ex, st)
	if f != nil {
		return p.finishError(ctx, ex, sink, st, f)
	}
	if f := p.stageFaultPost(ctx, ex, result); f != nil {
		return p.finishError(ctx, ex, sink, st, f)
	}
	return p.finishResult(ctx, ex, sink, st, result)
}

// pipelineState carries the per-request values the stages accumulate but that do
// not belong on the transport-visible [Exchange]: timing, the decoded _meta for
// the journal, the resolved result type, and the validation outcome. It lives on
// one goroutine's stack for the duration of Handle, so it needs no
// synchronization.
type pipelineState struct {
	start          time.Time
	method         string
	toolName       string
	meta           map[string]json.RawMessage
	metaValidation *journalapi.MetaValidationPart
	resultType     string
	era            string
}

// stageDecode is stage 1. It validates the JSON-RPC envelope, extracts params
// and params._meta, derives the content-addressed request key and the lazy RNG,
// builds the virtual clock, and captures the immutable snapshot exactly once
// (ADR-014). It draws zero RNG values. On a malformed request it returns a
// parse/invalid-request fault, having still captured enough state for a complete
// journal record.
func (p *Pipeline) stageDecode(_ context.Context, ex *Exchange, st *pipelineState) *Fault {
	p.enter(StageDecode)
	// Capture the snapshot ONCE; every later stage reads ex.Snapshot (ADR-014).
	ex.Snapshot = ex.Instance.LoadSnapshot()

	req, derr := jsonrpc.DecodeRequest(ex.Raw)
	if derr != nil {
		return faultParse.WithReason(derr.Error()).WithCause(derr)
	}
	ex.Request = req
	if verr := req.Validate(); verr != nil {
		return faultInvalidRequest.WithReason(verr.Message).WithCause(verr)
	}
	st.method = req.Method

	// Decode params._meta and the primitive name only when a consumer needs
	// them: the journal (to preserve _meta with unknown keys, MOCK-203.6) and
	// the tool name (Name on the record). On the MOCK-901 journal-off path this
	// full-params unmarshal is pure waste, so it is skipped; the stage-4
	// validator reads _meta from ex.Request.Params directly (TASK-017), so
	// skipping the decode here does not weaken validation.
	if ex.Snapshot.JournalEnabled() {
		meta, name := decodeParams(req.Params)
		st.meta = meta
		st.toolName = name
	}

	// Content-addressed derivation (ADR-002), made LAZY: the request key needs
	// the canonical body hash (a full parse) and an HMAC, so deferring it until
	// a stage actually draws keeps the MOCK-901 trivial-handler path free of
	// both costs (ADR-002 option 4). The key is derived at most once, on first
	// use of Rand or Clock, and both share that one derivation. Identical bytes
	// ⇒ identical key, independent of arrival order.
	instanceKey := ex.Instance.InstanceKey()
	epoch := ex.Instance.Epoch()
	method := req.Method
	rawID := req.ID.Raw()
	raw := ex.Raw
	keyOnce := sync.OnceValue(func() determinism.Key {
		return startKey(instanceKey, method, rawID, canonicalBodyHash(raw))
	})
	ex.Rand = sync.OnceValue(func() *rand.Rand { return keyOnce().RNG() })
	ex.Clock = sync.OnceValue(func() determinism.VirtualClock {
		return determinism.NewVirtualClock(keyOnce(), epoch)
	})
	return nil
}

// stageEraResolve is stage 2. Phase 1 is modern-only, so it records the era from
// the snapshot. It draws zero RNG values. A snapshot with no era is a
// misconfiguration the engine rejects with an internal fault rather than
// journalling an empty era — a real, reachable guard, so the stage's fault
// return is not vestigial. Era arbitration (MOCK-303/304) is a later phase that
// fills this stage by appending, not by inserting a new stage.
func (p *Pipeline) stageEraResolve(_ context.Context, ex *Exchange, st *pipelineState) *Fault {
	p.enter(StageEraResolve)
	era := ex.Snapshot.Era()
	if era == "" {
		return faultInternal.WithReason("snapshot has no resolved era")
	}
	st.era = era
	return nil
}

// stageAuthorize is stage 3. Phase 1 supports the `none` mode only, which
// accepts every request unconditionally, so this stage has nothing that can
// fail in Phase 1 and returns no fault. It is kept as a named, ordered stage
// (the p.enter marker is the seam) so bearer-static and oauth-rs (MOCK-401…407)
// fill it in a later phase by giving it a fault-returning signature and a real
// check; introducing that signature is a one-line change here plus a call-site
// edit, not a pipeline restructure. It draws zero RNG values.
func (p *Pipeline) stageAuthorize(_ context.Context, _ *Exchange, _ *pipelineState) {
	p.enter(StageAuthorize)
}

// stageValidate is stage 4 — the TASK-017 seam. It invokes the snapshot's
// [MetaValidator] and records its outcome for the journal. Phase 1's default
// validator ([AcceptAllMeta]) accepts every request, so this stage is a genuine
// pass-through that still executes in order. It draws zero RNG values.
func (p *Pipeline) stageValidate(ctx context.Context, ex *Exchange, st *pipelineState) *Fault {
	p.enter(StageValidate)
	validator := ex.Snapshot.MetaValidator()
	if validator == nil {
		validator = AcceptAllMeta
	}
	fault, part := validator.ValidateMeta(ctx, ex)
	st.metaValidation = part
	return fault
}

// stageFaultPre is stage 5. It calls the Phase 1 no-op [faultPreHook] so the
// ordered stage exists for Phase 9 to fill. It draws zero RNG values.
func (p *Pipeline) stageFaultPre(ctx context.Context, ex *Exchange) *Fault {
	p.enter(StageFaultPre)
	return faultPreHook(ctx, ex)
}

// stageDispatch is stage 6. It looks up the method handler, rejects an unknown
// or disabled method with -32601 (MOCK-202.2), and runs the handler. The
// handler's RNG draws (if any) are appended AFTER stages 1-5' zero draws, per
// ADR-002 rule 2. It resolves the result type from the internal/wire table for
// the journal and the response header.
func (p *Pipeline) stageDispatch(
	ctx context.Context, ex *Exchange, st *pipelineState,
) (json.RawMessage, *Fault) {
	p.enter(StageDispatch)
	method := st.method
	if !ex.Snapshot.MethodEnabled(method) {
		return nil, faultMethodNotFound.WithReason("method disabled by switches: " + method)
	}
	handler, ok := p.registry.Lookup(method)
	if !ok {
		return nil, faultMethodNotFound.WithReason("no handler for method: " + method)
	}
	if rt, known := wire.ResultTypeForMethod(method); known {
		st.resultType = rt
	}
	result, f := handler.Handle(ctx, ex)
	if f != nil {
		return nil, f
	}
	return result, nil
}

// stageFaultPost is stage 7. It calls the Phase 1 no-op [faultPostHook]. It
// draws zero RNG values.
func (p *Pipeline) stageFaultPost(ctx context.Context, ex *Exchange, result json.RawMessage) *Fault {
	p.enter(StageFaultPost)
	return faultPostHook(ctx, ex, result)
}

// finishResult runs stages 8 and 9 for a successful result. If the client has
// already gone (ctx canceled), it short-circuits to the cancellation path
// rather than emitting a response nobody will read (MOCK-212).
func (p *Pipeline) finishResult(
	ctx context.Context, ex *Exchange, sink Sink, st *pipelineState, result json.RawMessage,
) error {
	if canceled(ctx) {
		return p.finishCanceled(ctx, ex, sink, st)
	}
	body, encErr := encodeResult(ex.Request.ID, result)
	if encErr != nil {
		// An encode failure is an internal error; re-enter the error path with
		// a -32603 so the client still gets a well-formed response.
		return p.finishError(ctx, ex, sink, st,
			faultInternal.WithReason("result encode failed").WithCause(encErr))
	}
	if err := p.emit(sink, ex.Request.ID, FrameResult, body, okHeader(st)); err != nil {
		return err
	}
	p.commit(ctx, ex, st, respFromResult(st, body), outcomeOK)
	return nil
}

// finishError runs stages 8 and 9 for a wire error. It is reached from any stage
// that returned a fault. If the client has gone it records cancellation instead
// (a disconnected client will not read the error), otherwise it encodes the
// error through the single encoder (emit.go) and emits it.
func (p *Pipeline) finishError(
	ctx context.Context, ex *Exchange, sink Sink, st *pipelineState, f *Fault,
) error {
	if canceled(ctx) {
		return p.finishCanceled(ctx, ex, sink, st)
	}
	id := errorID(ex)
	body, encErr := encodeError(id, f)
	if encErr != nil {
		return encErr
	}
	if err := p.emit(sink, id, FrameError, body, errHeader(f)); err != nil {
		return err
	}
	p.commit(ctx, ex, st, respFromError(f, body), outcomeError)
	return nil
}

// finishCanceled is the MOCK-212 path: the client closed the stream, so the
// engine stops, closes the sink as ClientGone, and journals a record whose
// CancelPart carries the elapsed time since request start. It emits no response
// body — there is no one to receive it.
func (p *Pipeline) finishCanceled(
	ctx context.Context, ex *Exchange, sink Sink, st *pipelineState,
) error {
	p.enter(StageEncode)
	_ = sink.Close(CloseClientGone)
	elapsed := time.Since(st.start)
	resp := &journalapi.ResponsePart{
		Status:      0,
		CloseReason: CloseClientGone.String(),
	}
	p.enter(StageJournal)
	p.write(ctx, ex, st, resp, outcomeCanceled, &journalapi.CancelPart{
		ElapsedNs: elapsed.Nanoseconds(),
		Reason:    reasonFromCtx(ctx),
	})
	return nil
}

// emit runs stage 8: Begin the sink with the JSON-once shape, Send the single
// frame, and Flush. Close is the caller's deferred responsibility on the whole
// request, but a successful emit closes gracefully here so the journal sees
// CloseComplete. A sink error is returned to the transport.
func (p *Pipeline) emit(
	sink Sink, id jsonrpc.ID, kind FrameKind, body []byte, hdr ResponseHeader,
) error {
	p.enter(StageEncode)
	if err := sink.Begin(ShapeJSONOnce, hdr); err != nil {
		return err
	}
	if err := sink.Send(Frame{Kind: kind, ID: id, Bytes: body}); err != nil {
		return err
	}
	if err := sink.Flush(); err != nil {
		return err
	}
	return sink.Close(CloseComplete)
}

// commit runs stage 9 for a completed (non-canceled) request. It fills the
// close reason as Complete and defers to write.
func (p *Pipeline) commit(
	ctx context.Context, ex *Exchange, st *pipelineState, resp *journalapi.ResponsePart, outcome string,
) {
	p.enter(StageJournal)
	if resp != nil {
		resp.CloseReason = CloseComplete.String()
	}
	p.write(ctx, ex, st, resp, outcome, nil)
}

// write is the shared stage-9 body: it builds the capture [journal.Input] from
// the exchange and state and commits it to the ring, then records the metrics.
// When the instance has no journal, or journalling is off, Commit's own fast
// path returns without allocating (MOCK-901), so this is safe to call
// unconditionally.
func (p *Pipeline) write(
	ctx context.Context,
	ex *Exchange,
	st *pipelineState,
	resp *journalapi.ResponsePart,
	outcome string,
	cancel *journalapi.CancelPart,
) {
	elapsed := time.Since(st.start)
	in := buildInput(ex, st, resp, elapsed, cancel)
	ring, capturer := ex.Instance.Journal()
	written := false
	if ring != nil && capturer != nil {
		if seq, err := capturer.Commit(ctx, ring, in); err == nil && seq != 0 {
			written = true
		}
	}
	recordMetrics(ex, st, elapsed, outcome, written)
}
