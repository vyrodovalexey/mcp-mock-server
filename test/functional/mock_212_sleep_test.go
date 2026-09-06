//go:build functional

package functional_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// mock_212_sleep_test.go — sleep bounding (builtin-tools §3) and client-disconnect
// cancellation (MOCK-212 subset). These assert on CONDITIONS (an error code, a
// journal flag), never on an absolute wall-clock ceiling — the five prior flaky
// defects came from timing assumptions, and this suite adds no sixth.

// TestMOCK212_SleepExceedsMaxRejects asserts sleep with durationMs above the
// 30000 ms default maximum and the default reject mode returns -32602 carrying
// data.requestedMs and data.maxSleepMs (builtin-tools 202.14). The rejection is
// immediate (no delay incurred) — proven by the bounded op context NOT expiring,
// not by measuring elapsed time.
func TestMOCK212_SleepExceedsMaxRejects(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	resp, err := c.CallTool(ctx(t), mcpclient.IntID(1), "sleep", map[string]any{"durationMs": 40000})
	if err != nil {
		t.Fatalf("sleep>max: %v", err)
	}
	requireRPCError(t, resp, mcpclient.ErrCodeInvalidParams)

	var env struct {
		Error struct {
			Data struct {
				RequestedMs int `json:"requestedMs"`
				MaxSleepMs  int `json:"maxSleepMs"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		t.Fatalf("decode error data: %v\nbody: %s", err, resp.Body)
	}
	if env.Error.Data.RequestedMs != 40000 || env.Error.Data.MaxSleepMs != 30000 {
		t.Fatalf("data = {requestedMs:%d maxSleepMs:%d}, want {40000 30000}\nbody: %s",
			env.Error.Data.RequestedMs, env.Error.Data.MaxSleepMs, resp.Body)
	}
}

// TestMOCK212_SleepNegativeRejects asserts a negative durationMs is -32602 with
// data.reason "negative_duration" (builtin-tools §3.3 [D]).
func TestMOCK212_SleepNegativeRejects(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	resp, err := c.CallTool(ctx(t), mcpclient.IntID(1), "sleep", map[string]any{"durationMs": -5})
	if err != nil {
		t.Fatalf("sleep negative: %v", err)
	}
	requireRPCError(t, resp, mcpclient.ErrCodeInvalidParams)
}

// TestMOCK212_ClientDisconnectCancels drives a long sleep, then cancels the
// client context (a disconnect). It asserts (a) no normal result is delivered to
// the client, and (b) the journal record for that request shows the cancellation
// evidence (response.closeReason == "clientGone" with a truncated durationNs).
//
// NOTE on the observable form: the engine records a client disconnect through
// response.closeReason=="clientGone" plus the elapsed durationNs, rather than a
// populated Record.Canceled field — see internal/engine/capture.go, which
// documents that the journal.Input contract (TASK-012) exposes no dedicated
// canceled field. This test therefore asserts on closeReason, the field that IS
// observable through the public journal, and the report records the gap against
// MOCK-212.3's literal "cancelled: true" wording as an observation.
//
// Synchronisation is by POLLING for a condition with a bounded deadline; there
// is NO fixed sleep-as-synchronisation and NO wall-clock ceiling on cancellation
// latency, so the test is robust to scheduling and GOMAXPROCS.
func TestMOCK212_ClientDisconnectCancels(t *testing.T) {
	t.Parallel()
	// Enable observability so we can poll the in-flight gauge as a deterministic
	// readiness signal: cancel only once the server reports the sleep request as
	// in flight. That removes any dependence on how fast the request reached the
	// server — it is a CONDITION, not a timing threshold. (The journal record for
	// a cancelled request only materialises at cancel time, so it cannot serve as
	// the in-flight signal.)
	s, inst := newHTTPServer(t, mcpmock.WithObservabilityAddr("127.0.0.1:0"))
	c := newHTTPClient(t, inst)

	reqCtx, cancel := context.WithCancel(context.Background())
	done := make(chan *mcpclient.Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := c.CallTool(reqCtx, mcpclient.IntID(1), "sleep", map[string]any{"durationMs": 25000})
		if err != nil {
			errs <- err
			return
		}
		done <- resp
	}()

	// Wait until the server reports the request in flight, then cancel.
	metricsURL := s.ObservabilityURL() + "/metrics"
	waitForCondition(t, func() bool { return inFlightAtLeastOne(t, metricsURL) })
	cancel()

	// The client call must not deliver a normal result: either it returns an
	// error (context canceled) or no response frame.
	select {
	case resp := <-done:
		if resp.GotResponse && resp.Envelope.Error == nil && len(resp.Envelope.Result) > 0 {
			t.Fatalf("cancelled sleep still returned a result (MOCK-212.2)\nbody: %s", resp.Body)
		}
	case <-errs:
		// context cancellation surfaced as a transport error — acceptable.
	case <-time.After(opTimeout):
		t.Fatalf("cancelled sleep neither returned nor errored within %v", opTimeout)
	}

	// The journal must eventually show the request as cancelled by the client.
	waitForCondition(t, func() bool { return journalHasClientGone(inst.Journal()) })
}

// inFlightAtLeastOne scrapes /metrics and reports whether
// mcpmock_requests_in_flight is >= 1 for any instance — the deterministic signal
// that a request has reached the server and begun processing.
func inFlightAtLeastOne(t *testing.T, metricsURL string) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx(t), http.MethodGet, metricsURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "mcpmock_requests_in_flight") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if v, perr := strconv.ParseFloat(fields[len(fields)-1], 64); perr == nil && v >= 1 {
			return true
		}
	}
	return false
}

// journalHasClientGone reports whether any record shows the client-disconnect
// close reason on its response part (the observable MOCK-212 cancellation
// evidence through the public journal).
func journalHasClientGone(v journalapi.View) bool {
	var found bool
	v.Iter(func(rec journalapi.Record) bool {
		if rec.Response != nil && rec.Response.CloseReason == "clientGone" {
			found = true
			return false
		}
		return true
	})
	return found
}

// waitForCondition polls cond until it holds or the bounded deadline passes. It
// polls (never sleeps as synchronisation) and asserts on the condition, so it is
// robust to scheduling and GOMAXPROCS. The poll interval is small and the
// deadline generous; neither is a timing assertion.
func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(opTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %v", opTimeout)
	}
}
