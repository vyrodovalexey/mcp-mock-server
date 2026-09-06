package jsonrpc_test

import (
	"encoding/json"
	"runtime"
	"sync"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
)

// TestCanonicalConcurrentSafe asserts the encoder is safe for concurrent use by
// many goroutines producing identical output — the property ≥200 logical
// instances rely on. Run under -race, a data race or shared-state bug fails
// here.
func TestCanonicalConcurrentSafe(t *testing.T) {
	t.Parallel()
	doc := json.RawMessage(`{"m":"tools/call","p":{"z":1,"a":[1,2,{"k":true,"j":null}]},"id":7,"s":"a<b>&c"}`)
	want, err := jsonrpc.Canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 64
	const iters = 200
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				got, gerr := jsonrpc.Canonical(doc)
				if gerr != nil {
					errs <- gerr.Error()
					return
				}
				if string(got) != string(want) {
					errs <- "canonical output diverged under concurrency"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatal(msg)
	}
}

// TestCanonicalStableAcrossGOMAXPROCS covers acceptance criterion 3's
// cross-GOMAXPROCS clause within a single process: the canonical output does
// not depend on the GOMAXPROCS setting. The committed golden vector covers
// cross-process stability; this covers the in-process axis directly.
func TestCanonicalStableAcrossGOMAXPROCS(t *testing.T) {
	// Not parallel: this test mutates GOMAXPROCS.
	doc := json.RawMessage(`{"b":2,"a":1,"c":{"y":9,"x":8},"arr":[3,1,2]}`)
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)

	at1, err := jsonrpc.Canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GOMAXPROCS(8)
	at8, err := jsonrpc.Canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(at1) != string(at8) {
		t.Errorf("canonical output differs across GOMAXPROCS:\n P1: %s\n P8: %s", at1, at8)
	}
}

// TestResponseEncodeConcurrent asserts response emission is concurrency-safe and
// stable.
func TestResponseEncodeConcurrent(t *testing.T) {
	t.Parallel()
	id := jsonrpc.StringID("req-1")
	result := json.RawMessage(`{"content":[{"type":"text","text":"a<b>"}]}`)
	resp := jsonrpc.NewResultResponse(id, result)
	want, err := resp.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				got, gerr := resp.Encode()
				if gerr != nil || string(got) != string(want) {
					t.Errorf("Encode diverged or errored: %v", gerr)
					return
				}
			}
		}()
	}
	wg.Wait()
}
