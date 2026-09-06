package journal

import (
	"context"
	"errors"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// ErrOverflow is returned by [Ring.Write] under the error overflow policy when a
// bound is reached and the record is rejected rather than stored. The caller
// (TASK-012 capture) turns it into a 503 with a journalled reason (MOCK-902).
// Callers test for it with errors.Is.
var ErrOverflow = errors.New("journal: ring full, write rejected by error policy")

// recordSize estimates the retained byte weight of a record for the MaxBytes
// budget. It counts the variable-length byte slices the ring holds onto — the
// request and response bodies, header names and values, and the raw JSON-RPC id
// and params — plus a fixed per-record overhead for the scalar fields. It is an
// estimate, not a serialization: the budget only needs to be proportional to
// real retained memory so that a body-heavy workload trips MaxBytes before it
// exhausts the process, which is the MOCK-902 guarantee.
func recordSize(r journalapi.Record) int64 {
	const fixedOverhead = 256 // scalar fields, times, enums, small strings
	total := int64(fixedOverhead)
	total += int64(len(r.JSONRPC.Body))
	total += int64(len(r.JSONRPC.ID))
	total += int64(len(r.JSONRPC.Params))
	total += int64(len(r.Instance) + len(r.JSONRPC.Method) + len(r.JSONRPC.Name))
	if r.HTTP != nil {
		total += headerBytes(r.HTTP.Headers)
		total += headerBytes(r.HTTP.RespHeaders)
		total += int64(len(r.HTTP.Path) + len(r.HTTP.Query) + len(r.HTTP.Method))
	}
	if r.Response != nil {
		total += int64(len(r.Response.Body))
		total += responseFrameBytes(r.Response.Frames)
	}
	for k, v := range r.Meta {
		total += int64(len(k) + len(v))
	}
	return total
}

// headerBytes sums the byte weight of a wire-order header list.
func headerBytes(headers [][2]string) int64 {
	var total int64
	for _, h := range headers {
		total += int64(len(h[0]) + len(h[1]))
	}
	return total
}

// responseFrameBytes sums the byte weight of recorded SSE frames.
func responseFrameBytes(frames []journalapi.FrameRecord) int64 {
	var total int64
	for i := range frames {
		total += int64(len(frames[i].Data) + len(frames[i].EventID) + len(frames[i].EventType))
	}
	return total
}

// blockPollInterval is how often a blocked writer re-checks for drained capacity
// while waiting under the block overflow policy. It is short relative to the
// default 100 ms BlockTimeout so the wait degrades promptly, and long enough that
// a stuck writer does not busy-spin a core. Context cancellation and the overall
// timeout are checked on every tick, so the wait is always bounded (GAP-017).
const blockPollInterval = 200 * time.Microsecond

// waitForCapacity implements the block overflow policy's bounded wait. It returns
// true when the shard drained enough that the write may proceed without evicting
// an undrained record, and false when the wait was bounded out — either by
// ctx cancellation or by the blockTimeout — in which case the caller degrades to
// drop-oldest. The wait never blocks indefinitely: both the deadline and ctx are
// polled every blockPollInterval (ADR-005; GAP-017).
func waitForCapacity(ctx context.Context, sh *shard, timeout time.Duration) bool {
	if !sh.capacityFull() {
		return true
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(blockPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if !sh.capacityFull() {
				return true
			}
			if !time.Now().Before(deadline) {
				return false
			}
		}
	}
}
