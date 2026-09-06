package determinism_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// stdDerive is the reference for the public Derive surface: RFC 2104
// HMAC-SHA256 via the standard library over domain || 0x00 || parts, exactly as
// the derivation would be written the obvious (allocating) way.
func stdDerive(key [32]byte, domain determinism.Domain, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(domain))
	mac.Write([]byte{0x00})
	for _, p := range parts {
		mac.Write(p)
	}
	return mac.Sum(nil)
}

// TestDeriveMatchesStdlibHMAC asserts the allocation-free HMAC behind
// [Key.Derive] is byte-for-byte identical to crypto/hmac.New(sha256.New, ...)
// for the 32-byte keys the public API uses, across a range of domains and part
// shapes. This is what makes the hand-rolled fast path safe: it cannot silently
// drift from the canonical algorithm without this test failing. (Non-32-byte
// key handling — the RFC 2104 key-shortening branch — is covered white-box in
// hmac_internal_test.go, since the public Key is always 32 bytes.)
func TestDeriveMatchesStdlibHMAC(t *testing.T) {
	t.Parallel()

	keys := [][32]byte{
		{}, // all zero
		bytesTo32(bytes.Repeat([]byte{0xff}, 32)),
		bytesTo32([]byte("0123456789abcdef0123456789abcdef")),
	}
	domains := []determinism.Domain{
		determinism.DomainRequest,
		determinism.DomainInstance,
		determinism.Domain(""),
		determinism.Domain("a"),
	}
	partSets := [][][]byte{
		nil,
		{{}},
		{[]byte("tools/call")},
		{[]byte("tools/call"), []byte(`"id-1"`), bytes.Repeat([]byte{0xab}, 32)},
		{bytes.Repeat([]byte{0x01}, 100)},
	}

	for ki, key := range keys {
		for _, domain := range domains {
			for pi, parts := range partSets {
				want := stdDerive(key, domain, parts...)
				child := determinism.Key(key).Derive(domain, parts...)
				if !bytes.Equal(child[:], want) {
					t.Errorf("key#%d domain=%q parts#%d: Derive differs from stdlib HMAC\n got  %x\n want %x",
						ki, string(domain), pi, child[:], want)
				}
			}
		}
	}
}

func bytesTo32(b []byte) [32]byte {
	var k [32]byte
	copy(k[:], b)
	return k
}
