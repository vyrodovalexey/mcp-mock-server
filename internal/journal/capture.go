package journal

// This file adds the capture half of internal/journal (TASK-012): it turns a
// received request and the response it produced into a fully-faithful
// [journalapi.Record] and commits it to the ring. See doc.go for the storage
// engine overview.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// redactedHeaders is the always-redacted header set (security.md §5). Names are
// compared case-insensitively because HTTP header names are case-insensitive on
// the wire even though the journal preserves their original casing (MOCK-601.2).
// The value of any header whose lowercased name is in this set is replaced at
// capture; its name, casing and wire position are preserved so header-presence
// and header-order assertions still work.
var redactedHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
}

// credentialHeader is the header whose value, when present, is recorded as a
// hashed [journalapi.CredentialPart] (security.md §5, MOCK-407). It is matched
// case-insensitively.
const credentialHeader = "authorization"

// Input is the raw, transport-neutral description of one recorded exchange, the
// full MOCK-601.1 field set as the engine hands it to capture. It deliberately
// separates the received request from the produced response so a record can be
// written even for a request rejected before dispatch (MOCK-601.6): the
// response fields carry the emitted error and the request fields carry what was
// received.
//
// Input holds raw bytes and raw header lists exactly as received; the capturer
// applies the configured capture mode and the redaction rules. Byte slices in
// Input are copied into the record where retained, so the caller may reuse its
// buffers after Commit returns.
type Input struct {
	// Instance is the name of the *Instance recording this exchange.
	Instance string
	// Generation is Snapshot.Gen at request start (ADR-014, MOCK-702).
	Generation uint64
	// WallTime is the real clock at capture (MOCK-601.1); RFC 3339 nanos on
	// serialization. The engine passes time.Now() — this is evidence about the
	// run, not a modeled response value, so the wall clock is correct here.
	WallTime time.Time
	// MonoNs is nanoseconds since instance start (MOCK-601.1).
	MonoNs int64
	// DurationNs is elapsed processing time in nanoseconds.
	DurationNs int64
	// Transport is the wire transport (MOCK-601.1).
	Transport journalapi.Transport
	// Direction classifies the message; the zero value means an inbound
	// request, the common Phase 1 case.
	Direction journalapi.Direction
	// Era is the resolved protocol era ("modern" in Phase 1).
	Era string
	// Peer is the remote address (MOCK-601.1).
	Peer string

	// HTTP carries HTTP request/response line, headers and status; nil for a
	// stdio exchange.
	HTTP *HTTPInput

	// Method is the JSON-RPC method name.
	Method string
	// Name is the extracted primitive name (e.g. a tool name), when applicable.
	Name string
	// ID is the raw JSON-RPC id bytes as received, preserving its JSON type
	// (MOCK-247). Absent for a notification.
	ID json.RawMessage
	// Params is the raw params object as received.
	Params json.RawMessage
	// Body is the exact received request bytes (MOCK-601.1). Subject to the
	// capture mode.
	Body []byte
	// Meta is the decoded params._meta with every key preserved, including
	// unknown keys (MOCK-203.6). Values stay as raw JSON.
	Meta map[string]json.RawMessage
	// IsNotification is true when the message carried no id.
	IsNotification bool

	// Response describes the produced response, including a rejection response
	// emitted before dispatch (MOCK-601.4, MOCK-601.6); nil only for a
	// notification that produced nothing.
	Response *ResponseInput

	// Credential is the raw presented credential header value, hashed at
	// capture and then discarded. It is never stored raw (security.md §5).
	// When empty, no credential was presented. The engine may leave this empty
	// and rely on the Authorization header being present in HTTP.Headers; the
	// capturer extracts it from there as well.
	Credential string

	// MetaValidation records _meta presence validation, when it ran (MOCK-203);
	// present on records for requests rejected at that stage (MOCK-601.6).
	MetaValidation *journalapi.MetaValidationPart
	// Correlation carries inbound W3C trace context (ADR-016); copied verbatim.
	Correlation journalapi.CorrelationPart
}

// HTTPInput is the HTTP-transport slice of an [Input]. Headers are a wire-order,
// duplicate- and casing-preserving list, never an http.Header, because
// MOCK-601.2 depends on seeing exactly what was sent.
type HTTPInput struct {
	// Method is the HTTP method (e.g. "POST").
	Method string
	// Path is the request path.
	Path string
	// Query is the raw query string, without the leading '?'.
	Query string
	// Headers are the request headers in wire order, original casing and
	// duplicates preserved.
	Headers [][2]string
	// Status is the HTTP status code of the response.
	Status int
	// RespHeaders are the response headers in wire order.
	RespHeaders [][2]string
}

// ResponseInput is the produced-response slice of an [Input]. Body is subject to
// the capture mode; the ordered frame list and close reason are carried through
// for the streaming shapes later phases add.
type ResponseInput struct {
	// Status is the HTTP status code (200 for a JSON-RPC error in a successful
	// envelope, 503 for an overflow rejection, and so on).
	Status int
	// ResultType is the MCP resultType, when known.
	ResultType string
	// Body is the response bytes (MOCK-601.4). Subject to the capture mode.
	Body []byte
	// Shape is "json" or "sse"; empty defaults to "json".
	Shape string
	// Frames is the ordered SSE frame list (MOCK-601.4).
	Frames []journalapi.FrameRecord
	// CloseReason is why the response stream closed (MOCK-601.4).
	CloseReason string
	// Error is the JSON-RPC error object, when the response was an error
	// (MOCK-601.6).
	Error *journalapi.ErrorRecord
}

// Capturer builds fully-faithful [journalapi.Record] values from an [Input] and
// commits them to a [Ring], applying the instance's capture mode and the
// redaction rules exactly once. It is bound to one instance's configuration and
// credential hasher, so ≥200 instances each own an independent capturer with no
// process-global state (ADR-007).
//
// # Fidelity guarantee (MOCK-601)
//
// For a captured record every MOCK-601.1 field is populated from the [Input]:
// wall and monotonic timestamps, transport, all HTTP headers in wire order with
// original casing and duplicates, the full body (bounded by the capture mode),
// the decoded _meta with unknown keys preserved, the hashed credential, the peer
// address, and the produced response including status, response headers, body or
// digest, the ordered frame list and the close reason.
//
// # Redaction rule (security.md §5)
//
// The value of every always-redacted header (Authorization, Proxy-Authorization,
// Cookie, Set-Cookie) and of every header named in [journalapi.Config.RedactHeaders]
// is replaced with a marker carrying the keyed credential hash — never the raw
// value. The header name, its original casing and its wire position are
// preserved, so header-presence and header-order assertions still hold. The raw
// credential is hashed and then let go of; it is structurally impossible to
// serialize (see [journalapi.CredentialPart]).
type Capturer struct {
	cfg    journalapi.Config
	hasher *CredHasher
	redact map[string]struct{}
	truncN int
	mode   journalapi.CaptureMode
}

// NewCapturer returns a capturer for one instance. cfg supplies the capture mode
// and the configurable redaction header list; hasher supplies the keyed
// credential hash. cfg's zero-valued fields take their documented defaults, so a
// Capturer built from the same Config a Ring is built from applies the same
// mode.
func NewCapturer(cfg journalapi.Config, hasher *CredHasher) *Capturer {
	cfg = cfg.WithDefaults()
	redact := make(map[string]struct{}, len(redactedHeaders)+len(cfg.RedactHeaders))
	for name := range redactedHeaders {
		redact[name] = struct{}{}
	}
	for _, name := range cfg.RedactHeaders {
		redact[strings.ToLower(name)] = struct{}{}
	}
	return &Capturer{
		cfg:    cfg,
		hasher: hasher,
		redact: redact,
		truncN: cfg.TruncateBytes,
		mode:   cfg.Mode,
	}
}

// Commit builds the record for in and writes it to ring, returning its assigned
// global sequence. It is the engine's stage-9 entry point.
//
// The MOCK-901 cost discipline lives here: Commit does one atomic load of the
// ring's enabled flag and returns immediately when journaling is off, before it
// builds anything. No record is constructed, no body is copied, no credential is
// hashed and nothing is allocated on the disabled path — the cost of capture is
// paid only when journaling is on. When enabled it builds the record and defers
// to [Ring.Write], which enforces the bounds and overflow policy; under the
// error policy Write returns [ErrOverflow], which the caller turns into a 503
// with a journalled reason (MOCK-902).
func (c *Capturer) Commit(ctx context.Context, ring *Ring, in Input) (uint64, error) {
	if !ring.Enabled() {
		return 0, nil // MOCK-901: enabled-check before any capture work
	}
	rec := c.build(in)
	return ring.Write(ctx, rec)
}

// build assembles the [journalapi.Record] from in. It is separate from Commit so
// the disabled fast path never reaches record construction, and so tests can
// assert the built record's fidelity without a ring. It copies every retained
// byte slice so the record owns its bytes independently of the caller's buffers
// (ADR-005: records own their bytes).
func (c *Capturer) build(in Input) journalapi.Record {
	rec := journalapi.Record{
		Instance:    in.Instance,
		Generation:  in.Generation,
		WallTime:    in.WallTime,
		MonoNs:      in.MonoNs,
		DurationNs:  in.DurationNs,
		Transport:   in.Transport,
		Direction:   directionOrDefault(in.Direction),
		Era:         in.Era,
		Peer:        in.Peer,
		Meta:        cloneMeta(in.Meta),
		Correlation: in.Correlation,
	}
	rec.JSONRPC = c.captureJSONRPC(in)
	rec.HTTP = c.captureHTTP(in.HTTP)
	rec.Response = c.captureResponse(in.Response)
	rec.Credential = c.captureCredential(in)
	if in.MetaValidation != nil {
		mv := *in.MetaValidation
		mv.Missing = cloneStrings(in.MetaValidation.Missing)
		rec.MetaValidation = &mv
	}
	return rec
}

// directionOrDefault maps the zero Direction to the common inbound-request case
// so a caller that leaves Direction unset gets the correct classification
// without a special case at every call site.
func directionOrDefault(d journalapi.Direction) journalapi.Direction {
	if d == "" {
		return journalapi.DirectionInboundRequest
	}
	return d
}

// captureJSONRPC builds the JSON-RPC part, applying the body capture mode. Id and
// params are copied so the record owns them; the body is passed through
// captureBody, which enforces full/truncate/digest/off.
func (c *Capturer) captureJSONRPC(in Input) journalapi.JSONRPCPart {
	part := journalapi.JSONRPCPart{
		ID:             cloneRaw(in.ID),
		Method:         in.Method,
		Name:           in.Name,
		Params:         cloneRaw(in.Params),
		BodyLength:     len(in.Body),
		IsNotification: in.IsNotification,
	}
	body, sum, truncated := c.captureBody(in.Body)
	part.Body = body
	part.BodySHA256 = sum
	part.BodyTruncated = truncated
	return part
}

// captureHTTP builds the HTTP part with redaction applied to both request and
// response header lists. It returns nil for a stdio exchange (h == nil) so the
// record's HTTP field stays absent from the JSON.
func (c *Capturer) captureHTTP(h *HTTPInput) *journalapi.HTTPPart {
	if h == nil {
		return nil
	}
	return &journalapi.HTTPPart{
		Method:      h.Method,
		Path:        h.Path,
		Query:       h.Query,
		Headers:     c.redactHeaders(h.Headers),
		Status:      h.Status,
		RespHeaders: c.redactHeaders(h.RespHeaders),
	}
}

// captureResponse builds the response part, applying the body capture mode to
// the response body and copying the frame list. It returns nil when no response
// was produced (a notification) so the field stays absent.
func (c *Capturer) captureResponse(r *ResponseInput) *journalapi.ResponsePart {
	if r == nil {
		return nil
	}
	body, sum, _ := c.captureBody(r.Body)
	shape := r.Shape
	if shape == "" {
		shape = "json"
	}
	part := &journalapi.ResponsePart{
		Status:      r.Status,
		ResultType:  r.ResultType,
		Body:        body,
		BodySHA256:  sum,
		Shape:       shape,
		Frames:      c.captureFrames(r.Frames),
		CloseReason: r.CloseReason,
	}
	if r.Error != nil {
		e := *r.Error
		e.Data = cloneRaw(r.Error.Data)
		part.Error = &e
	}
	return part
}

// captureFrames copies and mode-bounds the ordered SSE frame list. Each frame's
// payload is subject to the same capture mode as a body, so a digest-mode
// capture of a large streamed response retains frame hashes and lengths without
// the payloads.
func (c *Capturer) captureFrames(frames []journalapi.FrameRecord) []journalapi.FrameRecord {
	if len(frames) == 0 {
		return nil
	}
	out := make([]journalapi.FrameRecord, len(frames))
	for i, f := range frames {
		data, sum, _ := c.captureBody(f.Data)
		out[i] = journalapi.FrameRecord{
			Seq:        f.Seq,
			EventID:    f.EventID,
			EventType:  f.EventType,
			Data:       data,
			DataSHA256: sum,
		}
	}
	return out
}

// captureCredential extracts the presented credential — from Input.Credential if
// set, otherwise from the Authorization header — hashes it, and returns the
// resulting part. The raw value is consumed by the hasher and never retained.
func (c *Capturer) captureCredential(in Input) *journalapi.CredentialPart {
	raw := in.Credential
	if raw == "" && in.HTTP != nil {
		raw = firstHeaderValue(in.HTTP.Headers, credentialHeader)
	}
	part, _ := c.hasher.Credential(raw)
	return part
}

// captureBody applies the configured capture mode to a body, returning the
// retained bytes, the hex SHA-256 of the whole (set for truncate and digest),
// and whether the retained bytes are only a prefix (ADR-005):
//
//   - full: the whole body is retained; no digest is computed.
//   - truncate:N: the first N bytes are retained, plus the SHA-256 of the whole
//     and the original length (carried by the caller in BodyLength).
//   - digest: only the SHA-256 and length are retained; no bytes.
//   - off: neither bytes nor digest are retained.
//
// The returned byte slice is always a fresh copy, so the record owns its body
// independently of the caller's buffer (ADR-005: records own their bytes).
func (c *Capturer) captureBody(body []byte) (retained []byte, sha256Hex string, truncated bool) {
	if len(body) == 0 {
		return nil, "", false
	}
	switch c.mode {
	case journalapi.CaptureOff:
		return nil, "", false
	case journalapi.CaptureDigest:
		return nil, hashHex(body), false
	case journalapi.CaptureTruncate:
		n := c.truncN
		if n < 0 {
			n = 0
		}
		if n >= len(body) {
			// Nothing to truncate: the prefix is the whole body. Still record
			// the digest so the mode's output shape is uniform.
			return cloneBytes(body), hashHex(body), false
		}
		return cloneBytes(body[:n]), hashHex(body), true
	default: // CaptureFull
		return cloneBytes(body), "", false
	}
}

// redactHeaders returns a copy of headers with the value of every redacted
// header replaced by a marker carrying the credential hash, preserving each
// header's name, casing and wire position (security.md §5, MOCK-601.2). A
// non-redacted header is copied through verbatim. The returned slice is owned by
// the record.
func (c *Capturer) redactHeaders(headers [][2]string) [][2]string {
	if len(headers) == 0 {
		return nil
	}
	out := make([][2]string, len(headers))
	for i, h := range headers {
		name, value := h[0], h[1]
		if _, ok := c.redact[strings.ToLower(name)]; ok {
			value = redactedMarker(c.hasher.Hash(value))
		}
		out[i] = [2]string{name, value}
	}
	return out
}

// redactedMarker builds the redacted header-value placeholder from a hash. The
// prefix is [journalapi.RedactedHeaderValue] so a reader can detect a redacted
// value without guessing; the hash lets assertions that key on the hash still
// work. An empty hash (empty original value) still yields a well-formed marker.
func redactedMarker(hash string) string {
	return journalapi.RedactedHeaderValue + hash + ">"
}

// firstHeaderValue returns the value of the first header whose name matches name
// case-insensitively, or "" when absent. It is how the capturer finds the
// Authorization value to hash when the engine did not pass it explicitly.
func firstHeaderValue(headers [][2]string, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h[0], name) {
			return h[1]
		}
	}
	return ""
}

// hashHex returns the lowercase hex SHA-256 of b. It backs the truncate and
// digest capture modes' whole-body digest.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// cloneBytes returns a fresh copy of b, or nil for an empty input, so a stored
// record never aliases a caller's buffer (ADR-005: records own their bytes).
func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// cloneRaw copies a json.RawMessage, preserving nil-vs-empty so the record's
// JSON serialization matches what was received.
func cloneRaw(r json.RawMessage) json.RawMessage {
	if r == nil {
		return nil
	}
	out := make(json.RawMessage, len(r))
	copy(out, r)
	return out
}

// cloneMeta deep-copies the decoded _meta map so the record owns its values and
// a later mutation of the caller's map cannot alter recorded evidence. Unknown
// keys are preserved because the whole map is copied (MOCK-203.6).
func cloneMeta(m map[string]json.RawMessage) map[string]json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = cloneRaw(v)
	}
	return out
}

// cloneStrings copies a string slice, preserving nil-vs-empty.
func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}
