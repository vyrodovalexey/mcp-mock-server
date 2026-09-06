package stdio

import (
	"context"
	"runtime"
	"sync"
)

// pool.go is the bounded worker pool that handles requests read off stdin.
//
// Why bounded. stdin is a single duplex channel that can deliver requests faster
// than they are served. Spawning a goroutine per inbound request is a trivial
// denial of service — an unbounded goroutine count breaks the MOCK-901
// throughput profile and the architecture.md §7.1 goroutine budget (which allows
// two goroutines plus at most the configured pool size per stdio transport). The
// pool caps concurrent in-flight work at a configured bound.
//
// Backpressure. The pool has no internal queue: [pool.submit] blocks until a
// worker is free (or the pool's context is canceled). Because the reader
// goroutine calls submit, a saturated pool stops the reader from consuming more
// of stdin, which propagates flow control back to the peer through the OS pipe
// buffer. This is the correct behavior for a single ordered channel: it bounds
// memory and in-flight concurrency without dropping or reordering requests. The
// alternative — an unbounded submit queue — would merely move the DoS from
// goroutines to heap.

// defaultPoolSize is the worker-pool bound used when the caller does not
// configure one. GOMAXPROCS matches the CPU parallelism available to serve
// deterministic, CPU-bound handlers; a handler that blocks (the sleep tool) is
// the caller's concern to size around via [Config.PoolSize].
func defaultPoolSize() int {
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = 1
	}
	return n
}

// pool is a fixed-size worker pool. It owns exactly size worker goroutines,
// started by [newPool] and stopped by [pool.stop]. Work is a func(context) run
// on a worker; the pool passes each worker the pool context so a worker observes
// pool shutdown. The pool adds no goroutine per task — the whole point — so the
// live goroutine count is exactly size while running and zero after stop.
type pool struct {
	// tasks is unbuffered: a send blocks until a worker receives, which is the
	// backpressure mechanism. It is never closed while submit may run; stop
	// signals shutdown through ctx and then drains via the WaitGroup.
	tasks chan func(context.Context)
	// ctx is the pool's lifetime context, derived from the transport context.
	// It is passed to each task so a worker can observe shutdown; it is NOT the
	// per-request context (that is created per request in transport.go).
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	size   int
}

// newPool starts size worker goroutines bound to a context derived from parent.
// size must be >= 1; callers normalize it via [defaultPoolSize] first.
func newPool(parent context.Context, size int) *pool {
	ctx, cancel := context.WithCancel(parent)
	p := &pool{
		tasks:  make(chan func(context.Context)),
		ctx:    ctx,
		cancel: cancel,
		size:   size,
	}
	p.wg.Add(size)
	for i := 0; i < size; i++ {
		go p.worker()
	}
	return p
}

// worker runs tasks until the pool context is canceled and the task channel is
// drained of any in-flight send. It observes ctx.Done() so a stop unblocks a
// worker that is idle-waiting for work.
func (p *pool) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case task := <-p.tasks:
			// A nil task cannot be enqueued (submit rejects nil implicitly by
			// never sending one); run it unconditionally.
			task(p.ctx)
		}
	}
}

// submit hands task to a free worker, blocking until one is available. It
// returns false without running task if the pool context is canceled first —
// which is how a reader goroutine learns the transport is shutting down and
// stops pulling from stdin. The bound holds because there are exactly size
// workers and the channel is unbuffered: at most size tasks run at once.
func (p *pool) submit(task func(context.Context)) bool {
	select {
	case <-p.ctx.Done():
		return false
	case p.tasks <- task:
		return true
	}
}

// stop cancels the pool context and waits for every worker to return. After stop
// returns, the pool owns zero goroutines. It is safe to call once; a second call
// is a no-op because cancel is idempotent and the WaitGroup is already drained.
func (p *pool) stop() {
	p.cancel()
	p.wg.Wait()
}
