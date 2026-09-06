// Package journalapi defines the public, serialization-stable contract for the
// mcpmock request journal: the [Record] type and its sub-structures, the
// read-only [View] interface, the [Selector] query surface, the capture
// [Config], and the NDJSON reader/writer.
//
// The journal is mcpmock's most valuable output (design principle §0.3): it
// records, with full fidelity, every request a hub under test sent. This package
// is the boundary that external hub test suites and golden files encode, so it
// imports nothing beyond the Go standard library (architecture.md §6.1 rule 4)
// and defines no package-level mutable state (ADR-007).
//
// # Stability posture
//
// This is a v0 contract. Its JSON form carries [SchemaVersion] from day one and
// is versioned independently of the control API (data-model.md §8). Additive
// fields do not bump SchemaVersion; a field removal or a type change does. The
// contract may still change incompatibly until ADR-019 ratifies the v1 wire
// surface; treat every exported symbol here as a promise that is nonetheless
// provisional at v0. ADR-005 records that [Record] is HARD to reverse once
// golden files and external suites encode it.
//
// # Credential safety (MOCK-407, security.md §5)
//
// The contract makes storing a raw credential in clear structurally
// impossible, not merely discouraged. [CredentialPart] has no Raw, no Value and
// no open map that could carry one: only a keyed [CredentialPart.Hash] plus
// non-sensitive parsed claims. The one place a credential could otherwise leak —
// the Authorization request header — is redacted at capture time (see
// [RedactedHeaderValue]); this contract preserves the header's name, casing and
// position while never carrying its value.
//
// # Disabled-path cost (MOCK-901)
//
// Journaling must be disableable at near-zero cost. Nothing in this contract
// forces allocation on the disabled path: [Config.Enabled] is a plain bool that
// a caller checks before constructing any [Record], and [Record] is a value
// type with no mandatory constructor.
package journalapi
