package httpx

import (
	"sort"
	"sync"
	"sync/atomic"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
)

// Mount binds one configurable MCP endpoint path to the instance and pipeline
// that serve it (MOCK-103). A single listener fans out to ≥200 instances by
// holding one Mount per instance, which is why the prefix router — not a
// per-instance listener — is the design that makes many instances share one
// port cheaply (ADR-012 §2).
//
// A Mount is immutable once placed in a [routeTable]; the router replaces the
// whole table atomically to add or remove a mount, so an in-flight request that
// already resolved a Mount keeps serving against a consistent view even while
// the set of mounts changes underneath it (ADR-012 §2, MOCK-702).
type Mount struct {
	// Path is the exact request path this instance is mounted at (default
	// "/mcp", configurable per instance, MOCK-102.2/MOCK-103).
	Path string
	// Instance is the owning instance the pipeline drives; the transport reads
	// only the engine.Instance surface and never a concrete *instance.Instance,
	// keeping the transport era-blind (architecture.md §6.1 rule 3).
	Instance engine.Instance
	// Pipeline is the nine-stage pipeline every request on this path flows
	// through. It is built once at instance construction and is safe for
	// concurrent use (internal/engine).
	Pipeline *engine.Pipeline
}

// routeTable is an immutable, exact-match path index. It is deliberately not a
// mutable map: the router publishes it through an atomic.Pointer and swaps a
// wholly new table on every add/remove, so lookups are lock-free and a
// concurrent mutation never races a reader (ADR-012 §2, architecture.md §7.2).
//
// Phase 1 routing is exact path match — the MCP endpoint is a single
// configurable path per instance, not a subtree. The type keeps a sorted key
// list so a future longest-prefix mode (subtree mounts) is an extension of the
// same immutable-and-swap structure rather than a rewrite.
type routeTable struct {
	// exact maps a mount path to its Mount for O(1) exact resolution, which is
	// the whole of Phase 1 routing.
	exact map[string]*Mount
	// paths is the sorted set of mount paths, retained so [routeTable.longest]
	// can offer deterministic longest-prefix resolution without allocating or
	// sorting on the request path.
	paths []string
}

// newRouteTable builds an immutable table from mounts. A later mount with the
// same path wins, which mirrors registry replacement semantics; callers hold no
// reference to the returned table's internals.
func newRouteTable(mounts []*Mount) *routeTable {
	exact := make(map[string]*Mount, len(mounts))
	for _, m := range mounts {
		exact[m.Path] = m
	}
	paths := make([]string, 0, len(exact))
	for p := range exact {
		paths = append(paths, p)
	}
	// Longest first so longest-prefix resolution is a single forward scan; the
	// sort is paid once at construction, never on the request path.
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) > len(paths[j])
		}
		return paths[i] < paths[j]
	})
	return &routeTable{exact: exact, paths: paths}
}

// lookup resolves an exact mount for path. It performs one map read and no
// allocation, which is what keeps routing off the MOCK-901 allocation budget.
func (t *routeTable) lookup(path string) (*Mount, bool) {
	m, ok := t.exact[path]
	return m, ok
}

// Router holds the current [routeTable] behind an atomic pointer so instances
// can be mounted and unmounted at runtime by the control API without restarting
// the listener (ADR-012 §2, MOCK-103/MOCK-702). The read path — [Router.match]
// — takes no lock; writers serialize on mu only to compute the next immutable
// table, never to block a reader.
//
// The zero Router is not usable; construct one with [newRouter].
type Router struct {
	// mu serializes writers (Mount/Unmount) so two concurrent mutations compute
	// their next table from a consistent base. Readers never take it.
	mu sync.Mutex
	// table is the current immutable routing table. Readers load it atomically;
	// writers store a wholly new table (ADR-012 §2).
	table atomic.Pointer[routeTable]
}

// newRouter returns a router seeded with mounts.
func newRouter(mounts []*Mount) *Router {
	r := &Router{}
	r.table.Store(newRouteTable(mounts))
	return r
}

// match resolves the Mount for path, lock-free, on the request hot path. A miss
// returns (nil, false) and the caller writes a well-formed not-found error
// rather than a bare 404 page (acceptance criterion 2).
func (r *Router) match(path string) (*Mount, bool) {
	return r.table.Load().lookup(path)
}

// Mounts returns the current mount set, sorted by path, for tests and control
// introspection. It allocates and is not for the request path.
func (r *Router) Mounts() []*Mount {
	t := r.table.Load()
	out := make([]*Mount, 0, len(t.exact))
	for _, p := range t.paths {
		out = append(out, t.exact[p])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Mount adds or replaces a mount atomically. It builds a new table from the
// current one plus m and swaps it, so a request that has already resolved a
// Mount is undisturbed and a concurrent reader sees either the old or the new
// table whole, never a torn one (MOCK-103, acceptance criterion 5).
func (r *Router) Mount(m *Mount) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.table.Load()
	next := make([]*Mount, 0, len(cur.exact)+1)
	for _, p := range cur.paths {
		if p == m.Path {
			continue
		}
		next = append(next, cur.exact[p])
	}
	next = append(next, m)
	r.table.Store(newRouteTable(next))
}

// Unmount removes the mount at path atomically, returning whether one was
// present. In-flight requests already dispatched to the removed instance run to
// completion; only new lookups miss (acceptance criterion 5).
func (r *Router) Unmount(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.table.Load()
	if _, ok := cur.exact[path]; !ok {
		return false
	}
	next := make([]*Mount, 0, len(cur.exact))
	for _, p := range cur.paths {
		if p == path {
			continue
		}
		next = append(next, cur.exact[p])
	}
	r.table.Store(newRouteTable(next))
	return true
}
