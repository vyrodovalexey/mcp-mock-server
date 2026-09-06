package engine

import (
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// buildInput assembles the transport-neutral [journal.Input] the capturer turns
// into a fully-faithful record at stage 9 (MOCK-601). It maps the exchange, the
// accumulated pipeline state, the produced response and the optional
// cancellation part into the capture input, copying nothing itself — the
// capturer owns the copy discipline (ADR-005). It is a pure function of its
// arguments.
func buildInput(
	ex *Exchange,
	st *pipelineState,
	resp *journalapi.ResponsePart,
	elapsed time.Duration,
	cancel *journalapi.CancelPart,
) journal.Input {
	in := journal.Input{
		Instance:   ex.Instance.Name(),
		Generation: snapshotGen(ex),
		WallTime:   time.Now(), // evidence about the run, not a modeled value (ADR-002 rule 3)
		// MonoNs is nanoseconds since instance start, from a monotonic clock
		// reading (MOCK-601.1, REV-002): time.Since uses the monotonic component,
		// so this elapsed-since-start value is trustworthy across wall-clock
		// adjustments — the property MOCK-212's cancellation-with-elapsed-time
		// relies on.
		MonoNs:     time.Since(ex.Instance.Start()).Nanoseconds(),
		DurationNs: elapsed.Nanoseconds(),
		Transport:  transportTag(ex.Transport),
		Era:        st.era,
		Peer:       ex.Peer,
		Method:     st.method,
		Name:       st.toolName,
		Body:       ex.Raw,
		Meta:       st.meta,
		HTTP:       httpInput(ex, resp),
	}
	if ex.Request != nil {
		in.ID = ex.Request.ID.Raw()
		in.Params = ex.Request.Params
		in.IsNotification = ex.Request.IsNotification()
	}
	in.Response = responseInput(resp)
	in.MetaValidation = st.metaValidation
	if cancel != nil {
		// MOCK-212: the cancellation's elapsed time is the record's DurationNs,
		// and the close reason (clientGone) is on the response part, so the
		// committed record shows both that the request was canceled and how
		// long it ran before closure. journal.Input (TASK-012) exposes no
		// dedicated Canceled field, so the engine records the cancellation
		// through these two faithful fields rather than reaching into the
		// journal package's contract, which is out of this task's boundary. The
		// journalapi.CancelPart the pipeline built is the same elapsed value; it
		// is asserted directly in the pipeline tests.
		in.DurationNs = cancel.ElapsedNs
	}
	return in
}

// snapshotGen returns the snapshot generation for the record, or zero when no
// snapshot was captured (a request that failed before stage 1 completed).
func snapshotGen(ex *Exchange) uint64 {
	if ex.Snapshot == nil {
		return 0
	}
	return ex.Snapshot.Gen()
}

// transportTag maps the engine transport kind to the journalapi transport value.
func transportTag(k Kind) journalapi.Transport {
	if k == KindStdio {
		return journalapi.TransportStdio
	}
	return journalapi.TransportHTTP
}

// httpInput builds the HTTP slice of the capture input from the exchange's HTTP
// context and the produced response, or returns nil for a stdio exchange. The
// header lists are passed through in wire order; the capturer applies redaction
// (security.md §5).
func httpInput(ex *Exchange, resp *journalapi.ResponsePart) *journal.HTTPInput {
	if ex.HTTP == nil {
		return nil
	}
	h := &journal.HTTPInput{
		Method:  ex.HTTP.Method,
		Path:    ex.HTTP.Path,
		Query:   ex.HTTP.Query,
		Headers: ex.HTTP.Headers,
	}
	if resp != nil {
		h.Status = resp.Status
	}
	return h
}

// responseInput maps the journal response part into the capture input's response
// slice, or returns nil when no response was produced (a notification). It
// carries the status, result type, body, shape, close reason and error object
// through so the record is complete (MOCK-601.4/.6).
func responseInput(resp *journalapi.ResponsePart) *journal.ResponseInput {
	if resp == nil {
		return nil
	}
	return &journal.ResponseInput{
		Status:      resp.Status,
		ResultType:  resp.ResultType,
		Body:        resp.Body,
		Shape:       resp.Shape,
		CloseReason: resp.CloseReason,
		Error:       resp.Error,
	}
}
