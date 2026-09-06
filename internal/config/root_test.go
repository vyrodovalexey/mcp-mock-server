package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestScenarioRoot_RejectsDotDotEscape proves an extends target escaping the
// root via ".." is rejected as *ErrScenarioRoot BEFORE any read (703.7).
func TestScenarioRoot_RejectsDotDotEscape(t *testing.T) {
	root, err := NewScenarioRoot(filepath.Join("testdata", "compose"))
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	_, err = ComposeFile(root, filepath.Join("testdata", "compose", "traversal.yaml"))
	if err == nil {
		t.Fatalf("expected a scenario-root rejection")
	}
	if !errors.Is(err, ErrScenarioRoot) {
		t.Fatalf("error is not ErrScenarioRoot: %v", err)
	}
}

// TestScenarioRoot_RejectsAbsoluteOutside proves an absolute extends target
// outside the root is rejected.
func TestScenarioRoot_RejectsAbsoluteOutside(t *testing.T) {
	root, err := NewScenarioRoot(t.TempDir())
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	includingDir := root.Dir()
	// An absolute path to a system directory is outside the temp root.
	_, err = root.Resolve(includingDir, "/etc/passwd")
	if err == nil {
		t.Fatalf("expected rejection of absolute outside path")
	}
	if !errors.Is(err, ErrScenarioRoot) {
		t.Errorf("error is not ErrScenarioRoot: %v", err)
	}
}

// TestScenarioRoot_RejectsSymlinkEscape proves a symlink INSIDE the root that
// points OUTSIDE it is rejected — resolution follows the link and re-checks
// containment, so the sandbox cannot be bypassed by a link (703.7, the case the
// task calls out specifically).
func TestScenarioRoot_RejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "root")
	outsideDir := filepath.Join(base, "outside")
	for _, d := range []string{rootDir, outsideDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// A real file outside the root.
	secret := filepath.Join(outsideDir, "secret.yaml")
	if err := os.WriteFile(secret, []byte("apiVersion: x\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	// A symlink inside the root pointing at the outside file.
	link := filepath.Join(rootDir, "link.yaml")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	root, err := NewScenarioRoot(rootDir)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	// Resolving the symlink target must be rejected: it resolves outside root.
	_, err = root.Resolve(rootDir, "link.yaml")
	if err == nil {
		t.Fatalf("expected rejection of symlink escaping the root")
	}
	if !errors.Is(err, ErrScenarioRoot) {
		t.Errorf("error is not ErrScenarioRoot: %v", err)
	}
}

// TestScenarioRoot_AllowsInside proves a legitimate relative target inside the
// root resolves without error.
func TestScenarioRoot_AllowsInside(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "overlays")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(sub, "o.yaml")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	root, err := NewScenarioRoot(dir)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	got, err := root.Resolve(dir, filepath.Join("overlays", "o.yaml"))
	if err != nil {
		t.Fatalf("legitimate target rejected: %v", err)
	}
	// The resolved path must point at the real file inside the root.
	if filepath.Clean(got) != filepath.Clean(target) {
		t.Errorf("resolved %q, want %q", got, target)
	}
}

// TestScenarioRoot_SymlinkedRootAllowsChildren proves that when the root itself
// is reached via a symlink, children inside it are still allowed — the root is
// symlink-resolved at construction so containment is compared consistently.
func TestScenarioRoot_SymlinkedRootAllowsChildren(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.MkdirAll(realRoot, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	child := filepath.Join(realRoot, "c.yaml")
	if err := os.WriteFile(child, []byte("x\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	root, err := NewScenarioRoot(linkRoot)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := root.Resolve(linkRoot, "c.yaml"); err != nil {
		t.Errorf("child under symlinked root wrongly rejected: %v", err)
	}
}
