package determinism

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
)

// stdHMACDomain is the standard-library reference for the internal primitive:
// HMAC-SHA256(key, domain || 0x00 || parts).
func stdHMACDomain(key []byte, domain Domain, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(domain))
	mac.Write([]byte{0x00})
	for _, p := range parts {
		mac.Write(p)
	}
	return mac.Sum(nil)
}

// TestPrimitiveMatchesStdlibAllKeyLengths asserts hmacSHA256Domain equals the
// stdlib across key lengths that a black-box test cannot reach through the
// 32-byte public Key — in particular the RFC 2104 branch for keys longer than
// the 64-byte block, and the empty and block-boundary keys where naive
// reimplementations diverge.
func TestPrimitiveMatchesStdlibAllKeyLengths(t *testing.T) {
	t.Parallel()
	keys := [][]byte{
		{},
		bytes.Repeat([]byte{0x01}, 1),
		bytes.Repeat([]byte{0x02}, 32),
		bytes.Repeat([]byte{0x03}, 63),
		bytes.Repeat([]byte{0x04}, 64),
		bytes.Repeat([]byte{0x05}, 65),
		bytes.Repeat([]byte{0x06}, 200),
	}
	for i, key := range keys {
		got := hmacSHA256Domain(key, DomainRequest, []byte("m"), []byte(`"id"`))
		want := stdHMACDomain(key, DomainRequest, []byte("m"), []byte(`"id"`))
		if !bytes.Equal(got[:], want) {
			t.Errorf("key#%d (len %d): primitive differs from stdlib\n got  %x\n want %x", i, len(key), got[:], want)
		}
	}
}

// TestRootPrimitiveMatchesStdlib pins the single-message Root primitive against
// stdlib HMAC over the fixed label.
func TestRootPrimitiveMatchesStdlib(t *testing.T) {
	t.Parallel()
	key := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	got := hmacSHA256(key, rootLabel)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(rootLabel))
	if want := mac.Sum(nil); !bytes.Equal(got[:], want) {
		t.Errorf("Root primitive differs from stdlib\n got  %x\n want %x", got[:], want)
	}
}

// TestPrimitiveOversizedFallsBackNotPanics is the REV-006 regression: an
// oversized message — for mcpmock, a long client-controlled JSON-RPC id fed in
// verbatim by ADR-002 — must NOT panic (a panic on the stdio worker pool is
// process-fatal and remotely triggerable). Instead it takes the streaming
// crypto/hmac fallback and returns the byte-identical result the fast path would
// have produced for a shorter message of the same construction.
func TestPrimitiveOversizedFallsBackNotPanics(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x07}, 32)
	// A part that overflows the fixed inner buffer many times over.
	huge := bytes.Repeat([]byte{0xAA}, hmacInnerBufLen*8)

	got := func() [sha256.Size]byte {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("oversized derivation input panicked: %v", r)
			}
		}()
		return hmacSHA256Domain(key, DomainRequest, []byte("tools/call"), huge)
	}()

	// The fallback must equal the stdlib reference for the exact same message.
	want := stdHMACDomain(key, DomainRequest, []byte("tools/call"), huge)
	if !bytes.Equal(got[:], want) {
		t.Errorf("oversized fallback differs from stdlib\n got  %x\n want %x", got[:], want)
	}
}

// TestPrimitiveBoundaryMatchesStdlib pins that the exact-fit and just-over-fit
// boundary of the inner buffer both agree with the stdlib, so the fast/fallback
// switch cannot introduce an off-by-one divergence.
func TestPrimitiveBoundaryMatchesStdlib(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x09}, 32)
	// Message length inside the fast path = block(64) + domain + 1 + part.
	base := sha256.BlockSize + len(DomainRequest) + 1
	for _, partLen := range []int{
		hmacInnerBufLen - base - 1, // fits exactly
		hmacInnerBufLen - base,     // fits exactly at the boundary
		hmacInnerBufLen - base + 1, // one over → fallback
		hmacInnerBufLen - base + 8, // clearly over → fallback
	} {
		part := bytes.Repeat([]byte{0x5A}, partLen)
		got := hmacSHA256Domain(key, DomainRequest, part)
		want := stdHMACDomain(key, DomainRequest, part)
		if !bytes.Equal(got[:], want) {
			t.Errorf("partLen=%d: differs from stdlib\n got  %x\n want %x", partLen, got[:], want)
		}
	}
}
