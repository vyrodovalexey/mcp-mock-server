package journalapi

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the current version of the [Record] JSON contract.
//
// Stability: additive fields do not bump it; a field removal or type change
// does (data-model.md §8). A reader comparing goldens uses it to report a
// version mismatch as a clear message rather than a confusing diff.
const SchemaVersion = 1

// Direction classifies a journalled message relative to mcpmock.
//
// Stability: v0. The set may gain members as later phases add server-initiated
// traffic; existing members and their string values are stable.
type Direction string

// Direction values. mcpmock is the server, so a hub's call is an
// [DirectionInboundRequest].
const (
	// DirectionInboundRequest is a request received from the hub under test.
	DirectionInboundRequest Direction = "inboundRequest"
	// DirectionInboundResponse is a response received on a server-initiated call.
	DirectionInboundResponse Direction = "inboundResponse"
	// DirectionOutboundRequest is a request mcpmock initiated (MOCK-302).
	DirectionOutboundRequest Direction = "outboundRequest"
	// DirectionOutboundNotification is a notification mcpmock emitted.
	DirectionOutboundNotification Direction = "outboundNotification"
)

// Transport identifies the wire transport a record was observed on.
//
// Stability: v0. Values are stable; the set is closed for Phase 1 to the two
// members below.
type Transport string

// Transport values.
const (
	// TransportHTTP is the streamable-http transport.
	TransportHTTP Transport = "http"
	// TransportStdio is the newline-delimited stdio transport.
	TransportStdio Transport = "stdio"
)

// RedactedHeaderValue is the exact placeholder written in place of a sensitive
// header value at capture (security.md §5). The header's name, original casing
// and wire position are preserved in [HTTPPart.Headers]; only the value is
// replaced, and the replacement carries the credential hash so assertions that
// key on the hash still work. The literal prefix is part of the contract so a
// reader can detect a redacted value without guessing.
//
// Stability: v0. The prefix is stable; the hash suffix is per-record.
const RedactedHeaderValue = "<redacted:sha256:"

// Record is one fully-recorded exchange in the journal — the central public
// contract of this package (ENT-REC, MOCK-601). Golden files encode its JSON
// form, so it carries [Record.SchemaVersion] and is HARD to reverse (ADR-005).
//
// A Record is a value type with only omitempty-tagged optional sub-structures,
// so the disabled journal path never has to construct one (MOCK-901). Records
// handed to callers by a [View] are always copies; the storage ring may
// overwrite a slot at any time and a pointer into it would corrupt evidence
// (ADR-005).
//
// Stability: v0. The field set is the MOCK-601.1 contract; see the per-field
// notes for the stability posture of each.
type Record struct {
	// SchemaVersion is the [Record] contract version; see [SchemaVersion].
	SchemaVersion int `json:"schemaVersion"`
	// Seq is the global total order across shards and instances (ADR-005).
	// It is unique and gapless for accepted writes within a process.
	Seq uint64 `json:"seq"`
	// Instance is the name of the *Instance that recorded this exchange.
	Instance string `json:"instance"`
	// Generation is Snapshot.Gen at request start, making a mid-test
	// configuration mutation observable in the evidence (ADR-014).
	Generation uint64 `json:"generation"`

	// WallTime is the real clock at capture, serialized as RFC 3339 with
	// nanoseconds (MOCK-601). It is volatile and redacted from goldens.
	WallTime time.Time `json:"wallTime"`
	// MonoNs is nanoseconds since instance start — a monotonic timestamp
	// independent of wall-clock adjustment (MOCK-601). Volatile in goldens.
	MonoNs int64 `json:"monoNs"`
	// DurationNs is the request's elapsed processing time in nanoseconds.
	// Volatile in goldens.
	DurationNs int64 `json:"durationNs"`

	// Transport is the wire transport; see [Transport].
	Transport Transport `json:"transport"`
	// Direction classifies the message; see [Direction].
	Direction Direction `json:"direction"`
	// Era is the resolved protocol era (MOCK-303.5). Empty until era
	// resolution lands; "modern" in Phase 1.
	Era string `json:"era"`
	// Peer is the remote address. Volatile in goldens.
	Peer string `json:"peer"`

	// HTTP carries HTTP-specific fields; nil for stdio records.
	HTTP *HTTPPart `json:"http,omitempty"`
	// TLS carries client-certificate evidence; nil unless mTLS was used.
	TLS *TLSPart `json:"tls,omitempty"`

	// JSONRPC is the decoded JSON-RPC envelope of the exchange.
	JSONRPC JSONRPCPart `json:"jsonrpc"`
	// Meta is the decoded params._meta object with every key preserved,
	// including unknown keys (MOCK-203.6). Values stay as raw JSON so their
	// type and byte form are not lost.
	Meta map[string]json.RawMessage `json:"meta,omitempty"`
	// Response is the produced response; nil for a notification or a request
	// whose response was not (yet) emitted.
	Response *ResponsePart `json:"response,omitempty"`

	// Credential is the presented credential recorded as a hash only; nil when
	// none was presented (MOCK-407). It is structurally incapable of holding a
	// raw value — see [CredentialPart].
	Credential *CredentialPart `json:"credential,omitempty"`
	// MetaValidation records the outcome of _meta presence validation
	// (MOCK-203, AMEND-6); nil when validation was not run.
	MetaValidation *MetaValidationPart `json:"metaValidation,omitempty"`
	// Validation records the tools/call argument schema-validation outcome
	// (MOCK-606); nil when no argument validation applied.
	Validation *ValidationPart `json:"validation,omitempty"`
	// MRTR carries multi-round tool-result correlation fields (MOCK-605); nil
	// outside an MRTR chain.
	MRTR *MRTRPart `json:"mrtr,omitempty"`
	// Session carries legacy-era session fields; nil in modern mode.
	Session *SessionPart `json:"session,omitempty"`
	// Subscription carries subscription fields; nil when not a subscription.
	Subscription *SubscriptionPart `json:"subscription,omitempty"`
	// Correlation carries trace/span context, populated unconditionally by the
	// pipeline regardless of export state (ADR-016), so trace assertions work
	// in a plain unit test (MOCK-603 §3.5).
	Correlation CorrelationPart `json:"correlation"`
	// Faults lists faults that fired for this request; nil when none did.
	Faults []FaultFired `json:"faults,omitempty"`
	// CorpusIDs lists hostile-corpus entries emitted in the response (ADR-018);
	// nil when none were.
	CorpusIDs []string `json:"corpusIds,omitempty"`
	// Canceled records client cancellation with elapsed time (MOCK-212); nil
	// when the request completed normally.
	//
	// NOTE: data-model.md §3 uses the British spelling for this field and its
	// JSON tag. The repository's misspell linter is locale:US and rejects that
	// spelling, so both the Go identifier and the JSON tag use the US spelling
	// "canceled" here. This is a v0 field outside the TASK-005 Phase-1 required
	// set; the deviation is recorded for ratification (ADR-019).
	Canceled *CancelPart `json:"canceled,omitempty"`
	// Dropped is true when a gap in [Record.Seq] precedes this record because
	// the ring overwrote one or more earlier records (ADR-005 overflow).
	Dropped bool `json:"dropped,omitempty"`
}

// HTTPPart holds HTTP-transport fields (MOCK-601.2).
//
// Headers use [][2]string, never http.Header: MOCK-601.2 and MOCK-204 both
// depend on seeing exactly what was sent — original casing, duplicates and wire
// order — which a map[string][]string destroys.
//
// Stability: v0.
type HTTPPart struct {
	// Method is the HTTP method (e.g. "POST").
	Method string `json:"method"`
	// Path is the request path.
	Path string `json:"path"`
	// Query is the raw query string, without the leading '?'.
	Query string `json:"query"`
	// Headers are the request headers in wire order, with original casing and
	// duplicates preserved. Sensitive values are redacted at capture; see
	// [RedactedHeaderValue].
	Headers [][2]string `json:"headers"`
	// Status is the HTTP status code of the response.
	Status int `json:"status"`
	// RespHeaders are the response headers in wire order, same fidelity rules
	// as [HTTPPart.Headers].
	RespHeaders [][2]string `json:"respHeaders,omitempty"`
}

// TLSPart holds client-certificate evidence recorded on an mTLS connection
// (MOCK-106.3).
//
// Stability: v0.
type TLSPart struct {
	// Subject is the client certificate subject DN.
	Subject string `json:"subject,omitempty"`
	// Issuer is the client certificate issuer DN.
	Issuer string `json:"issuer,omitempty"`
	// Serial is the certificate serial number in hex.
	Serial string `json:"serial,omitempty"`
	// FingerprintSHA256 is the SHA-256 fingerprint of the certificate, hex.
	FingerprintSHA256 string `json:"fingerprintSha256,omitempty"`
	// NotAfter is the certificate expiry.
	NotAfter *time.Time `json:"notAfter,omitempty"`
}

// JSONRPCPart holds the decoded JSON-RPC envelope (MOCK-203).
//
// ID is raw JSON so the id type is preserved: MOCK-247 /
// [Correlation.RetryIDsDistinct] must distinguish 1 from "1" from 1.0.
//
// Stability: v0.
type JSONRPCPart struct {
	// ID is the JSON-RPC id as received, byte-for-byte, preserving its JSON
	// type. Absent for notifications.
	ID json.RawMessage `json:"id,omitempty"`
	// Method is the JSON-RPC method name.
	Method string `json:"method"`
	// Name is the extracted primitive name (e.g. a tool name), when applicable.
	Name string `json:"name,omitempty"`
	// Params is the raw params object as received.
	Params json.RawMessage `json:"params,omitempty"`
	// Body is the exact received request bytes, subject to the capture mode
	// (full retains all; truncate retains a prefix; digest and off retain
	// none).
	Body []byte `json:"body,omitempty"`
	// BodySHA256 is the SHA-256 of the whole body, hex; set for truncate and
	// digest modes.
	BodySHA256 string `json:"bodySha256,omitempty"`
	// BodyLength is the original body length in bytes, before any truncation.
	BodyLength int `json:"bodyLength"`
	// BodyTruncated is true when [JSONRPCPart.Body] holds only a prefix.
	BodyTruncated bool `json:"bodyTruncated,omitempty"`
	// IsNotification is true when the message carried no id.
	IsNotification bool `json:"isNotification"`
}

// ResponsePart holds the produced response (MOCK-601.4).
//
// Stability: v0.
type ResponsePart struct {
	// Status is the HTTP status code (200 for a JSON-RPC error carried in a
	// successful envelope).
	Status int `json:"status"`
	// ResultType is the MCP resultType of the response (MOCK-209), when known.
	ResultType string `json:"resultType,omitempty"`
	// Body is the response bytes, subject to the capture mode.
	Body []byte `json:"body,omitempty"`
	// BodySHA256 is the SHA-256 of the whole response body, hex; set for
	// truncate and digest modes.
	BodySHA256 string `json:"bodySha256,omitempty"`
	// Shape is the response shape: "json" or "sse".
	Shape string `json:"shape"`
	// Frames is the ordered list of SSE frames for a streamed response.
	Frames []FrameRecord `json:"frames,omitempty"`
	// CloseReason is why the response stream closed, when it did.
	CloseReason string `json:"closeReason,omitempty"`
	// Error is the JSON-RPC error object, when the response was an error.
	Error *ErrorRecord `json:"error,omitempty"`
}

// FrameRecord is one recorded SSE frame in response order.
//
// Stability: v0.
type FrameRecord struct {
	// Seq is the frame's index within the response, from zero.
	Seq int `json:"seq"`
	// EventID is the SSE event id, when set. Volatile in goldens.
	EventID string `json:"eventId,omitempty"`
	// EventType is the SSE event type, when set.
	EventType string `json:"eventType,omitempty"`
	// Data is the frame payload, subject to the capture mode.
	Data []byte `json:"data,omitempty"`
	// DataSHA256 is the SHA-256 of the whole frame payload, hex, for
	// truncate/digest modes.
	DataSHA256 string `json:"dataSha256,omitempty"`
}

// ErrorRecord is a recorded JSON-RPC error object (MOCK-505).
//
// Code is an unconstrained integer because mcpmock must be able to emit
// arbitrary and retired codes.
//
// Stability: v0.
type ErrorRecord struct {
	// Code is the JSON-RPC error code.
	Code int `json:"code"`
	// Message is the error message.
	Message string `json:"message"`
	// Data is the raw error data payload, when present.
	Data json.RawMessage `json:"data,omitempty"`
}

// CredentialPart records a presented credential as a hash and non-sensitive
// parsed claims only (MOCK-407, security.md §5).
//
// There is deliberately no Raw field, no Value field and no open map: the raw
// credential is unrepresentable. Its absence is the control (MOCK-407.2). The
// capture function computes [CredentialPart.Hash] and lets the raw bytes go out
// of scope.
//
// Stability: v0. No field that could carry a raw secret will ever be added;
// that is a fixed property of this contract, not a convention.
type CredentialPart struct {
	// Present is true when a credential was presented.
	Present bool `json:"present"`
	// Scheme is the auth scheme (e.g. "Bearer", "Basic").
	Scheme string `json:"scheme,omitempty"`
	// HashAlg names the hash construction, e.g. "hmac-sha256/128".
	HashAlg string `json:"hashAlg,omitempty"`
	// Hash is the keyed hash of the raw value, hex. Never the raw value.
	// Volatile in goldens (the key may be per-process).
	Hash string `json:"hash,omitempty"`
	// Audience is the parsed JWT "aud" claim, when the value parsed as a JWT.
	Audience []string `json:"aud,omitempty"`
	// Issuer is the parsed JWT "iss" claim.
	Issuer string `json:"iss,omitempty"`
	// Subject is the parsed JWT "sub" claim.
	Subject string `json:"sub,omitempty"`
	// Scopes is the parsed scope list.
	Scopes []string `json:"scope,omitempty"`
	// ExpiresAt is the parsed JWT "exp" claim.
	ExpiresAt *time.Time `json:"exp,omitempty"`
	// KeyID is the parsed JWT "kid" header.
	KeyID string `json:"kid,omitempty"`
	// Decision is the authorization outcome: "accepted" or an ADR-020 reason.
	Decision string `json:"decision,omitempty"`
}

// MetaValidationPart records the outcome of _meta presence validation
// (MOCK-203, AMEND-6).
//
// In lenient mode mcpmock accepts a request that a strict server would reject
// but still records what was missing, so the latent hub bug stays visible; in
// off mode Missing is always empty because the work is skipped. The distinction
// is therefore observable here.
//
// Stability: v0.
type MetaValidationPart struct {
	// Mode is the configured strictness: "strict", "lenient" or "off".
	Mode string `json:"mode"`
	// Outcome is the recorded outcome label matching the metric:
	// "ok", "rejected", "tolerated" or "skipped".
	Outcome string `json:"outcome"`
	// Missing lists the _meta fields a strict server would have required but
	// that were absent (e.g. "protocolVersion", "clientCapabilities"). Always
	// empty in off mode; populated in strict and lenient.
	Missing []string `json:"missing,omitempty"`
}

// ValidationPart records a tools/call argument schema-validation outcome
// (MOCK-606).
//
// Stability: v0.
type ValidationPart struct {
	// Status is one of "valid", "invalid", "schema_unusable", "timeout" or
	// "skipped".
	Status string `json:"status"`
	// SchemaRef identifies the schema that was applied.
	SchemaRef string `json:"schemaRef,omitempty"`
	// DurationNs is validation time in nanoseconds. Volatile in goldens.
	DurationNs int64 `json:"durationNs"`
	// Errors lists the validation errors when Status is "invalid".
	Errors []SchemaErr `json:"errors,omitempty"`
}

// SchemaErr is one schema-validation error (MOCK-606).
//
// Stability: v0.
type SchemaErr struct {
	// Pointer is the JSON Pointer into the instance that failed.
	Pointer string `json:"pointer"`
	// Keyword is the failing schema keyword.
	Keyword string `json:"keyword"`
	// Message is the human-readable explanation.
	Message string `json:"message"`
}

// MRTRPart carries multi-round tool-result correlation fields (MOCK-605,
// ADR-010).
//
// Stability: v0.
type MRTRPart struct {
	// ChainID is the stable grouping key across rounds (MOCK-605).
	ChainID string `json:"chainId"`
	// Round is the one-based round number.
	Round int `json:"round"`
	// InitialID is the raw JSON-RPC id of the first round.
	InitialID json.RawMessage `json:"initialId,omitempty"`
	// RequestState is the opaque continuation token; redacted from goldens by
	// default because it is volatile.
	RequestState string `json:"requestState,omitempty"`
	// VerifyReason is the requestState verification outcome, one of ADR-010's
	// reason vocabulary when a rejection occurred.
	VerifyReason string `json:"verifyReason,omitempty"`
	// OutstandingKeys lists inputs still awaited.
	OutstandingKeys []string `json:"outstandingKeys,omitempty"`
}

// SessionPart carries legacy-era session fields.
//
// Stability: v0.
type SessionPart struct {
	// ID is the session id; redacted from goldens by default (volatile).
	ID string `json:"id,omitempty"`
	// NegotiatedVersion is the version agreed at handshake.
	NegotiatedVersion string `json:"negotiatedVersion,omitempty"`
}

// SubscriptionPart carries subscription fields.
//
// Stability: v0.
type SubscriptionPart struct {
	// ID is the subscription id; redacted from goldens by default (volatile).
	ID string `json:"id,omitempty"`
	// AcceptedTypes lists the notification types the subscription accepted.
	AcceptedTypes []string `json:"acceptedTypes,omitempty"`
}

// CorrelationPart carries W3C trace context (MOCK-603 §3.5).
//
// TraceparentValid is a first-class field because a malformed traceparent is a
// reported finding, not merely a missing one.
//
// Stability: v0.
type CorrelationPart struct {
	// TraceID is the W3C trace-id, when a valid traceparent was present.
	// Volatile in goldens.
	TraceID string `json:"traceId,omitempty"`
	// SpanID is the W3C parent span-id. Volatile in goldens.
	SpanID string `json:"spanId,omitempty"`
	// Sampled is the sampled flag from the traceparent.
	Sampled bool `json:"sampled,omitempty"`
	// TraceparentRaw is the raw inbound traceparent value, retained so a
	// malformed value can be reported verbatim.
	TraceparentRaw string `json:"traceparentRaw,omitempty"`
	// TraceparentValid is true only when TraceparentRaw parsed as a valid W3C
	// traceparent. A false with a non-empty TraceparentRaw is the malformed
	// finding class (MOCK-603 §3.5).
	TraceparentValid bool `json:"traceparentValid"`
	// Tracestate is the raw inbound tracestate value.
	Tracestate string `json:"tracestate,omitempty"`
}

// FaultFired records a fault that fired for a request (MOCK-505).
//
// Stability: v0.
type FaultFired struct {
	// RuleID is the id of the fault rule that fired.
	RuleID string `json:"ruleId"`
	// Phase is where it fired (e.g. "request", "response", "connection").
	Phase string `json:"phase,omitempty"`
	// Action is the action the rule took.
	Action string `json:"action,omitempty"`
}

// CancelPart records client cancellation (MOCK-212).
//
// Stability: v0.
type CancelPart struct {
	// ElapsedNs is how long the request ran before cancellation, nanoseconds.
	// Volatile in goldens.
	ElapsedNs int64 `json:"elapsedNs"`
	// Reason is the cancellation cause (e.g. "clientDisconnect", a client
	// cancel notification, or "stdinEOF"). The engine sets the exact value.
	Reason string `json:"reason,omitempty"`
}
