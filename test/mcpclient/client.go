package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// transport is the byte-level round-trip this client drives. Both the HTTP and
// stdio transports implement it. It is intentionally minimal: it moves one
// request payload to the server and returns one raw response payload, making no
// assumption about the payload's shape beyond "some bytes in, some bytes out".
// This is what keeps the client transport-neutral (MOCK-102) and lets the same
// JSON-RPC payload be sent over stdio and HTTP for byte-for-byte comparison
// (TASK-008 acceptance criterion 4).
type transport interface {
	// roundTrip sends payload and returns the raw response bytes. hdrs are
	// extra request headers; a transport that has no notion of headers (stdio)
	// ignores them. The bool reports whether a response payload was produced at
	// all (an stdio notification, or a cancelled request, yields none).
	roundTrip(ctx context.Context, payload []byte, hdrs []Header) (raw []byte, gotResponse bool, err error)
	// close releases any transport resources. Safe to call more than once.
	close() error
}

// Client is an independent MCP client over a single transport. It shares no
// code with the mcpmock server (ADR-017). It is safe for sequential use; it is
// the caller's responsibility to serialise concurrent calls if the underlying
// transport (notably stdio) is not itself concurrency-safe.
//
// A Client never stores a context.Context; every method that performs I/O takes
// one explicitly.
type Client struct {
	tr transport
	// defaultClientInfo is attached to _meta.clientInfo when a Request does not
	// override it and does not suppress it. It is client configuration, not
	// process-global state.
	defaultClientInfo *ClientInfo
}

// ClientInfo is the _meta.clientInfo shape. annex 2.6 [P-07]: {name, version,
// title?}. Title is omitted from the wire when empty.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// NewHTTP constructs a Client that speaks JSON-RPC over HTTP POST
// (streamable-http, MOCK-102) to endpoint. See [HTTPOption] for tuning and
// [Client.RawSocket] semantics via [WithRawSocket]-style construction in http.go.
func NewHTTP(endpoint string, opts ...HTTPOption) (*Client, error) {
	tr, err := newHTTPTransport(endpoint, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{tr: tr, defaultClientInfo: defaultClientInfo()}, nil
}

// NewStdio constructs a Client that speaks newline-delimited JSON-RPC over the
// supplied reader/writer pair (MOCK-102 stdio). See [newStdioTransport].
func NewStdio(cfg StdioConfig) (*Client, error) {
	tr, err := newStdioTransport(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{tr: tr, defaultClientInfo: defaultClientInfo()}, nil
}

// defaultClientInfo is the identity attached to _meta.clientInfo when a request
// neither overrides nor suppresses it.
func defaultClientInfo() *ClientInfo {
	return &ClientInfo{Name: "mcpmock-test-client", Version: "0"}
}

// Close releases the transport.
func (c *Client) Close() error {
	if c.tr == nil {
		return nil
	}
	return c.tr.close()
}

// Do sends req and returns the raw response together with a decoded JSON-RPC
// envelope. The raw bytes are always returned when a response was produced, so
// callers doing byte-identical determinism checks (design principle §0.1) work
// at the wire level; the decoded [Response.Envelope] is a convenience that
// makes NO assumption about the result shape beyond the JSON-RPC frame.
//
// If the server produces no response payload (an stdio notification, or a
// cancelled request), Response.GotResponse is false and Response.Body is nil.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	if req == nil {
		return nil, errors.New("mcpclient: nil request")
	}
	payload, err := req.Marshal()
	if err != nil {
		return nil, fmt.Errorf("mcpclient: marshal request: %w", err)
	}
	raw, got, err := c.tr.roundTrip(ctx, payload, req.Headers)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: round trip: %w", err)
	}
	resp := &Response{Body: raw, GotResponse: got}
	if got {
		// A parse failure is not a client error: the server may have
		// deliberately returned malformed bytes, and the caller asserts on
		// Response.Body. We record the parse error on the envelope and move on.
		resp.ParseErr = json.Unmarshal(raw, &resp.Envelope)
	}
	return resp, nil
}

// Header is a single HTTP header occurrence, preserved with its exact name
// casing and value. A slice of these preserves wire order and duplicates, which
// is what lets the raw-socket mode transmit the same header name twice with
// differing casing (MOCK-601.2) without http.Header canonicalisation.
type Header struct {
	Name  string
	Value string
}

// Request is a JSON-RPC request builder. It is deliberately low-level so that
// malformed requests are a first-class capability (MOCK-203, MOCK-204), not a
// workaround: the caller controls the id, the method, the params, whether and
// where _meta appears, and may bypass structured construction entirely with
// [Request.RawBody].
//
// The zero value is not useful; construct requests with [NewRequest] or one of
// the method helpers ([DiscoverRequest], [ToolsListRequest], [ToolsCallRequest]),
// then mutate the exported fields as the test requires.
type Request struct {
	// ID is the raw JSON-RPC id, encoded verbatim into the envelope. Because it
	// is json.RawMessage, a numeric id 1 and a string id "1" are distinguishable
	// and are transmitted exactly as given (echo-preservation is asserted at the
	// server; here the client controls the exact lexical form). A nil ID omits
	// the id field entirely (a JSON-RPC notification).
	ID json.RawMessage

	// Method is the JSON-RPC method. Use the Method* constants; a raw string is
	// permitted so a test can send an unknown method to exercise -32601.
	Method string

	// Params is the decoded params object that structured marshalling builds
	// from. _meta is injected into it at marshal time per the Meta* fields
	// below. If RawBody is set, Params and Meta* are ignored.
	Params map[string]any

	// Meta controls _meta construction. See [MetaSpec]. Its omission toggles are
	// how the client exercises MOCK-203's rejection paths.
	Meta MetaSpec

	// Headers are extra request headers in wire order, honoured by the HTTP
	// transport (including the raw-socket mode) and ignored by stdio.
	Headers []Header

	// RawBody, when non-nil, is sent verbatim as the request payload and every
	// structured field above is ignored. This is the escape hatch for
	// deliberately malformed JSON — truncated objects, wrong jsonrpc version,
	// non-object params, invalid UTF-8 — that no structured builder could
	// produce. Malformation is a first-class capability here.
	RawBody []byte
}

// MetaSpec describes how params._meta is constructed for a [Request]. Its
// fields make the omission of each required _meta field explicit, so a test can
// send a request with _meta complete, with protocolVersion absent, with
// clientCapabilities absent, or with _meta absent entirely (TASK-008
// acceptance criterion 3).
type MetaSpec struct {
	// Omit, when true, suppresses _meta entirely: no _meta key is written into
	// params. Exercises "_meta absent".
	Omit bool

	// OmitProtocolVersion suppresses only _meta.protocolVersion. Exercises
	// MOCK-203.1's rejection path.
	OmitProtocolVersion bool

	// OmitClientCapabilities suppresses only _meta.clientCapabilities. Exercises
	// MOCK-203.2's rejection path.
	OmitClientCapabilities bool

	// ProtocolVersion overrides the protocol version string written to
	// _meta.protocolVersion. Empty means use [ProtocolRevision].
	ProtocolVersion string

	// Capabilities overrides _meta.clientCapabilities. Nil means the empty
	// capability set {} (annex 2.7 [P-08] shape: a map of capability name to
	// capability object).
	Capabilities map[string]any

	// ClientInfo overrides _meta.clientInfo. Nil means the client's default.
	ClientInfo *ClientInfo

	// OmitClientInfo suppresses _meta.clientInfo even when a default exists.
	OmitClientInfo bool

	// Extra are additional _meta keys written verbatim. They let a test carry
	// unknown _meta keys (annex 2.11: preserved in the journal) or a
	// deliberately malformed _meta member.
	Extra map[string]any
}

// NewRequest builds a request for method with the supplied id and params. Pass
// a nil id for a notification. params may be nil.
func NewRequest(id json.RawMessage, method string, params map[string]any) *Request {
	return &Request{ID: id, Method: method, Params: params}
}

// IntID encodes n as a numeric JSON-RPC id.
func IntID(n int64) json.RawMessage {
	return json.RawMessage(fmt.Appendf(nil, "%d", n))
}

// StringID encodes s as a JSON string id, distinguishable on the wire from a
// numeric id of the same digits.
func StringID(s string) json.RawMessage {
	// json.Marshal of a string cannot fail for a Go string.
	b, _ := json.Marshal(s)
	return b
}

// Marshal renders the request to its wire bytes. When [Request.RawBody] is set
// it is returned verbatim. Otherwise a JSON-RPC 2.0 object is built with _meta
// injected into params per [Request.Meta].
//
// Marshal intentionally does not validate: it will happily emit a request with
// _meta omitted, so the server's rejection path can be tested.
func (r *Request) Marshal() ([]byte, error) {
	if r.RawBody != nil {
		return r.RawBody, nil
	}
	body, err := r.newRequestBody()
	if err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

// newRequestBody assembles the ordered JSON-RPC object as a map, injecting
// params._meta per the annex 2.5 [P-06] location. It uses ordered emission via
// an explicit key list so the bytes are stable for the golden/determinism
// assertions this client supports.
func (r *Request) newRequestBody() (map[string]any, error) {
	obj := map[string]any{
		"jsonrpc": JSONRPCVersion,
		"method":  r.Method,
	}
	if r.ID != nil {
		obj["id"] = r.ID
	}

	params := map[string]any{}
	for k, v := range r.Params {
		params[k] = v
	}
	if meta, ok := r.buildMeta(); ok {
		params[MetaKey] = meta
	}
	// A request with no params and no _meta still carries an (empty) params
	// object only if the caller supplied params; otherwise params is omitted so
	// the client can also send a params-less request to exercise -32600/-32602.
	if len(params) > 0 {
		obj[MetaKeyParams] = params
	}
	return obj, nil
}

// buildMeta constructs the _meta object per [Request.Meta]. The bool is false
// when _meta is suppressed entirely.
func (r *Request) buildMeta() (map[string]any, bool) {
	m := r.Meta
	if m.Omit {
		return nil, false
	}
	meta := map[string]any{}
	if !m.OmitProtocolVersion {
		pv := m.ProtocolVersion
		if pv == "" {
			pv = ProtocolRevision
		}
		meta[MetaKeyProtocolVersion] = pv
	}
	if !m.OmitClientCapabilities {
		caps := m.Capabilities
		if caps == nil {
			caps = map[string]any{}
		}
		meta[MetaKeyClientCapabilities] = caps
	}
	if !m.OmitClientInfo {
		info := m.ClientInfo
		if info == nil {
			info = r.metaClientInfoFallback()
		}
		if info != nil {
			meta[MetaKeyClientInfo] = info
		}
	}
	for k, v := range m.Extra {
		meta[k] = v
	}
	return meta, true
}

// metaClientInfoFallback returns nil; a per-Client default is applied by the
// method helpers, which know the owning Client. A bare Request built by hand
// with no ClientInfo simply omits it, which is a valid test input.
func (r *Request) metaClientInfoFallback() *ClientInfo { return nil }

// Discover builds and sends a server/discover request with a complete _meta
// envelope (protocolVersion, clientCapabilities, and the client's default
// clientInfo). It is a convenience over [Client.Do]; a test needing to omit a
// _meta field builds the [Request] directly and sets [MetaSpec] toggles.
func (c *Client) Discover(ctx context.Context, id json.RawMessage) (*Response, error) {
	return c.Do(ctx, c.bindDefault(DiscoverRequest(id)))
}

// ListTools builds and sends a tools/list request with a complete _meta
// envelope. See [Client.Discover].
func (c *Client) ListTools(ctx context.Context, id json.RawMessage) (*Response, error) {
	return c.Do(ctx, c.bindDefault(ToolsListRequest(id)))
}

// CallTool builds and sends a tools/call request for the named tool with the
// supplied arguments (which may be nil) and a complete _meta envelope.
func (c *Client) CallTool(ctx context.Context, id json.RawMessage, name string, args map[string]any) (*Response, error) {
	return c.Do(ctx, c.bindDefault(ToolsCallRequest(id, name, args)))
}

// bindDefault attaches the client's default clientInfo to a request whose
// MetaSpec neither overrides nor suppresses it. It never overrides an explicit
// choice, so tests keep full control.
func (c *Client) bindDefault(r *Request) *Request {
	if r.Meta.ClientInfo == nil && !r.Meta.OmitClientInfo {
		r.Meta.ClientInfo = c.defaultClientInfo
	}
	return r
}

// DiscoverRequest builds a server/discover request with no params. The _meta
// envelope defaults to complete; adjust [Request.Meta] to omit fields.
func DiscoverRequest(id json.RawMessage) *Request {
	return NewRequest(id, MethodDiscover, nil)
}

// ToolsListRequest builds a tools/list request with no params.
func ToolsListRequest(id json.RawMessage) *Request {
	return NewRequest(id, MethodToolsList, nil)
}

// ToolsCallRequest builds a tools/call request for the named tool. args may be
// nil (equivalent to an absent arguments object, builtin-tools.md 1.3).
func ToolsCallRequest(id json.RawMessage, name string, args map[string]any) *Request {
	params := map[string]any{ParamsKeyName: name}
	if args != nil {
		params[ParamsKeyArguments] = args
	}
	return NewRequest(id, MethodToolsCall, params)
}

// Response is the outcome of a [Client.Do]. Body is the raw response bytes for
// wire-level assertion; Envelope is a best-effort decode of the JSON-RPC frame.
type Response struct {
	// Body is the raw response payload exactly as received, for byte-identical
	// comparison. Nil when GotResponse is false.
	Body []byte

	// GotResponse is false when the server produced no response payload (an
	// stdio notification, or a cancelled request that wrote no frame).
	GotResponse bool

	// Envelope is the decoded JSON-RPC frame. It is populated only when the body
	// parsed as JSON; see ParseErr.
	Envelope Envelope

	// ParseErr is the error from decoding Body as a JSON-RPC envelope, or nil.
	// A non-nil ParseErr is NOT a client failure: the server may have returned
	// deliberately malformed bytes, which is a valid thing to assert on.
	ParseErr error
}

// Envelope is the JSON-RPC 2.0 response frame, decoded no further than the
// frame itself. Result and Error are left as raw JSON so the client makes no
// assumption about the result shape (TASK-008 acceptance criterion 6): a
// Phase 2 SSE result shape decodes here unchanged.
type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC error object. Data is left raw so error.data.missing
// (annex 2.12 [P-11]) and any other data payload can be asserted without this
// package modelling every shape.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}
