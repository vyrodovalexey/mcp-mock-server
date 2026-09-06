package instance

import "github.com/vyrodovalexey/mcp-mock-server/scenario"

// export_test.go exposes unexported snapshot mutators to the external
// instance_test package so the copy-on-write tests can mutate a snapshot clone
// (as the control API will in a later phase) without those setters becoming part
// of the production API. It is a _test.go file, so it ships in no build.

// SetMethodEnabledForTest sets the method's enabled switch on a snapshot clone,
// replacing the switch map wholesale so the mutation obeys ADR-014's
// replace-not-edit rule. It is only ever called on the clone inside
// Instance.Mutate's fn, so it never mutates a published snapshot.
func (s *Snapshot) SetMethodEnabledForTest(method string, enabled bool) {
	next := make(map[string]scenario.MethodSwitch, len(s.methods)+1)
	for k, v := range s.methods {
		next[k] = v
	}
	e := enabled
	next[method] = scenario.MethodSwitch{Enabled: &e}
	s.methods = next
}
