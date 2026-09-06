// Command ssebench measures the honest, RSS-based, live-socket per-stream memory
// cost of the mcpmock SSE seam for MOCK-903 / TASK-029 (test asset).
//
// TASK-019 measured ~2.9 KiB/stream but only the Go-heap slice it owned (SSESink
// + wheel registration + stream-loop stack), EXCLUDING net/http's per-connection
// read goroutine, bufio buffers, and kernel socket buffers. This harness includes
// all of them: it drives the REAL exported production seam
//
//	httpx.Server.Stream(ctx, httpx.NewSSESink(w, onClose), keepAlive)
//
// from an http.Handler over REAL accepted TCP sockets.
//
// Two modes, so the SERVER-attributable RSS is isolated from the client:
//
//	-mode=server : start the SSE server, print its addr+pid, sample its own RSS
//	               on SIGUSR1-free polling, and stay up until SIGINT.
//	-mode=client : open N persistent GET streams against -addr and hold them.
//	-mode=combined (default): both in one process (client+server socket buffers
//	               both charged here — an over-count, stated).
//
// The orchestrator (run_sse.sh) uses server+client split so `ps` on the server
// pid gives the server-only figure.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
)

func rssKB() int64 {
	v, err := runPS(os.Getpid())
	if err != nil {
		return -1
	}
	return v
}

func main() {
	mode := flag.String("mode", "combined", "server|client|combined")
	addr := flag.String("addr", "", "server addr (client mode)")
	n := flag.Int("n", 1000, "streams to open (client mode)")
	steps := flag.String("steps", "1000,2000,5000,10000,20000", "comma stream counts (combined mode)")
	hold := flag.Duration("hold", 30*time.Second, "hold duration per step")
	keepAlive := flag.Duration("keepalive", 15*time.Second, "SSE keep-alive interval")
	flag.Parse()

	switch *mode {
	case "server":
		runServer(*keepAlive)
	case "client":
		runClient(*addr, *n)
	default:
		runCombined(*steps, *hold, *keepAlive)
	}
}

// startSSEServer builds the real httpx.Server (owns the shared timer wheel) and a
// plain http.Server whose handler drives the production Stream seam over each
// accepted socket. Returns the listen addr and a live-stream counter.
func startSSEServer(keepAlive time.Duration) (string, *int64, func()) {
	hx, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", KeepAliveTick: 10 * time.Millisecond})
	if err != nil {
		panic(err)
	}
	go func() { _ = hx.Serve() }()

	var openStreams int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sink := httpx.NewSSESink(w, nil)
		if err := sink.Begin(engine.ShapeSSEStream, engine.ResponseHeader{Status: http.StatusOK}); err != nil {
			return
		}
		atomic.AddInt64(&openStreams, 1)
		defer atomic.AddInt64(&openStreams, -1)
		// Real production Stream loop over the real socket; context is the
		// request context, cancelled by client disconnect (no extra goroutine).
		_ = hx.Stream(r.Context(), sink, keepAlive)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), &openStreams, func() { _ = srv.Close(); _ = hx.Close() }
}

func runServer(keepAlive time.Duration) {
	addr, counter, stop := startSSEServer(keepAlive)
	defer stop()
	runtime.GC()
	time.Sleep(300 * time.Millisecond)
	fmt.Printf("SSESERVER_ADDR=%s\n", addr)
	fmt.Printf("SSESERVER_PID=%d\n", os.Getpid())
	fmt.Printf("SSESERVER_BASELINE_RSS_KB=%d\n", rssKB())
	os.Stdout.Sync()

	// Report open-stream count + own RSS + goroutines every second to stderr so
	// the orchestrator can log it; the definitive server RSS is sampled by the
	// orchestrator via `ps` on SSESERVER_PID.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-sigc:
			return
		case <-tick.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			fmt.Fprintf(os.Stderr, "streams=%d rss_kb=%d goroutines=%d heap_kb=%d\n",
				atomic.LoadInt64(counter), rssKB(), runtime.NumGoroutine(), int64(ms.HeapAlloc)/1024)
		}
	}
}

func runClient(addr string, n int) {
	if addr == "" {
		fmt.Fprintln(os.Stderr, "client mode requires -addr")
		os.Exit(2)
	}
	var conns []net.Conn
	established := 0
	for i := 0; i < n; i++ {
		c, err := openStream(addr)
		if err != nil {
			fmt.Printf("CLIENT_HALT established=%d err=%v\n", established, err)
			break
		}
		conns = append(conns, c)
		established++
	}
	fmt.Printf("CLIENT_ESTABLISHED=%d\n", established)
	fmt.Printf("CLIENT_PID=%d\n", os.Getpid())
	os.Stdout.Sync()
	// Hold until signalled.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	for _, c := range conns {
		_ = c.Close()
	}
}

func runCombined(steps string, hold, keepAlive time.Duration) {
	addr, counter, stop := startSSEServer(keepAlive)
	defer stop()
	runtime.GC()
	time.Sleep(300 * time.Millisecond)
	baselineRSS := rssKB()
	fmt.Printf("baseline_rss_kb=%d baseline_goroutines=%d\n", baselineRSS, runtime.NumGoroutine())
	fmt.Println("streams,attempted,established,rss_kb,rss_delta_kb,per_stream_bytes,goroutines,heap_alloc_kb,fd_note")

	var conns []net.Conn
	var mu sync.Mutex
	prev := 0
	for _, target := range parseSteps(steps) {
		add := target - prev
		established := 0
		var fdNote string
		for i := 0; i < add; i++ {
			c, err := openStream(addr)
			if err != nil {
				fdNote = fmt.Sprintf("stopped_at_%d_err=%v", prev+established, err)
				break
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			established++
		}
		total := prev + established
		prev = total
		waitStreams(counter, int64(total), 10*time.Second)
		time.Sleep(hold)
		runtime.GC()
		time.Sleep(200 * time.Millisecond)
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		rss := rssKB()
		delta := rss - baselineRSS
		var perStream int64
		if total > 0 {
			perStream = (delta * 1024) / int64(total)
		}
		fmt.Printf("%d,%d,%d,%d,%d,%d,%d,%d,%q\n",
			target, add, established, rss, delta, perStream,
			runtime.NumGoroutine(), int64(ms.HeapAlloc)/1024, fdNote)
		if fdNote != "" {
			fmt.Printf("HALT: %s\n", fdNote)
			break
		}
	}
	mu.Lock()
	for _, c := range conns {
		_ = c.Close()
	}
	mu.Unlock()
	time.Sleep(300 * time.Millisecond)
}

func parseSteps(s string) []int {
	var out []int
	cur, has := 0, false
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] >= '0' && s[i] <= '9' {
			cur = cur*10 + int(s[i]-'0')
			has = true
		} else {
			if has {
				out = append(out, cur)
			}
			cur, has = 0, false
		}
	}
	return out
}

// openStream opens a raw TCP connection, writes a GET request, reads the SSE
// response head, and returns the still-open connection (held to keep the stream
// live). The body stays unread so the stream stays open.
func openStream(addr string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	req := "GET /stream HTTP/1.1\r\nHost: " + addr + "\r\nAccept: text/event-stream\r\n\r\n"
	if err := c.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = c.Close()
		return nil, err
	}
	if _, err := c.Write([]byte(req)); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	if _, err := br.ReadString('\n'); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.SetReadDeadline(time.Time{})
	return c, nil
}

func waitStreams(counter *int64, want int64, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(counter) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
