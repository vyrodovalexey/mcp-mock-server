package determinism_test

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// The fixed inputs used by the golden vectors and by the reproduction helpers
// below. They MUST match testdata/vectors.json exactly; the vectors test would
// fail loudly if they drifted.
const (
	fixedSeed     = uint64(0x0123456789abcdef)
	fixedInstance = "alpha"
	fixedMethod   = "tools/call"
)

var (
	fixedRawID = []byte(`"req-1"`)
	fixedBody  = []byte(`{"b":2,"a":1}`)
)

// deriveRequestKey reproduces the canonical request-key derivation path from
// ADR-002 for the fixed test inputs:
//
//	root        = Root(seed)
//	instanceKey = root.Derive(DomainInstance, instanceName)
//	requestKey  = instanceKey.Derive(DomainRequest, method, rawID, sha256(body))
//
// It is the exact call shape TASK-011/014/016 will use, kept in one place so
// every test exercises the same path a real caller does.
func deriveRequestKey(seed uint64, instance, method string, rawID, body []byte) determinism.Key {
	bodyHash := sha256.Sum256(body)
	root := determinism.Root(seed)
	inst := root.Derive(determinism.DomainInstance, []byte(instance))
	return inst.Derive(determinism.DomainRequest, []byte(method), rawID, bodyHash[:])
}

// rngHead256 draws the first 256 bytes of a key's RNG stream using the same
// low-byte-of-Uint32 method the golden generator used, so the head can be
// compared byte-for-byte against the committed vector.
func rngHead256(k determinism.Key) string {
	r := k.RNG()
	buf := make([]byte, 256)
	for i := range buf {
		buf[i] = byte(r.Uint32())
	}
	return hex.EncodeToString(buf)
}
