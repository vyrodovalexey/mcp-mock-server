package mcpmock

import "github.com/vyrodovalexey/mcp-mock-server/internal/instance"

// InternalInstance returns the internal *instance.Instance named name, for
// white-box tests that must drive the copy-on-write write path
// ([instance.Instance.Mutate]) directly to prove MOCK-702 semantics through the
// control surface. It is exported only in the test build; production callers use
// the public facade.
func InternalInstance(s *Server, name string) *instance.Instance {
	for _, in := range s.instances {
		if in.inst.Name() == name {
			return in.inst
		}
	}
	return nil
}
