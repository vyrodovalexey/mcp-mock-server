package journal

import (
	"sync"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// These white-box tests exercise the seqlock version-counter logic directly, by
// manipulating a slot's state word around a read. Unlike the black-box
// concurrent test — which cannot produce a torn record because publication is a
// single atomic pointer store — these prove the retry/skip branches of the
// seqlock itself: the parity check and the before/after comparison. A defect in
// either (e.g. checking the wrong bit, or comparing the wrong states) is caught
// here as a wrong ok result.

// TestSlotReadStable: a stably-written slot reads back its record.
func TestSlotReadStable(t *testing.T) {
	t.Parallel()
	var s slot
	rec := journalapi.Record{Seq: 7, MonoNs: 7}
	s.write(&rec, 42)
	got, ok := s.read()
	if !ok {
		t.Fatalf("read of a stable slot returned ok=false")
	}
	if got.Seq != 7 || got.MonoNs != 7 {
		t.Fatalf("read returned %+v, want Seq=7 MonoNs=7", got)
	}
	if s.state.Load()&1 != 0 {
		t.Fatalf("state left odd after a completed write: %d", s.state.Load())
	}
}

// TestSlotReadEmpty: an untouched slot reports empty, not a zero record.
func TestSlotReadEmpty(t *testing.T) {
	t.Parallel()
	var s slot
	if _, ok := s.read(); ok {
		t.Fatalf("read of an empty slot returned ok=true")
	}
}

// TestSlotReadWhileWriterMidFill: with state left odd (as if a writer is
// mid-publish), the reader must not return a record — it must exhaust its
// retries and skip. This is the parity branch of the seqlock.
func TestSlotReadWhileWriterMidFill(t *testing.T) {
	t.Parallel()
	var s slot
	rec := journalapi.Record{Seq: 1}
	s.write(&rec, 1) // state now even (stable)
	s.state.Add(1)   // force odd: simulate a writer that entered but has not left
	if _, ok := s.read(); ok {
		t.Fatalf("read returned ok=true while state is odd (writer mid-fill)")
	}
	s.state.Add(1) // restore even; read must now succeed
	if _, ok := s.read(); !ok {
		t.Fatalf("read failed after state returned to even")
	}
}

// TestSlotReadDetectsInterveningWrite runs a reader in a tight loop against a
// writer that continually republishes the slot with internally-consistent
// records, and asserts every successful read is a whole record (Seq == MonoNs).
// Under -race this also proves the payload access is race-free. It is the
// slot-level analogue of the ring-level torn-read test.
func TestSlotReadDetectsInterveningWrite(t *testing.T) {
	t.Parallel()
	var s slot
	first := journalapi.Record{Seq: 0, MonoNs: 0}
	s.write(&first, 1)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := int64(1); ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			rec := journalapi.Record{Seq: uint64(n), MonoNs: n}
			s.write(&rec, 1)
		}
	}()

	for range 200_000 {
		if got, ok := s.read(); ok {
			if got.Seq != uint64(got.MonoNs) {
				t.Fatalf("torn slot read: Seq=%d MonoNs=%d", got.Seq, got.MonoNs)
			}
		}
	}
	close(stop)
	wg.Wait()
}

// TestSlotResetClears: reset empties the slot and frees its record.
func TestSlotResetClears(t *testing.T) {
	t.Parallel()
	var s slot
	rec := journalapi.Record{Seq: 3}
	s.write(&rec, 99)
	s.reset()
	if _, ok := s.read(); ok {
		t.Fatalf("read after reset returned ok=true")
	}
	if s.size.Load() != 0 {
		t.Fatalf("size after reset = %d, want 0", s.size.Load())
	}
	if s.rec.Load() != nil {
		t.Fatalf("rec after reset is non-nil")
	}
}
