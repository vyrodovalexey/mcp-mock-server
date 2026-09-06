package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// helpers.go holds the small, pure helpers the pipeline stages compose from.
// Keeping them here keeps each stage function short (well under the funlen and
// gocognit budgets) and keeps the stage code reading as a sequence of named
// steps.

// metricsOf returns the instance's metric handles, or nil when metrics are not
// configured. It guards the nil-Instance case defensively so a partially
// constructed exchange in a test cannot panic the pipeline.
func metricsOf(ex *Exchange) Metrics {
	if ex == nil || ex.Instance == nil {
		return nil
	}
	return ex.Instance.Metrics()
}

// canonicalBodyHash returns sha256 of the RFC 8785 canonical form of body, or
// sha256 of the raw bytes when the body is not valid JSON. ADR-002 hashes the
// canonical body so two byte sequences denoting the same JSON value derive the
// same request key; a non-JSON body (a MOCK-503 malformed request) still needs a
// stable 32-byte hash, so it falls back to hashing the raw bytes rather than
// failing. The result is always exactly 32 bytes, which is what
// determinism.Derive requires (a raw body would panic by design).
func canonicalBodyHash(body []byte) []byte {
	if canon, err := jsonrpc.CanonicalJSON(body); err == nil {
		sum := sha256.Sum256(canon)
		return sum[:]
	}
	sum := sha256.Sum256(body)
	return sum[:]
}

// decodeParams extracts the decoded params._meta map (with every key preserved,
// including unknown ones, MOCK-203.6) and the primitive name (params.name, e.g.
// a tool name) from a raw params object. It never fails: a params value that is
// absent, null, or not an object yields (nil, ""), because a malformed params is
// a validation concern for a later stage, not a decode error here. It performs
// no wire-literal lookups beyond the internal/wire param keys (ADR-019).
func decodeParams(params json.RawMessage) (meta map[string]json.RawMessage, name string) {
	if len(params) == 0 {
		return nil, ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		return nil, ""
	}
	if raw, ok := obj[wire.MetaKey]; ok {
		_ = json.Unmarshal(raw, &meta) // best-effort; a non-object _meta stays nil
	}
	if raw, ok := obj[wire.ParamsKeyName]; ok {
		_ = json.Unmarshal(raw, &name)
	}
	return meta, name
}

// okHeader builds the response header for a successful result: HTTP 200 and the
// resolved result type from the wire table.
func okHeader(st *pipelineState) ResponseHeader {
	return ResponseHeader{Status: http.StatusOK, ResultType: st.resultType}
}

// errHeader builds the response header for a wire error, carrying the fault's
// HTTP status so the transport sets it (200 for a JSON-RPC error in a well-formed
// envelope, 400 for a parse/validation error, per the wire codes).
func errHeader(f *Fault) ResponseHeader {
	status := f.HTTPStatus
	if status == 0 {
		status = http.StatusOK
	}
	return ResponseHeader{Status: status}
}

// errorID returns the id to echo on an error response. It is the request id when
// the envelope decoded far enough to have one, and an absent id otherwise — a
// request so malformed it had no id gets a null id in the error response, which
// jsonrpc.ID renders correctly.
func errorID(ex *Exchange) jsonrpc.ID {
	if ex.Request != nil {
		return ex.Request.ID
	}
	return jsonrpc.AbsentID
}

// respFromResult builds the journal response part for a successful result: HTTP
// 200, the result type, the emitted body and the JSON shape.
func respFromResult(st *pipelineState, body []byte) *journalapi.ResponsePart {
	return &journalapi.ResponsePart{
		Status:     http.StatusOK,
		ResultType: st.resultType,
		Body:       body,
		Shape:      "json",
	}
}

// respFromError builds the journal response part for a wire error: the fault's
// HTTP status, the emitted body, and the JSON-RPC error object so MOCK-601.6 (a
// rejected request's record carries its emitted error) holds.
func respFromError(f *Fault, body []byte) *journalapi.ResponsePart {
	status := f.HTTPStatus
	if status == 0 {
		status = http.StatusOK
	}
	return &journalapi.ResponsePart{
		Status: status,
		Body:   body,
		Shape:  "json",
		Error:  &journalapi.ErrorRecord{Code: f.Code, Message: f.Message, Data: f.Data},
	}
}

// canceled reports whether ctx has been canceled — the MOCK-212 signal that the
// client closed the response stream. It treats any non-nil ctx.Err() as
// cancellation for the purpose of stopping work and recording elapsed time.
func canceled(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

// reasonFromCtx maps a canceled context's error to the journal cancellation
// reason string. A deadline exceeded and an explicit cancel are distinguished so
// the evidence names the cause; a transport that cancels on client disconnect
// wraps context.Canceled, which reads as "clientDisconnect".
func reasonFromCtx(ctx context.Context) string {
	switch {
	case ctx == nil:
		return "clientDisconnect"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "deadlineExceeded"
	default:
		return "clientDisconnect"
	}
}

// recordMetrics records the completed request and, when a journal record was
// written, the record and the current snapshot generation. It is a no-op when
// the instance has no metrics. Every call is allocation-free and bounded-label
// (observability.md §2.3), so it is safe on the hot path.
func recordMetrics(ex *Exchange, st *pipelineState, elapsed time.Duration, outcome string, written bool) {
	m := metricsOf(ex)
	if m == nil {
		return
	}
	m.RecordRequest(ex.Transport.String(), st.era, st.method, outcome, elapsed.Seconds())
	if ex.Snapshot != nil {
		m.SetSnapshotGeneration(ex.Snapshot.Gen())
	}
	if written {
		m.IncJournalRecord(elapsed.Seconds())
	}
}
