package instance

import (
	"sort"

	"github.com/vyrodovalexey/mcp-mock-server/internal/catalogue"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// eraModern is the only protocol era Phase 1 resolves (MOCK-303 modern-only).
// It is a metric/era label recorded at pipeline stage 2, not a wire literal, so
// it is not subject to ADR-019 wire containment. Later phases add legacy/dual/
// probe eras by widening the snapshot's era resolution, not by editing readers.
const eraModern = "modern"

// Snapshot is one instance's deeply-immutable configuration at a point in time
// (ADR-014). It is published behind [Instance]'s atomic.Pointer and is NEVER
// mutated after publication: a change allocates a fresh Snapshot via [clone],
// edits the clone, bumps [Snapshot.Gen] and swaps the pointer. A reader holding
// a *Snapshot therefore observes a consistent view for as long as it holds the
// pointer, even across a concurrent mutation — the property the whole
// copy-on-write scheme exists to provide.
//
// It satisfies engine.Snapshot (the consumer-defined interface the pipeline
// reads through), so the engine need not import this package. Every exported
// method is a pure read of an immutable field and is safe for unbounded
// concurrent use.
//
// The catalog and validator it carries are themselves immutable values: the
// [catalog.Catalog] is a pure function of the instance key and its config
// (ADR-004), and the [engine.MetaValidator] is stateless. A clone shares those
// immutable references with its parent; only fields a mutation actually changes
// are replaced, and any slice or map a future mutation edits is replaced whole,
// never edited in place (ADR-014 rule 2).
type Snapshot struct {
	// gen is the generation, monotonically increasing on every mutation and
	// journalled on every record (ADR-014), so a test can partition its journal
	// by generation and know which requests saw which configuration.
	gen uint64
	// era is the resolved protocol era ("modern" in Phase 1), read at stage 2.
	era string
	// methods carries per-method enable/disable switches (MOCK-202.2). A method
	// absent from the map is enabled by default; a present entry with a non-nil
	// Enabled pointer to false is disabled. The map is treated as immutable: a
	// mutation replaces it wholesale rather than editing it.
	methods map[string]scenario.MethodSwitch
	// validator is the stage-4 _meta validator (TASK-017 seam). It is never
	// nil; a snapshot with no configured validator carries engine.AcceptAllMeta.
	validator engine.MetaValidator
	// journalEnabled reports whether the instance configured journalling on
	// (distinct from the ring's runtime enabled flag): stage 9 uses it to skip
	// capture setup cheaply when the instance disabled the journal in config.
	journalEnabled bool
	// catalog is the immutable virtual catalog (ADR-004). It is carried on the
	// snapshot so a catalog mutation (drift, later phases) is a snapshot swap
	// with no cache invalidation. Nil is legal and means an empty catalog.
	catalog *catalog.Catalog
	// discover holds the server/discover response configuration (MOCK-201),
	// emitted verbatim by the discover handler. Nil when the scenario omits it.
	discover *scenario.Discover
	// omitResultType is the resolved switches.omitResultType flag (MOCK-209.3).
	// When true the resultType key is ABSENT from every result. It is a resolved
	// bool (not a pointer): "unset" and "explicitly false" both resolve to false
	// here because both mean "do not omit" — the distinction the scenario
	// pointer preserves matters only to composition (TASK-007), which has already
	// merged by the time buildSnapshot runs.
	omitResultType bool
	// omitServerInfoMeta is the resolved switches.omitServerInfoMeta flag
	// (MOCK-209.4). When true the whole result-level _meta object (carrying
	// serverInfo) is ABSENT. Resolved to a bool for the same reason as
	// omitResultType. The two switches are independent: setting one does not
	// force the other.
	omitServerInfoMeta bool
	// selfCheck is the resolved switches.selfCheck flag (MOCK-201.4, AMEND-3,
	// AMEND-10). When true, every outgoing Phase 1 result is validated against
	// the AMEND-4 wire subset before it is written, and a failing result is
	// turned into a server-side -32603 rather than emitted. Resolved to a plain
	// bool with an ON default (AMEND-10): an absent switch resolves to true, and
	// only an explicit false turns it off. The distinction the scenario pointer
	// preserves matters only to composition, which has merged by the time
	// buildSnapshot runs.
	selfCheck bool
	// hiddenCapabilities is the resolved, sorted set of method names whose
	// switches.methods.<name>.hideFromCapabilities is true (MOCK-202.3). The
	// discover handler removes each named method's key from the advertised
	// capabilities object. It is a sorted slice (not a map) so the consumer
	// iterates it deterministically (ADR-003) and so it is an immutable value a
	// clone shares with its parent. Nil when no method is hidden. It is resolved
	// INDEPENDENTLY of the per-method enable/disable map, so "hidden-but-enabled"
	// and "hidden-and-disabled" are both expressible (MOCK-202.3 independence).
	hiddenCapabilities []string
}

// Compile-time assertion that a *Snapshot satisfies the engine's consumer-side
// Snapshot interface. If the engine widens the interface, this fails to compile
// here rather than at a distant call site.
var _ engine.Snapshot = (*Snapshot)(nil)

// Gen returns the snapshot generation (ADR-014). It increases by one on every
// mutation.
func (s *Snapshot) Gen() uint64 { return s.gen }

// Era returns the resolved protocol era recorded at stage 2 ("modern" in
// Phase 1).
func (s *Snapshot) Era() string { return s.era }

// MetaValidator returns the stage-4 _meta validator. It is never nil: a snapshot
// with no configured validator returns engine.AcceptAllMeta, so the pipeline can
// call the seam unconditionally.
func (s *Snapshot) MetaValidator() engine.MetaValidator {
	if s.validator == nil {
		return engine.AcceptAllMeta
	}
	return s.validator
}

// MethodEnabled reports whether the named method is enabled
// (switches.methods.<name>.enabled, MOCK-202.2). The Phase 1 default is enabled:
// a method absent from the switch map, or present with no explicit Enabled
// value, is enabled. Only an explicit Enabled:false disables it.
func (s *Snapshot) MethodEnabled(method string) bool {
	sw, ok := s.methods[method]
	if !ok || sw.Enabled == nil {
		return true
	}
	return *sw.Enabled
}

// JournalEnabled reports whether journalling is on for this snapshot; it lets
// stage 9 skip capture setup cheaply when the instance disabled the journal in
// configuration.
func (s *Snapshot) JournalEnabled() bool { return s.journalEnabled }

// Catalog returns the instance's immutable virtual catalog, or nil when the
// scenario configured none. It is exported for handlers (TASK-018) that read the
// catalog from ex.Snapshot; the returned *catalog.Catalog is immutable and
// safe for concurrent use (ADR-004).
func (s *Snapshot) Catalog() *catalog.Catalog { return s.catalog }

// Discover returns the server/discover configuration, or nil when the scenario
// omits it. It is emitted verbatim by the discover handler (MOCK-201).
func (s *Snapshot) Discover() *scenario.Discover { return s.discover }

// OmitResultType reports whether the resultType key must be ABSENT from every
// result (switches.omitResultType, MOCK-209.3). The default is false ⇒
// resultType present, the conformant shape. It — together with
// [Snapshot.OmitServerInfoMeta] — makes a *Snapshot satisfy the consumer-side
// omissionSnapshot interface internal/modern reads through, which is the seam
// that was built at every layer except this one (DEF-209): the modern handlers
// already call omissionsFor, wire already models an absent key as a nil pointer,
// and the scenario schema already carries the switch; carrying it onto the
// snapshot here is the missing link. It is safe for unbounded concurrent use:
// the field is an immutable bool set at construction and never edited in place.
func (s *Snapshot) OmitResultType() bool { return s.omitResultType }

// OmitServerInfoMeta reports whether the result-level _meta.serverInfo must be
// ABSENT from every result (switches.omitServerInfoMeta, MOCK-209.4). The
// default is false ⇒ serverInfo present. It is independent of
// [Snapshot.OmitResultType]: either switch may be set without the other. Same
// immutability and concurrency properties as OmitResultType.
func (s *Snapshot) OmitServerInfoMeta() bool { return s.omitServerInfoMeta }

// SelfCheck reports whether the outgoing-result self-check is enabled
// (switches.selfCheck, MOCK-201.4). The default is true ⇒ self-check ON
// (AMEND-10): PRIN-5 requires that switches-at-defaults produce only
// self-check-passing responses, so an absent switch enables it; only an explicit
// false disables it. It is resolved to an immutable bool at construction so a
// consumer's guard is a single field read before any validation work. It —
// together with [Snapshot.HiddenCapabilities] — makes a *Snapshot satisfy the
// consumer-side interfaces internal/modern reads through, the same seam shape
// DEF-209 used for the MOCK-209 switches. Safe for unbounded concurrent use.
func (s *Snapshot) SelfCheck() bool { return s.selfCheck }

// HiddenCapabilities returns the sorted, immutable set of method names whose
// capabilities entry the discover handler must omit
// (switches.methods.<name>.hideFromCapabilities, MOCK-202.3). It is nil when no
// method is hidden. The returned slice is owned by the snapshot and MUST NOT be
// mutated by the caller: it is an immutable value shared across clones (ADR-014).
// It is resolved independently of [Snapshot.MethodEnabled], so a method may be
// hidden-but-enabled or hidden-and-disabled.
func (s *Snapshot) HiddenCapabilities() []string { return s.hiddenCapabilities }

// clone returns a shallow struct copy of s. It is the single place a mutation
// starts (ADR-014 write path): the caller edits the returned value, replacing
// any slice or map it changes with a fresh one rather than mutating in place,
// then bumps gen and stores the result. Because every field is either a value,
// an immutable reference (catalog, validator), or a map/slice the caller must
// replace-not-edit, a shallow copy is a correct starting point; the immutable
// references are deliberately shared with the parent snapshot to keep a clone
// cheap (a 5000-item catalog is not re-generated on a switch toggle).
func (s *Snapshot) clone() *Snapshot {
	next := *s
	return &next
}

// buildSnapshot assembles the initial (generation 0) snapshot for an instance
// from its validated scenario spec and its virtual catalog. It is the single
// construction site so the mapping from scenario config to snapshot fields lives
// in one place; a mutation later clones the result rather than rebuilding it.
//
// era defaults to "modern" (Phase 1 is modern-only); a scenario era of
// "modern" or absent both resolve there. journalEnabled follows
// switches/journal config: the journal is on by default (schema default true)
// and off only when the scenario sets journal.enabled to false. methods and
// discover are carried through from the spec.
//
// validator is the stage-4 _meta validator injected by the caller (the mcpmock
// facade builds it from switches.validateMeta via internal/modern, MOCK-203).
// A nil validator means the caller supplied none — a directly-constructed test
// instance, or a configuration that wants no _meta validation — and the
// snapshot falls back to engine.AcceptAllMeta so the pipeline can invoke the
// seam unconditionally (the same non-nil invariant [Snapshot.MetaValidator]
// upholds).
func buildSnapshot(spec scenario.InstanceSpec, cat *catalog.Catalog, validator engine.MetaValidator) *Snapshot {
	if validator == nil {
		validator = engine.AcceptAllMeta
	}
	s := &Snapshot{
		gen:                0,
		era:                resolveEra(spec.Era),
		methods:            methodSwitches(spec.Switches),
		validator:          validator,
		journalEnabled:     journalEnabled(spec.Journal),
		catalog:            cat,
		discover:           spec.Discover,
		omitResultType:     omitResultType(spec.Switches),
		omitServerInfoMeta: omitServerInfoMeta(spec.Switches),
		selfCheck:          selfCheck(spec.Switches),
		hiddenCapabilities: hiddenCapabilities(spec.Switches),
	}
	return s
}

// omitResultType resolves switches.omitResultType (MOCK-209.3) to a plain bool,
// applying the absent-vs-set pointer idiom: an absent switches block, or an
// absent (nil) flag pointer, resolves to false ("do not omit" — the conformant
// default); only an explicit true turns the switch on.
func omitResultType(sw *scenario.Switches) bool {
	if sw == nil {
		return false
	}
	return sw.OmitResultType != nil && *sw.OmitResultType
}

// omitServerInfoMeta resolves switches.omitServerInfoMeta (MOCK-209.4) to a
// plain bool with the same absent-vs-set idiom as [omitResultType]. It is a
// separate helper reading a separate flag pointer so the two switches resolve
// independently — setting one never affects the other.
func omitServerInfoMeta(sw *scenario.Switches) bool {
	if sw == nil {
		return false
	}
	return sw.OmitServerInfoMeta != nil && *sw.OmitServerInfoMeta
}

// selfCheck resolves switches.selfCheck (MOCK-201.4) to a plain bool applying
// the absent-vs-set pointer idiom with an ON default: an absent switches block,
// or an absent (nil) flag, resolves to TRUE — self-check ON — and only an
// explicit false turns it off.
//
// # Effective default: ON (AMEND-10, matching the schema's default:true)
//
// The scenario schema documents selfCheck default:true (ADR-017 §5) and AMEND-10
// (2026-09-04) ruled decisively that the resolver must materialize that default:
// PRIN-5 requires "switches at defaults produces only self-check-passing
// responses", and MOCK-244.4 / MOCK-503.3 require the check be "suppressible only
// by an explicit fault rule". Off-by-default suppressed it for everyone by
// absence, contradicting that. The hot-path objection does not apply: MOCK-901's
// benchmark conditions already specify "self-check off" explicitly, so the ON
// default never costs the benchmark. A scenario that wants the check off sets
// switches.selfCheck:false explicitly (or a deliberate-fault rule suppresses it,
// journaled with the rule id).
func selfCheck(sw *scenario.Switches) bool {
	if sw == nil || sw.SelfCheck == nil {
		return true
	}
	return *sw.SelfCheck
}

// hiddenCapabilities resolves the set of method names whose
// switches.methods.<name>.hideFromCapabilities is true (MOCK-202.3), returning
// them sorted so the consumer (the discover handler) processes them in a
// deterministic order (ADR-003) and the output stays byte-stable (§0.1) in every
// switch combination. It reads only HideFromCapabilities, never Enabled, so the
// hide set is INDEPENDENT of the enable/disable set: a method may be both hidden
// and enabled, or hidden and disabled. Returns nil when no method is hidden, so
// the common case allocates nothing.
func hiddenCapabilities(sw *scenario.Switches) []string {
	if sw == nil || len(sw.Methods) == 0 {
		return nil
	}
	var hidden []string
	for name, m := range sw.Methods {
		if m.HideFromCapabilities != nil && *m.HideFromCapabilities {
			hidden = append(hidden, name)
		}
	}
	if len(hidden) == 0 {
		return nil
	}
	sort.Strings(hidden)
	return hidden
}

// resolveEra maps the scenario era pointer to the resolved era string. Phase 1
// is modern-only, so an absent era, an explicit "modern", or any other value all
// resolve to "modern"; era arbitration (MOCK-303/304) is a later phase that
// widens this without changing readers.
func resolveEra(_ *string) string {
	return eraModern
}

// methodSwitches returns the per-method switch map from the scenario switches
// block, or nil when absent. A nil map is a valid immutable "all methods
// enabled" configuration: [Snapshot.MethodEnabled] defaults a missing entry to
// enabled.
func methodSwitches(sw *scenario.Switches) map[string]scenario.MethodSwitch {
	if sw == nil || len(sw.Methods) == 0 {
		return nil
	}
	// Copy so the snapshot owns an immutable map independent of the caller's
	// spec, honoring ADR-014's replace-not-edit rule for any later mutation.
	out := make(map[string]scenario.MethodSwitch, len(sw.Methods))
	for k, v := range sw.Methods {
		out[k] = v
	}
	return out
}

// journalEnabled reports whether the scenario configured journalling on. The
// schema default is on (MOCK-601), so an absent journal block or an absent
// enabled flag both mean enabled; only an explicit enabled:false disables it.
func journalEnabled(j *scenario.Journal) bool {
	if j == nil || j.Enabled == nil {
		return true
	}
	return *j.Enabled
}
