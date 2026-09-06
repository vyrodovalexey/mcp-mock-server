package determinism_test

import (
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// TestVirtualClockDeterministic asserts acceptance criterion 3's first half:
// the same request key and epoch yield the same instant, every time.
func TestVirtualClockDeterministic(t *testing.T) {
	t.Parallel()
	k := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)
	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	want := determinism.NewVirtualClock(k, epoch).Now()
	for i := 0; i < 1000; i++ {
		if got := determinism.NewVirtualClock(k, epoch).Now(); !got.Equal(want) {
			t.Fatalf("run %d: virtual clock instant changed: %s != %s", i, got, want)
		}
	}
}

// TestVirtualClockNoRealTimeLeak proves real wall time does not leak into the
// clock (acceptance criterion 3, second half; ADR-002 rule 3). It constructs
// two clocks from the same (key, epoch) with a real sleep in between: if the
// implementation consulted time.Now() the two instants would differ by roughly
// the sleep. It also asserts the instant is derived from the epoch — moving the
// epoch by a known delta moves the instant by exactly that delta — which is
// only possible if the offset is a pure function of the key and not of the
// wall clock.
func TestVirtualClockNoRealTimeLeak(t *testing.T) {
	t.Parallel()
	k := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)
	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	before := determinism.NewVirtualClock(k, epoch).Now()
	time.Sleep(20 * time.Millisecond)
	after := determinism.NewVirtualClock(k, epoch).Now()
	if !before.Equal(after) {
		t.Fatalf("virtual clock advanced across a real sleep — time.Now leaked: %s != %s", before, after)
	}

	// Epoch-linearity: shifting the epoch shifts the instant by the same delta,
	// so the key-derived offset is constant and epoch-relative.
	const delta = 72 * time.Hour
	shifted := determinism.NewVirtualClock(k, epoch.Add(delta)).Now()
	if got := shifted.Sub(before); got != delta {
		t.Fatalf("instant did not track the epoch: shift by %s moved instant by %s", delta, got)
	}
}

// TestVirtualClockKeySensitiveAndBounded asserts two clocks derived from
// different request keys generally differ, and that the derived offset stays
// within the documented bound of the epoch (0 <= offset < 24h). The bound keeps
// emitted timestamps plausibly near the author's epoch.
func TestVirtualClockKeySensitiveAndBounded(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	ka := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, []byte(`"a"`), fixedBody)
	kb := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, []byte(`"b"`), fixedBody)

	ta := determinism.NewVirtualClock(ka, epoch).Now()
	tb := determinism.NewVirtualClock(kb, epoch).Now()
	if ta.Equal(tb) {
		t.Error("two distinct request keys produced the same virtual instant")
	}

	for _, tc := range []struct {
		name string
		inst time.Time
	}{{"a", ta}, {"b", tb}} {
		offset := tc.inst.Sub(epoch)
		if offset < 0 || offset >= 24*time.Hour {
			t.Errorf("clock %s: offset %s is outside [0,24h)", tc.name, offset)
		}
	}
}

// TestVirtualClockNoMonotonicComponent asserts the returned instant carries no
// monotonic clock reading, so a value serialised from it is a pure, reproducible
// wall time. A monotonic component would be process-relative and non-portable.
func TestVirtualClockNoMonotonicComponent(t *testing.T) {
	t.Parallel()
	k := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)
	got := determinism.NewVirtualClock(k, time.Now()).Now()
	if got.Round(0) != got {
		t.Error("virtual clock instant retains a monotonic reading; it must be a pure wall time")
	}
}
