---
id: ADR-008
title: Scenario schema — versioned document, JSON Schema 2020-12, keyed strategic-merge composition
status: accepted
date: 2026-09-04
reversibility: HARD — the scenario file is the primary user-facing contract (§10.3)
requirements: MOCK-701, MOCK-703, MOCK-705, §10.3
---

# ADR-008 — Scenario schema and composition

## Context

`MOCK-701`: a single declarative YAML/JSON file, **versioned**, **schema-validated on load**, with
a `--validate` mode exiting non-zero on error. `MOCK-703`: composition (base + overlays) so a
fleet of N mocks is defined without copy-paste. §10.3 makes the JSON Schema itself a deliverable.

The scenario file is the largest surface in the product. It carries: catalogue generation
parameters, authored primitives with deliberately invalid schemas (`MOCK-224`, `MOCK-225`), the
whole §5 fault vocabulary, auth configuration, era selection, pagination behaviour, subscription
behaviour, MRTR rounds, and journal configuration. It is where users spend their time, and it is
the thing that will be wrong most often.

Composition is where config systems go to die. Kubernetes' strategic merge patch, Helm's value
coalescing and Kustomize all have well-known sharp edges around **list merging**.

## Decision

### Document shape

```yaml
apiVersion: mcpmock.dev/v1alpha1     # MOCK-701 "versioned"
kind: Scenario                       # or Fleet
metadata:
  name: happy-path
  description: ...
spec:
  seed: 42                           # optional; --seed overrides
  era: modern                        # modern | legacy | dual | probe
  transport: {...}
  catalogue: {...}
  paging: {...}
  mrtr: {...}
  subscriptions: {...}
  auth: {...}
  faults: [...]
  journal: {...}
  switches: {...}                    # per-method enable/disable (MOCK-202), omission switches (MOCK-209)
```

`kind: Fleet` wraps N instances:

```yaml
apiVersion: mcpmock.dev/v1alpha1
kind: Fleet
spec:
  defaults: { extends: [ "base.yaml" ] }
  instances:
    - name: upstream-a
      mountPath: /mock/a/mcp
      extends: [ "base.yaml", "overlays/flaky.yaml" ]
      spec: { ... }                  # inline overlay, highest precedence
  generate:                          # MOCK-904: 200 instances without copy-paste
    count: 200
    nameTemplate: "upstream-{{i:03d}}"
    mountTemplate: "/mock/{{name}}/mcp"
    extends: [ "base.yaml" ]
    vary:                            # deterministic per-instance variation
      - path: spec.catalogue.tools.count
        values: [10, 100, 5000]
```

### Composition semantics (`MOCK-703`)

Documents are decoded to JSON trees (via `sigs.k8s.io/yaml`, so YAML and JSON are literally the
same input) and merged **left to right, later wins**:

| Node type | Rule |
|---|---|
| Object | Recursive merge. |
| Scalar | Replace. |
| `null` | **Delete the key.** Explicit removal is required (an overlay that turns off a base's fault list). |
| Array of objects with a `name` field | **Merge by `name`**, order = base order then new names appended. |
| Array of objects without `name`, array of scalars | **Replace wholesale.** |
| Any array | Overridable per-node with the sibling directive `$patch: replace \| merge \| append`. |

The keyed-list rule is stated in the JSON Schema itself via a custom annotation
`x-mcpmock-merge-key: name`, so the merge behaviour is discoverable from the schema rather than
buried in code. Every list in the schema that carries objects **must** declare either
`x-mcpmock-merge-key` or `x-mcpmock-merge: replace`; a schema unit test enforces this, which
prevents the "we forgot how this list merges" failure mode.

`extends` is resolved depth-first with cycle detection; paths are relative to the *including*
file; absolute paths and `..` escapes outside a configured `--scenario-root` are rejected
(path-traversal control, see `security.md`).

### Validation (`MOCK-701`)

JSON Schema **draft 2020-12**, hand-maintained at `specification/contracts/scenario.schema.json`
and `go:embed`ed. Validation runs:

- always on load (fail fast, exit non-zero);
- **after** composition, not before — an overlay may legitimately produce an incomplete
  intermediate;
- additionally per-document in `--validate --strict`, which also reports unknown keys.

`additionalProperties: false` throughout. Typos in a fault name silently doing nothing is the
single most expensive failure mode for a tool like this.

`mcpmock validate` output is machine-readable (`--output json`) with JSON Pointer locations, and
maps back to source file + line by tracking provenance during composition (each merged node
records `{file, line}` in a side table). Exit codes: `0` ok, `1` schema/semantic error,
`2` I/O error. Semantic checks beyond JSON Schema (cross-field rules such as "size fault larger
than `journal.maxBytes/16` requires `journal.bodies != full`", ADR-005) live in
`scenario.Validate()` and are reported in the same format.

### Deliberate invalidity

`MOCK-224`/`MOCK-225` require the scenario to carry *invalid* JSON Schemas and *invalid*
annotations, which must survive our validation untouched. Resolution: those fields are typed as
`json.RawMessage` in the scenario schema (`"inputSchema": {}` — any JSON) and are **never**
validated by us, only stored and emitted verbatim. The schema documents this explicitly so the
distinction "mcpmock validates the scenario, not the payloads inside it" is visible.

## Options considered

1. **Go structs + `yaml.Unmarshal`, no schema** — rejected: §10.3 requires the JSON Schema as a
   deliverable, and struct tags alone give no `additionalProperties: false` and no editor support.
2. **Generate the schema from Go structs** (`invopop/jsonschema` or similar) — attractive, and
   avoids drift, but the deliberately-untyped fields, the merge-key annotations and the
   cross-field rules all need hand annotation anyway, and it adds a codegen step plus a
   dependency. **Rejected for v0; a `make schema-check` test asserts the Go structs and the
   hand-written schema agree** (round-trips every scenario in `scenarios/` through both).
3. **CUE / Jsonnet / Starlark for composition** — rejected: a new language for users to learn, a
   heavy dependency, and Starlark/Jsonnet reintroduce nondeterminism risk. `MOCK-703` asks for
   base+overlay, not a programming language.
4. **Kustomize-style separate patch files** — rejected: two file formats.
5. **`extends` + keyed strategic merge (chosen).**

## Consequences

**Positive.** One format, one schema, editor completion via `$schema`. A 200-instance fleet is a
20-line `generate:` block. Deletion is expressible (`null`), which most naive merge schemes miss.

**Negative.** Hand-maintained schema drifts from Go structs unless `make schema-check` is kept
green — a real ongoing cost, explicitly accepted. Merge-by-`name` means every mergeable list item
must have a `name`; fault rules therefore require an `id`, which is also what `MOCK-501`…`508`
need for control-API triggering, so this is a happy accident rather than a tax.

**Forecloses.** Comments surviving composition (JSON has none), and YAML anchors across files.
Users wanting DRY within one file may still use YAML anchors — they resolve before we see them.

**Open.** `apiVersion` is `v1alpha1` until the wire annex (ADR-019) is ratified, because the
protocol shape leaks into the scenario shape. Stated in `gap-analysis.md` GAP-003.
