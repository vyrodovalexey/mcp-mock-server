package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// This file resolves the extends chain and produces a single composed,
// schema-validated scenario document (ADR-008, MOCK-703).
//
// # Resolution order
//
// A document's extends list is resolved depth-first and left-to-right, with
// LATER winning. For a document D with extends [A, B]:
//
//  1. resolve A fully (including A's own extends), then B fully;
//  2. merge them left to right: base = merge(compose(A), compose(B));
//  3. merge D's own body over that base: result = merge(base, D).
//
// So precedence, lowest to highest, is: the leftmost leaf of the leftmost
// branch … the rightmost branch … the including document itself. A diamond
// (A and B both extend a common Base) composes Base twice; because merge is
// deterministic and idempotent on identical inputs, the diamond is well-defined
// and its shared keys resolve to Base's values unless an overlay changes them.
//
// # Cycle detection
//
// The active resolution stack is tracked by absolute path. Re-encountering a
// path already on the stack is a cycle; it is reported as *CycleError naming the
// full chain (a → b → a) rather than recursing until the stack overflows
// (MOCK-703.1).
//
// # Depth limit
//
// The extends chain depth is bounded by maxExtendsDepth. A chain deeper than the
// limit is rejected as *DepthError before it can exhaust memory or wall-clock,
// so a pathological or generated file cannot hang the loader and --validate
// exits non-zero cleanly (MOCK-701 / the composition part of the startup
// budget, MOCK-107).

// maxExtendsDepth bounds the extends resolution recursion. A legitimate
// base+overlays hierarchy is a handful of levels deep; 64 is far above any real
// use and still shallow enough that the recursion cannot overflow the stack.
// The limit counts nesting levels, not total documents, so a wide diamond is
// unaffected.
const maxExtendsDepth = 64

// CycleError reports an extends cycle: a document that transitively extends
// itself. Chain is the path chain from the first offending document to the
// repeat, so the message points a user straight at the loop (MOCK-703.1). It
// wraps [ErrValidation] so a caller branching on validation failure catches it.
type CycleError struct {
	// Chain is the ordered list of absolute file paths forming the cycle, with
	// the repeated path appearing as both the entry that closed the loop and,
	// implicitly, an earlier element.
	Chain []string
}

// Error renders the cycle as "a -> b -> a".
func (e *CycleError) Error() string {
	return "config: extends cycle detected: " + strings.Join(e.Chain, " -> ")
}

// Unwrap ties CycleError to ErrValidation for errors.Is.
func (e *CycleError) Unwrap() error { return ErrValidation }

// DepthError reports that an extends chain exceeded [maxExtendsDepth]. It names
// the path at which the limit was hit and wraps [ErrValidation].
type DepthError struct {
	// Limit is the exceeded depth bound.
	Limit int
	// At is the absolute path of the document whose resolution crossed the
	// limit.
	At string
}

// Error renders the depth-limit failure.
func (e *DepthError) Error() string {
	return fmt.Sprintf("config: extends chain exceeds maximum depth %d at %q", e.Limit, e.At)
}

// Unwrap ties DepthError to ErrValidation for errors.Is.
func (e *DepthError) Unwrap() error { return ErrValidation }

// ComposeFile loads the scenario document at path, resolves and merges its
// extends chain within root, validates the composed result against the embedded
// schema and the Phase 1 semantic rules, and returns the decoded
// [scenario.Document].
//
// Validation runs AFTER composition (ADR-008 / MOCK-703.8): an intermediate
// document reached via extends may be incomplete (missing spec, missing a
// required field) and still compose into a valid final document. Only the final
// merged tree is validated.
//
// Errors:
//   - a path escaping root is *[ErrScenarioRoot], reported before any read
//     (MOCK-703.7);
//   - a cycle is *[CycleError] naming the chain (MOCK-703.1);
//   - a chain deeper than [maxExtendsDepth] is *[DepthError];
//   - an unreadable file is a plain wrapped os error (exit code 2, MOCK-701.5);
//   - an invalid composed result is a *[ValidationError] (exit code 1).
func ComposeFile(root *ScenarioRoot, path string) (scenario.Document, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return scenario.Document{}, fmt.Errorf("config: resolve scenario path %q: %w", path, err)
	}
	c := &composer{root: root}
	tree, err := c.resolve(abs, nil, 0)
	if err != nil {
		return scenario.Document{}, err
	}
	return finalizeComposed(tree, abs)
}

// Compose composes an already-loaded root document with in-memory overlay
// documents applied last (highest precedence), returning the decoded, validated
// result. It resolves no files and enforces no sandbox: it is the programmatic
// overlay entry point (library WithOverlay, MOCK-703) for callers that build
// documents in code rather than on disk. base and each overlay are merged left
// to right, later winning, then the result is validated.
//
// base and overlays are treated as opaque trees: their extends lists, if any,
// are NOT resolved here (there is no file context to resolve them against).
// Callers wanting file-based extends resolution use [ComposeFile].
func Compose(source string, base []byte, overlays ...[]byte) (scenario.Document, error) {
	if source == "" {
		source = "<input>"
	}
	dir, err := mergeDirectivesFor()
	if err != nil {
		return scenario.Document{}, err
	}
	acc, err := decodeTree(base, source)
	if err != nil {
		return scenario.Document{}, err
	}
	for i, ov := range overlays {
		ovTree, decErr := decodeTree(ov, fmt.Sprintf("%s#overlay%d", source, i))
		if decErr != nil {
			return scenario.Document{}, decErr
		}
		acc = mergeTrees(acc, ovTree, "", dir)
	}
	return finalizeComposed(acc, source)
}

// composer carries the sandbox and the memoized merge-directive table across a
// single extends resolution.
type composer struct {
	root *ScenarioRoot
}

// resolve reads the document at abs, recursively resolves its extends chain, and
// returns the merged JSON tree for this document (its extends bases merged left
// to right, then this document's own body merged on top). stack is the active
// resolution path used for cycle detection; depth bounds recursion.
func (c *composer) resolve(abs string, stack []string, depth int) (any, error) {
	if depth > maxExtendsDepth {
		return nil, &DepthError{Limit: maxExtendsDepth, At: abs}
	}
	if idx := indexOf(stack, abs); idx >= 0 {
		chain := append(append([]string{}, stack[idx:]...), abs)
		return nil, &CycleError{Chain: chain}
	}

	raw, err := os.ReadFile(filepath.Clean(abs))
	if err != nil {
		return nil, fmt.Errorf("config: read scenario %q: %w", abs, err)
	}
	tree, err := decodeTree(raw, abs)
	if err != nil {
		return nil, err
	}

	extends, err := extendsOf(tree, abs)
	if err != nil {
		return nil, err
	}

	dir, err := mergeDirectivesFor()
	if err != nil {
		return nil, err
	}

	nextStack := append(append([]string{}, stack...), abs)
	var merged any // nil until the first base is resolved

	includingDir := filepath.Dir(abs)
	for _, target := range extends {
		baseAbs, resErr := c.root.Resolve(includingDir, target)
		if resErr != nil {
			return nil, resErr
		}
		baseTree, resolveErr := c.resolve(baseAbs, nextStack, depth+1)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if merged == nil {
			merged = baseTree
		} else {
			merged = mergeTrees(merged, baseTree, "", dir)
		}
	}

	// Merge this document's own body over the accumulated bases. The extends key
	// itself is stripped so it never appears in the composed output; a composed
	// document is a self-contained scenario, not a still-extending one.
	self := withoutExtends(tree)
	if merged == nil {
		return self, nil
	}
	return mergeTrees(merged, self, "", dir), nil
}

// decodeTree converts raw YAML/JSON bytes into a decoded JSON tree
// (map[string]any / []any / scalars). It reports a decode failure as a
// *[ValidationError] so a malformed included file is surfaced in the same shape
// as a schema failure.
func decodeTree(raw []byte, source string) (any, error) {
	jsonBytes, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, &ValidationError{
			Source:   source,
			Problems: []Problem{{Pointer: "", Message: "not valid YAML or JSON: " + err.Error()}},
		}
	}
	tree, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, &ValidationError{
			Source:   source,
			Problems: []Problem{{Pointer: "", Message: "not a JSON value: " + err.Error()}},
		}
	}
	return tree, nil
}

// extendsOf extracts the extends list from a decoded document tree, validating
// that it is an array of strings. A missing extends is nil (no bases). A
// malformed extends (not an array of strings) is a *[ValidationError]; the
// schema would also reject it, but composition reads it before validation, so it
// must fail cleanly here rather than panic.
func extendsOf(tree any, source string) ([]string, error) {
	obj, ok := tree.(map[string]any)
	if !ok {
		return nil, nil
	}
	v, has := obj["extends"]
	if !has || v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, &ValidationError{
			Source:   source,
			Problems: []Problem{{Pointer: "/extends", Message: "extends must be an array of strings"}},
		}
	}
	out := make([]string, 0, len(arr))
	for i, el := range arr {
		s, isStr := el.(string)
		if !isStr {
			return nil, &ValidationError{
				Source: source,
				Problems: []Problem{{
					Pointer: fmt.Sprintf("/extends/%d", i),
					Message: "extends entry must be a string path",
				}},
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// withoutExtends returns a shallow copy of the document tree with the top-level
// extends key removed, so the composed output is a plain scenario document
// carrying no residual extends directive. Non-object trees are returned as-is.
func withoutExtends(tree any) any {
	obj, ok := tree.(map[string]any)
	if !ok {
		return tree
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		if k == "extends" {
			continue
		}
		out[k] = v
	}
	return out
}

// finalizeComposed validates a composed JSON tree against the schema and the
// Phase 1 semantic rules (post-composition, MOCK-703.8), then decodes it into a
// typed [scenario.Document]. The decode goes through the canonical JSON encoding
// of the tree so the bytes handed to the decoder — and thus the resulting
// document — are deterministic regardless of Go map iteration order (ADR-008
// §0.1).
func finalizeComposed(tree any, source string) (scenario.Document, error) {
	if verr := Validate(tree); verr != nil {
		verr.Source = source
		return scenario.Document{}, verr
	}
	canon, err := canonicalJSON(tree)
	if err != nil {
		return scenario.Document{}, err
	}
	var doc scenario.Document
	if err := yaml.Unmarshal(canon, &doc); err != nil {
		return scenario.Document{}, &ValidationError{
			Source:   source,
			Problems: []Problem{{Pointer: "", Message: "composed document decode: " + err.Error()}},
		}
	}
	return doc, nil
}

// indexOf returns the index of s in stack, or -1.
func indexOf(stack []string, s string) int {
	for i, v := range stack {
		if v == s {
			return i
		}
	}
	return -1
}
