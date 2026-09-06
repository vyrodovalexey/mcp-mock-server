package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the only value JSON-RPC 2.0 permits in the "jsonrpc" member.
//
// mcpmock must be able to represent a request whose jsonrpc member is missing
// or wrong (MOCK-503 "missing jsonrpc"), so the envelope stores the member's
// presence and value rather than asserting it; validation is a separate,
// explicit step. This constant is the value a well-formed envelope carries.
const Version = "2.0"

// Error is a JSON-RPC 2.0 error object (§5). Code is an int, never an enum:
// MOCK-505 (Phase 9) requires arbitrary and retired codes to be emittable, and
// the Phase 1 codes are named in codes.go. Data is an optional, opaque payload
// preserved as raw JSON so an authored data shape (for example the -32602
// data.missing payload, TASK-010) survives byte-for-byte.
type Error struct {
	// Code is the numeric error code (JSON-RPC 2.0 §5.1).
	Code int `json:"code"`
	// Message is a short human-readable description.
	Message string `json:"message"`
	// Data is an optional application-defined payload, omitted when absent.
	Data json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface so an [Error] can be returned and
// matched with errors.As. The rendering is stable and includes the code.
func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc: code %d: %s", e.Code, e.Message)
}

// NewError builds an [Error] with the given code and message and no data.
func NewError(code int, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Request is a JSON-RPC 2.0 request or notification envelope.
//
// The envelope deliberately does not enforce JSON-RPC well-formedness on
// decode: MOCK-503 must be able to represent a request with a missing or wrong
// jsonrpc member, a null id, or a response-shaped payload. Presence of each
// member is tracked so those pathological cases stay distinguishable from their
// well-formed neighbors; [Request.Validate] performs the checks when they are
// wanted.
type Request struct {
	// Version is the value of the jsonrpc member, or "" if it was absent.
	Version string
	// HasVersion records whether a jsonrpc member was present at all, so a
	// missing member is distinguishable from an empty-string value.
	HasVersion bool
	// ID is the request id, preserving its raw bytes and kind. It is AbsentID
	// for a notification.
	ID ID
	// Method is the method name. It is "" when absent; presence is tracked by
	// HasMethod.
	Method string
	// HasMethod records whether a method member was present.
	HasMethod bool
	// Params is the raw params value, preserved verbatim so authored key order
	// survives (MOCK-222.4). It is nil when absent.
	Params json.RawMessage
}

// IsNotification reports whether the request is a notification: a request with
// no id member (JSON-RPC 2.0 §4.1). An explicit null id is not a notification.
func (r *Request) IsNotification() bool {
	return r.ID.IsAbsent()
}

// DecodeRequest parses a single JSON-RPC request from data.
//
// It returns a parse failure (code -32700) when data is not valid JSON, and
// otherwise fills a [Request] whose member presence flags let a caller detect
// the MOCK-503 pathologies (missing jsonrpc, null id, absent method) without
// this function deciding they are errors. It never panics, including on
// truncated or deeply nested input; nesting depth is bounded by the standard
// library decoder. Trailing content after the first JSON value is rejected as a
// parse error, matching one-message-per-frame framing (wire clause 1.9).
//
// Member presence is recovered from a raw member map rather than from struct
// pointers, because encoding/json does not call a field's UnmarshalJSON for a
// JSON null and would leave a *ID field nil — making an explicit null id
// (present) indistinguishable from an absent id (a notification). That
// distinction is load-bearing for MOCK-503, so it is decoded structurally.
func DecodeRequest(data []byte) (*Request, error) {
	if err := checkSingleJSONValue(data); err != nil {
		return nil, err
	}
	// The top level must be a JSON object to be a JSON-RPC request; anything
	// else is an invalid request the caller reports via Validate, but a
	// non-object (array, scalar) cannot even carry members, so it parses to an
	// empty member set and fails Validate as "missing method".
	var members map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&members); err != nil {
		// A syntactically valid but non-object top level (e.g. "5" or "[]")
		// is not a parse error; represent it as an empty request that fails
		// validation. Only genuine syntax errors are -32700.
		if !isTypeError(err) {
			return nil, parseErrorFrom(err)
		}
		return &Request{}, nil
	}

	req := &Request{}
	if raw, ok := members["jsonrpc"]; ok {
		var v string
		if err := json.Unmarshal(raw, &v); err == nil {
			req.Version = v
		}
		req.HasVersion = true
	}
	if raw, ok := members["method"]; ok {
		var v string
		if err := json.Unmarshal(raw, &v); err == nil {
			req.Method = v
		}
		req.HasMethod = true
	}
	if raw, ok := members["params"]; ok {
		req.Params = raw
	}
	if raw, ok := members["id"]; ok {
		// The member is present (possibly null); preserve its raw bytes.
		id, err := RawID(raw)
		if err != nil {
			return nil, parseErrorFrom(err)
		}
		req.ID = id
	}
	return req, nil
}

// isTypeError reports whether err is an encoding/json type mismatch (a valid
// JSON value of the wrong Go type) rather than a syntax error.
func isTypeError(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

// Validate checks the request against JSON-RPC 2.0 well-formedness and returns
// a JSON-RPC [Error] (never a bare Go error) suitable for emission:
//
//   - CodeInvalidRequest (-32600) if the jsonrpc member is absent or not "2.0",
//     or if the method member is absent.
//
// It is deliberately separate from decoding so the engine can journal a
// malformed request before rejecting it, and so MOCK-503 can construct invalid
// requests that decode cleanly but fail here. It returns nil for a well-formed
// request or notification.
func (r *Request) Validate() *Error {
	if !r.HasVersion || r.Version != Version {
		return NewError(CodeInvalidRequest, "invalid Request: jsonrpc must be \"2.0\"")
	}
	if !r.HasMethod {
		return NewError(CodeInvalidRequest, "invalid Request: missing method")
	}
	return nil
}

// Response is a JSON-RPC 2.0 response envelope (§5). Exactly one of Result or
// Err is meaningful for a well-formed response, but the type can hold either,
// both, or neither so MOCK-503 (response to a notification, unsolicited
// response) is representable. Result is raw JSON so an authored result body is
// emitted with its key order intact (MOCK-222.4); the canonical encoder is
// never applied to it.
type Response struct {
	// ID echoes the request id byte-for-byte (MOCK-203). For a response to a
	// notification or an unsolicited response it may be AbsentID or NullID.
	ID ID
	// Result is the raw result value. Nil when the response carries an error.
	Result json.RawMessage
	// Err is the error object. Nil when the response carries a result.
	Err *Error
}

// NewResultResponse builds a success response echoing id and carrying result.
// The result bytes are stored as-is and are the caller's responsibility to
// have produced in the desired key order.
func NewResultResponse(id ID, result json.RawMessage) *Response {
	return &Response{ID: id, Result: result}
}

// NewErrorResponse builds a failure response echoing id and carrying err.
func NewErrorResponse(id ID, err *Error) *Response {
	return &Response{ID: id, Err: err}
}

// responseWire is the on-the-wire member set of a response. The jsonrpc member
// is always emitted as Version. result and error are mutually exclusive on a
// well-formed response; both are pointers so each can be omitted independently.
type responseWire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Encode is the byte-stable response emitter and the entry point the engine
// uses to turn a response into wire bytes.
//
// The id is echoed exactly as stored and the jsonrpc member is fixed at "2.0".
// HTML escaping is disabled so characters such as <, > and & in an authored
// result survive unaltered — the same byte-stability discipline the canonical
// encoder applies (see the package documentation). A response with neither
// result nor error emits neither member, which is what MOCK-503's "unsolicited
// response" case requires; a caller wanting strict JSON-RPC must set exactly
// one.
//
// Prefer Encode over passing a *Response to json.Marshal: json.Marshal invokes
// MarshalJSON and then re-escapes the bytes it returns, re-introducing the HTML
// escapes this method suppresses. Encode returns the final bytes directly.
func (r *Response) Encode() ([]byte, error) {
	idBytes, err := r.ID.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: encoding response id: %w", err)
	}
	w := responseWire{
		JSONRPC: Version,
		ID:      idBytes,
		Result:  r.Result,
		Error:   r.Err,
	}
	return marshalNoHTMLEscape(w)
}

// MarshalJSON implements json.Marshaler for interoperability. Note that when a
// *Response is passed to json.Marshal, the standard library re-escapes the
// returned bytes and HTML-significant characters become \u003c/\u003e/\u0026.
// For byte-stable emission use [Response.Encode] instead.
func (r *Response) MarshalJSON() ([]byte, error) {
	return r.Encode()
}

// checkSingleJSONValue confirms data is exactly one JSON value with no trailing
// content, returning a -32700 parse error otherwise. It bounds work by the
// standard decoder and never panics.
func checkSingleJSONValue(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var probe json.RawMessage
	if err := dec.Decode(&probe); err != nil {
		return parseErrorFrom(err)
	}
	if dec.More() {
		return NewError(CodeParseError, "Parse error: trailing content after JSON value")
	}
	return nil
}

// parseErrorFrom converts a decode failure into a JSON-RPC -32700 parse error,
// preserving the underlying cause for errors.Is/As at the call site while
// presenting the standard message to the wire.
func parseErrorFrom(err error) *Error {
	var synErr *json.SyntaxError
	if errors.As(err, &synErr) {
		return &Error{
			Code:    CodeParseError,
			Message: fmt.Sprintf("Parse error: %s", synErr.Error()),
		}
	}
	return &Error{Code: CodeParseError, Message: fmt.Sprintf("Parse error: %s", err.Error())}
}

// marshalNoHTMLEscape encodes v as JSON with HTML escaping disabled and without
// the trailing newline json.Encoder appends. It is the shared primitive behind
// byte-stable response emission: encoding/json's default escapes <, > and & to
// \u003c, \u003e and \u0026, which silently changes the bytes of an authored
// body and breaks golden-file comparison.
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	// Encode always appends exactly one '\n'; trim it for a bare value.
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}
