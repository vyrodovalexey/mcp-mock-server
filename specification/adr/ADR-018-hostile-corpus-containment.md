---
id: ADR-018
title: The prompt-injection corpus is a contained asset — encoded at rest, gated at use, manifested in CI
status: accepted
date: 2026-09-04
reversibility: EASY mechanically; the containment policy is HARD once the corpus ships
requirements: MOCK-507, MOCK-506, MOCK-225, §0.4
---

# ADR-018 — Hostile corpus containment

## Context

`MOCK-507`:

> Prompt-injection payloads: tool descriptions, `annotations` and `instructions` containing
> instruction-like text and hidden Unicode, drawn from a **bundled corpus**, for testing the hub's
> untrusted-content handling and any sanitization layer.

Bundling attack payloads in a repository and shipping them in a container image creates real
hazards that a normal test fixture does not:

1. **Escape from the test boundary.** A scenario file copied into a staging environment, or a
   mock instance left running on a shared cluster, feeds injection payloads to whatever LLM sits
   behind the hub. The payloads are designed to be effective.
2. **Contamination of tooling.** These strings are rendered in IDEs, code-review UIs, CI logs,
   Slack notifications and — increasingly — AI coding assistants reading the repository. A payload
   whose content is "ignore previous instructions and …" sitting in a plain-text file is being fed
   to systems it was never aimed at.
3. **Security scanners.** Secret scanners and DLP tools flag hostile-looking content; a
   repository full of it generates noise that trains reviewers to ignore alerts.
4. **Provenance.** Where did each payload come from, and is it licensed for redistribution?

`MOCK-225` (network `$ref` to a public host) and `MOCK-506` (`javascript:` / `file:` icon URLs)
carry a smaller version of the same problem: they are deliberate SSRF and deliberate XSS vectors.

## Decision

### 1. Encoded at rest

`internal/corpus/corpus.bin` is a `go:embed`ed, **base64-with-per-entry-XOR-obfuscation** blob —
not encryption, and not claimed to be. Its only purposes are (a) to stop tooling and humans from
incidentally *reading and acting on* the payloads, and (b) to make accidental copy-paste out of
the repository fail visibly. It is decoded in memory on first use.

Alongside it, `internal/corpus/manifest.json` — **plaintext** — lists for each entry:
`id`, `category`, `sha256`, `sourceRef`, `license`, `oneLineDescription`, `unicodeTricks: []`.
Reviewers and auditors read the manifest; nobody needs to read the payloads to review a change.

### 2. Integrity-manifested in CI

`make corpus-verify` recomputes every `sha256` and fails on mismatch or on an entry present in the
blob but absent from the manifest. Wired into the `lint` job. This makes "someone quietly added a
payload" a build failure. Corpus changes require an explicit manifest edit and are reviewable as a
diff of *descriptions*, which is the reviewable artifact.

### 3. Gated at use — three independent gates

A corpus payload is emitted only when **all** hold:

1. The scenario declares `spec.hostile.enabled: true` at the instance level.
2. The specific fault rule declares `action: {kind: corpus, categories: [...]}`.
3. The process was not started with `--safe-mode` (which is the **default in the container image**
   — see below).

Missing any gate yields a placeholder string
`[[mcpmock:corpus:<id>:withheld:<reason>]]` and a `WARN` log. The placeholder is deliberately
recognisable so a test that *expects* hostile content fails loudly instead of silently passing
against benign input.

### 4. Loud while armed

- Startup log at `WARN`: `{"msg":"hostile corpus armed","instances":[...],"categories":[...]}`.
- Prometheus gauge `mcpmock_hostile_mode{instance}` = 1.
- Every response carrying a payload is journaled with `corpusIDs: [...]`.
- The control API `GET /v1/instances` reports `hostile: true`.
- HTTP responses from a hostile instance carry `X-Mcpmock-Hostile: 1`. A gateway or proxy can
  therefore refuse them, and an operator grepping a capture can find them.

### 5. Delimited on the wire

Every emitted payload is wrapped:

```
<<MCPMOCK-UNTRUSTED id=inj-014>>…payload…<</MCPMOCK-UNTRUSTED>>
```

Two reasons. It makes payload boundaries unambiguous in the journal and in any downstream
incident. And — importantly — it does **not** weaken the test: a hub that strips or escapes
untrusted content must handle the payload regardless of the wrapper, and a hub that "passes" only
because it recognised our wrapper is a finding the wrapper makes visible. The wrapper is
disableable per rule (`wrap: false`) for tests that specifically need raw content; that option is
documented as requiring the deployment to be network-isolated.

### 6. Deployment posture

- The container image sets `MCPMOCK_SAFE_MODE=1` by default; the Helm chart exposes
  `hostile.enabled` which must be explicitly set to `false`-safe-mode-off, and the chart's
  `NOTES.txt` prints a warning when it is.
- The Helm chart, when hostile mode is on, **requires** a `NetworkPolicy` to be enabled
  (`networkPolicy.enabled: true`), defaulting to deny-all egress. Template fails with a clear
  message otherwise. Egress to an LLM provider from a hostile mock is the scenario we are
  preventing.
- Documentation states plainly: never point a hostile-mode mock at a hub connected to a production
  model endpoint.

### 7. `MOCK-225` network `$ref` and `MOCK-506` URL schemes

Same shape, smaller blast radius:

- `$ref` to a **loopback** address: allowed by default (it is served by our own embedded
  `$ref` host on a bound loopback port, so nothing leaves the machine).
- `$ref` to a **public host**: requires `spec.hostile.allowExternalRefs: true` **and** an explicit
  allowlist of hosts. Never enabled in CI. Off in `--safe-mode`.
- `javascript:` / `file:` icon URLs are inert data from mcpmock's side (we never fetch them) and
  are gated only by `hostile.enabled`.

## Options considered

1. **Ship the corpus as plain text** — rejected on hazards 2 and 3 above.
2. **Do not bundle; require users to supply payloads** — rejected: `MOCK-507` says "bundled", and
   pushing the problem to users means every user assembles a worse corpus with no provenance.
3. **Encrypt with a key** — rejected: the key must ship with the binary, so it is obfuscation with
   extra steps and a false claim of security. Obfuscation is what we need; call it that.
4. **Separate optional module / separate image tag** — genuinely considered. Rejected for v0
   because it splits the release and complicates `MOCK-705`'s scenario library. Recorded as the
   escape hatch if the containment above proves insufficient.
5. **Encoded blob + manifest + three gates + loud signalling (chosen).**

## Consequences

**Positive.** The corpus cannot be emitted by accident, cannot grow unreviewed, and is always
visible in metrics, logs, journal and response headers when armed. Reviewers audit descriptions,
not payloads.

**Negative.** Obfuscation is friction for a legitimate maintainer who needs to inspect a payload;
`mcpmock corpus dump --id inj-014` exists for that, and its use is logged. Three gates mean a user
who wants hostile mode has three chances to be confused — mitigated by the placeholder string,
which names the failing gate.

**Requires a human decision.** Who authors and approves corpus content, and under what licence, is
not an architectural question. GAP-015.

**Forecloses.** Users adding payloads by editing a YAML file (they must go through the manifest).
Custom payloads are still possible via ordinary authored primitives (`MOCK-222`) — which is the
right place for them, since those are the user's own content and their own risk.
