package determinism_test

import (
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// TestDomainSet pins the full set of derivation domains and their exact string
// values (acceptance criterion 5). A later accidental rename, reorder, or
// removal fails here loudly rather than silently invalidating every golden
// fixture and recorded seed. The expected values are written out literally —
// deliberately NOT referencing the constants — so that a change to a constant
// is caught by a divergence from this table, not masked by it.
func TestDomainSet(t *testing.T) {
	t.Parallel()

	// The complete, ordered contract. Order matters because AllDomains's order
	// is itself part of the pinned surface.
	want := []struct {
		got   determinism.Domain
		value string
	}{
		{determinism.DomainInstance, "instance"},
		{determinism.DomainRequest, "request"},
		{determinism.DomainCatalogue, "catalogue"},
		{determinism.DomainCursor, "cursor"},
		{determinism.DomainRequestState, "requeststate"},
		{determinism.DomainEventID, "eventid"},
		{determinism.DomainSessionID, "sessionid"},
		{determinism.DomainSubscriptionID, "subscriptionid"},
		{determinism.DomainJitter, "jitter"},
		{determinism.DomainShape, "shape"},
		{determinism.DomainOrdering, "ordering"},
		// "credhash" is DELIBERATELY absent (AMEND-8): the credential-hash key is
		// per-process crypto/rand, not seed-derived, so it is not a domain in the
		// seed tree (ADR-002 named exception). Re-adding it here would reintroduce
		// the dictionary-attack vulnerability the exception exists to close.
		{determinism.DomainASToken, "astoken"},
		{determinism.DomainClock, "clock"},
	}

	for i, w := range want {
		if string(w.got) != w.value {
			t.Errorf("domain %d: got %q, want %q (a domain rename is a BREAKING change)", i, string(w.got), w.value)
		}
	}

	all := determinism.AllDomains()
	if len(all) != len(want) {
		t.Fatalf("AllDomains length = %d, want %d — a domain was added or removed without updating the pinned set", len(all), len(want))
	}
	for i, w := range want {
		if all[i] != w.got {
			t.Errorf("AllDomains[%d] = %q, want %q — order is part of the contract", i, string(all[i]), string(w.got))
		}
	}

	// No two domains may share a value: a collision would let two distinct
	// purposes derive the same key and silently entangle them.
	seen := make(map[determinism.Domain]int, len(all))
	for i, d := range all {
		if j, dup := seen[d]; dup {
			t.Errorf("duplicate domain value %q at positions %d and %d", string(d), j, i)
		}
		seen[d] = i
	}
}
