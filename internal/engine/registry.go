package engine

import (
	"context"
	"encoding/json"
)

// Handler is one method handler in the dispatch stage (stage 6). It is written
// once, against the transport-neutral [Exchange], and serves every transport
// (MOCK-102). TASK-018 implements the three Phase 1 handlers (server/discover,
// tools/list, tools/call) against this interface.
//
// A handler returns either a result (as raw JSON, so authored key order survives
// per MOCK-222.4) or a [*Fault] carrying a wire error code; it never constructs
// a JSON-RPC error object itself — that is emit.go's sole responsibility
// (ADR-019, architecture.md §8). It reads configuration from ex.Snapshot and
// randomness from ex.Rand (drawing in a fixed order, ADR-002 rule 2), and it
// honors ex.Ctx: a handler that holds a request (sleep) selects on
// ctx.Done() rather than sleeping through cancellation (MOCK-212).
type Handler interface {
	// Handle produces the raw result JSON for ex, or a [*Fault] describing a
	// wire error. ctx is ex.Ctx, passed explicitly so contextcheck
	// (.golangci.yml:50) is satisfied and so the signature reads naturally.
	// The returned result bytes are emitted verbatim; the handler is
	// responsible for their key order.
	Handle(ctx context.Context, ex *Exchange) (json.RawMessage, *Fault)
}

// HandlerFunc adapts a function to a [Handler], for handlers that need no
// receiver state (the Phase 1 handlers are stateless, reading everything from
// the snapshot).
type HandlerFunc func(ctx context.Context, ex *Exchange) (json.RawMessage, *Fault)

// Handle calls f. It makes a HandlerFunc a [Handler].
func (f HandlerFunc) Handle(ctx context.Context, ex *Exchange) (json.RawMessage, *Fault) {
	return f(ctx, ex)
}

// Registry maps a JSON-RPC method name to its [Handler] (architecture.md §5
// "Method handlers (registry)"). It is built once at instance construction and
// is read-only on the request path, so dispatch is a single map lookup with no
// lock and no allocation. Adding a method is a registry entry, mirroring the
// wire package's table-not-switch discipline.
//
// A Registry holds no process-global state (ADR-007): each instance owns its
// own, though in practice all instances of one scenario share an identical set
// built from the same wire vocabulary. It is safe for concurrent reads once
// built; [Registry.Register] is not safe to call concurrently with dispatch and
// is intended for the construction phase only.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry returns an empty registry ready for [Registry.Register]. The
// caller registers the method set (the three Phase 1 methods in TASK-018) at
// construction, before any request is dispatched.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// Register binds method to h, replacing any previous handler for that method.
// The method name comes from internal/wire (no literal here, ADR-019). It is a
// construction-time call, not safe against concurrent dispatch.
func (r *Registry) Register(method string, h Handler) {
	r.handlers[method] = h
}

// Lookup returns the handler for method and whether one is registered. A missing
// handler is how the dispatch stage learns a method is unknown and answers
// -32601 (the same answer a disabled method gets); it is not an error condition
// of Lookup itself.
func (r *Registry) Lookup(method string) (Handler, bool) {
	h, ok := r.handlers[method]
	return h, ok
}

// Methods returns the registered method names. It allocates and is intended for
// construction-time or test use, never the request path. The order is
// unspecified; a caller needing determinism sorts it.
func (r *Registry) Methods() []string {
	out := make([]string, 0, len(r.handlers))
	for m := range r.handlers {
		out = append(out, m)
	}
	return out
}
