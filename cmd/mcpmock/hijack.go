package main

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// maxLeakLineBytes bounds a single re-emitted stdout-leak line (ADR-011
// consequences): the drainer assembles at most this many bytes of a line before
// it flushes what it has as one leak record and marks the remainder truncated.
// A pathological writer that emits a gigabyte with no newline is therefore
// re-emitted as a stream of bounded records rather than buffered without limit,
// and — crucially — the drainer never stops reading, so the OS pipe buffer never
// fills and the writer never blocks (the deadlock ADR-011 warns about).
const maxLeakLineBytes = 64 * 1024

// drainReadBuf is the size of the drainer's read buffer. It is unrelated to the
// line bound: it only sets how much the drainer pulls from the pipe per read.
const drainReadBuf = 32 * 1024

// LeakCounter is the sink for the count of bytes that leaked to the hijacked
// stdout. It is deliberately a one-method, consumer-defined interface so this
// package depends on nothing but the standard library and slog for its drain
// logic, and so a test can supply a trivial counter without constructing a full
// observability bundle. In cmd/mcpmock it is satisfied by
// *obs.Metrics.AddStdoutLeakBytes via [MetricsLeakCounter], which increments
// mcpmock_stdout_leak_bytes_total (ADR-011). Add MUST be safe to call from the
// single drainer goroutine; it is never called concurrently by the drainer.
type LeakCounter interface {
	// Add records n additional leaked bytes.
	Add(n float64)
}

// Hijack is an installed stdout hijack: it holds the captured real fd 1 (handed
// to the stdio transport as its protocol writer), the pipe that has replaced
// os.Stdout process-wide, and the single drainer goroutine that re-emits every
// stray write to stderr as a counted diagnostic.
//
// # The contained exception to the no-process-globals rule (ADR-007)
//
// ADR-007 forbids our design from mutating process globals. os.Stdout is a
// process-global *os.File, and this type mutates it. That is the deliberate,
// single, contained exception ADR-011 sanctions, and it is allowed ONLY because:
//   - it lives under cmd/ and NOWHERE in the library (an AST scan enforces this;
//     the library form of the stdio transport takes an explicit writer instead);
//   - it is installed by an explicit [Install] call, NEVER by an init() side
//     effect or on import — importing this package mutates nothing, so an
//     embedding host (MOCK-107) is never ambushed;
//   - it is fully reversible by [Hijack.Restore], which puts os.Stdout back
//     exactly as it was and stops the drainer, so an in-process test harness
//     (TASK-022's facade runs inside the gateway's own suite) can install and
//     uninstall around a stdio run without corrupting the host's stdout.
//
// # Ordering requirement (capture fd 1 first)
//
// [Install] captures os.Stdout into protoOut BEFORE replacing it. That order is
// load-bearing: the stdio transport must write protocol frames to the real fd 1,
// so the caller passes [Hijack.ProtoOut] to the transport's Config.Out. If the
// replacement happened first, protoOut would be the drain and the mock would be
// mute. For the same reason [Install] must run before any other initialisation
// that might itself capture os.Stdout — anything that snapshots os.Stdout after
// the hijack correctly gets the pipe and is drained; anything that snapshotted
// it before escapes the hijack (ADR-011 "before any other initialisation").
//
// # Concurrency guarantee
//
// Stray writes may come from any goroutine at any time; they all land in the one
// pipe and are serialized by the OS. Exactly one drainer goroutine reads that
// pipe, so re-emission and byte counting need no locking. [Hijack.LeakBytes] is
// safe to call concurrently with the drainer (it reads an atomic). [Install] and
// [Hijack.Restore] mutate the process-global os.Stdout and must not run
// concurrently with each other; that is the caller's responsibility and is
// trivially satisfied by the cmd/ startup/shutdown sequence.
type Hijack struct {
	// protoOut is the captured real fd 1 — the protocol channel. It is handed to
	// the stdio transport and is NOT closed by Restore (the process still owns
	// fd 1; closing it would break the frame stream on the way out).
	protoOut *os.File

	// orig is os.Stdout as it was before Install, restored verbatim by Restore.
	orig *os.File

	// w is the write end of the pipe now installed as os.Stdout; r is the read
	// end the drainer consumes. Restore closes w to give the drainer EOF.
	w *os.File
	r *os.File

	counter LeakCounter
	logger  *slog.Logger

	// leaked accumulates every byte drained off the hijacked stdout, so a CI
	// test can assert it is zero after a clean run (ADR-011 / MOCK-105.4). It
	// mirrors the Prometheus counter but needs no registry to read.
	leaked atomic.Uint64

	// done is closed when the drainer goroutine has fully exited, so Restore can
	// join it deterministically (no leaked goroutine — goleak-clean).
	done chan struct{}

	// restoreOnce makes Restore idempotent, so a deferred Restore plus an
	// explicit one on the shutdown path do not double-close the pipe.
	restoreOnce sync.Once
	restoreErr  error
}

// MetricsLeakCounter adapts *obs.Metrics to [LeakCounter] by forwarding to
// AddStdoutLeakBytes, which increments mcpmock_stdout_leak_bytes_total. It keeps
// the hijack independent of the observability package's concrete type while
// still feeding the one metric ADR-011 names.
type MetricsLeakCounter struct {
	// Metrics is the per-Bundle metric set whose stdout-leak counter is fed.
	Metrics *obs.Metrics
}

// Add forwards n leaked bytes to the Prometheus counter.
func (m MetricsLeakCounter) Add(n float64) { m.Metrics.AddStdoutLeakBytes(n) }

// Install captures the real fd 1, replaces os.Stdout with the write end of a
// fresh pipe, and starts the single drainer goroutine that re-emits everything
// arriving on the read end to stderr as structured "stdout_leak" records via
// logger, counting the bytes into counter.
//
// It returns the installed [Hijack]. The caller MUST hand [Hijack.ProtoOut] to
// the stdio transport as its protocol writer and MUST call [Hijack.Restore]
// during shutdown. Install is the ONLY thing that mutates os.Stdout in the
// module, and it does so only when called — never on import (ADR-011 layer 1).
//
// logger must not be nil and must write to stderr (cmd/mcpmock passes the
// obs.Bundle's stderr logger); a nil counter is tolerated as a no-op count so a
// caller without metrics can still get the tagging and the [Hijack.LeakBytes]
// accessor.
func Install(logger *slog.Logger, counter LeakCounter) (*Hijack, error) {
	if logger == nil {
		return nil, errors.New("mcpmock: Install requires a non-nil stderr logger")
	}
	if counter == nil {
		counter = noopCounter{}
	}

	// ORDER MATTERS: capture the real fd 1 as the protocol channel BEFORE any
	// replacement, so protoOut is fd 1 and not the drain.
	protoOut := os.Stdout

	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	h := &Hijack{
		protoOut: protoOut,
		orig:     os.Stdout,
		w:        w,
		r:        r,
		counter:  counter,
		logger:   logger,
		done:     make(chan struct{}),
	}

	// Replace the process-global stdout only after the pipe and drainer state
	// are ready, so no window exists where os.Stdout is the pipe but nothing is
	// draining it.
	os.Stdout = w
	go h.drain()

	return h, nil
}

// ProtoOut returns the captured real fd 1 — the protocol channel. The caller
// passes it to the stdio transport's Config.Out so protocol frames reach the
// peer while everything else in the process is drained. It is never nil for an
// installed Hijack.
func (h *Hijack) ProtoOut() *os.File { return h.protoOut }

// LeakBytes returns the total number of bytes drained off the hijacked stdout so
// far. A correct run leaks nothing, so a CI test asserts this is zero after a
// full scenario (ADR-011 / MOCK-105.4). It is safe to call at any time,
// including concurrently with the drainer.
func (h *Hijack) LeakBytes() uint64 { return h.leaked.Load() }

// Restore puts os.Stdout back exactly as it was before [Install], closes the
// pipe so the drainer sees EOF, and waits for the drainer goroutine to exit
// (bounded by ctx so a stalled drain cannot hang shutdown — MOCK-508). It is
// idempotent and safe to defer. After Restore returns, os.Stdout is the original
// handle and no drainer goroutine remains.
//
// Restore restores os.Stdout FIRST, then closes the pipe write end: that order
// means any new write after Restore goes to the real stdout (not to a
// closed-pipe error), and closing w unblocks the drainer with EOF. The captured
// protoOut (fd 1) is intentionally NOT closed — the process still owns it.
func (h *Hijack) Restore(ctx context.Context) error {
	h.restoreOnce.Do(func() {
		// Restore the global first so post-Restore writes are not lost to a
		// closed pipe.
		os.Stdout = h.orig
		// Closing the write end gives the drainer EOF and lets it finish.
		h.restoreErr = h.w.Close()

		select {
		case <-h.done:
			// Drainer exited; close the read end to release the fd.
			_ = h.r.Close()
		case <-ctx.Done():
			// A stalled drain must not hang shutdown. Close the read end to
			// unblock the drainer's Read, then join it so no goroutine leaks.
			_ = h.r.Close()
			<-h.done
			if h.restoreErr == nil {
				h.restoreErr = ctx.Err()
			}
		}
	})
	return h.restoreErr
}

// drain is the single drainer goroutine. It reads the pipe until EOF (Restore
// closed the write end) or an unrecoverable read error, re-emitting each line as
// a structured "stdout_leak" record on stderr and counting every byte. It never
// stops reading before EOF, which is what keeps the OS pipe buffer from filling
// and blocking a stray writer (the ADR-011 deadlock hazard).
func (h *Hijack) drain() {
	defer close(h.done)

	br := bufio.NewReaderSize(h.r, drainReadBuf)
	var (
		line      []byte
		truncated bool
	)
	flush := func() {
		if len(line) == 0 && !truncated {
			return
		}
		h.emit(line, truncated)
		line = line[:0]
		truncated = false
	}

	for {
		b, err := br.ReadByte()
		if err != nil {
			// EOF (clean shutdown) or a read error both end the drain. Flush any
			// partial line first so a trailing unterminated leak is not lost.
			flush()
			return
		}
		h.leaked.Add(1)
		h.counter.Add(1)
		if b == '\n' {
			flush()
			continue
		}
		if len(line) < maxLeakLineBytes {
			line = append(line, b)
			continue
		}
		// The line exceeds the bound: emit what we have, mark it truncated, and
		// keep consuming (and counting) the rest so the writer never blocks. The
		// remaining bytes up to the next newline are counted but not buffered.
		truncated = true
		flush()
	}
}

// emit writes one stdout-leak diagnostic to stderr via the logger. The leaked
// text is carried as a field, not as the message, so the message stays constant
// and a log consumer groups by the "event" field (obs.EventStdoutLeak). This is
// ADR-011's whole point: a stray write becomes a loud, attributable, counted log
// line instead of silent protocol corruption.
func (h *Hijack) emit(text []byte, truncated bool) {
	attrs := []any{
		slog.String(obs.FieldEvent, obs.EventStdoutLeak),
		slog.String("text", string(text)),
	}
	if truncated {
		attrs = append(attrs, slog.Bool("truncated", true))
	}
	h.logger.Warn("stdout leak", attrs...)
}

// noopCounter is the LeakCounter used when a caller supplies none: it still lets
// the drain run and LeakBytes accumulate, but records nothing to Prometheus.
type noopCounter struct{}

// Add discards the count.
func (noopCounter) Add(float64) {}
