package modern

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// behavior_sleep.go implements the sleep built-in (builtin-tools.md §3,
// MOCK-212). It provides a controllable, CANCELLABLE delay so a hub's timeout,
// cancellation and concurrency behavior can be tested.
//
// # Cancellation (MOCK-212, builtin-tools.md 3.4)
//
// The delay is a select on a timer AND ctx.Done(). A bare time.Sleep is a
// specification violation because it cannot observe cancellation and would break
// MOCK-212.1's 50 ms bound. On the ctx.Done() branch sleep emits NOTHING — it
// returns (nil, nil), and the pipeline's finishResult sees canceled(ctx) and
// records the cancellation with elapsed time rather than emitting a response
// (builtin-tools.md 3.4, MOCK-212.2). The timer is stopped on BOTH branches so
// no timer outlives the request (goleak clean, MOCK-107.7).
//
// # Determinism (builtin-tools.md 1.4 / 3.6)
//
// The delay is real wall-clock time and therefore varies, but the RESPONSE BODY
// contains no timing value: sleptMs is the intended (configured/bounded)
// duration, NEVER a measurement, so two identical requests at a fixed seed
// produce byte-identical bodies. The virtual clock is not advanced by a sleep
// (builtin-tools.md 1.4). sleep draws no RNG.
//
// # The maximum (builtin-tools.md 3.3 [P-46]/[P-47])
//
// maxSleepMs defaults to 30 000. On exceeding it, onExceedMaxSleep selects
// reject (default) or clamp. reject fails with -32602 IMMEDIATELY, incurring NO
// delay, so a hub timeout test cannot pass for the wrong reason via silent
// clamping. clamp delays maxSleepMs and marks _meta.clamped:true.

// sleep builds the sleep tool result, honoring cancellation. It resolves the
// effective delay, applies the bound (which may reject immediately), waits under
// a cancellable select, and — if not canceled — emits a byte-stable result
// reporting the intended duration.
func (h *toolsCallHandler) sleep(
	ctx context.Context, args callArgs, rc resultContext,
) (json.RawMessage, *engine.Fault) {
	requested, fault := h.requestedSleepMs(args)
	if fault != nil {
		return nil, fault
	}
	effective, clamped, fault := h.boundSleepMs(requested)
	if fault != nil {
		return nil, fault
	}

	if !waitCancellable(ctx, time.Duration(effective)*time.Millisecond) {
		// Canceled: emit nothing. finishResult sees canceled(ctx) and records
		// the cancellation with elapsed time (MOCK-212.2).
		return nil, nil
	}
	return h.sleepResult(effective, clamped, requested, rc)
}

// requestedSleepMs resolves the requested delay before bounding
// (builtin-tools.md 3.3 [P-45]): arguments.durationMs when present and client
// duration is allowed, otherwise behavior.sleepMs. A non-integer durationMs is a
// -32602; a negative durationMs is a -32602 with data.reason "negative_duration"
// (builtin-tools.md 3.3 [D]).
func (h *toolsCallHandler) requestedSleepMs(args callArgs) (int, *engine.Fault) {
	if !h.allowClientDuration() {
		return h.cfg.Sleep.SleepMs, nil
	}
	raw, ok := args.obj["durationMs"]
	if !ok {
		return h.cfg.Sleep.SleepMs, nil
	}
	var ms int
	if err := json.Unmarshal(raw, &ms); err != nil {
		return 0, invalidParamsReason(
			"mcpmock: durationMs must be an integer", "duration_not_integer")
	}
	if ms < 0 {
		return 0, invalidParamsReason(
			"mcpmock: durationMs must not be negative", "negative_duration")
	}
	return ms, nil
}

// allowClientDuration reports whether arguments.durationMs may override the
// configured sleepMs (builtin-tools.md 3.3, default true). The *bool nil value
// means default-true.
func (h *toolsCallHandler) allowClientDuration() bool {
	if h.cfg.Sleep.AllowClientDuration == nil {
		return true
	}
	return *h.cfg.Sleep.AllowClientDuration
}

// boundSleepMs applies maxSleepMs (builtin-tools.md 3.3 [P-47]). Within the
// bound it returns the requested delay unchanged. Over the bound it either
// REJECTS immediately (default: -32602 with data.requestedMs and data.maxSleepMs,
// no delay incurred) or CLAMPS to maxSleepMs and reports clamped=true. The
// clamped bool and the requested value are threaded to the result so clamp mode
// can mark _meta.clamped and _meta.requestedMs.
func (h *toolsCallHandler) boundSleepMs(requested int) (effective int, clamped bool, fault *engine.Fault) {
	maxMs := h.cfg.Sleep.MaxSleepMs
	if requested <= maxMs {
		return requested, false, nil
	}
	if h.cfg.Sleep.OnExceedMax == ExceedClamp {
		return maxMs, true, nil
	}
	// reject (default): fail immediately, no delay.
	data := mustMarshal(map[string]int{"requestedMs": requested, "maxSleepMs": maxMs})
	return 0, false, engine.InvalidParamsFault(
		"mcpmock: requested sleep exceeds maxSleepMs", data).
		WithReason("requested sleep exceeds maxSleepMs")
}

// sleepResult builds the completed-sleep result. content[0].text is the
// canonical "slept <n>ms" form (builtin-tools.md 3.6 [P-48]); structuredContent
// carries {"sleptMs": <n>} and, in clamp mode, the _meta clamp fields.
// sleptMs is the INTENDED duration, never a measurement (builtin-tools.md 3.6),
// so the body is byte-stable.
func (h *toolsCallHandler) sleepResult(
	effective int, clamped bool, requested int, rc resultContext,
) (json.RawMessage, *engine.Fault) {
	res := wire.ToolCallResult{
		ResultType:        resultTypePtr(rc.method, rc.om),
		Content:           []wire.TextContentBlock{wire.NewTextContentBlock(sleptText(effective))},
		StructuredContent: sleptStructured(effective, clamped),
		Meta:              sleepMeta(rc, clamped, requested),
	}
	return marshalToolResult(res)
}

// sleptText is the canonical content-block text "slept <n>ms" (builtin-tools.md
// 3.6 [P-48]), chosen so the block is deterministic and trivially assertable.
func sleptText(ms int) string {
	return "slept " + strconv.Itoa(ms) + "ms"
}

// sleptStructured builds {"sleptMs": <n>} and, in clamp mode, {"sleptMs": <n>,
// "clamped": true}. The keys are fixed and the marshal of a fixed-field struct is
// byte-stable.
func sleptStructured(ms int, clamped bool) json.RawMessage {
	if clamped {
		return mustMarshal(struct {
			SleptMs int  `json:"sleptMs"`
			Clamped bool `json:"clamped"`
		}{SleptMs: ms, Clamped: true})
	}
	return mustMarshal(struct {
		SleptMs int `json:"sleptMs"`
	}{SleptMs: ms})
}

// sleepMeta builds the result _meta: serverInfo always (unless omitted), plus
// the clamp fields (_meta.clamped:true and _meta.requestedMs) in clamp mode
// (builtin-tools.md 3.3). When serverInfo is omitted AND the sleep did not clamp,
// the whole _meta object is absent.
func sleepMeta(rc resultContext, clamped bool, requested int) *wire.ResultMeta {
	meta := serverInfoMeta(rc.discover, rc.om)
	if !clamped {
		return meta
	}
	if meta == nil {
		meta = &wire.ResultMeta{}
	}
	t := true
	meta.Clamped = &t
	meta.RequestedMs = &requested
	return meta
}
