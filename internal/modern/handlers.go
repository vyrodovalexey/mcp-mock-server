package modern

import (
	"context"
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/catalogue"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// handlers.go holds the cross-cutting seams the three Phase 1 method handlers
// share: the snapshot surface they read configuration from, the built-in tool
// behavior configuration, and the registration entry point that binds them into
// an engine [engine.Registry].
//
// # Provisional wire dependence (GAP-003)
//
// Every one of these handlers emits the 2026-07-28 ("modern") wire surface,
// which GAP-003 has NOT ratified. The result shapes, the resultType value set
// and _meta.serverInfo are authored-annex proposals ([P-21], [P-22], [P-39]…),
// and the three built-in behaviors are builtin-tools.md's [P-39]..[P-52]. mcpmock
// emulates requirements.md plus the annex and makes NO conformance claim; every
// wire literal is sourced from [wire] (ADR-019 containment), so ratification is a
// single-package edit. See doc.go for the per-handler clause map.

// configSnapshot is the CONSUMER-DEFINED surface the handlers read beyond the
// engine's own [engine.Snapshot]. The engine's Snapshot interface exposes only
// the pipeline's needs (Gen, Era, MethodEnabled, …); the handlers additionally
// need the discover configuration, the virtual catalog and the MOCK-209 omission
// switches. internal/instance's *Snapshot already exposes Catalog and Discover
// (TASK-015/016), so its concrete type satisfies this interface; naming the
// surface here keeps internal/modern from importing internal/instance and keeps
// the dependency arrow pointing the right way (architecture.md §6.1).
//
// A snapshot that does not satisfy this interface (a bare test double, or a
// future engine.Snapshot with no catalog) is handled gracefully: the handlers
// fall back to empty configuration rather than panicking, so a missing seam is a
// well-formed empty result, never a crash.
type configSnapshot interface {
	// Catalog returns the instance's immutable virtual catalog, or nil when the
	// scenario configured none (an empty catalog).
	Catalog() *catalog.Catalog
	// Discover returns the server/discover configuration, or nil when the
	// scenario omits it (all discover fields absent).
	Discover() *scenario.Discover
}

// omissionSnapshot is the CONSUMER-DEFINED surface carrying the MOCK-209
// omission switches (switches.omitResultType, switches.omitServerInfoMeta). It
// is separate from [configSnapshot] because the switches are optional: a
// snapshot that does not expose them defaults both to "do not omit" (the
// conformant shape), which is the Phase 1 default when a scenario sets no
// switch.
type omissionSnapshot interface {
	// OmitResultType reports whether the resultType key is omitted from every
	// result (MOCK-209.3). Default false ⇒ resultType present.
	OmitResultType() bool
	// OmitServerInfoMeta reports whether _meta.serverInfo is omitted from every
	// result (MOCK-209.4). Default false ⇒ serverInfo present.
	OmitServerInfoMeta() bool
}

// capabilitiesSnapshot is the CONSUMER-DEFINED surface carrying the MOCK-202.3
// hide-from-capabilities set: the method names whose advertised capabilities
// entry the discover handler must omit. It is separate from [configSnapshot]
// because it is optional — a snapshot that does not expose it hides nothing,
// which is the Phase 1 default when a scenario sets no hideFromCapabilities
// switch. It is deliberately independent of [engine.Snapshot.MethodEnabled]:
// hiding a method from capabilities and disabling it are orthogonal, so
// "advertised but disabled" and "hidden but enabled" are both expressible
// (MOCK-202.3).
type capabilitiesSnapshot interface {
	// HiddenCapabilities returns the sorted method names whose capabilities key
	// is omitted from the server/discover result (MOCK-202.3). Nil ⇒ hide
	// nothing (the conformant default).
	HiddenCapabilities() []string
}

// omissions carries the two MOCK-209 switches resolved for one request. It is a
// tiny value computed once per handler from the snapshot so each result-building
// helper reads a plain bool rather than re-type-asserting the snapshot.
type omissions struct {
	// resultType is true when the resultType key must be absent (MOCK-209.3).
	resultType bool
	// serverInfo is true when _meta.serverInfo must be absent (MOCK-209.4).
	serverInfo bool
}

// omissionsFor resolves the MOCK-209 switches for the request's snapshot. A
// snapshot that does not expose them (a bare double) yields both-false — the
// conformant shape — so the omission behavior is opt-in, matching the schema
// defaults.
func omissionsFor(snap engine.Snapshot) omissions {
	os, ok := snap.(omissionSnapshot)
	if !ok {
		return omissions{}
	}
	return omissions{resultType: os.OmitResultType(), serverInfo: os.OmitServerInfoMeta()}
}

// resultTypePtr returns a pointer to the resultType value for method, or nil
// when the request's omitResultType switch is set (MOCK-209.3: the key is then
// ABSENT, not null — wire models this as a nil *string). The value itself comes
// from the internal/wire table (ADR-019: no resultType literal here), so a
// method with no table entry yields nil and the key is simply absent.
func resultTypePtr(method string, om omissions) *string {
	if om.resultType {
		return nil
	}
	rt, ok := wire.ResultTypeForMethod(method)
	if !ok {
		return nil
	}
	return &rt
}

// serverInfoMeta builds the result-level _meta carrying serverInfo (MOCK-209.2),
// or nil when the omitServerInfoMeta switch is set so the whole _meta object is
// ABSENT (MOCK-209.4). The serverInfo value is drawn from the discover
// configuration's serverInfo when the scenario authored one, and otherwise from
// the built-in default, so every result carries a serverInfo unless the switch
// removed it. Additional _meta members (the sleep clamp fields) are layered on
// by the caller through [withMetaExtra].
func serverInfoMeta(cfg *scenario.Discover, om omissions) *wire.ResultMeta {
	if om.serverInfo {
		return nil
	}
	return &wire.ResultMeta{ServerInfo: resolveServerInfo(cfg)}
}

// RegisterHandlers binds the three Phase 1 method handlers — server/discover,
// tools/list and tools/call — into reg, using bcfg for the built-in tool
// behaviors. It is the single wiring entry point TASK-022's facade calls when it
// builds an instance's registry; the method names come from [wire] (ADR-019), so
// no wire literal appears at the call site either.
//
// The handlers are stateless with respect to per-request data: they read all
// per-request configuration from ex.Snapshot, so one registration serves every
// request on the instance and the registry stays a read-only map on the hot path
// (engine/registry.go). bcfg is captured by the tools/call handler and is the
// scenario-derived behavior configuration — the caller (a scenario) selects the
// fail mode and the sleep bounds, never the client (builtin-tools.md 4.4 [P-52]).
//
// Every handler's result is passed through the MOCK-201.4 self-check before it is
// returned, but the check runs only when switches.selfCheck is on for the request
// (a single snapshot bool read otherwise); see [selfChecker.check]. A nil logger
// is legal and disables only the failure log line, not the rejection.
func RegisterHandlers(reg *engine.Registry, bcfg BuiltinConfig) {
	RegisterHandlersWithLogger(reg, bcfg, nil)
}

// RegisterHandlersWithLogger is [RegisterHandlers] with an explicit self-check
// failure logger (MOCK-201.4): a self-check rejection logs an ERROR naming the
// schema path through log. It is the entry point the facade uses so the
// per-instance logger reaches the check without internal/modern importing
// internal/obs (the logger is passed as a [SelfCheckLogger], which *slog.Logger
// satisfies). log may be nil, which disables the log line while keeping the
// -32603 rejection and its journalled reason. RegisterHandlers delegates here
// with a nil logger, preserving the existing call sites.
func RegisterHandlersWithLogger(reg *engine.Registry, bcfg BuiltinConfig, log SelfCheckLogger) {
	sc := &selfChecker{log: log}
	reg.Register(wire.MethodDiscover, checkedHandler(sc, wire.MethodDiscover, handleDiscover))
	reg.Register(wire.MethodToolsList, checkedHandler(sc, wire.MethodToolsList, handleToolsList))
	reg.Register(wire.MethodToolsCall, checkedToolsCall(sc, newToolsCallHandler(bcfg.withDefaults())))
}

// checkedHandler wraps a bare handler func so its successful result is run
// through the self-check before it is returned. A fault from the inner handler is
// passed through untouched (an error response is not a Phase 1 RESULT, so it is
// out of the self-check's scope — the check validates result bodies, not error
// objects). It captures the method name so the failure log and reason name it.
func checkedHandler(
	sc *selfChecker, method string,
	inner func(context.Context, *engine.Exchange) (json.RawMessage, *engine.Fault),
) engine.Handler {
	return engine.HandlerFunc(func(ctx context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
		body, fault := inner(ctx, ex)
		if fault != nil {
			return nil, fault
		}
		if f := sc.check(ex.Snapshot, method, body); f != nil {
			return nil, f
		}
		return body, nil
	})
}

// checkedToolsCall wraps the stateful tools/call handler the same way. It is a
// separate wrapper only because tools/call is an engine.Handler (a struct), not a
// bare func; the self-check discipline is identical.
func checkedToolsCall(sc *selfChecker, inner engine.Handler) engine.Handler {
	return engine.HandlerFunc(func(ctx context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
		body, fault := inner.Handle(ctx, ex)
		if fault != nil {
			return nil, fault
		}
		if f := sc.check(ex.Snapshot, wire.MethodToolsCall, body); f != nil {
			return nil, f
		}
		return body, nil
	})
}

// configOf returns the request snapshot as a [configSnapshot], or (nil, false)
// when the snapshot does not expose the discover/catalog surface. A handler that
// gets false falls back to empty configuration, so a bare engine.Snapshot double
// yields a well-formed empty result rather than a panic.
func configOf(snap engine.Snapshot) (configSnapshot, bool) {
	cs, ok := snap.(configSnapshot)
	return cs, ok
}
