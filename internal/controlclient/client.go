package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
)

// defaultTimeout bounds every control request. The control API is loopback or a
// unix socket and answers from memory, so a short deadline is generous; it also
// means `mcpmock ctl` never hangs against a wedged server (MOCK-508).
const defaultTimeout = 10 * time.Second

// pathPrefix is the versioned control API prefix (ADR-015). It must match the
// server's; the parity of route paths is asserted by TestRouteParity.
const pathPrefix = "/v1"

// unixHost is the fixed authority used in the request URL when dialing a unix
// socket. The socket path is carried by the dialer, not the URL, so the host is
// a constant placeholder that never leaves the process.
const unixHost = "unix"

// Client is a control-API client bound to exactly one endpoint: a TCP base URL
// or a unix-domain socket. It is safe for concurrent use. It dials only the
// operator-supplied endpoint — never a URL derived from an inbound request —
// which is why it lives in this sibling package away from the handler (see the
// package doc's gosec G704 note).
type Client struct {
	// base is the scheme+authority a request path is appended to. For a TCP
	// endpoint it is the operator's --url; for a socket it is "http://unix".
	base string
	// hc is the underlying HTTP client. For a socket endpoint its transport
	// dials the socket for every connection; for TCP it is the stdlib default
	// with a bounded timeout.
	hc *http.Client
}

// Endpoint selects where a [Client] connects. Exactly one of URL or Socket must
// be set; SetURL/SetSocket on the CLI enforce that precedence (flag > env >
// default) before an Endpoint is built.
type Endpoint struct {
	// URL is a TCP base URL such as "http://127.0.0.1:8081". Mutually exclusive
	// with Socket.
	URL string
	// Socket is a unix-domain-socket path. Mutually exclusive with URL.
	Socket string
}

// New builds a [Client] for ep. It returns an error when neither or both of the
// endpoint fields are set, or when a supplied URL is unparsable, so a
// misconfiguration is reported at construction rather than on first request.
func New(ep Endpoint) (*Client, error) {
	hasURL := ep.URL != ""
	hasSock := ep.Socket != ""
	switch {
	case hasURL && hasSock:
		return nil, errors.New("controlclient: set exactly one of URL or Socket, not both")
	case !hasURL && !hasSock:
		return nil, errors.New("controlclient: an endpoint (URL or Socket) is required")
	case hasURL:
		return newTCP(ep.URL)
	default:
		return newUnix(ep.Socket)
	}
}

// newTCP builds a client for a TCP base URL, validating the URL shape.
func newTCP(raw string) (*Client, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("controlclient: parse url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("controlclient: url %q must be http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("controlclient: url %q has no host", raw)
	}
	base := u.Scheme + "://" + u.Host
	return &Client{
		base: base,
		hc:   &http.Client{Timeout: defaultTimeout},
	}, nil
}

// newUnix builds a client that dials the unix socket at path for every request.
// The request URL carries a fixed placeholder host; the dialer ignores it and
// connects to the socket, so the same shared control [Handler] is reached over
// the socket transport.
func newUnix(path string) (*Client, error) {
	hc := &http.Client{
		Timeout: defaultTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
	return &Client{base: "http://" + unixHost, hc: hc}, nil
}

// get issues GET base+pathPrefix+p with query q and decodes a JSON body into
// out. A control error envelope is mapped back to a control sentinel via
// statusToError. out may be nil for a no-body response (e.g. 204).
func (c *Client) get(ctx context.Context, p string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, p, q, out)
}

// del issues DELETE base+pathPrefix+p, expecting a 204. It is the clearJournal
// verb's transport.
func (c *Client) del(ctx context.Context, p string, q url.Values) error {
	return c.do(ctx, http.MethodDelete, p, q, nil)
}

// do performs the request and dispatches the response to decodeResponse. It
// propagates ctx into the request and never substitutes a fresh context, so a
// caller's cancellation reaches the wire (ADR-007).
func (c *Client) do(ctx context.Context, method, p string, q url.Values, out any) error {
	target := c.base + pathPrefix + p
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, http.NoBody)
	if err != nil {
		return fmt.Errorf("controlclient: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("controlclient: %s %s: %w", method, p, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeResponse(resp, out)
}

// decodeResponse maps an HTTP response to a Go outcome: a 2xx decodes into out
// (or returns nil for an empty body), and a 4xx/5xx is mapped to a control
// sentinel error carrying the server's message.
func decodeResponse(resp *http.Response, out any) error {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || len(body) == 0 {
			return nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("controlclient: decode response: %w", err)
		}
		return nil
	}
	return statusToError(resp.StatusCode, body)
}

// wireError mirrors the control error envelope (control.errorObject is
// unexported). The client decodes just the fields it branches on.
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// statusToError maps a control error envelope to the matching control sentinel,
// so a caller can branch with errors.Is(err, control.ErrNotFound) regardless of
// which transport carried the response. The server's human message is preserved
// in the wrapped error text.
func statusToError(status int, body []byte) error {
	var we wireError
	_ = json.Unmarshal(body, &we)
	msg := strings.TrimSpace(we.Message)
	if msg == "" {
		msg = http.StatusText(status)
	}
	sentinel := sentinelForCode(we.Code, status)
	return fmt.Errorf("controlclient: %w: %s", sentinel, msg)
}

// sentinelForCode picks the control sentinel for a wire code, falling back to a
// status-based choice when the code is absent or unknown. Keeping the mapping
// here (not a shared table) keeps the client decoupled from the handler's
// internals while still agreeing on identity through the exported sentinels.
func sentinelForCode(code string, status int) error {
	switch code {
	case "not_found":
		return control.ErrNotFound
	case "unsupported":
		return control.ErrUnsupported
	case "validation_failed":
		return control.ErrValidation
	case "unauthorized":
		return control.ErrUnauthorized
	}
	switch status {
	case http.StatusNotFound:
		return control.ErrNotFound
	case http.StatusNotImplemented:
		return control.ErrUnsupported
	case http.StatusUnprocessableEntity:
		return control.ErrValidation
	case http.StatusUnauthorized:
		return control.ErrUnauthorized
	default:
		return errControl
	}
}

// errControl is the generic control-side failure for a status with no specific
// sentinel (e.g. 500). It carries no server detail beyond the message the
// envelope provided.
var errControl = errors.New("control error")
