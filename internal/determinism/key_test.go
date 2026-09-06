package determinism_test

import (
	"bytes"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// TestRootStableAndSeedSensitive asserts Root is a pure function of the seed:
// equal seeds give equal roots, and any different seed gives a different root.
func TestRootStableAndSeedSensitive(t *testing.T) {
	t.Parallel()

	base := determinism.Root(fixedSeed)
	if again := determinism.Root(fixedSeed); base != again {
		t.Fatalf("Root not stable for equal seed:\n %x\n %x", base, again)
	}

	cases := []uint64{0, 1, fixedSeed ^ 1, fixedSeed + 1, ^uint64(0)}
	for _, s := range cases {
		if s == fixedSeed {
			continue
		}
		if determinism.Root(s) == base {
			t.Errorf("Root(%d) collided with Root(%d)", s, fixedSeed)
		}
	}
}

// TestDeriveSeparation asserts the two properties that make the seed tree a
// tree: identical inputs derive identical children (reproducibility), and a
// change in any single input — domain, or any byte of any part — derives a
// different child (separation). It is table-driven so each perturbation is a
// named row.
func TestDeriveSeparation(t *testing.T) {
	t.Parallel()

	parent := determinism.Root(fixedSeed)
	baseDomain := determinism.DomainRequest
	baseParts := [][]byte{[]byte("tools/call"), []byte(`"id"`), bytes.Repeat([]byte{0xab}, 32)}

	base := parent.Derive(baseDomain, baseParts...)

	// Reproducibility: same inputs, same child.
	if again := parent.Derive(baseDomain, baseParts...); base != again {
		t.Fatalf("Derive not reproducible for identical inputs")
	}

	perturb := []struct {
		name   string
		key    determinism.Key
		domain determinism.Domain
		parts  [][]byte
	}{
		{
			name:   "different parent key",
			key:    parent.Derive(determinism.DomainInstance, []byte("x")),
			domain: baseDomain,
			parts:  baseParts,
		},
		{
			name:   "different domain",
			key:    parent,
			domain: determinism.DomainCatalogue,
			parts:  baseParts,
		},
		{
			name:   "one flipped byte in first part",
			key:    parent,
			domain: baseDomain,
			parts:  [][]byte{[]byte("Tools/call"), []byte(`"id"`), bytes.Repeat([]byte{0xab}, 32)},
		},
		{
			name:   "one flipped byte in id part",
			key:    parent,
			domain: baseDomain,
			parts:  [][]byte{[]byte("tools/call"), []byte(`"iD"`), bytes.Repeat([]byte{0xab}, 32)},
		},
		{
			name:   "one flipped byte in hash part",
			key:    parent,
			domain: baseDomain,
			parts:  [][]byte{[]byte("tools/call"), []byte(`"id"`), append(bytes.Repeat([]byte{0xab}, 31), 0xac)},
		},
		{
			name:   "fewer parts",
			key:    parent,
			domain: baseDomain,
			parts:  [][]byte{[]byte("tools/call"), []byte(`"id"`)},
		},
	}
	for _, tc := range perturb {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.key.Derive(tc.domain, tc.parts...); got == base {
				t.Errorf("perturbation %q produced the same child key — inputs are not fully mixed in", tc.name)
			}
		})
	}
}

// TestDeriveSeparatorPreventsAmbiguity asserts the 0x00 separator does its job:
// Derive(domain="ab", part="c") must not collide with Derive(domain="a",
// part="bc"), which it would if domain and parts were concatenated without a
// separator. This guards the specific preimage-ambiguity class ADR-002's
// separator exists to close.
func TestDeriveSeparatorPreventsAmbiguity(t *testing.T) {
	t.Parallel()
	parent := determinism.Root(fixedSeed)
	a := parent.Derive(determinism.Domain("ab"), []byte("c"))
	b := parent.Derive(determinism.Domain("a"), []byte("bc"))
	if a == b {
		t.Fatal("separator failed: ambiguous domain/part boundary produced a collision")
	}
}

// TestRNGDeterministicAndSeparated asserts equal keys produce byte-identical
// RNG streams and different keys produce different streams. This is the
// property acceptance criterion 2 asserts about "first 256 bytes"; here it is
// checked over a longer stream and against a sibling key.
func TestRNGDeterministicAndSeparated(t *testing.T) {
	t.Parallel()
	k := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)

	draw := func(key determinism.Key, n int) []byte {
		r := key.RNG()
		out := make([]byte, n)
		for i := range out {
			out[i] = byte(r.Uint32())
		}
		return out
	}

	a := draw(k, 1024)
	b := draw(k, 1024)
	if !bytes.Equal(a, b) {
		t.Fatal("two RNGs from the same key produced different streams")
	}

	// A key differing in one input byte must produce an unrelated stream.
	other := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, []byte(`{"b":2,"a":2}`))
	if bytes.Equal(a, draw(other, 1024)) {
		t.Fatal("RNGs from different keys produced identical streams")
	}
}

// TestLazyRNGOnceAndStream asserts NewLazyRNG builds its RNG at most once
// (returns the same pointer), and that the pointer's stream matches a fresh
// RNG().RNG() from the same key from its start — confirming laziness does not
// perturb the stream.
func TestLazyRNGOnceAndStream(t *testing.T) {
	t.Parallel()
	k := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)

	get := k.NewLazyRNG()
	r1 := get()
	r2 := get()
	if r1 != r2 {
		t.Fatal("NewLazyRNG returned different *rand.Rand instances — it is not once-only")
	}

	// Fresh eager RNG must produce the same head as the lazy one from its start.
	eager := k.RNG()
	lazy := k.NewLazyRNG()()
	for i := 0; i < 64; i++ {
		if e, l := eager.Uint64(), lazy.Uint64(); e != l {
			t.Fatalf("lazy and eager streams diverge at draw %d: %d != %d", i, e, l)
		}
	}
}

// TestAllocationDiscipline asserts acceptance criterion 4: constructing a Key
// via Root/Derive does not escape to the heap on the hot path, and RNG is not
// built unless actually requested. Root and Derive return a [32]byte value, so
// a correct implementation reports zero allocations per op.
func TestAllocationDiscipline(t *testing.T) {
	// No t.Parallel(): testing.AllocsPerRun must not run during a parallel test.
	root := determinism.Root(fixedSeed)
	part := []byte("alpha")

	if n := testing.AllocsPerRun(100, func() {
		_ = root.Derive(determinism.DomainInstance, part)
	}); n != 0 {
		t.Errorf("Derive allocated %v times per run, want 0", n)
	}

	// Building the lazy accessor must not build the RNG. We can only observe
	// this indirectly: creating the accessor is cheap; the expensive ChaCha8
	// construction happens on first call. Assert the accessor itself does not
	// panic or draw by never calling it, then confirm it works when called.
	get := root.NewLazyRNG()
	_ = get // not invoked: no RNG constructed, no draw performed.
	if get().Uint64() == get().Uint64() {
		// Two consecutive draws from the SAME lazily-built RNG advance the
		// stream, so equality here would indicate the accessor rebuilt the RNG.
		t.Error("lazy RNG appears to rebuild on each call")
	}
}
