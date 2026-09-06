package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// pathPrefix is the versioned control API path prefix (ADR-015 "REST-ish, /v1
// prefix"). Every route is under it.
const pathPrefix = "/v1"

// Content types the control API uses.
const (
	contentTypeJSON   = "application/json"
	contentTypeNDJSON = "application/x-ndjson"
	contentTypeYAML   = "application/yaml"
)

// defaultJournalLimit is the page size when a query names none
// (contracts/control-api.openapi.yaml journal.limit default). maxJournalLimit is
// the contract's ceiling.
const (
	defaultJournalLimit = 100
	maxJournalLimit     = 10000
)

// Handler is the control API's http.Handler. The identical Handler is served
// over TCP and over a unix socket (ADR-015): the transport differs, the handler
// does not, which is why the two front ends cannot diverge. It is built around
// one [Backend] and one route table and holds no mutable package state, so two
// facades in one process get independent handlers (ADR-007).
type Handler struct {
	backend Backend
	routes  []route
	openAPI []byte
}

// NewHandler builds a control [Handler] around backend, serving openAPI at
// GET /v1/openapi.yaml. openAPI may be nil, in which case that route reports 404.
func NewHandler(backend Backend, openAPI []byte) *Handler {
	return &Handler{
		backend: backend,
		routes:  phase1Routes(),
		openAPI: openAPI,
	}
}

// ServeHTTP dispatches a request through the shared route table. It strips the
// /v1 prefix, matches a route (distinguishing 404 from 405), parses the request,
// and writes either a JSON body, an NDJSON stream, or the OpenAPI document.
// r.Context() is propagated into the Backend call and never recreated.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutPrefix(r.URL.Path, pathPrefix)
	if !ok {
		writeError(w, ErrNotFound)
		return
	}
	segs := splitPath(path)

	var rc reqContext
	rt, pathOK, methodOK := matchRoute(h.routes, r.Method, segs, &rc)
	switch {
	case !pathOK:
		writeError(w, ErrNotFound)
		return
	case !methodOK:
		w.Header().Set("Allow", allowedMethods(h.routes, segs))
		writeStatusError(w, http.StatusMethodNotAllowed, codeConflict,
			"method not allowed on this control resource")
		return
	}

	if rt.opID == opGetOpenAPI {
		h.serveOpenAPI(w)
		return
	}
	if rt.opID == opGetJournal {
		if err := parseJournalQuery(r, &rc); err != nil {
			writeError(w, err)
			return
		}
		if rc.ndjson {
			h.streamJournal(r.Context(), w, &rc)
			return
		}
	}
	if rt.opID == opGetCorrelations {
		if err := parseJournalQuery(r, &rc); err != nil {
			writeError(w, err)
			return
		}
	}

	h.dispatch(r.Context(), w, rt, &rc)
}

// dispatch runs a route's handler and writes its JSON result or error.
func (h *Handler) dispatch(ctx context.Context, w http.ResponseWriter, rt route, rc *reqContext) {
	val, status, err := rt.handle(ctx, h.backend, rc)
	if err != nil {
		writeError(w, err)
		return
	}
	if status == http.StatusNoContent || val == nil {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, val)
}

// serveOpenAPI writes the contract document as YAML (GET /v1/openapi.yaml,
// MOCK-104.1). A handler built without the document reports 404 rather than an
// empty 200.
func (h *Handler) serveOpenAPI(w http.ResponseWriter) {
	if len(h.openAPI) == 0 {
		writeError(w, ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", contentTypeYAML)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.openAPI)
}

// streamJournal writes the matching records as NDJSON, one JSON object per line,
// pulling from [InstanceBackend.JournalStream] so the whole journal is never
// materialized (MOCK-602.2). A per-record encode failure ends the stream; the
// status line is already sent, so nothing more can be signaled but stopping.
func (h *Handler) streamJournal(ctx context.Context, w http.ResponseWriter, rc *reqContext) {
	seq, err := h.backend.For(rc.name).JournalStream(ctx, rc.query)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentTypeNDJSON)
	w.WriteHeader(http.StatusOK)
	jw := journalapi.NewWriter(w)
	flusher, _ := w.(http.Flusher)
	for rec, rerr := range seq {
		if rerr != nil {
			return
		}
		if werr := jw.Write(rec); werr != nil {
			return
		}
		if flusher != nil {
			_ = jw.Flush()
			flusher.Flush()
		}
	}
	_ = jw.Close()
}

// parseJournalQuery fills rc.query and rc.ndjson from the request's query
// parameters and Accept header (contracts/control-api.openapi.yaml getJournal).
// Every MOCK-602.4 filter dimension maps to a Selector field; an unparsable
// numeric or time parameter is a validation error rather than a silent default.
func parseJournalQuery(r *http.Request, rc *reqContext) error {
	q := r.URL.Query()
	rc.query.Method = q.Get("method")
	rc.query.Name = q.Get("name")
	rc.query.Transport = q.Get("transport")
	rc.query.Era = q.Get("era")
	rc.query.ChainID = firstNonEmpty(q.Get("chain"), q.Get("correlationId"))
	rc.query.TraceID = q.Get("traceId")
	rc.query.FaultRule = q.Get("faultRule")

	if err := parseTimeParam(q.Get("since"), &rc.query.Since); err != nil {
		return err
	}
	if err := parseTimeParam(q.Get("until"), &rc.query.Until); err != nil {
		return err
	}
	if err := parseStatusParam(q.Get("status"), &rc.query.Selector); err != nil {
		return err
	}
	if err := parseUint64Param(q.Get("after"), &rc.query.After); err != nil {
		return err
	}
	if err := parseLimitParam(q.Get("limit"), &rc.query.Limit); err != nil {
		return err
	}
	rc.query.Consistent = q.Get("consistent") == "true"
	rc.query.Follow = q.Get("follow") == "true"
	rc.ndjson = q.Get("format") == "ndjson" || acceptsNDJSON(r.Header.Get("Accept"))
	return nil
}

// acceptsNDJSON reports whether the Accept header requests the NDJSON form.
func acceptsNDJSON(accept string) bool {
	return strings.Contains(accept, contentTypeNDJSON)
}

// firstNonEmpty returns the first non-empty of its arguments, or "".
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// parseTimeParam parses an RFC 3339 time into dst; empty leaves dst zero.
func parseTimeParam(v string, dst *time.Time) error {
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return errors.Join(ErrValidation, err)
	}
	*dst = t
	return nil
}

// parseStatusParam parses an integer HTTP status filter into sel.StatusCode.
func parseStatusParam(v string, sel *journalapi.Selector) error {
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return errors.Join(ErrValidation, err)
	}
	sel.StatusCode = &n
	return nil
}

// parseUint64Param parses an unsigned integer parameter into dst; empty is a
// no-op.
func parseUint64Param(v string, dst *uint64) error {
	if v == "" {
		return nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return errors.Join(ErrValidation, err)
	}
	*dst = n
	return nil
}

// parseLimitParam parses the page limit, clamping to the contract ceiling and
// rejecting a negative value.
func parseLimitParam(v string, dst *int) error {
	if v == "" {
		*dst = defaultJournalLimit
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return errors.Join(ErrValidation, errors.New("limit must be a non-negative integer"))
	}
	if n == 0 {
		n = defaultJournalLimit
	}
	if n > maxJournalLimit {
		n = maxJournalLimit
	}
	*dst = n
	return nil
}

// allowedMethods lists the methods permitted on the resource matching segs, for
// a 405 Allow header.
func allowedMethods(routes []route, segs []string) string {
	var methods []string
	for _, cand := range routes {
		if _, ok := matchSegments(splitPath(cand.tmpl), segs); ok {
			methods = append(methods, cand.method)
		}
	}
	return strings.Join(methods, ", ")
}

// writeJSON encodes v as JSON with the given status. HTML escaping is disabled so
// header values and bodies captured verbatim survive byte-identically.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError maps err to its contract code and status and writes the error
// object (contracts/control-api.openapi.yaml #/components/schemas/Error).
func writeError(w http.ResponseWriter, err error) {
	code, status, msg := statusFor(err)
	writeStatusError(w, status, code, msg)
}

// writeStatusError writes an [errorObject] with an explicit status and code.
func writeStatusError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorObject{Code: code, Message: msg, Retryable: retryable(code)})
}

// CheckBind refuses an unsafe control listener configuration at startup
// (MOCK-104.5, security.md §2.1): a non-loopback TCP bind with no token
// configured is rejected with a message naming the address and the missing
// token. A loopback bind, or any bind with a token, is allowed. hasToken reports
// whether a control token is configured.
func CheckBind(addr string, hasToken bool) error {
	if hasToken {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port form (e.g. a bare host): treat the whole string as the host.
		host = addr
	}
	if isLoopbackHost(host) {
		return nil
	}
	return &BindError{Addr: addr}
}

// RequiresAuth reports whether a TCP control listener on addr must authenticate
// every request (security.md §2.1, contract bearerAuth: "Required whenever the
// listener is not loopback and not a unix socket"). It is true for a
// non-loopback bind — the reachable surface — and false for loopback. It is the
// single predicate the facade uses to decide whether to wrap the TCP handler in
// [requireBearer], so the auth boundary and the [CheckBind] gate share one
// notion of "loopback".
func RequiresAuth(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return !isLoopbackHost(host)
}

// isLoopbackHost reports whether host is a loopback address or the empty/
// wildcard host that binds all interfaces (which is NOT loopback and is refused).
func isLoopbackHost(host string) bool {
	if host == "" {
		// An empty host means "all interfaces" (0.0.0.0 / ::), which is not
		// loopback and must be refused without a token.
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// BindError reports a refused non-loopback control bind without a token
// (MOCK-104.5). It names the address so the operator sees exactly what was
// refused.
type BindError struct {
	// Addr is the refused bind address.
	Addr string
}

// Error implements error.
func (e *BindError) Error() string {
	return "control: refusing to bind control API to non-loopback address " +
		strconv.Quote(e.Addr) + " without a token (set MCPMOCK_CONTROL_TOKEN or --control-token-file)"
}
