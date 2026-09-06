// Package stdio implements the newline-delimited JSON-RPC stdio transport with a
// bounded worker pool (MOCK-102 stdio; MOCK-256 interleaving). It multiplexes
// many concurrent requests over a single duplex channel, driving each through
// the transport-neutral engine pipeline (ADR-006) and tagging every response
// frame with its originating JSON-RPC id so interleaved responses on the one
// output channel stay correctly attributed.
//
// It is era-blind (architecture.md §6.1 rule 3): it imports internal/engine and
// internal/jsonrpc but never internal/modern, internal/legacy or internal/mrtr.
// Its job is exactly the ADR-006 transport contract — decode bytes into an
// engine.Exchange, supply an engine.Sink, honor Close — and nothing about wire
// vocabulary.
//
// # Framing contract
//
// One JSON-RPC message per line, UTF-8, no embedded raw newline (wire clause 1.9
// [P-04]). A read yields one message with its trailing '\n' stripped; a write
// emits the message followed by exactly one '\n' in a SINGLE io.Writer.Write, so
// a frame is never split across a newline boundary. Inbound frames are bounded
// by Config.MaxLineBytes (16 MiB default); an oversized frame ends the read loop
// as ErrLineTooLong rather than growing an unbounded buffer. Malformed frame
// content is not the transport's concern: it is handed to the engine, which
// answers -32700/-32600 without panicking.
//
// # Interleaving and serialization guarantee (MOCK-256)
//
// All response frames from all in-flight requests are funneled onto ONE writer
// channel consumed by ONE writer goroutine, which writes each frame with a
// single atomic Write. There is exactly one writer and each frame is one Write,
// so two concurrent requests can never interleave their bytes — a frame is
// emitted whole or not at all. This is the property MOCK-256 demands and the one
// the package's concurrent-write tests exercise under -race.
//
// # Worker-pool bound and backpressure
//
// Requests are handled on a bounded worker pool, not a goroutine per request
// (architecture.md §7.1: two goroutines plus at most the pool size per stdio
// transport). Config.PoolSize sets the bound; it defaults to GOMAXPROCS. The
// pool has no internal queue: submitting a request BLOCKS until a worker is free,
// which stops the reader from consuming more of stdin and propagates flow control
// back to the peer through the OS pipe buffer. Saturation therefore bounds
// in-flight concurrency and memory without dropping or reordering requests — the
// correct backpressure for a single ordered channel, and the reason unbounded
// goroutine spawn (a trivial DoS) is structurally excluded.
//
// # Injected writer — the ADR-011 seam
//
// Protocol output goes exclusively through the injected Config.Out writer; this
// package NEVER references os.Stdout. That is deliberate and load-bearing:
// cmd/mcpmock (TASK-021) captures the real fd 1 as protoOut, hands it here as
// Out, then replaces os.Stdout with a stderr-tagging drain so a stray write to
// stdout anywhere in the process (a debug fmt.Println, a chatty third-party
// library) becomes a counted, attributable diagnostic instead of silent
// frame-stream corruption. If this transport hardcoded os.Stdout the hijack
// could not substitute the pipe; because it takes an explicit writer, the hijack
// lives entirely in cmd/ and the library form stays free of process-global
// mutation (ADR-007).
//
// # Cancellation (MOCK-212)
//
// Every in-flight request registers its cancel func in a request table. When the
// input stream reaches EOF — the peer closed the pipe, or the parent process
// exited (MOCK-508) — the reader cancels every in-flight request context; the
// engine observes the cancellation at stages 8-9, stops work, and journals a
// record with elapsed time rather than emitting a response nobody will read.
// Phase 1 wires cancellation on EOF only; the same table carries per-id
// cancel-notification cancellation in a later phase.
//
// # Goroutine budget (architecture.md §7.1)
//
// A running Transport owns exactly two long-lived goroutines — the reader (which
// runs on Serve's own goroutine) and the single writer — plus at most PoolSize
// workers. After Serve returns, zero remain: the pool is stopped and joined, the
// writer channel is closed and the writer joined, and every per-request context
// is canceled.
//
// # Determinism (§0.1, ADR-002)
//
// The transport introduces no nondeterminism: it derives no keys, draws no
// randomness, and reads no clock. Response bytes come from the engine's
// byte-stable encoder, so the same request at the same seed yields byte-identical
// frames on stdio and on HTTP (MOCK-102 102.3/102.4).
package stdio
