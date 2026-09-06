// Package controlclient is the HTTP/unix-socket client for the mcpmock control
// API (ADR-015). It is the transport `mcpmock ctl` speaks over, and it is a
// consumer of the same control route table the server exposes — never a second
// definition of the operations.
//
// # Why this package is separate from internal/control (gosec G704)
//
// TASK-023 deliberately left the production control client out of
// internal/control. gosec's G704 (SSRF) fires when an HTTP *client* is
// co-located in the same package as the control *handler*, because gosec
// aggregates taint at the package level: the handler reads r.URL.Query() and any
// http.Client call in the same package then looks, to the analyzer, like it
// could be dialing a request-controlled URL. Splitting the client into this
// sibling package breaks that package-level aggregation honestly — the client
// dials only an operator-supplied base URL or socket path, never a value derived
// from an inbound request — so no //nolint is required (and none is used).
//
// # One route table, three front ends
//
// The verbs the CLI exposes come from [Routes], which mirrors the server's
// Phase 1 route set. A [Client] issues the matching HTTP request against a TCP
// base URL (--url) or a unix socket (--socket), decodes the JSON response into
// the shared internal/control read models, and maps the control error envelope
// back onto the control sentinel errors so a caller branches with errors.Is.
package controlclient
