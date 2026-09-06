package modern

import (
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// serverinfo.go resolves the result-level _meta.serverInfo value (MOCK-209.2,
// annex 4.3/4.4 [P-22]). serverInfo is present on every result unless
// switches.omitServerInfoMeta removes it, so a default must exist even when the
// scenario authors none.

// defaultServerInfoName is the serverInfo.name emitted when the scenario does
// not author a spec.discover.serverInfo. It is the product name and is a stable
// constant, not a wall-clock or seed-derived value, so it does not disturb
// byte-stability. It is NOT wire vocabulary (a method name / resultType / error
// code); it is the identity of THIS server, so ADR-019 containment does not
// reach it — it is named here the same way the engine names its own error
// message strings.
const defaultServerInfoName = "mcpmock"

// defaultServerInfoVersion is the serverInfo.version emitted when the scenario
// authors none. The 2026-07-28 wire revision is UNRATIFIED (GAP-003) and mcpmock
// makes no conformance claim, so this reports the emulator's own provisional
// version, deliberately distinct from wire.ProtocolRevision (which is a protocol
// revision, not a server version). It is a plain server-identity constant.
const defaultServerInfoVersion = "0.1.0"

// resolveServerInfo returns the *wire.ServerInfo to place under
// _meta.serverInfo. When the scenario authored a spec.discover.serverInfo object
// (raw JSON), its {name, version, title} are decoded and used so an authored
// identity reaches the client; any field the authored object omits falls back to
// the default. When no serverInfo is authored, the built-in default
// {name, version} is returned. It never returns nil for a caller that has
// already decided serverInfo is present (the omit switch is handled by the
// caller), so MOCK-209.2's "present on every result" holds.
func resolveServerInfo(cfg *scenario.Discover) *wire.ServerInfo {
	info := &wire.ServerInfo{
		Name:    defaultServerInfoName,
		Version: defaultServerInfoVersion,
	}
	if cfg == nil || len(cfg.ServerInfo) == 0 {
		return info
	}
	var authored wire.ServerInfo
	if err := json.Unmarshal(cfg.ServerInfo, &authored); err != nil {
		// An authored serverInfo that does not decode to the {name, version,
		// title} shape is left to the default rather than rejected: discover
		// config is schema-validated upstream, so this is a defensive fallback,
		// not a validation site.
		return info
	}
	if authored.Name != "" {
		info.Name = authored.Name
	}
	if authored.Version != "" {
		info.Version = authored.Version
	}
	if authored.Title != "" {
		info.Title = authored.Title
	}
	return info
}
