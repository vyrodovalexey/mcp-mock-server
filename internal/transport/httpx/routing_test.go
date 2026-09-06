package httpx_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// TestRuntimeMountUnmount asserts the router adds and removes mounts atomically
// at runtime (MOCK-103 mechanics): a path is 404 before Mount, served after, and
// 404 again after Unmount.
func TestRuntimeMountUnmount(t *testing.T) {
	srv, base := newServer(t) // no mounts yet

	if status, _ := post(t, base+"/late/mcp", rawRequest("1", wire.MethodToolsList)); status != http.StatusNotFound {
		t.Fatalf("pre-mount status = %d, want 404", status)
	}

	srv.Mount(newMount(9, "late", "/late/mcp", echoHandler(`{"who":"late"}`)))
	if status, body := post(t, base+"/late/mcp", rawRequest("1", wire.MethodToolsList)); status != http.StatusOK {
		t.Fatalf("post-mount status = %d, want 200; body=%s", status, body)
	}

	if !srv.Unmount("/late/mcp") {
		t.Fatal("Unmount reported no mount present")
	}
	if status, _ := post(t, base+"/late/mcp", rawRequest("1", wire.MethodToolsList)); status != http.StatusNotFound {
		t.Fatalf("post-unmount status = %d, want 404", status)
	}
}

// TestConcurrentMutationDoesNotDropInflight asserts acceptance criterion 5: a
// concurrent instance add/remove on OTHER paths does not disturb in-flight
// requests on a stable path. It hammers one mount while churning others.
func TestConcurrentMutationDoesNotDropInflight(t *testing.T) {
	stable := newMount(1, "stable", "/stable/mcp", echoHandler(`{"ok":true}`))
	srv, base := newServer(t, stable)

	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Add(1)
	go func() {
		defer churn.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			path := "/churn/mcp"
			srv.Mount(newMount(uint64(i+100), "churn", path, echoHandler(`{"churn":true}`)))
			srv.Unmount(path)
			i++
		}
	}()

	var reqs sync.WaitGroup
	fail := make(chan int, 64)
	for i := 0; i < 200; i++ {
		reqs.Add(1)
		go func() {
			defer reqs.Done()
			if status, _ := post(t, base+"/stable/mcp", rawRequest("1", wire.MethodToolsCall)); status != http.StatusOK {
				fail <- status
			}
		}()
	}
	reqs.Wait()
	close(stop)
	churn.Wait()
	close(fail)
	for status := range fail {
		t.Fatalf("in-flight request on stable path saw status %d during concurrent mutation", status)
	}
}

// TestGracefulShutdownWithOpenStream asserts MOCK-508 mechanics: Shutdown drains
// and returns; an open, slow request completes rather than being dropped, and
// Shutdown does not hang past its deadline.
func TestGracefulShutdownWithOpenStream(t *testing.T) {
	release := make(chan struct{})
	h := gatedHandler(release, `{"ok":true}`)
	mount := newMount(1, "inst", httpx.DefaultPath, h)
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", Mounts: []*httpx.Mount{mount}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	base := "http://" + srv.Addr().String()

	// Fire a request that blocks in the handler.
	respCh := make(chan int, 1)
	go func() {
		status, _ := post(t, base+httpx.DefaultPath, rawRequest("1", wire.MethodToolsCall))
		respCh <- status
	}()

	// Let the request reach the handler, then begin shutdown.
	time.Sleep(100 * time.Millisecond)
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	// Shutdown must be blocked on the in-flight request, not returned yet.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while a request was still in flight")
	case <-time.After(150 * time.Millisecond):
	}

	// Release the handler; the request completes and Shutdown returns cleanly.
	close(release)
	select {
	case status := <-respCh:
		if status != http.StatusOK {
			t.Fatalf("in-flight request status = %d, want 200 (must not be dropped)", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never completed")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown returned %v, want nil after clean drain", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown hung after in-flight request completed (MOCK-508)")
	}
}

// TestShutdownDeadlineDoesNotHang asserts Shutdown honors its context deadline
// even when a request never completes: it returns the context error rather than
// hanging forever.
func TestShutdownDeadlineDoesNotHang(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := gatedHandler(release, `{"ok":true}`)
	mount := newMount(1, "inst", httpx.DefaultPath, h)
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", Mounts: []*httpx.Mount{mount}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	base := "http://" + srv.Addr().String()

	go func() {
		// Fire-and-forget; this request is deliberately stuck in the handler and
		// its outcome is irrelevant. Use a bare client so a late error never
		// touches *testing.T from this goroutine.
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			base+httpx.DefaultPath, strings.NewReader(string(rawRequest("1", wire.MethodToolsCall))))
		resp, e := http.DefaultClient.Do(req)
		if e == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = srv.Shutdown(ctx)
	if err == nil {
		t.Fatal("Shutdown returned nil despite a stuck request; want deadline error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %v; must honor the 200ms deadline", elapsed)
	}
	_ = srv.Close()
}

// TestHeaderFidelityToJournal asserts acceptance criterion 4 / MOCK-601.2:
// headers reach the journal in wire order with original casing and duplicates,
// captured from the raw request head before net/http canonicalises them.
func TestHeaderFidelityToJournal(t *testing.T) {
	inst := newInstance(1, "inst")
	reg := newRegistryForMethods(echoHandler(`{"ok":true}`))
	mount := &httpx.Mount{Path: httpx.DefaultPath, Instance: inst, Pipeline: newPipelineFor(reg)}
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", Mounts: []*httpx.Mount{mount}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	base := "http://" + srv.Addr().String()

	// Send x-mcp-header twice with differing casing over a raw socket, bypassing
	// http.Header canonicalisation (the same technique MOCK-601.2 requires).
	body := rawRequest("1", wire.MethodToolsCall)
	head := "POST " + httpx.DefaultPath + " HTTP/1.1\r\n" +
		"Host: " + strings.TrimPrefix(base, "http://") + "\r\n" +
		"Content-Type: application/json\r\n" +
		"x-mcp-header: first\r\n" +
		"X-MCP-Header: second\r\n" +
		"Content-Length: " + itoa(len(body)) + "\r\n" +
		"Connection: close\r\n\r\n" + string(body)
	if err := rawExchange(base, head); err != nil {
		t.Fatalf("raw exchange: %v", err)
	}

	// The journal record must carry both occurrences, in wire order, with
	// original casing.
	recs := inst.ring.Query()
	if len(recs) != 1 {
		t.Fatalf("journal has %d records, want 1", len(recs))
	}
	got := recs[0].HTTP
	if got == nil {
		t.Fatal("record has no HTTP part")
	}
	var firsts, seconds []string
	for _, h := range got.Headers {
		if strings.EqualFold(h[0], "x-mcp-header") {
			if h[0] == "x-mcp-header" {
				firsts = append(firsts, h[1])
			}
			if h[0] == "X-MCP-Header" {
				seconds = append(seconds, h[1])
			}
		}
	}
	if len(firsts) != 1 || firsts[0] != "first" {
		t.Fatalf("lowercase x-mcp-header not preserved: %+v", got.Headers)
	}
	if len(seconds) != 1 || seconds[0] != "second" {
		t.Fatalf("uppercase X-MCP-Header not preserved: %+v", got.Headers)
	}
	// Wire order: the lowercase occurrence precedes the uppercase one.
	idxLower, idxUpper := -1, -1
	for i, h := range got.Headers {
		if h[0] == "x-mcp-header" {
			idxLower = i
		}
		if h[0] == "X-MCP-Header" {
			idxUpper = i
		}
	}
	if idxLower < 0 || idxUpper < 0 || idxLower > idxUpper {
		t.Fatalf("wire order not preserved (lower=%d upper=%d): %+v", idxLower, idxUpper, got.Headers)
	}
}
