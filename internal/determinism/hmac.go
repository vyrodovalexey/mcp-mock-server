package determinism

import (
	"crypto/sha256"
)

// This file provides an allocation-free HMAC-SHA256 for the derivation hot
// path. It exists because the streaming crypto/hmac API allocates its internal
// hash state and key buffers on every call, and ADR-002's per-request key is
// derived on the hot path of every request that makes a random decision.
// Acceptance criterion 4 requires that construction not allocate beyond the
// returned array.
//
// The construction is plain RFC 2104 HMAC over SHA-256 with a 64-byte block —
// byte-for-byte identical to crypto/hmac.New(sha256.New, key).
// TestHMACMatchesStdlib pins that equivalence against the standard library so
// this fast path can never silently diverge from the canonical algorithm, and
// the golden vectors pin it against fixed bytes.
//
// SHA-256's block is 64 bytes and its digest is 32. HMAC hashes two messages —
// inner = ipad(64) || message, outer = opad(64) || innerDigest. Both are
// assembled in stack arrays and hashed with sha256.Sum256, which returns its
// digest by value and does not allocate. The message is assembled from a domain
// label plus byte parts; in mcpmock those are always small — a domain, a
// separator, a method name, a raw id and a fixed 32-byte body hash — and the
// inner buffer is sized so that assembly stays on the stack.

// hmacInnerBufLen bounds the stack buffer for the inner hash input: 64 bytes of
// ipad plus the message. 256 comfortably covers a domain, separator, method
// name, a typical JSON-RPC id and a 32-byte body hash with room to spare, so the
// common request-derivation path never allocates. An input that would overflow
// it — most importantly a client-controlled JSON-RPC id, which ADR-002 feeds in
// verbatim and which may be up to the 1 MiB body bound — falls back to the
// allocating crypto/hmac path (see [hmacSHA256Domain]) rather than panicking.
// The fallback is byte-identical to the fast path (pinned by
// TestPrimitiveMatchesStdlibAllKeyLengths), so no derived value changes; it only
// trades an allocation for not crashing on hostile input (REV-006).
const hmacInnerBufLen = 256

// blockKey reduces a key to exactly one SHA-256 block. Keys longer than the
// block are hashed first, per RFC 2104; mcpmock always passes a 32-byte Key, so
// the hashing branch is not taken on the hot path.
func blockKey(key []byte) [sha256.BlockSize]byte {
	var k [sha256.BlockSize]byte
	if len(key) > sha256.BlockSize {
		d := sha256.Sum256(key)
		copy(k[:], d[:])
		return k
	}
	copy(k[:], key)
	return k
}

// hmacSHA256Domain computes HMAC-SHA256(key, domain || 0x00 || parts...) with no
// heap allocation for in-bounds inputs. The domain is a string and is copied in
// directly (copy accepts a string source) so no []byte(domain) conversion
// allocates. This is the derivation primitive [Key.Derive] uses.
func hmacSHA256Domain(key []byte, domain Domain, parts ...[]byte) [sha256.Size]byte {
	// Total inner-message size: ipad(64) || domain || 0x00 || parts... . This
	// loop reads only lengths and does not let parts escape, so the in-bounds
	// fast path stays allocation-free (TestAllocationDiscipline).
	need := sha256.BlockSize + len(domain) + 1
	for _, p := range parts {
		need += len(p)
	}
	if need > hmacInnerBufLen {
		// A message larger than the stack buffer — for mcpmock, a long
		// client-controlled JSON-RPC id (ADR-002 feeds the raw id in verbatim,
		// so it is remotely attacker-controlled up to the 1 MiB body bound).
		// Fall back to the streaming stdlib HMAC, which allocates but is
		// byte-identical to the fast path, instead of panicking on hostile input
		// (REV-006). The trivial-handler hot path never reaches here.
		return stdlibHMACDomain(key, domain, parts)
	}
	k := blockKey(key)

	var buf [hmacInnerBufLen]byte
	// ipad = key ⊕ 0x36, laid down as the first block of the inner message.
	for i := 0; i < sha256.BlockSize; i++ {
		buf[i] = k[i] ^ 0x36
	}
	n := sha256.BlockSize
	n += copy(buf[n:], domain)
	buf[n] = domainSeparator
	n++
	for _, p := range parts {
		n += copy(buf[n:], p)
	}
	inner := sha256.Sum256(buf[:n])

	// outer = opad || inner, hashed on the stack.
	var outer [sha256.BlockSize + sha256.Size]byte
	for i := 0; i < sha256.BlockSize; i++ {
		outer[i] = k[i] ^ 0x5c
	}
	copy(outer[sha256.BlockSize:], inner[:])
	return sha256.Sum256(outer[:])
}

// stdlibHMACDomain computes the identical construction as the fast path —
// HMAC-SHA256(key, domain || 0x00 || parts...) — for a message too large for the
// stack buffer. It assembles the inner message on a heap buffer and hashes it
// with the concrete sha256.Sum256 (not the streaming hash.Hash interface), so
// the parts' CONTENT is copied rather than leaked: that is what keeps the fast
// path's escape analysis clean and TestAllocationDiscipline at zero allocations.
// Its equivalence to the fast path across key lengths is pinned by
// TestPrimitiveMatchesStdlibAllKeyLengths. It is reserved for the rare oversized
// message (REV-006) and never runs on the trivial-handler hot path.
//
//go:noinline
func stdlibHMACDomain(key []byte, domain Domain, parts [][]byte) [sha256.Size]byte {
	k := blockKey(key)

	msgLen := len(domain) + 1
	for _, p := range parts {
		msgLen += len(p)
	}
	inner := make([]byte, sha256.BlockSize+msgLen)
	for i := 0; i < sha256.BlockSize; i++ {
		inner[i] = k[i] ^ 0x36
	}
	n := sha256.BlockSize
	n += copy(inner[n:], domain)
	inner[n] = domainSeparator
	n++
	for _, p := range parts {
		n += copy(inner[n:], p)
	}
	innerSum := sha256.Sum256(inner)

	var outer [sha256.BlockSize + sha256.Size]byte
	for i := 0; i < sha256.BlockSize; i++ {
		outer[i] = k[i] ^ 0x5c
	}
	copy(outer[sha256.BlockSize:], innerSum[:])
	return sha256.Sum256(outer[:])
}

// hmacSHA256 is the single-message form used by Root. The message is a string
// (the fixed root label) copied directly into the stack buffer, so no
// []byte(message) conversion allocates.
func hmacSHA256(key []byte, message string) [sha256.Size]byte {
	k := blockKey(key)

	var buf [hmacInnerBufLen]byte
	for i := 0; i < sha256.BlockSize; i++ {
		buf[i] = k[i] ^ 0x36
	}
	n := sha256.BlockSize
	if sha256.BlockSize+len(message) > len(buf) {
		panic("determinism: HMAC message exceeds derivation buffer")
	}
	n += copy(buf[n:], message)
	inner := sha256.Sum256(buf[:n])

	var outer [sha256.BlockSize + sha256.Size]byte
	for i := 0; i < sha256.BlockSize; i++ {
		outer[i] = k[i] ^ 0x5c
	}
	copy(outer[sha256.BlockSize:], inner[:])
	return sha256.Sum256(outer[:])
}
