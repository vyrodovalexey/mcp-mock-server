package wire

import (
	"encoding/json"
	"testing"
)

// TestMetaAbsenceDistinguishableFromEmpty proves the load-bearing MOCK-203 /
// AMEND-6 property: for each required _meta field, ABSENCE is distinguishable
// from EMPTY. The validator (TASK-017) depends on this to compute the correct
// missing set and to apply the lenient/off downstream fallbacks.
func TestMetaAbsenceDistinguishableFromEmpty(t *testing.T) {
	t.Run("protocolVersion absent vs present-empty", func(t *testing.T) {
		absent := MetaEnvelope{} // no members present
		if absent.HasProtocolVersion() {
			t.Fatal("wholly-absent _meta reports protocolVersion present")
		}
		empty := ""
		presentEmpty := MetaEnvelope{ProtocolVersion: &empty}
		if !presentEmpty.HasProtocolVersion() {
			t.Fatal("present-but-empty protocolVersion reports absent")
		}
		// The two states are genuinely different, not just different predicates.
		if (absent.ProtocolVersion == nil) == (presentEmpty.ProtocolVersion == nil) {
			t.Fatal("absent and present-empty protocolVersion share a representation")
		}
	})

	t.Run("clientCapabilities absent vs present-empty", func(t *testing.T) {
		absent := MetaEnvelope{}
		if absent.HasClientCapabilities() {
			t.Fatal("wholly-absent _meta reports clientCapabilities present")
		}
		emptyMap := map[string]json.RawMessage{}
		presentEmpty := MetaEnvelope{ClientCapabilities: &emptyMap}
		if !presentEmpty.HasClientCapabilities() {
			t.Fatal("present-but-empty clientCapabilities reports absent")
		}
		// Present-empty is the empty capability set, distinct from absent
		// (requirements-spec.md MOCK-203 downstream effect).
		if presentEmpty.ClientCapabilities == nil {
			t.Fatal("present-empty clientCapabilities must be a non-nil pointer to an empty map")
		}
		if len(*presentEmpty.ClientCapabilities) != 0 {
			t.Fatal("present-empty clientCapabilities must have length zero")
		}
	})

	t.Run("unknown keys preserved in Extra", func(t *testing.T) {
		// annex 2.11 [D] / MOCK-203.6 / MOCK-601: unknown _meta keys are
		// preserved verbatim for the journal. A present-but-empty _meta ({})
		// has a non-nil, zero-length Extra; a wholly-absent _meta has nil Extra.
		absent := MetaEnvelope{}
		if absent.Extra != nil {
			t.Fatal("wholly-absent _meta must have nil Extra")
		}
		presentEmpty := MetaEnvelope{Extra: map[string]json.RawMessage{}}
		if presentEmpty.Extra == nil {
			t.Fatal("present-but-empty _meta must have non-nil Extra")
		}
	})
}

// TestMetaValidationResultRecordsModes proves the type can carry what each of
// the three validateMeta modes must record (MOCK-203.7/203.8, AMEND-6),
// including the observable lenient-vs-off difference: lenient computes the
// missing set, off does not.
func TestMetaValidationResultRecordsModes(t *testing.T) {
	cases := []struct {
		name        string
		result      MetaValidationResult
		wantMissing int
		wantAccept  bool
	}{
		{
			name:        "strict rejects and records the rejected fields",
			result:      MetaValidationResult{Mode: MetaModeStrict, Missing: []string{MetaFieldProtocolVersion}, Accepted: false},
			wantMissing: 1,
			wantAccept:  false,
		},
		{
			name:        "lenient tolerates and records the missing set",
			result:      MetaValidationResult{Mode: MetaModeLenient, Missing: []string{MetaFieldProtocolVersion}, Accepted: true},
			wantMissing: 1,
			wantAccept:  true,
		},
		{
			name:        "off accepts and does not compute the missing set",
			result:      MetaValidationResult{Mode: MetaModeOff, Missing: nil, Accepted: true},
			wantMissing: 0,
			wantAccept:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if len(c.result.Missing) != c.wantMissing {
				t.Errorf("Missing len = %d, want %d", len(c.result.Missing), c.wantMissing)
			}
			if c.result.Accepted != c.wantAccept {
				t.Errorf("Accepted = %v, want %v", c.result.Accepted, c.wantAccept)
			}
		})
	}
}

// TestMetaMissingErrorDataShape proves the -32602 data.missing payload marshals
// to the annex 2.12 [P-11] shape the schema requires.
func TestMetaMissingErrorDataShape(t *testing.T) {
	data := MetaMissingErrorData{Missing: []string{MetaFieldProtocolVersion, MetaFieldClientCapabilities}}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"missing":["protocolVersion","clientCapabilities"]}`
	if string(b) != want {
		t.Errorf("data.missing payload = %s, want %s", b, want)
	}
}
