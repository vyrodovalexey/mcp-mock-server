package config

// This file exposes internal instrumentation to the package's white-box tests
// only. It is a _test.go file, so it never enters a production build and the
// symbols it exposes have no production reader — keeping the instrumentation
// itself out of the shipped API.

import "path/filepath"

// compileCountForTest returns how many times the embedded schema has actually
// been compiled in this process. The compile-once test asserts it is exactly 1
// regardless of how many documents are loaded (acceptance criterion 7).
func compileCountForTest() int {
	return int(compileCount.Load())
}

// composeTreeForTest resolves the extends chain at path within root and returns
// the canonical JSON bytes of the merged tree WITHOUT validating or decoding
// it. The merge-table and determinism tests assert on this raw composed tree,
// including intentionally-incomplete intermediates (MOCK-703.8), before typed
// decoding would reject them.
func composeTreeForTest(root *ScenarioRoot, path string) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c := &composer{root: root}
	tree, err := c.resolve(abs, nil, 0)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(tree)
}

// mergeDirectivesForTest exposes the process-wide merge-directive table to the
// schema-lint test (703.5), which walks the schema to assert every
// object-bearing array declares a directive.
func mergeDirectivesForTest() (*mergeDirectives, error) {
	return mergeDirectivesFor()
}

// arrayKeyForTest reports the resolved merge key for an instance path pattern,
// so a test can assert the directive resolver reads the schema correctly.
func (d *mergeDirectives) arrayKeyForTest(path string) (string, bool) {
	return d.arrayKey(path)
}

// mergeTreesForTest exposes the raw tree merge to white-box unit tests that
// assert one ADR-008 table row at a time without any file I/O.
func mergeTreesForTest(base, overlay any, dir *mergeDirectives) any {
	return mergeTrees(base, overlay, "", dir)
}
