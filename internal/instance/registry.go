package instance

import (
	"sync"
	"sync/atomic"
)

// Registry maps a mount path to an [*Instance] behind an
// atomic.Pointer[routeTable] (ADR-007 §2), so the control API can add or remove
// an instance at runtime without restarting a listener and without disturbing an
// in-flight lookup on any other instance. Lookup is lock-free — a single atomic
// load plus a map read — which is what keeps instance routing off the 20 000 rps
// contention path (MOCK-901). Mutations serialize on a writer mutex and publish a
// new immutable table; they never block a reader.
//
// The Phase 1 binary constructs one instance, but the registry is built for N
// (MOCK-103, MOCK-904) so a later fleet does not have to retrofit every call
// site. A Registry holds no process-global state (ADR-007); a Server owns its
// own. Construct one with [NewRegistry]; the zero value is not usable.
type Registry struct {
	// table is the current immutable route table. Readers Load() it lock-free;
	// a mutation builds a new table under mu and Store()s it, so a reader sees
	// either the whole old table or the whole new one.
	table atomic.Pointer[routeTable]
	// mu serializes Add/Remove writers only. Readers never take it.
	mu sync.Mutex
}

// routeTable is one immutable snapshot of the path→instance routing. It is never
// mutated after publication: [Registry.Add] and [Registry.Remove] each build a
// fresh table by copying and editing the copy, then publish it atomically. The
// copy cost is O(#instances) per mutation, which is negligible against a
// low-frequency control-API operation and buys a lock-free read path.
type routeTable struct {
	byPath map[string]*Instance
}

// lookup returns the instance mounted at path and whether one exists. It is a
// pure read of the immutable table, safe for unbounded concurrent callers.
func (t *routeTable) lookup(path string) (*Instance, bool) {
	inst, ok := t.byPath[path]
	return inst, ok
}

// clone returns a copy of the table with the same entries. It is the base of the
// copy-on-write mutation: a writer clones, edits the clone, and publishes it, so
// the table a concurrent reader holds is never mutated.
func (t *routeTable) clone() *routeTable {
	next := &routeTable{byPath: make(map[string]*Instance, len(t.byPath)+1)}
	for k, v := range t.byPath {
		next.byPath[k] = v
	}
	return next
}

// NewRegistry returns an empty registry ready for [Registry.Add]. It publishes an
// empty immutable table so the read path never observes a nil table.
func NewRegistry() *Registry {
	r := &Registry{}
	r.table.Store(&routeTable{byPath: make(map[string]*Instance)})
	return r
}

// Lookup returns the instance mounted at path and whether one is registered. It
// is lock-free: one atomic table load and one map read, with no allocation, safe
// for unbounded concurrent callers on the request path.
func (r *Registry) Lookup(path string) (*Instance, bool) {
	return r.table.Load().lookup(path)
}

// Add registers inst under its mount path and reports whether it was added. It
// returns false without changing the registry when an instance is already
// mounted at that path — a duplicate registration is refused, not silently
// overwritten, because two instances at one path is a configuration error, not a
// last-wins situation. Add serializes on the writer mutex and publishes a fresh
// table atomically, so a concurrent [Registry.Lookup] on any other path is
// undisturbed.
func (r *Registry) Add(inst *Instance) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.table.Load()
	if _, exists := cur.byPath[inst.mountPath]; exists {
		return false
	}
	next := cur.clone()
	next.byPath[inst.mountPath] = inst
	r.table.Store(next)
	return true
}

// Remove unregisters the instance mounted at path and reports whether one was
// removed. Removing an instance publishes a fresh table atomically and does not
// disturb an in-flight request already routed to another instance (MOCK-103.4
// mechanics): that request holds its own *Instance and never re-consults the
// registry. Removing a path that is not registered is a no-op returning false.
func (r *Registry) Remove(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.table.Load()
	if _, exists := cur.byPath[path]; !exists {
		return false
	}
	next := cur.clone()
	delete(next.byPath, path)
	r.table.Store(next)
	return true
}

// Len returns the number of registered instances. It is a lock-free read of the
// current table.
func (r *Registry) Len() int {
	return len(r.table.Load().byPath)
}

// Instances returns the registered instances in an unspecified order. It
// allocates and is intended for lifecycle and test use, never the request path.
// The returned slice is a fresh copy; mutating it does not affect the registry.
func (r *Registry) Instances() []*Instance {
	t := r.table.Load()
	out := make([]*Instance, 0, len(t.byPath))
	for _, inst := range t.byPath {
		out = append(out, inst)
	}
	return out
}
