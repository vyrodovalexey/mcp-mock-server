package determinism_test

import (
	"bytes"
	"sync"
	"testing"
)

// TestConcurrentDerivationOrderIndependent is the concurrency test ADR-002
// rates the #3 project risk. It runs many goroutines that each, independently
// and in an unpredictable interleaving:
//
//   - derive the request key for one of a small set of DISTINCT requests, and
//   - draw a fixed-length RNG stream from it.
//
// It then asserts that every draw for a given request equals the single-
// threaded reference draw for that request, byte-for-byte, regardless of how
// the goroutines interleaved.
//
// Why this catches what a naive test would not: the rejected designs in ADR-002
// (a shared *rand.Rand, or a per-request RNG seeded from a global counter)
// would make the bytes a request draws depend on how many other requests drew
// first — i.e. on scheduling. Under this concurrent load the interleaving
// varies run to run, so such a design would produce draws that differ from the
// single-threaded reference and this test would fail (and the -race detector
// would additionally flag the shared mutable RNG). A happy-path test that
// derives one request on one goroutine sees none of that: it passes even for
// the broken counter-based design, which is precisely the trap ADR-002
// documents. Run under `go test -race -count=10`.
func TestConcurrentDerivationOrderIndependent(t *testing.T) {
	t.Parallel()

	// A handful of DISTINCT requests. Distinctness is the point: different
	// requests must never share a stream, and identical requests must always
	// share one, no matter the concurrency.
	type request struct {
		method string
		rawID  []byte
		body   []byte
	}
	requests := []request{
		{"tools/call", []byte(`"a"`), []byte(`{"x":1}`)},
		{"tools/call", []byte(`"b"`), []byte(`{"x":1}`)}, // same body, different id
		{"tools/list", []byte(`1`), []byte(`{}`)},
		{"server/discover", []byte(`null`), []byte(`{"deep":{"z":[1,true]}}`)},
	}

	const streamLen = 512
	draw := func(r request) []byte {
		k := deriveRequestKey(fixedSeed, fixedInstance, r.method, r.rawID, r.body)
		rng := k.RNG()
		out := make([]byte, streamLen)
		for i := range out {
			out[i] = byte(rng.Uint32())
		}
		return out
	}

	// Single-threaded reference, computed before any goroutine starts.
	want := make([][]byte, len(requests))
	for i, r := range requests {
		want[i] = draw(r)
	}

	// Confirm the references are pairwise distinct, so "all equal to reference"
	// is a meaningful assertion and not trivially true.
	for i := range want {
		for j := i + 1; j < len(want); j++ {
			if bytes.Equal(want[i], want[j]) {
				t.Fatalf("reference streams %d and %d are equal; distinct requests must not share a stream", i, j)
			}
		}
	}

	const goroutines = 64
	const perGoroutine = 50
	var mismatches sync.Map // index -> struct{} on first mismatch, for reporting
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for iter := 0; iter < perGoroutine; iter++ {
				idx := (g + iter) % len(requests)
				got := draw(requests[idx])
				if !bytes.Equal(got, want[idx]) {
					mismatches.Store(idx, struct{}{})
				}
			}
		}(g)
	}
	wg.Wait()

	mismatches.Range(func(key, _ any) bool {
		t.Errorf("request %v produced a scheduling-dependent stream — determinism is broken under concurrency", key)
		return true
	})
}

// TestConcurrentIdenticalRequestsShareStream asserts the positive half of
// ADR-002 rule 1 directly: many goroutines deriving the SAME request
// concurrently all obtain the identical first-256-byte stream. Under a shared
// mutable RNG this would fail because each goroutine would advance a common
// stream position.
func TestConcurrentIdenticalRequestsShareStream(t *testing.T) {
	t.Parallel()

	reference := func() []byte {
		k := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)
		rng := k.RNG()
		out := make([]byte, 256)
		for i := range out {
			out[i] = byte(rng.Uint32())
		}
		return out
	}
	want := reference()

	const goroutines = 128
	results := make([][]byte, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = reference()
		}(i)
	}
	wg.Wait()

	for i, got := range results {
		if !bytes.Equal(got, want) {
			t.Fatalf("goroutine %d derived a different stream for an identical request", i)
		}
	}
}

// TestRepeatedRunByteIdentity asserts full byte-identity across many repeated
// derivations, not a two-value coin flip. It derives the same request key 1000
// times and requires every RNG head and every derived key to be identical. A
// 50%-luck bug (e.g. a stream that alternated) would fail here with
// overwhelming probability.
func TestRepeatedRunByteIdentity(t *testing.T) {
	t.Parallel()

	first := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)
	firstHead := rngHead256(first)

	for run := 0; run < 1000; run++ {
		k := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)
		if k != first {
			t.Fatalf("run %d: derived key differs from run 0", run)
		}
		if head := rngHead256(k); head != firstHead {
			t.Fatalf("run %d: RNG head differs from run 0", run)
		}
	}

	// Confirm the head is not degenerate (an empty head would pass identity but
	// signal a dead RNG).
	if firstHead == "" {
		t.Fatal("empty RNG head")
	}
}
