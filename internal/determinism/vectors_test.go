package determinism_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// vectorsFile mirrors testdata/vectors.json. It is the committed determinism
// contract: any change to a derivation constant changes these bytes and is a
// breaking change requiring a scenario apiVersion bump (ADR-002, reversibility
// HARD).
type vectorsFile struct {
	Note        string `json:"_note"`
	Seed        uint64 `json:"seed"`
	Instance    string `json:"instance"`
	Method      string `json:"method"`
	RawID       string `json:"rawId"`
	BodyForHash string `json:"bodyForHash"`
	Keys        []struct {
		Desc string `json:"desc"`
		Key  string `json:"key"`
	} `json:"keys"`
	RNG []struct {
		Desc    string `json:"desc"`
		Head256 string `json:"head256"`
	} `json:"rng"`
	Clock []struct {
		Desc    string `json:"desc"`
		EpochNs int64  `json:"epochNs"`
		RFC3339 string `json:"rfc3339"`
	} `json:"clock"`
}

func loadVectors(t *testing.T) vectorsFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectorsFile
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	return v
}

// hexKey renders a Key as lowercase hex for comparison against the vectors.
func hexKey(k determinism.Key) string { return hex.EncodeToString(k[:]) }

// TestGoldenVectors asserts that Root, Derive, RNG and VirtualClock reproduce
// the committed golden file exactly. This is acceptance criterion 1: a fixed
// seed and a fixed set of derivation paths must match byte-for-byte. Because
// the outputs are pure functions of their inputs, matching here also
// establishes cross-process and cross-GOMAXPROCS stability (further pinned by
// the separate cross-process test), which the vectors' _note records as a
// breaking-change tripwire.
func TestGoldenVectors(t *testing.T) {
	t.Parallel()
	v := loadVectors(t)

	// Guard: the fixed inputs in helpers_test.go must match the vector file, or
	// the reproduced paths below would silently diverge from what was pinned.
	if v.Seed != fixedSeed || v.Instance != fixedInstance || v.Method != fixedMethod {
		t.Fatalf("vector inputs drifted from test constants: %+v", v)
	}
	if v.RawID != string(fixedRawID) || v.BodyForHash != string(fixedBody) {
		t.Fatalf("vector id/body drifted: rawId=%q body=%q", v.RawID, v.BodyForHash)
	}

	bodyHash := sha256.Sum256(fixedBody)
	root := determinism.Root(fixedSeed)
	inst := root.Derive(determinism.DomainInstance, []byte(fixedInstance))
	req := inst.Derive(determinism.DomainRequest, []byte(fixedMethod), fixedRawID, bodyHash[:])
	catalogue := inst.Derive(determinism.DomainCatalogue, []byte("item-0"))
	ordering := req.Derive(determinism.DomainOrdering, []byte{0, 0, 0, 1})
	clockKey := req.Derive(determinism.DomainClock)

	got := map[string]string{
		"root = Root(seed)": hexKey(root),
		"instanceKey = root.Derive(instance, \"alpha\")":                        hexKey(inst),
		"requestKey = instanceKey.Derive(request, method, rawId, sha256(body))": hexKey(req),
		"catalogueKey = instanceKey.Derive(catalogue, \"item-0\")":              hexKey(catalogue),
		"orderingKey = requestKey.Derive(ordering, be32(1))":                    hexKey(ordering),
		"clockKey = requestKey.Derive(clock)":                                   hexKey(clockKey),
	}
	for _, kv := range v.Keys {
		want := kv.Key
		g, ok := got[kv.Desc]
		if !ok {
			t.Fatalf("vector describes an unknown key path %q; test and vectors disagree", kv.Desc)
		}
		if g != want {
			t.Errorf("key %q:\n got  %s\n want %s\n(a mismatch means a derivation constant changed — this is a BREAKING change)", kv.Desc, g, want)
		}
	}

	// RNG head.
	if len(v.RNG) != 1 {
		t.Fatalf("expected exactly one RNG vector, got %d", len(v.RNG))
	}
	if gotHead := rngHead256(req); gotHead != v.RNG[0].Head256 {
		t.Errorf("RNG head256:\n got  %s\n want %s", gotHead, v.RNG[0].Head256)
	}

	// VirtualClock.
	for _, cv := range v.Clock {
		epoch := time.Unix(0, cv.EpochNs).UTC()
		vc := determinism.NewVirtualClock(req, epoch)
		if gotRFC := vc.Now().UTC().Format(time.RFC3339Nano); gotRFC != cv.RFC3339 {
			t.Errorf("clock %q:\n got  %s\n want %s", cv.Desc, gotRFC, cv.RFC3339)
		}
	}
}
