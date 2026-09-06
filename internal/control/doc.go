// Package control implements the mcpmock control plane: one operation set (the
// [Backend] port) served by transports over a single shared route table
// (ADR-015 one-interface-three-front-ends). The HTTP front end ([Handler]) and
// the unix-socket front end are the same [Handler] on different listeners, so
// they cannot diverge; the in-process front end is the [Backend] itself, held
// directly by an embedded caller and (TASK-024) by the CLI. Because every front
// end is driven by the one route table, "the same operations" (MOCK-104) is
// structural, not a matter of discipline, and TestControlSurfaceParity enforces
// the bijection between the route table and the [Backend]/[InstanceBackend]
// method sets. The HTTP client that TASK-024's `mcpmock ctl --url` uses is a
// consumer of this same route table and lands with that task.
//
// The root mcpmock package re-exports [Backend] as mcpmock.Control and
// [InstanceBackend] as mcpmock.InstanceControl (contracts/library-api.md §4), so
// the published surface is defined once, here.
//
// # Security posture (security.md §2.1, TB-1/TB-6)
//
// The control plane reads a journal containing every request body and header the
// hub sent, so it is the process's highest-value target. The HTTP front end
// binds to loopback by default and refuses a non-loopback bind without a token
// ([CheckBind], MOCK-104.5). The unix-socket front end creates a 0600 owner-only
// socket and never widens it ([ListenUDS], AMEND-7). Nothing this package returns
// widens the journal's credential redaction: records carry credentials hashed
// ([journalapi.CredentialPart] has no raw field) and Authorization header values
// redacted at capture ([journalapi.RedactedHeaderValue]).
//
// # The unix-socket lifecycle (AMEND-7)
//
// [ResolveSocketPath] computes the socket path with the documented precedence
// (explicit flag > MCPMOCK_CONTROL_SOCKET > ${XDG_RUNTIME_DIR}/mcpmock-${pid}.sock
// > /tmp/mcpmock-${pid}.sock), applying the platform sun_path length fallback.
// [ListenUDS] binds it with probe-before-unlink: on EADDRINUSE it dials the
// existing socket, and only a refused dial (nothing listening) leads to an
// unlink-and-retry — a live socket is never stolen, which under ADR-007's
// multi-instance model is a real scenario. The socket is chmod'd to 0600 before
// it accepts, so a permissive umask cannot widen it.
//
// # NDJSON streaming (MOCK-602.2)
//
// The journal NDJSON path streams: the [Handler] pulls from
// [InstanceBackend.JournalStream]'s iterator and encodes each record as it
// arrives, and the in-process backend drives the ring's bounded k-way shard
// merge, so the whole journal is never materialized.
//
// # No process globals (ADR-007)
//
// This package holds no package-level mutable state. A [Handler] is built around
// one [Backend], and two facades in one process each get their own [Handler] and
// listeners without colliding.
package control
