package determinism

// Domain is a stable label that names an edge in the seed tree. Every call to
// [Key.Derive] passes a Domain so that two derivations for different purposes
// from the same parent key can never collide, and so that a value derived for
// one purpose is independent of a value derived for another.
//
// The string values below are load-bearing constants. They are hashed into
// every derived key (ADR-002: HMAC over domain || 0x00 || parts), so changing
// any one of them changes every downstream key, every RNG stream seeded from
// it, and therefore every golden fixture and every recorded seed. ADR-002
// rates this reversibility HARD. A rename is a breaking change that MUST bump
// the scenario apiVersion; see testdata/vectors.json for the committed
// consequence.
type Domain string

// The complete set of derivation domains defined by ADR-002. Phase 1 uses only
// a subset (instance, request, clock, catalog, ordering), but all are
// declared now so that later phases append new leaves rather than insert them
// between existing ones — insertion would shift the draw order and invalidate
// golden files (ADR-002 rule 2).
//
// Each constant's string value is fixed forever. Do not reorder, rename, or
// repurpose them. TestDomainSet asserts the full set and its exact values so an
// accidental edit fails loudly rather than silently corrupting determinism.
const (
	// DomainInstance separates one logical instance's subtree from another's.
	// The instance key is root.Derive(DomainInstance, []byte(instanceName)).
	DomainInstance Domain = "instance"
	// DomainRequest separates one request's subtree from another's within an
	// instance. The request key is content-addressed: it is derived from the
	// method, the raw JSON-RPC id and the SHA-256 of the canonical body, so it
	// depends only on the request bytes and never on arrival order.
	DomainRequest Domain = "request"
	// DomainCatalogue seeds deterministic generation of virtual catalog
	// items (names, shapes) — MOCK-221. Its value is the exact, immutable
	// domain string mandated by ADR-002; changing it to satisfy the US-locale
	// spell checker would change every derived key and break the golden
	// vectors, so the false positive is suppressed rather than "fixed".
	DomainCatalogue Domain = "catalogue"

	// DomainCursor seeds opaque pagination cursor derivation.
	DomainCursor Domain = "cursor"
	// DomainRequestState seeds per-request state that is not part of the body.
	DomainRequestState Domain = "requeststate"
	// DomainEventID seeds SSE event id derivation.
	DomainEventID Domain = "eventid"
	// DomainSessionID seeds session id derivation.
	DomainSessionID Domain = "sessionid"
	// DomainSubscriptionID seeds subscription id derivation.
	DomainSubscriptionID Domain = "subscriptionid"
	// DomainJitter seeds jitter added to fault timing and TTLs.
	DomainJitter Domain = "jitter"
	// DomainShape seeds the JSON-vs-SSE response shape decision — MOCK-208.
	DomainShape Domain = "shape"
	// DomainOrdering seeds the deliberately non-deterministic-from-the-hub's-
	// viewpoint permutation of MOCK-226. It is reproducible for mcpmock while
	// appearing unordered to the system under test (ADR-002 Consequences).
	DomainOrdering Domain = "ordering"
	// NOTE: "credhash" is DELIBERATELY NOT a domain (AMEND-8, 2026-09-04). The
	// journal credential-hash key is per-process crypto/rand, never seed-derived
	// — the seed is published (MOCK-704), so a seed-derived key would make the
	// hash dictionary-attackable from a journal export. It is a named exception
	// to seed-determinism (ADR-002 "Named exceptions", security.md §3/§5). Do
	// NOT re-add DomainCredHash to make a golden stable: credential.hash is a
	// volatile field, normalised out of golden comparison instead.
	//
	// DomainASToken seeds authorization-server token derivation.
	DomainASToken Domain = "astoken"
	// DomainClock seeds the virtual clock — the derived instant that replaces
	// time.Now() in any value serialized into a response body (ADR-002 rule 3).
	DomainClock Domain = "clock"
)

// rootLabel is the fixed message hashed under the seed to produce the root key:
// Root(seed) = HMAC-SHA256(key=seed_be8, message=rootLabel) (ADR-002). Changing
// it is as breaking as changing a domain string.
const rootLabel = "mcpmock/v1/root"

// domainSeparator is the single 0x00 byte placed between the domain and the
// parts in a derivation message (ADR-002: domain || 0x00 || parts...). It
// prevents a domain from running into the first part and creating an ambiguous
// preimage where Derive("ab", "c") and Derive("a", "bc") would otherwise
// collide.
const domainSeparator = 0x00

// AllDomains returns the complete, ordered set of derivation domains defined by
// ADR-002. The order is the declaration order above and is itself part of the
// contract that TestDomainSet pins. Callers that need to enumerate domains
// (for example, a documentation or coverage check) use this rather than a
// hand-maintained second list.
func AllDomains() []Domain {
	return []Domain{
		DomainInstance,
		DomainRequest,
		DomainCatalogue,
		DomainCursor,
		DomainRequestState,
		DomainEventID,
		DomainSessionID,
		DomainSubscriptionID,
		DomainJitter,
		DomainShape,
		DomainOrdering,
		DomainASToken,
		DomainClock,
	}
}
