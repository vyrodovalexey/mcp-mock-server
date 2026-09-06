# Scenario reference

Audience: **user** / **operator** — you are writing or editing a scenario
document to configure a mock instance.

This reference is derived from
[`internal/config/schema/scenario.schema.json`](../internal/config/schema/scenario.schema.json),
the schema mcpmock actually validates against (`mcpmock validate` and
`serve` both load it), not from `specification/contracts/scenario.schema.json`,
which is the architect's forward-looking draft of the full 12-phase schema.
**Only the keys documented here are meaningful in this build.** The full
schema accepts many more keys (`faults`, `mrtr`, `subscriptions`, `auth`,
`catalogue.items` beyond the three built-ins, …) that validate successfully
but currently have **no runtime effect** — see
[`docs/not-yet-implemented.md`](not-yet-implemented.md).

## Table of contents

- [Document shape](#document-shape)
- [`metadata`](#metadata)
- [`spec.transport`](#spectransport)
- [`spec.discover`](#specdiscover)
- [`spec.switches`](#specswitches)
- [`spec.journal`](#specjournal)
- [Built-in tools (`catalogue.items[].behavior`)](#built-in-tools-catalogueitemsbehavior)
- [`extends` composition](#extends-composition)
- [Validating a scenario](#validating-a-scenario)

## Document shape

```yaml
apiVersion: mcpmock.dev/v1alpha1   # required, exact string
kind: Scenario                      # required: Scenario | Fleet
metadata:
  name: my-scenario                 # required
spec:                                # required when kind: Scenario
  # ... see below
```

`kind: Fleet` (multiple named instances in one document) validates against
the schema's `fleetSpec`, but Phase 1 does not exercise a multi-instance
fleet end to end — treat it as accepted-but-unverified in this build.

## `metadata`

| Key | Type | Notes |
|---|---|---|
| `name` | string | Required. Pattern `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`. |
| `description` | string | Free text. |
| `labels` | object of string→string | Free-form. |
| `coversRequirements` | array of `MOCK-nnn` strings | **Documentation only** — used to generate a scenario→requirement map. Has no runtime effect. |

## `spec.transport`

```yaml
spec:
  transport:
    kinds: [http]        # http, stdio, or both
    http:
      path: /mcp          # default "/mcp"
      listener: shared    # shared | own (default "shared")
```

| Key | Type | Default | Notes |
|---|---|---|---|
| `kinds` | array of `http`\|`stdio` | `["http"]` | |
| `http.path` | string | `/mcp` | Must start with `/`. |
| `http.listener` | `shared`\|`own` | `shared` | `own` is reserved for transport-fault scenarios not yet implemented. |
| `http.addr` | string | — | Only meaningful with `listener: own`. |
| `http.forceHTTP1`, `http.tls.*` | — | — | Schema-accepted, **not implemented in Phase 1** (TLS is tracked as `MOCK-106`, Phase 4). |

A scenario with `transport` given as a bare string (rather than this object)
fails validation — this is a real defect an earlier chart default hit
(`DEF-012`); the shape above is the one that passes.

## `spec.discover`

Controls the `server/discover` result. Every field here is a `switches`/
`discover` value the requirements label MOCK-201; the field *shapes* are
defined by the still-unratified wire annex (see the root
[README's GAP-003 note](../README.md#-the-wire-format-is-not-a-public-standard)).

| Key | Type | Notes |
|---|---|---|
| `supportedVersions` | array of string | Emitted verbatim. |
| `capabilities` | any | **Emitted verbatim, including unknown keys.** Absent unless you set it — the default scenario does not populate it. |
| `instructions` | string | Emitted verbatim. |
| `serverInfo` | any | Emitted verbatim. Absent by default; the server always attaches its own `_meta.serverInfo` (`{"name":"mcpmock","version":"..."}`) unless `switches.omitServerInfoMeta` is set. |
| `ttlMs`, `cacheScope` | integer\|null, string\|null | Accepted; paging/caching that would consume them is not implemented in Phase 1 (`MOCK-231`–`233`). |

## `spec.switches`

The behavioural toggles Phase 1 actually implements:

| Key | Type | Default | Effect (verified) |
|---|---|---|---|
| `validateMeta` | `strict`\|`lenient`\|`off` | `strict` | `strict`: missing `_meta.protocolVersion` or `_meta.clientCapabilities` → `-32602` + HTTP 400, `error.data.missing` names the fields. `lenient`: request proceeds, the journal's `metaValidation.missing` records the omission and a WARN is logged. `off`: no check; a non-object `_meta` is still rejected by JSON structure, not by this switch. |
| `omitResultType` | boolean | `false` | When `true`, drops the `resultType` discriminator from results. |
| `omitServerInfoMeta` | boolean | `false` | When `true`, drops the `_meta.serverInfo` block the server otherwise attaches to every result. |
| `selfCheck` | boolean | `true` | Before a result leaves the process, the server validates its own shape against the wire schema; a malformed result (e.g. produced by `omitResultType` combined with a self-check that requires it) is rejected internally with `-32603`/HTTP 500 instead of being sent malformed. Verified live: `TestSelfCheckFromScenarioFileRejectsMalformed`. |
| `methods.<name>.enabled` | boolean | `true` | Disables a method entirely. |
| `methods.<name>.hideFromCapabilities` | boolean | `false` | Removes the method's entry from `discover.capabilities` **without** disabling it — a hidden-but-enabled method still answers. Verified live: `TestHideFromCapabilitiesFromScenarioFile`. |
| `validateHeaders` | boolean | `true` | Accepted; header-mirroring behaviour it would gate (`MOCK-204`) is Phase 2. |
| `nonConformant.*` | booleans | all `false` | Accepted (11 named switches for deliberate spec violations); none has an implemented effect yet — these are Phase 2+ (`MOCK-245`, `MOCK-257`, era/session features). |

Example — a scenario that hides `tools/list` from capabilities while keeping
it callable, and turns on the self-check:

```yaml
apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata:
  name: selfcheck-hide
spec:
  transport:
    kinds: [http]
  switches:
    selfCheck: true
    methods:
      tools/list:
        enabled: true
        hideFromCapabilities: true
```

(This is [`testdata/selfcheck-hide.yaml`](../testdata/selfcheck-hide.yaml),
exercised by the functional and e2e test suites.)

## `spec.journal`

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | boolean | `true` | |
| `bodies` | `full`\|`truncate`\|`digest`\|`off` | `full` | Body capture mode. |
| `truncateBytes` | integer | `65536` | Only with `bodies: truncate`. |
| `maxRecords` | integer | `100000` | Per-instance ring size (the CLI/library default; see [`docs/library.md`](library.md) for the library's own smaller default). |
| `maxBytes` | integer | `268435456` | Per-instance byte budget. |
| `overflow` | `drop-oldest`\|`block`\|`error` | `drop-oldest` | |
| `validateArguments` | boolean | `true` | Schema-validates `tools/call` arguments; costs CPU under load (`MOCK-902` path). |

Credentials are **never** written to the journal in clear; the journal's
`credential` field, when present, carries a hash and non-sensitive parsed
claims only. There is no field capable of holding a raw credential value.

## Built-in tools (`catalogue.items[].behavior`)

Phase 1 ships exactly three built-in tool behaviors — `echo`, `sleep`,
`fail`. Their full semantics are normatively defined in
[`specification/contracts/builtin-tools.md`](../specification/contracts/builtin-tools.md);
this table is the config surface, cross-checked against the schema and
against live responses.

### `echo`

| Key | Type | Default | Effect |
|---|---|---|---|
| `echo.key` | string\|null | `null` | If set, echoes only `arguments[key]`. |
| `echo.structured` | boolean | `true` | Also emit `structuredContent`. |
| `echo.prefix` | string | `""` | Prepended to the text block. |

Verified: `echo` returns canonical JSON (keys sorted) in `content[0].text`
and a type-preserving `structuredContent.echoed`.

### `sleep`

| Key | Type | Default | Effect |
|---|---|---|---|
| `sleepMs` | integer | `0` | Default delay when the caller supplies no `durationMs`. |
| `maxSleepMs` | integer | `30000` | Upper bound on the *effective* delay from either source. |
| `onExceedMaxSleep` | `reject`\|`clamp` | `reject` | `reject`: `-32602` with `data.{requestedMs,maxSleepMs}`. `clamp`: sleeps `maxSleepMs` and sets `_meta.clamped=true`. |
| `allowClientDuration` | boolean | `true` | If `false`, the caller's `durationMs` is ignored. |

Duration is **milliseconds**, verified: `{"durationMs":50}` returns after
~50ms with `structuredContent.sleptMs: 50`. `sleep` observes context
cancellation.

### `fail`

| Key | Type | Default | Effect |
|---|---|---|---|
| `fail.mode` | `toolError`\|`protocolError` | `toolError` | `toolError`: a **successful** JSON-RPC result with `isError: true`. `protocolError`: a JSON-RPC error object. |
| `fail.code` | integer | `-32603` | `protocolError` only. |
| `fail.message` | string | `"mcpmock: deliberate failure"` | |
| `fail.data` | any | — | `protocolError` only, emitted verbatim. |
| `fail.allowClientOverride` | boolean | `false` | If `true`, `arguments.mode`/`code`/`message` may override the configured values. |

Verified: the default `fail` tool call returns `isError: true` inside a
`200`-equivalent JSON-RPC result, not a JSON-RPC error — **the scenario, not
the caller, decides the failure mode**, unless `allowClientOverride: true`.

## `extends` composition

```yaml
apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
extends:
  - base.yaml
metadata:
  name: overlay
spec:
  switches:
    validateMeta: lenient
```

Documents listed in `extends` are merged left to right, then this document is
merged last (highest precedence). Array fields marked
`x-mcpmock-merge-key` in the schema merge by that key (e.g. `catalogue.items`
merges by `name`); a `null` value at a key deletes it. `extends` paths are
resolved relative to the including file and are rejected if they would
resolve outside `--scenario-root` (when set) — including via `..` or a
symlink.

## Validating a scenario

```console
$ mcpmock validate path/to/scenario.yaml
mcpmock validate: path/to/scenario.yaml is valid (kind Scenario)
$ echo $?
0
```

An invalid document prints every problem as a JSON Pointer plus the failing
keyword and exits `1`:

```console
$ mcpmock validate /tmp/bad.yaml
mcpmock validate: /tmp/bad.yaml: scenario validation failed: 1 problem
  - /spec/bogusField: unknown key "bogusField" is not permitted here (additionalProperties: false)
$ echo $?
1
```

See [`docs/cli-reference.md`](cli-reference.md#validate) for `validate`'s
flags, including `--output json`.
