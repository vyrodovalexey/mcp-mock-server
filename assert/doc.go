// Package assert provides assertions over an mcpmock request journal
// ([journalapi.View]). It is the Go API the gateway's own test suite imports to
// turn hub defects into visible test failures (design principle §0.3): an
// assertion whose failure message does not identify the offending request wastes
// the debugging time it was meant to save, so this package treats failure-message
// quality as its primary contract (contracts/assertions-api.md §7).
//
// # Two forms
//
// Every assertion has two forms that share a name stem and a rendering:
//
//   - The fatal form on [TB] (Assert*) calls [TestingT.Helper] then
//     [TestingT.Fatalf] on violation, so it stops a test at the caller's line.
//   - The non-fatal form on [Asserts] (Check*) returns an error describing every
//     violation found, or nil, and is usable outside a test — from the CLI, the
//     control API, or a hub harness.
//
// [TestAssertParity] pins the two method sets together so the forms cannot drift.
//
// # Not testing.T
//
// [T] binds a [journalapi.View] to a [TestingT], a minimal interface of
// Errorf/Fatalf/Helper. A real *testing.T satisfies it, so assert.T(t, view)
// compiles unchanged; but because the interface is minimal and exported, the
// same assertions run from a CLI, the control API or a hub harness — exactly the
// places a hard *testing.T dependency would make them unusable. This is a
// deliberate, documented refinement of the assertions-api.md §1 signature, which
// names testing.TB; see the TASK-025 report for the rationale.
//
// # Credential safety
//
// [TB.AssertNoHeader] and [TB.AssertHeaderNotValue] are token-passthrough checks,
// so their failure path is precisely where a token would otherwise leak into a CI
// log. No raw credential, token or Authorization value ever appears in any message
// in any mode: header values are truncated to a bounded prefix with a length
// indication, and [journalapi.CredentialPart] carries only hashes
// (contracts/assertions-api.md §7). [TestNoSecretInMessage] enforces this by
// running every assertion against a journal containing a sentinel secret.
//
// # Working from a file
//
// [FromFile] and [FromReader] load an NDJSON journal export (MOCK-602) into a
// [journalapi.View] with no server running, so assertions work against a recorded
// journal (MOCK-603.4).
//
// # Stability and scope
//
// This is a v0 contract that external suites encode; treat every exported symbol
// as a provisional promise (ADR-019). Phase 1 implements three of the eight
// MOCK-603 assertions — [TB.AssertNoHeader], [TB.AssertHeaderNotValue] and
// [TB.AssertRequestCount] with their Check* counterparts. The remaining five
// assertions, the MOCK-604 golden machinery and the MOCK-605 correlation helpers
// arrive in Phase 10 (TASK-603). The dependency rule (ADR-001, enforced by
// make deps-check) restricts this package to journalapi, the standard library and
// testing, and it holds no package-level mutable state (ADR-007).
package assert
