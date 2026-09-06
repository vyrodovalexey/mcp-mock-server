package modern

import (
	"context"
	"time"
)

// wait.go holds the cancellable delay primitive the sleep built-in uses. It is
// separated so the MOCK-212 select-and-stop discipline lives in one small,
// testable place.

// waitCancellable blocks for d, or until ctx is canceled, whichever comes
// first. It reports true when the full duration elapsed (the caller should emit
// its result) and false when ctx was canceled (the caller should emit nothing —
// the MOCK-212 abandon path).
//
// It is the mandatory select on a timer AND ctx.Done() (builtin-tools.md 3.4): a
// bare time.Sleep cannot observe cancellation. The timer is stopped on the
// ctx.Done() branch so it does not outlive the request (goleak clean,
// MOCK-107.7). A zero (or negative) duration is the fast path: it returns
// immediately without arming a timer, but still honors an already-canceled ctx
// so a client that disconnected before a 0 ms sleep is still recorded as
// canceled.
func waitCancellable(ctx context.Context, d time.Duration) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
