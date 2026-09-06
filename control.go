package mcpmock

import (
	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
)

// This file is the root-package face of the ADR-015 single control-operation
// definition. The operations are defined exactly once, in internal/control, and
// re-exported here so the published surface is mcpmock.Control /
// mcpmock.InstanceControl (contracts/library-api.md §4) while the HTTP front end,
// the unix-socket front end and (TASK-024) the CLI all remain transports over
// that one definition. Because the aliases below and the internal route table
// share one method set, a front end cannot diverge without failing to compile or
// failing TestControlSurfaceParity, which walks the route table against the
// method set.
//
// # Security posture (security.md §2.1, TB-1/TB-6)
//
// The control plane is the highest-value target in the process: it reads a
// journal that contains every request body and header the hub sent. The
// transports bind to loopback or a 0600 unix socket by default and refuse a
// non-loopback bind without a token (see [WithControlAddr], [WithControlSocket]
// and the internal/control package). This surface carries no credential
// material: journal records return credentials hashed
// ([journalapi.CredentialPart] has no raw field) and Authorization header values
// redacted ([journalapi.RedactedHeaderValue]); no operation here widens that.
//
// # Consumer-only
//
// Control and [InstanceControl] are consumer-only: callers use them, they do not
// implement them, and this package reserves the right to add methods in a minor
// version (ADR-015 Consequences). Obtain the in-process implementation from
// [Server.Control]; do not embed a mock.
//
// # Phase 1 scope
//
// This is the Phase 1 subset (TASK-023): instance inspection, journal read and
// clear, effective seed and health. The mutating operations named in ADR-015 and
// contracts/library-api.md §4 — era switching, catalogue regeneration, fault
// toggling, credential rotation, notification firing, stream and session control
// — are Phase 2+ and are deliberately absent here rather than declared and
// stubbed, so a caller never sees a declared-but-unreachable method (the
// errors.go discipline). Later phases extend the interface; the route table and
// the method set grow together.

// Control is the process-scoped control operation set (ADR-015). It is safe for
// concurrent use. Obtain the in-process implementation from [Server.Control];
// the HTTP and unix-socket front ends expose the identical operation set over
// their transports.
//
// Stability: v0, consumer-only.
type Control = control.Backend

// InstanceControl is the per-instance control operation set (ADR-015). It is
// safe for concurrent use; mutations are serialized per instance (ADR-014).
// Obtain it from [Control.For] or [Instance.Control]. Phase 1 exposes journal
// read, stream, correlation and clear.
//
// Stability: v0, consumer-only.
type InstanceControl = control.InstanceBackend

// InstanceInfo is the read model of one logical instance returned by the control
// API (contracts/control-api.openapi.yaml #/components/schemas/InstanceInfo). It
// is a snapshot value taken at the moment of the call, not a live handle.
//
// Stability: v0.
type InstanceInfo = control.InstanceInfo

// JournalInfo summarizes an instance's journal ring for [InstanceInfo].
//
// Stability: v0.
type JournalInfo = control.JournalInfo

// SeedInfo is the effective root seed and where it came from (GET /v1/seed,
// MOCK-704.2).
//
// Stability: v0.
type SeedInfo = control.SeedInfo

// Health is process health (GET /v1/health,
// contracts/control-api.openapi.yaml #/components/schemas/Health).
//
// Stability: v0.
type Health = control.Health

// Seed source values for [SeedInfo.Source] (contracts/control-api.openapi.yaml
// /seed source enum). Phase 1 produces "flag" (an explicit [WithSeed]) and
// "random" (a generated seed); "scenario" is reserved for a later phase that
// reads a seed from the scenario document.
const (
	seedSourceFlag   = "flag"
	seedSourceRandom = "random"
)
