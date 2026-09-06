package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// httpTransport speaks JSON-RPC over HTTP POST (streamable-http, MOCK-102).
// It has two modes:
//
//   - the default mode uses net/http, which is the realistic path a hub takes;
//   - raw-socket mode (rawSocket=true) writes the HTTP/1.1 request bytes to a
//     raw TCP connection, so it can transmit duplicate headers with differing
//     casing (x-mcp-header twice) WITHOUT net/http's http.Header
//     canonicalisation intervening. This is required by MOCK-601.2 / TASK-008
//     acceptance criterion 5.
type httpTransport struct {
	endpoint  string
	parsedURL *url.URL
	client    *http.Client
	rawSocket bool
	timeout   time.Duration
}

// HTTPOption configures an HTTP [Client].
type HTTPOption func(*httpTransport)

// WithHTTPClient supplies a custom *http.Client (e.g. one wired to an
// httptest.Server or a custom transport). When unset a client with a bounded
// per-request timeout is used.
func WithHTTPClient(hc *http.Client) HTTPOption {
	return func(t *httpTransport) { t.client = hc }
}

// WithHTTPTimeout bounds each round trip. Zero leaves the client's own timeout
// in force. Every outbound call is time-bounded either way (via this or the
// caller's context).
func WithHTTPTimeout(d time.Duration) HTTPOption {
	return func(t *httpTransport) { t.timeout = d }
}

// WithRawSocket switches the transport into raw-socket mode, in which requests
// are written as literal HTTP/1.1 bytes over a fresh TCP connection so that
// header names are transmitted with their exact casing and duplicates, bypassing
// http.Header canonicalisation. Only http:// endpoints are supported in this
// mode; it exists to exercise the server's on-the-wire header handling, not TLS.
func WithRawSocket() HTTPOption {
	return func(t *httpTransport) { t.rawSocket = true }
}

func newHTTPTransport(endpoint string, opts ...HTTPOption) (*httpTransport, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: parse endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("mcpclient: endpoint scheme %q is not http/https", u.Scheme)
	}
	t := &httpTransport{
		endpoint:  endpoint,
		parsedURL: u,
		timeout:   30 * time.Second,
	}
	for _, o := range opts {
		o(t)
	}
	if t.client == nil {
		t.client = &http.Client{Timeout: t.timeout}
	}
	if t.rawSocket && u.Scheme != "http" {
		return nil, errors.New("mcpclient: raw-socket mode requires an http:// endpoint")
	}
	return t, nil
}

func (t *httpTransport) roundTrip(ctx context.Context, payload []byte, hdrs []Header) ([]byte, bool, error) {
	if t.rawSocket {
		return t.roundTripRaw(ctx, payload, hdrs)
	}
	return t.roundTripNet(ctx, payload, hdrs)
}

// roundTripNet is the ordinary net/http path.
func (t *httpTransport) roundTripNet(ctx context.Context, payload []byte, hdrs []Header) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, false, fmt.Errorf("build http request: %w", err)
	}
	req.Header.Set(HeaderContentType, ContentTypeJSON)
	for _, h := range hdrs {
		// Add preserves multiplicity; net/http still canonicalises the NAME,
		// which is exactly why raw-socket mode exists for the casing case.
		req.Header.Add(h.Name, h.Value)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read http response: %w", err)
	}
	return body, true, nil
}

// roundTripRaw writes a literal HTTP/1.1 request over a raw TCP connection so
// header casing and duplicates survive on the wire, then parses one response.
func (t *httpTransport) roundTripRaw(ctx context.Context, payload []byte, hdrs []Header) ([]byte, bool, error) {
	host := t.parsedURL.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, false, fmt.Errorf("raw dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else if t.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(t.timeout))
	}

	reqBytes := t.buildRawRequest(payload, hdrs)
	if _, err := conn.Write(reqBytes); err != nil {
		return nil, false, fmt.Errorf("raw write: %w", err)
	}

	// Parse the response with net/http's reader, which is fine on the RESPONSE
	// side — canonicalisation there does not affect the request-side property
	// under test.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, false, fmt.Errorf("raw read response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("raw read body: %w", err)
	}
	return body, true, nil
}

// buildRawRequest assembles the literal request bytes. Header names are written
// verbatim, in order, with duplicates preserved — no canonicalisation. The
// caller is responsible for supplying Content-Type if it wants one; a default
// Content-Type is added only if the caller did not provide any header of that
// name (case-insensitively), so a test can deliberately omit or mis-case it.
func (t *httpTransport) buildRawRequest(payload []byte, hdrs []Header) []byte {
	reqPath := t.parsedURL.RequestURI()
	if reqPath == "" {
		reqPath = "/"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "POST %s HTTP/1.1\r\n", reqPath)
	fmt.Fprintf(&b, "Host: %s\r\n", t.parsedURL.Host)
	// Explicit Content-Length so the server can frame the body; Connection:
	// close so http.ReadResponse gets a clean EOF-terminated body.
	fmt.Fprintf(&b, "Content-Length: %s\r\n", strconv.Itoa(len(payload)))
	b.WriteString("Connection: close\r\n")
	if !hasHeader(hdrs, HeaderContentType) {
		fmt.Fprintf(&b, "%s: %s\r\n", HeaderContentType, ContentTypeJSON)
	}
	for _, h := range hdrs {
		// Verbatim: exact name casing, duplicates preserved. This is the whole
		// point of raw-socket mode (MOCK-601.2).
		fmt.Fprintf(&b, "%s: %s\r\n", h.Name, h.Value)
	}
	b.WriteString("\r\n")
	out := make([]byte, 0, b.Len()+len(payload))
	out = append(out, b.String()...)
	out = append(out, payload...)
	return out
}

// hasHeader reports whether name (case-insensitively) is present in hdrs.
func hasHeader(hdrs []Header, name string) bool {
	for _, h := range hdrs {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

// close is a no-op for the HTTP transport: connections are per-request.
func (t *httpTransport) close() error { return nil }
