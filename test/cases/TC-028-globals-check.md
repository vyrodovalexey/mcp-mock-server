# TC-028-G — Mechanical no-globals / no-`init()`-side-effect check

| | |
|---|---|
| **Task** | `TASK-028` |
| **Requirements** | `MOCK-107` (107.5, 107.6) |
| **Level** | functional (mechanical AST scan of the public packages) |
| **Harness** | a runnable Go program in the embed module (`globalscheck` package) plus its own tests; **not** wired into the root `Makefile` (that is `TASK-033`'s scope — see report) |

## Why this case exists

`MOCK-107.5` requires no exported package-level mutable state and `107.6`
requires no `init()` that performs I/O, compiles a schema, generates a key or
allocates more than 4 KiB. A grep is defeatable by accident (a future contributor
adds `var Foo = ...` on one line and the pattern misses it). This check is
**AST-based**: it parses the source of the public packages with `go/parser`,
walks the declarations, and classifies them, so it is mechanical and robust.

## Allowlist

The check permits, as immutable or non-state:
- `const` declarations (immutable by definition),
- typed/untyped `var` of `error` created by `errors.New(...)` at package scope —
  the exported sentinels (`ErrValidation`, …) are effective constants and are the
  documented public error contract; they are matched structurally (RHS is a call
  to `errors.New`/`fmt.Errorf`), not by name, and
- `var _ = ...` blank-identifier compile-time assertions (no runtime state).

Anything else at package scope that is a mutable `var` is a finding.

---

## TC-028-G.1 — Public packages have zero mutable package-level vars outside the allowlist (`107.5`)

**Given** the four public packages `mcpmock` (root), `assert`, `journalapi`,
`scenario` — their non-test `.go` files.
**When** the AST scanner walks every top-level `GenDecl`.
**Then** it reports zero mutable exported/unexported package-level `var`
declarations outside the allowlist.
**Observable outcome.** Scanner exits 0 with an empty findings list. **Runtime:**
< 2 s.

## TC-028-G.2 — No `init()` performs a forbidden side effect (`107.6`)

**Given** the same package set.
**When** the scanner finds every `func init()` and inspects its body.
**Then** either there is no `init()`, or its body contains no call whose callee
suggests I/O, schema compilation, key generation or large allocation
(`os.*`, `net.*`, `Compile`, `GenerateKey`, `make([...]byte, >4096)`, etc.), per a
documented conservative denylist. Any match is a finding to escalate.
**Observable outcome.** Zero findings. **Runtime:** < 1 s.

## TC-028-G.3 — Negative case: a deliberately injected global is detected (`107.5`)

**Given** a synthetic in-memory source file containing
`var Leaked = map[string]int{}`.
**When** the scanner runs over it.
**Then** it reports exactly one finding naming `Leaked`.
**Observable outcome.** The scanner's own unit test asserts the finding, proving
the check *fails* when a global exists — the negative case required by acceptance
criterion 5. **Runtime:** < 1 s.
