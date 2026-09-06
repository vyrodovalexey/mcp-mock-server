package httpx

import (
	"bytes"
	"context"
	"net"
	"sync"
)

// rawHeaders exists because MOCK-601.2 requires the journal to record HTTP
// headers in WIRE ORDER, with duplicates and ORIGINAL CASING — and net/http's
// http.Header canonicalises names ("x-mcp-header" → "X-Mcp-Header"), drops the
// original order, and merges duplicates' ordering into a value slice. To record
// what was actually sent, the transport must see the header block before
// net/http rewrites it.
//
// The stdlib-only mechanism: wrap each accepted net.Conn so it tees the CURRENT
// request head (everything up to the terminating CRLFCRLF) into a per-conn
// buffer, then expose that buffer to the handler through http.Server.ConnContext
// (which hands us back the very conn we returned from Accept). The handler
// parses the buffered head to recover the exact header lines, and consuming it
// re-arms the tee so the NEXT request on a reused (keep-alive) connection
// captures ITS OWN head rather than inheriting the first request's headers
// (REV-003). This adds no goroutine and one small buffer per connection, and
// touches nothing on the hot response path.

// connKey is the context key under which the wrapped conn is stashed. It is an
// unexported zero-size type per the context-keys-type rule, so no other package
// can collide with it.
type connKey struct{}

// headCap bounds how many bytes of the request head are teed per connection. A
// well-formed MCP request head is well under this; a larger head simply is not
// fully captured, which degrades header fidelity but never blocks or allocates
// unboundedly.
const headCap = 1 << 16 // 64 KiB, matching http.Server MaxHeaderBytes below.

// headConn wraps a net.Conn to capture the bytes of the CURRENT request head.
// Once the terminating blank line is seen, capture stops and reads pass straight
// through until the head is consumed at exchange-build time, which re-arms the
// tee for the NEXT request on the connection. This per-request re-arm is what
// keeps a keep-alive connection from misattributing request #1's headers — and
// its Authorization credential — to every later request on the same connection
// (MOCK-601.2, REV-003). HTTP/1.1 serializes requests on a connection, so the
// next head cannot begin arriving until the current handler has returned and
// takeHead has re-armed, which makes the re-arm race-free against Read.
type headConn struct {
	net.Conn
	mu   sync.Mutex
	buf  bytes.Buffer
	done bool // current head fully captured; stop teeing until re-armed
}

// Read tees bytes into buf until the head terminator is seen. It never grows buf
// past headCap.
func (c *headConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.tee(p[:n])
	}
	return n, err
}

// tee copies head bytes into the buffer until the blank-line terminator or the
// cap is reached.
func (c *headConn) tee(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	room := headCap - c.buf.Len()
	if room <= 0 {
		c.done = true
		return
	}
	if len(b) > room {
		b = b[:room]
	}
	c.buf.Write(b)
	if bytes.Contains(c.buf.Bytes(), []byte("\r\n\r\n")) {
		c.done = true
	}
}

// takeHead returns a copy of the current request's captured head bytes and
// re-arms the tee for the next request on the connection: it clears the buffer
// and reopens capture (done = false). It is called EXACTLY ONCE per request,
// from buildExchange, after net/http has finished reading the request head and
// before the next request's head can arrive (HTTP/1.1 is serialized per
// connection). Re-arming here — rather than never (the REV-003 bug) — is what
// makes each journal record carry its own headers on a keep-alive connection.
func (c *headConn) takeHead() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	head := append([]byte(nil), c.buf.Bytes()...)
	c.buf.Reset()
	c.done = false
	return head
}

// wrapConn is http.Server.ConnContext: it stashes the wrapped conn on the
// per-connection context so the handler can retrieve its captured head.
func wrapConn(ctx context.Context, c net.Conn) context.Context {
	if hc, ok := c.(*headConn); ok {
		return context.WithValue(ctx, connKey{}, hc)
	}
	return ctx
}

// headListener wraps a net.Listener so every accepted conn is a headConn.
type headListener struct {
	net.Listener
}

// Accept returns a headConn wrapping the underlying accepted conn.
func (l headListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &headConn{Conn: c}, nil
}

// wireHeaders parses the captured head into the wire-order, casing- and
// duplicate-preserving header list the journal expects (MOCK-601.2). It returns
// nil when no head was captured (e.g. HTTP/2, where header casing is not a wire
// concept and this listener path does not apply). It never allocates on the
// response path — it runs once per request during Exchange construction.
func wireHeaders(ctx context.Context) [][2]string {
	hc, ok := ctx.Value(connKey{}).(*headConn)
	if !ok {
		return nil
	}
	head := hc.takeHead()
	i := bytes.Index(head, []byte("\r\n\r\n"))
	if i >= 0 {
		head = head[:i]
	}
	lines := bytes.Split(head, []byte("\r\n"))
	if len(lines) <= 1 {
		return nil
	}
	// lines[0] is the request line ("POST /mcp HTTP/1.1"); header lines follow.
	out := make([][2]string, 0, len(lines)-1)
	sc := lines[1:]
	for _, ln := range sc {
		if len(ln) == 0 {
			continue
		}
		colon := bytes.IndexByte(ln, ':')
		if colon < 0 {
			continue
		}
		name := string(ln[:colon])
		val := string(bytes.TrimLeft(ln[colon+1:], " \t"))
		out = append(out, [2]string{name, val})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
