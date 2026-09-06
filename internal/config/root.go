package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrScenarioRoot is the sentinel wrapped by every path-sandbox rejection, so a
// caller (mcpmock validate, TASK-024) can branch on a traversal attempt with
// errors.Is. A sandbox rejection is a security control (ADR-008 / security.md
// path-traversal), not an ordinary I/O error, and it is reported BEFORE any
// file read (MOCK-703.7).
var ErrScenarioRoot = errors.New("extends path resolves outside --scenario-root")

// ScenarioRoot is the sandbox an extends chain may not escape. Every extends
// target — whether reached via "..", an absolute path, or a symlink that leaves
// the tree — is resolved and confirmed to lie within Root before it is opened
// (MOCK-703.7). A zero ScenarioRoot (empty Root) disables sandboxing and is
// only appropriate for in-memory composition with no file resolution; the
// file-based entry points always construct one.
type ScenarioRoot struct {
	// root is the cleaned, symlink-resolved absolute directory that bounds all
	// extends resolution. It is resolved once at construction so a symlinked
	// root itself is handled consistently with symlinked children.
	root string
}

// NewScenarioRoot resolves dir to a cleaned, absolute, symlink-evaluated path
// and returns the sandbox bounding all extends resolution beneath it. It
// resolves symlinks in the root itself so that a child path's resolved form is
// compared against the resolved root, not a symlinked alias of it — otherwise a
// symlinked root would make every containment check spuriously fail or pass.
func NewScenarioRoot(dir string) (*ScenarioRoot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("config: resolve scenario root %q: %w", dir, err)
	}
	// EvalSymlinks requires the path to exist. A non-existent root is a
	// configuration error worth reporting now rather than on first resolve.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("config: scenario root %q: %w", dir, err)
	}
	return &ScenarioRoot{root: resolved}, nil
}

// Dir returns the resolved absolute root directory.
func (r *ScenarioRoot) Dir() string { return r.root }

// Resolve turns an extends target — relative to the including file's directory
// — into an absolute path guaranteed to lie within the sandbox, or returns a
// *[ErrScenarioRoot]-wrapping error naming the offending target. It is the
// single choke point every extends read passes through.
//
// The check is TOCTOU-safe against symlink escape in the common case: it
// resolves symlinks on the resolvable prefix of the target and confirms the
// resolved path is still within the resolved root, so a symlink inside the root
// that points outside it is rejected rather than followed. A target that does
// not yet exist (a typo) resolves lexically and is still bounded, so a
// traversal via "../.." is rejected before the open that would report "not
// found".
//
// includingDir is the directory of the file that declared the extends entry;
// target is the raw string from that file's extends list.
func (r *ScenarioRoot) Resolve(includingDir, target string) (string, error) {
	if r == nil || r.root == "" {
		return "", fmt.Errorf("config: no scenario root configured for extends %q", target)
	}
	// Join the target against the including file's directory. filepath.Join
	// cleans the result, collapsing "." and ".." lexically; an absolute target
	// replaces includingDir entirely, which the containment check below then
	// rejects unless it happens to fall within root.
	var joined string
	if filepath.IsAbs(target) {
		joined = filepath.Clean(target)
	} else {
		joined = filepath.Join(includingDir, target)
	}

	// Resolve symlinks on the longest existing prefix so a symlink that escapes
	// the root is caught, while a not-yet-existing leaf (a typo) is still
	// bounded lexically.
	resolved := resolveExistingPrefix(joined)

	if !withinRoot(r.root, resolved) {
		return "", fmt.Errorf("%w: %q resolves to %q, outside %q",
			ErrScenarioRoot, target, resolved, r.root)
	}
	return joined, nil
}

// resolveExistingPrefix evaluates symlinks on the longest existing prefix of
// path and re-appends the non-existent remainder. filepath.EvalSymlinks fails
// on a non-existent path, so a target naming a file that does not exist yet
// (which composition must still bound before reporting "not found") is handled
// by walking up to the first existing ancestor, resolving that, and rejoining.
func resolveExistingPrefix(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := path
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding an existing prefix;
			// fall back to the lexical clean, which is still bounded.
			return filepath.Clean(path)
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
		dir = parent
	}
}

// withinRoot reports whether candidate is root itself or lies beneath it. It
// compares cleaned absolute paths segment-wise via a separator-terminated
// prefix test, so "/rootx" is not treated as being within "/root".
func withinRoot(root, candidate string) bool {
	if candidate == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(candidate, prefix)
}
