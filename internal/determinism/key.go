package determinism

import (
	"encoding/binary"
	"math/rand/v2"
	"sync"
)

// Key is a 32-byte node in the determinism seed tree. It is both an HMAC key
// (when deriving a child) and a ChaCha8 seed (when producing an RNG). It is a
// value type: copying a Key copies the seed material, and a Key carries no
// mutable state, so it is safe to share a Key across goroutines and to store it
// in an immutable snapshot (ADR-014). There are no process globals here
// (ADR-007) — every Key is reachable only from an explicit Root(seed).
type Key [32]byte

// Root returns the root of the seed tree for a given --seed value. It is
// HMAC-SHA256 keyed by the 8 big-endian bytes of seed over the fixed label
// "mcpmock/v1/root" (ADR-002). Big-endian is chosen so the derivation is
// independent of the host byte order and therefore reproducible across
// architectures.
//
// The returned Key is a pure function of seed: Root(s) always equals Root(s),
// on every process and every GOMAXPROCS setting. This is the anchor of the
// whole determinism guarantee.
func Root(seed uint64) Key {
	var seedBE [8]byte
	binary.BigEndian.PutUint64(seedBE[:], seed)
	// rootLabel is copied into a stack buffer inside hmacSHA256 without a
	// []byte(rootLabel) heap conversion, keeping Root allocation-free.
	return Key(hmacSHA256(seedBE[:], rootLabel))
}

// Derive returns the child key for a domain and an ordered list of byte parts.
// It is HMAC-SHA256 keyed by the parent key k over the message
// domain || 0x00 || parts[0] || parts[1] || ... (ADR-002). The 0x00 separator
// after the domain prevents preimage ambiguity between the domain and the first
// part; the parts themselves are concatenated in order, so callers that need
// unambiguous framing between variable-length parts must length-prefix or hash
// them before passing them (the request derivation does this by passing a fixed
// 32-byte sha256(canonicalBody) as its final part — see the package example).
//
// Derive is a pure function of (k, domain, parts): identical inputs yield an
// identical child on every process, and any single differing input byte yields
// a different child with overwhelming probability. It performs no I/O, consults
// no clock, and holds no lock.
func (k Key) Derive(domain Domain, parts ...[]byte) Key {
	return Key(hmacSHA256Domain(k[:], domain, parts...))
}

// RNG returns a new deterministic pseudo-random source seeded from this key,
// backed by ChaCha8 from math/rand/v2 (ADR-002). Two Keys that are equal
// produce RNGs with byte-identical output streams; two Keys that differ produce
// independent streams.
//
// Each call returns a fresh, independent *rand.Rand positioned at the start of
// the stream — RNG is not memoised, because a caller that needs the same stream
// twice must derive from the same Key twice, and sharing a single mutable
// *rand.Rand across calls would reintroduce exactly the order-dependence
// ADR-002 exists to forbid. The returned *rand.Rand is owned by its single
// caller and is not safe for concurrent use; do not share one across
// goroutines.
//
// RNG is constructed eagerly here; laziness (paying nothing when no random
// decision is made, per ADR-002 option 4 and MOCK-901) is provided separately
// by [Key.NewLazyRNG], which a request pipeline uses to defer this cost until a
// stage actually draws.
func (k Key) RNG() *rand.Rand {
	seed := [32]byte(k)
	// gosec G404: a non-crypto PRNG is intentional here. ADR-002 mandates a
	// reproducible ChaCha8 stream; cryptographic unpredictability is explicitly
	// not the goal — determinism is.
	return rand.New(rand.NewChaCha8(seed)) //nolint:gosec // deterministic PRNG is the requirement (ADR-002)
}

// NewLazyRNG returns a function that builds this key's RNG at most once, on
// first call, and returns the same *rand.Rand on every subsequent call. It is
// the per-request accessor ADR-002 option 4 describes: the engine hands a lazy
// accessor to every stage, and a request that makes no random decision
// (MOCK-901's trivial-handler path) never pays for RNG construction or body
// entropy expansion.
//
// The returned function is safe to call concurrently — construction happens
// exactly once under the hood (sync.OnceValue). The *rand.Rand it returns is,
// like any RNG from [Key.RNG], NOT safe for concurrent draws: a request
// pipeline draws from it in a single, fixed order on one goroutine (ADR-002
// rule 2), so the accessor may be shared but the draws must not race.
func (k Key) NewLazyRNG() func() *rand.Rand {
	return sync.OnceValue(k.RNG)
}
