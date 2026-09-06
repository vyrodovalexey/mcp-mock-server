package httpx

import (
	"context"
	"sync"
	"time"
)

// timerWheel is the single shared timer that drives every SSE stream's periodic
// keep-alive, implementing ADR-012 §4's "no per-stream ticker" decision. The
// reason is MOCK-903: at 20 000 concurrent streams, one time.Ticker per stream
// is 20 000 runtime timer-heap entries and a steady wake-up storm that makes an
// idle stream's cost non-zero — exactly the quantity MOCK-903 measures. One
// wheel replaces all of them: registering a keep-alive is inserting a slot,
// O(1); the wheel's cost is one goroutine and one 10 ms tick regardless of how
// many streams are registered.
//
// The wheel NEVER writes to a socket. On fire it enqueues a keep-alive onto the
// stream's buffered channel and re-arms; if the channel is full it DROPS the
// keep-alive (a slow client must not stall the wheel and freeze every other
// stream, ADR-012 §4 backpressure rule). This is the Phase-1 home of a
// component ADR-012 places in internal/sched for Phase 2, where five other
// requirements (MOCK-253/210/227/231, MRTR expiry) share it; it lives here now
// so this task stays within its file boundary and the SSE framing it needs has
// its timer without a per-stream ticker.
//
// Concurrency: registration and cancellation are safe from any goroutine; the
// tick loop runs on the single wheel goroutine. The zero value is not usable;
// construct one with [newTimerWheel] and Stop it at shutdown.
type timerWheel struct {
	tick     time.Duration
	mu       sync.Mutex
	entries  map[uint64]*wheelEntry
	nextID   uint64
	stopCh   chan struct{}
	stopOnce sync.Once
	doneCh   chan struct{}
	// now is the clock source, injectable so tests drive the wheel
	// deterministically instead of sleeping. It defaults to time.Now.
	now func() time.Time
}

// wheelEntry is one registered periodic callback: a stream's keep-alive.
type wheelEntry struct {
	interval time.Duration
	next     time.Time
	fire     func()
}

// newTimerWheel returns a stopped-until-Started wheel ticking every tick. A tick
// of 10 ms matches ADR-012 §4; a keep-alive interval is rounded up to a whole
// number of ticks, which is the granularity trade the shared wheel makes in
// exchange for costing nothing per stream.
func newTimerWheel(tick time.Duration) *timerWheel {
	if tick <= 0 {
		tick = 10 * time.Millisecond
	}
	return &timerWheel{
		tick:    tick,
		entries: make(map[uint64]*wheelEntry),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		now:     time.Now,
	}
}

// start launches the single wheel goroutine. It is the only goroutine the wheel
// owns (architecture.md §7.1's "timer wheel: 1").
func (w *timerWheel) start() {
	go w.run()
}

// run is the wheel's single goroutine. It wakes every tick, fires every entry
// whose deadline has passed, and re-arms it. It exits on Stop, closing doneCh so
// Stop can wait for a clean teardown (graceful shutdown must not leak this
// goroutine, MOCK-508).
func (w *timerWheel) run() {
	defer close(w.doneCh)
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-t.C:
			w.fireDue()
		}
	}
}

// fireDue fires every entry whose next deadline is at or before now and re-arms
// it. It holds the lock only to snapshot the due callbacks, then fires them
// outside the lock so a fire callback (a non-blocking channel send) never
// deadlocks against registration.
func (w *timerWheel) fireDue() {
	now := w.now()
	var due []func()
	w.mu.Lock()
	for _, e := range w.entries {
		if !now.Before(e.next) {
			due = append(due, e.fire)
			// Re-arm from the scheduled deadline so intervals do not drift with
			// tick jitter; catch up if we fell behind.
			e.next = e.next.Add(e.interval)
			if e.next.Before(now) {
				e.next = now.Add(e.interval)
			}
		}
	}
	w.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// register adds a periodic fire callback at the given interval and returns a
// cancel func the caller MUST call when the stream ends, so the entry does not
// outlive its stream. Registration is O(1) and allocation-light; this is what
// makes an idle stream cheap (MOCK-903).
func (w *timerWheel) register(interval time.Duration, fire func()) (cancel func()) {
	if interval <= 0 {
		return func() {}
	}
	// Round the interval up to whole ticks so the shared wheel need not track
	// sub-tick precision it cannot honor.
	if r := interval % w.tick; r != 0 {
		interval += w.tick - r
	}
	w.mu.Lock()
	id := w.nextID
	w.nextID++
	w.entries[id] = &wheelEntry{
		interval: interval,
		next:     w.now().Add(interval),
		fire:     fire,
	}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		delete(w.entries, id)
		w.mu.Unlock()
	}
}

// stop halts the wheel goroutine and waits, bounded by ctx, for it to exit. It
// is idempotent. A nil ctx waits unconditionally; graceful shutdown passes a
// deadline so a wedged wheel cannot hang shutdown (MOCK-508).
func (w *timerWheel) stop(ctx context.Context) {
	w.stopOnce.Do(func() { close(w.stopCh) })
	if ctx == nil {
		<-w.doneCh
		return
	}
	select {
	case <-w.doneCh:
	case <-ctx.Done():
	}
}
