package modern

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// discover.go implements the server/discover handler (MOCK-201, partial:
// 201.1 configurable fields, 201.3 absent-not-null omission).
//
// Wire clauses it depends on (all provisional, GAP-003): annex §5 (the discover
// result field set), annex 4.2 [P-21] (resultType "discovery"), annex 4.3/4.4
// [P-22] (_meta.serverInfo), annex 4.5/4.6 [P-23] (ttlMs / cacheScope). Every
// such value is sourced from [wire]; no wire literal appears here (ADR-019).
//
// # Configurable fields and the omission rule (MOCK-201.1 / 201.3)
//
// Each of the six discover fields — supportedVersions, capabilities,
// instructions, serverInfo, ttlMs, cacheScope — is emitted only when the
// scenario's spec.discover configured it. An UNCONFIGURED field is ABSENT from
// the result (a JSON key that is not present), never present with a null value:
// wire.DiscoverResult models this with pointers, slices and maps carrying
// omitempty, so a nil field marshals to no key at all. serverInfo is carried in
// _meta and is present on every result unless switches.omitServerInfoMeta
// removed it (MOCK-209.4), independent of whether the scenario authored one.
//
// # Determinism (§0.1)
//
// The handler draws no RNG and reads no clock: the result is a pure function of
// the immutable snapshot, so two identical requests at a fixed seed produce
// byte-identical bodies. Raw-JSON config members (capabilities, serverInfo) are
// emitted verbatim, preserving authored key order (MOCK-222.4).

// handleDiscover is the server/discover [engine.Handler]. It reads spec.discover
// from the snapshot and builds a wire.DiscoverResult in which every field the
// scenario omitted is absent, then marshals it through wire.MarshalResult so the
// bytes are byte-stable (HTML escaping disabled to match the response encoder).
func handleDiscover(_ context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
	cfg := discoverConfig(ex.Snapshot)
	om := omissionsFor(ex.Snapshot)

	caps, capFault := capabilities(cfg, hiddenCapabilitiesFor(ex.Snapshot))
	if capFault != nil {
		return nil, capFault
	}

	res := wire.DiscoverResult{
		ResultType:        resultTypePtr(wire.MethodDiscover, om),
		SupportedVersions: supportedVersions(cfg),
		Capabilities:      caps,
		Instructions:      instructions(cfg),
		CacheHints:        discoverCacheHints(cfg),
		Meta:              serverInfoMeta(cfg, om),
	}

	body, err := wire.MarshalResult(res)
	if err != nil {
		return nil, engine.InvalidParamsFault("mcpmock: encoding discover result", nil).
			WithReason("marshal discover result: " + err.Error())
	}
	return body, nil
}

// hiddenCapabilitiesFor resolves the MOCK-202.3 hide-from-capabilities set for
// the request's snapshot. A snapshot that does not expose the surface (a bare
// double) hides nothing — the conformant default — so the behavior is opt-in and
// matches the schema default. The returned slice is the snapshot's immutable,
// sorted set and MUST NOT be mutated here.
func hiddenCapabilitiesFor(snap engine.Snapshot) []string {
	cs, ok := snap.(capabilitiesSnapshot)
	if !ok {
		return nil
	}
	return cs.HiddenCapabilities()
}

// discoverConfig returns the scenario's spec.discover, or nil when the snapshot
// does not expose the discover surface or the scenario omitted the block. A nil
// config means every field is absent, which is a valid (empty) discover result.
func discoverConfig(snap engine.Snapshot) *scenario.Discover {
	cs, ok := configOf(snap)
	if !ok {
		return nil
	}
	return cs.Discover()
}

// supportedVersions returns the configured supportedVersions slice, or nil so
// the key is absent (MOCK-201.3). The slice is returned as-is; wire's omitempty
// drops a nil or empty slice.
func supportedVersions(cfg *scenario.Discover) []string {
	if cfg == nil {
		return nil
	}
	return cfg.SupportedVersions
}

// capabilities returns the configured capabilities object as raw JSON, or nil so
// the key is absent, after removing the top-level entry for each hidden method
// (MOCK-202.3, switches.methods.<name>.hideFromCapabilities).
//
// # Verbatim survival vs. hiding (MOCK-201.2 / MOCK-202.3)
//
// With no method hidden the authored capabilities object is returned BYTE-FOR-
// BYTE and never parsed, so an arbitrary/extension capability shape (MOCK-201.2)
// reaches the client unaltered and the byte-stable path (§0.1) is unchanged. A
// method is "hidden from capabilities" by removing the top-level capabilities
// key whose NAME equals the method name: capabilities is annex 5.3's object of
// capability-name → object, and MOCK-202.3's switch is keyed by method name, so
// an author advertises a method by adding a key named after it (e.g.
// "tools/list") and hides it by setting that method's hideFromCapabilities. This
// is INDEPENDENT of the method's enable/disable state (MOCK-202.2), so
// "advertised but disabled" and "hidden but enabled" are both expressible.
//
// # Deterministic hiding (§0.1)
//
// When at least one method is hidden the object is decoded into an
// insertion-ordered map, the named keys are deleted, and it is re-encoded
// preserving the order of the SURVIVING keys — so the output stays byte-identical
// for a fixed input across runs and processes (ADR-003). A capabilities value
// that is not a JSON object (an authored non-object, or a hideFromCapabilities
// set applied to a scenario that authored no object of that shape) is left
// verbatim: there is no method key to remove from a non-object, and mangling it
// would violate MOCK-201.2 verbatim survival.
func capabilities(cfg *scenario.Discover, hidden []string) (json.RawMessage, *engine.Fault) {
	if cfg == nil {
		return nil, nil
	}
	raw := cfg.Capabilities
	if len(raw) == 0 || len(hidden) == 0 {
		return raw, nil
	}
	return hideCapabilities(raw, hidden)
}

// hideCapabilities removes each name in hidden from the top-level members of the
// raw capabilities object, preserving both the order AND the VERBATIM bytes of
// the surviving members (MOCK-201.2 / §0.1). raw is guaranteed non-empty and
// hidden non-empty by the caller.
//
// It streams the object with a [json.Decoder], reading each surviving member's
// value into a [json.RawMessage] — which the decoder copies BYTE-FOR-BYTE, with
// no re-escaping or re-compaction — and re-emits {"key":<verbatim>,…} in the
// original order. This is why it does not decode into a map and re-marshal: that
// would HTML-escape and compact the authored values, silently changing the
// bytes an extension capability was authored with. A raw value that is not a
// JSON object is returned unchanged (there is no method key to remove from a
// non-object), so an authored non-object capabilities survives verbatim. A
// genuinely malformed object is an internal error surfaced as a -32603, never a
// silently corrupted result.
func hideCapabilities(raw json.RawMessage, hidden []string) (json.RawMessage, *engine.Fault) {
	hide := make(map[string]struct{}, len(hidden))
	for _, name := range hidden {
		hide[name] = struct{}{}
	}
	members, ok, err := filterObjectMembers(raw, hide)
	if err != nil {
		return nil, engine.InvalidParamsFault("mcpmock: encoding discover capabilities", nil).
			WithReason("hide capabilities decode: " + err.Error())
	}
	if !ok {
		// Not a JSON object: nothing to hide, survive verbatim (MOCK-201.2).
		return raw, nil
	}
	return assembleObject(members), nil
}

// objectMember is one surviving top-level member of the capabilities object: its
// key (verbatim, still JSON-quoted as decoded) and its value bytes (verbatim, as
// the decoder copied them). Retaining the raw quoted key and raw value avoids any
// re-encoding of authored content.
type objectMember struct {
	keyJSON []byte
	value   json.RawMessage
}

// filterObjectMembers streams the top-level members of a JSON object, returning
// the members whose key is NOT in hide, in input order and with verbatim value
// bytes. ok is false when raw is not a JSON object (JSON null, array, scalar), in
// which case the caller leaves the value untouched. An error is a genuinely
// malformed object the caller maps to a -32603.
func filterObjectMembers(raw json.RawMessage, hide map[string]struct{}) (members []objectMember, ok bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, terr := dec.Token()
	if terr != nil {
		return nil, false, terr
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim || delim != '{' {
		// Not an object (null, array, number, string): not our shape to edit.
		return nil, false, nil
	}
	for dec.More() {
		keyTok, kerr := dec.Token()
		if kerr != nil {
			return nil, false, kerr
		}
		key, isString := keyTok.(string)
		if !isString {
			return nil, false, errNonStringKey
		}
		var value json.RawMessage
		if verr := dec.Decode(&value); verr != nil {
			return nil, false, verr
		}
		if _, hidden := hide[key]; hidden {
			continue
		}
		kb, merr := marshalKeyNoEscape(key)
		if merr != nil {
			return nil, false, merr
		}
		members = append(members, objectMember{keyJSON: kb, value: value})
	}
	if _, terr = dec.Token(); terr != nil { // consume closing '}'
		return nil, false, terr
	}
	return members, true, nil
}

// marshalKeyNoEscape re-quotes an object key with HTML escaping DISABLED, so a
// surviving capability name containing <, > or & keeps the bytes it was authored
// with (§0.1) rather than being rewritten to \u003c etc. It mirrors the
// escaping discipline of wire.MarshalResult for the key stream.
func marshalKeyNoEscape(key string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(key); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// errNonStringKey guards the impossible-in-valid-JSON case of a non-string
// object key, so filterObjectMembers surfaces it as a decode error (mapped to a
// -32603) rather than silently dropping a member.
var errNonStringKey = errors.New("capabilities object key is not a string")

// assembleObject reconstructs a JSON object from surviving members, emitting
// {"k":<verbatim v>,…} with each key and value copied byte-for-byte. An empty
// members slice yields the empty object {} — a scenario that hid every advertised
// capability advertises none, which is the intended, testable state.
func assembleObject(members []objectMember) json.RawMessage {
	if len(members) == 0 {
		return json.RawMessage("{}")
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(m.keyJSON)
		buf.WriteByte(':')
		buf.Write(m.value)
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes())
}

// instructions returns a pointer to the configured instructions string, or nil
// so the key is absent (MOCK-201.3). It is a pointer so a configured empty
// string ("") is still emitted, distinct from an absent instructions field.
func instructions(cfg *scenario.Discover) *string {
	if cfg == nil {
		return nil
	}
	return cfg.Instructions
}

// discoverCacheHints maps the scenario's ttlMs and cacheScope config to the wire
// cache hints. Both are carried in the scenario as raw JSON so absent, a value
// and an explicit null stay distinct; Phase 1 treats absent and null alike as
// "omit the key" (MOCK-201.3), emitting the key only for a concrete value. The
// ttlMs boundary values (0, negative, > 2^53) are MOCK-201.5, Phase 2, so a
// Phase 1 numeric ttlMs is passed through unclamped but its edge semantics are
// not exercised here.
func discoverCacheHints(cfg *scenario.Discover) wire.CacheHints {
	if cfg == nil {
		return wire.CacheHints{}
	}
	return wire.CacheHints{
		TTLMs:      rawInt64OrNil(cfg.TTLMs),
		CacheScope: rawStringOrNil(cfg.CacheScope),
	}
}

// rawInt64OrNil decodes a raw-JSON config member into a *int64: nil when the
// member was absent or an explicit null or not a number, and a pointer to the
// value otherwise. It never fails: a non-numeric ttlMs is treated as absent
// rather than rejected, because discover config is authored and schema-validated
// upstream, and a Phase 1 handler does not re-validate it.
func rawInt64OrNil(raw json.RawMessage) *int64 {
	if isAbsentOrNull(raw) {
		return nil
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return &v
}

// rawStringOrNil decodes a raw-JSON config member into a *string: nil when
// absent, explicit null or not a string, and a pointer to the value otherwise.
func rawStringOrNil(raw json.RawMessage) *string {
	if isAbsentOrNull(raw) {
		return nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return &v
}

// isAbsentOrNull reports whether a raw-JSON config member is absent (nil/empty)
// or the literal JSON null. Both mean "omit the resulting key" in Phase 1
// (MOCK-201.3); distinguishing an explicit null (to emit null) is MOCK-201.5 /
// MOCK-232, Phase 2.
func isAbsentOrNull(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	return string(raw) == "null"
}
