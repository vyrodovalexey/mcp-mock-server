// Package globalscheck is a mechanical, AST-based audit of the mcpmock public
// packages for two MOCK-107 properties:
//
//   - 107.5: no exported (or unexported) package-level MUTABLE state — a global
//     `var` outside a small, structurally-matched allowlist. A process-global
//     mutable variable in a library shared by many host tests is a correctness
//     hazard: one host test mutates it and another sees the change.
//
//   - 107.6: no init() that performs I/O, compiles a schema, generates a key or
//     allocates more than 4 KiB. An init() side effect runs on import, before
//     the host's TestMain, invisibly to the host.
//
// It is AST-based, not a grep, so a future contributor cannot defeat it by
// accident with unusual formatting: it parses each source file with go/parser
// and walks the declaration list.
//
// It is invoked as a runnable program (see cmd sibling / the RunReport helper)
// and is exercised by globalscheck_test.go, which includes the REQUIRED negative
// case: a synthetic source containing a deliberate global must be flagged.
package globalscheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one violation the scan reported.
type Finding struct {
	// Kind is "mutable-global" or "init-side-effect".
	Kind string
	// Pos is the source position "file:line:col".
	Pos string
	// Detail explains what was found (the identifier name, or the offending
	// call in an init body).
	Detail string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s: %s", f.Kind, f.Pos, f.Detail)
}

// forbiddenInitCallees is the conservative denylist of call targets an init()
// body must not contain (107.6). Matched against the last selector segment or
// the function identifier, so both `os.ReadFile` and a dot-imported `ReadFile`
// are caught. It is intentionally broad: an init() in a public package should do
// essentially nothing, so a false positive here is a signal to review, not a
// nuisance.
var forbiddenInitCallees = []string{
	// I/O.
	"ReadFile", "WriteFile", "Open", "OpenFile", "Create", "Stat", "ReadDir",
	"Dial", "DialContext", "Listen", "ListenAndServe", "Get", "Post",
	// schema compilation.
	"Compile", "MustCompile", "CompileString", "NewCompiler",
	// key / randomness generation.
	"GenerateKey", "Read", // crypto/rand.Read, rsa.GenerateKey, etc.
	// goroutine / timer scheduling that would outlive import.
	"Go", "AfterFunc", "NewTicker", "NewTimer",
}

// ScanDir parses every non-test .go file in dir (one package) and returns the
// findings. It does not recurse; call it once per package directory.
func ScanDir(dir string) ([]Finding, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		name := fi.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dir, err)
	}
	var findings []Finding
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			findings = append(findings, scanFile(fset, file)...)
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Pos < findings[j].Pos })
	return findings, nil
}

// scanSrc parses a single in-memory source (used by the negative test) under the
// given filename and returns findings.
func scanSrc(filename, src string) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	return scanFile(fset, file), nil
}

func scanFile(fset *token.FileSet, file *ast.File) []Finding {
	var findings []Finding
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue // const and type are immutable / not state.
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if allowedVarSpec(vs) {
					continue
				}
				for _, name := range vs.Names {
					findings = append(findings, Finding{
						Kind:   "mutable-global",
						Pos:    fset.Position(name.Pos()).String(),
						Detail: fmt.Sprintf("package-level var %q is mutable state", name.Name),
					})
				}
			}
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == "init" && d.Body != nil {
				findings = append(findings, scanInitBody(fset, d.Body)...)
			}
		}
	}
	return findings
}

// allowedVarSpec reports whether a package-level var spec is in the allowlist:
//   - a blank-identifier compile-time assertion (`var _ = ...`, `var _ T = ...`),
//     which holds no runtime state;
//   - a var whose every RHS value is a call to errors.New or fmt.Errorf — the
//     documented exported error sentinels, matched STRUCTURALLY (by the callee
//     shape) rather than by name, so the allowance cannot be widened by naming a
//     mutable var "ErrFoo".
func allowedVarSpec(vs *ast.ValueSpec) bool {
	allBlank := true
	for _, n := range vs.Names {
		if n.Name != "_" {
			allBlank = false
			break
		}
	}
	if allBlank {
		return true
	}
	if len(vs.Values) == 0 || len(vs.Values) != len(vs.Names) {
		return false
	}
	for _, v := range vs.Values {
		if !isErrorSentinelCall(v) {
			return false
		}
	}
	return true
}

// isErrorSentinelCall reports whether expr is errors.New(...) or fmt.Errorf(...).
func isErrorSentinelCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch {
	case pkg.Name == "errors" && sel.Sel.Name == "New":
		return true
	case pkg.Name == "fmt" && sel.Sel.Name == "Errorf":
		return true
	}
	return false
}

// scanInitBody inspects an init() body for forbidden calls and oversized byte
// allocations (107.6).
func scanInitBody(fset *token.FileSet, body *ast.BlockStmt) []Finding {
	var findings []Finding
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name := calleeName(call.Fun); name != "" {
			for _, bad := range forbiddenInitCallees {
				if name == bad {
					findings = append(findings, Finding{
						Kind:   "init-side-effect",
						Pos:    fset.Position(call.Pos()).String(),
						Detail: fmt.Sprintf("init() calls %q (forbidden by MOCK-107.6)", name),
					})
				}
			}
			// make([]byte, N) with N > 4096.
			if name == "make" {
				if f := oversizedByteMake(fset, call); f != nil {
					findings = append(findings, *f)
				}
			}
		}
		return true
	})
	return findings
}

// calleeName returns the final identifier of a call target: "ReadFile" for
// os.ReadFile, "make" for make, "" for anything else.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// oversizedByteMake reports a finding if call is make([]byte, N) with a literal
// N greater than 4096 (the 107.6 threshold).
func oversizedByteMake(fset *token.FileSet, call *ast.CallExpr) *Finding {
	if len(call.Args) < 2 {
		return nil
	}
	at, ok := call.Args[0].(*ast.ArrayType)
	if !ok {
		return nil
	}
	elt, ok := at.Elt.(*ast.Ident)
	if !ok || elt.Name != "byte" {
		return nil
	}
	lit, ok := call.Args[1].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return nil // non-literal size: cannot statically judge, leave to review.
	}
	var n int64
	if _, err := fmt.Sscan(lit.Value, &n); err != nil {
		return nil
	}
	if n > 4096 {
		f := Finding{
			Kind:   "init-side-effect",
			Pos:    fset.Position(call.Pos()).String(),
			Detail: fmt.Sprintf("init() allocates make([]byte, %d) > 4096 (MOCK-107.6)", n),
		}
		return &f
	}
	return nil
}

// PublicPackageDirs resolves the source directories of the public packages under
// parentRoot (the mcpmock module root). The public surface is the root package
// plus assert, journalapi and scenario.
func PublicPackageDirs(parentRoot string) []string {
	return []string{
		parentRoot, // root package mcpmock (root .go files)
		filepath.Join(parentRoot, "assert"),
		filepath.Join(parentRoot, "journalapi"),
		filepath.Join(parentRoot, "scenario"),
	}
}

// RunReport scans every public package under parentRoot and returns all
// findings. It is the entry point both the CLI (main) and the test use.
func RunReport(parentRoot string) ([]Finding, error) {
	var all []Finding
	for _, dir := range PublicPackageDirs(parentRoot) {
		fs, err := ScanDir(dir)
		if err != nil {
			return nil, err
		}
		all = append(all, fs...)
	}
	return all, nil
}
