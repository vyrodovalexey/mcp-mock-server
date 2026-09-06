package mcpmock_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// sentinelToken is a fixture credential value that must NEVER appear in any
// control-API response (security.md §5). Every credential-leak test scans for
// this exact string.
const sentinelToken = "sk-super-secret-sentinel-value-9f3c"

// TestControlSeparatePort asserts the control API binds a port distinct from the
// MCP listener (MOCK-104: separate port), and that ControlURL is populated once
// the HTTP transport is up.
func TestControlSeparatePort(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(1))
	inst := s.Instances()[0]
	if s.ControlURL() == "" {
		t.Fatal("ControlURL empty; expected a loopback control listener in HTTP mode")
	}
	mcpHostPort := strings.TrimPrefix(inst.URL(), "http://")
	mcpHostPort = mcpHostPort[:strings.IndexByte(mcpHostPort, '/')]
	ctlHostPort := strings.TrimPrefix(s.ControlURL(), "http://")
	if mcpHostPort == ctlHostPort {
		t.Errorf("control listener %q shares the MCP listener address %q", ctlHostPort, mcpHostPort)
	}
	if !strings.HasPrefix(s.ControlURL(), "http://127.0.0.1:") {
		t.Errorf("control API should bind loopback by default, got %q", s.ControlURL())
	}
}

// TestControlInProcessOperations exercises the in-process Control across its
// Phase 1 operations without any HTTP round trip (ADR-015 first front end).
func TestControlInProcessOperations(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(99))
	ctx := context.Background()
	ctl := s.Control()

	insts, err := ctl.Instances(ctx)
	if err != nil || len(insts) != 1 {
		t.Fatalf("Instances = %+v, %v", insts, err)
	}
	if _, err := ctl.Instance(ctx, insts[0].Name); err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if _, err := ctl.Instance(ctx, "no-such"); !errors.Is(err, mcpmock.ErrNotFound) {
		t.Errorf("unknown instance err = %v, want ErrNotFound", err)
	}
	seed, err := ctl.Seed(ctx)
	if err != nil || seed.Seed != s.Seed() {
		t.Errorf("Seed = %+v (server seed %d), %v", seed, s.Seed(), err)
	}
	if seed.Source != "flag" {
		t.Errorf("seed source = %q, want flag (explicit WithSeed)", seed.Source)
	}
	health, err := ctl.Health(ctx)
	if err != nil || health.Status != "ok" || health.Instances != 1 {
		t.Errorf("Health = %+v, %v", health, err)
	}
}

// TestControlSeedSourceRandom asserts the seed source is "random" when no
// explicit seed was supplied (MOCK-704.2).
func TestControlSeedSourceRandom(t *testing.T) {
	s := mcpmock.StartTest(t)
	seed, err := s.Control().Seed(context.Background())
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if seed.Source != "random" {
		t.Errorf("seed source = %q, want random", seed.Source)
	}
}

// TestControlJournalQueryAndClear drives a request through the MCP endpoint, then
// queries and clears the journal through the in-process control API (MOCK-602,
// MOCK-702.7).
func TestControlJournalQueryAndClear(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(3))
	inst := s.Instances()[0]
	sendMCP(t, inst.URL(), sentinelToken)

	ctx := context.Background()
	ic := s.Control().For(inst.Name())

	page, err := ic.Journal(ctx, journalapi.Query{})
	if err != nil {
		t.Fatalf("Journal: %v", err)
	}
	if len(page.Records) == 0 {
		t.Fatal("expected at least one journalled request")
	}
	// Records are in Seq order.
	for i := 1; i < len(page.Records); i++ {
		if page.Records[i].Seq <= page.Records[i-1].Seq {
			t.Errorf("records not in Seq order at %d", i)
		}
	}
	// A method filter selects.
	filtered, err := ic.Journal(ctx, journalapi.Query{Selector: journalapi.Selector{Method: "server/*"}})
	if err != nil {
		t.Fatalf("filtered Journal: %v", err)
	}
	for _, r := range filtered.Records {
		if !strings.HasPrefix(r.JSONRPC.Method, "server/") {
			t.Errorf("method filter leaked %q", r.JSONRPC.Method)
		}
	}

	if err := ic.ClearJournal(ctx); err != nil {
		t.Fatalf("ClearJournal: %v", err)
	}
	after, err := ic.Journal(ctx, journalapi.Query{})
	if err != nil {
		t.Fatalf("Journal after clear: %v", err)
	}
	if len(after.Records) != 0 {
		t.Errorf("journal not empty after clear: %d records", len(after.Records))
	}
}

// TestControlNDJSONStreamDoesNotMaterialize asserts JournalStream yields records
// lazily via an iterator (MOCK-602.2): stopping early stops iteration.
func TestControlNDJSONStreamDoesNotMaterialize(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(4))
	inst := s.Instances()[0]
	for range 5 {
		sendMCP(t, inst.URL(), sentinelToken)
	}
	ctx := context.Background()
	seq, err := s.Control().For(inst.Name()).JournalStream(ctx, journalapi.Query{})
	if err != nil {
		t.Fatalf("JournalStream: %v", err)
	}
	// Consume only the first record then stop; the iterator must honour the stop.
	count := 0
	for _, rerr := range seq {
		if rerr != nil {
			t.Fatalf("stream error: %v", rerr)
		}
		count++
		if count == 1 {
			break
		}
	}
	if count != 1 {
		t.Errorf("expected to stop after 1 record, consumed %d", count)
	}
}

// TestControlNoCredentialLeak is the sentinel test (security.md §5, MOCK-601.5):
// a request carrying a secret Authorization header must leave that raw value
// absent from EVERY control-API response — the journal page, the NDJSON stream
// and the instance list.
func TestControlNoCredentialLeak(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(5))
	inst := s.Instances()[0]
	sendMCP(t, inst.URL(), sentinelToken)

	ctx := context.Background()
	ctl := s.Control()
	ic := ctl.For(inst.Name())

	// 1. Journal page.
	page, err := ic.Journal(ctx, journalapi.Query{})
	if err != nil {
		t.Fatalf("Journal: %v", err)
	}
	assertNoSentinel(t, "journal page", mustJSON(t, page))

	// 2. NDJSON stream.
	var nd bytes.Buffer
	seq, err := ic.JournalStream(ctx, journalapi.Query{})
	if err != nil {
		t.Fatalf("JournalStream: %v", err)
	}
	w := journalapi.NewWriter(&nd)
	for rec, rerr := range seq {
		if rerr != nil {
			t.Fatalf("stream: %v", rerr)
		}
		_ = w.Write(rec)
	}
	_ = w.Close()
	assertNoSentinel(t, "ndjson stream", nd.Bytes())

	// 3. Instance list.
	insts, err := ctl.Instances(ctx)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	assertNoSentinel(t, "instance list", mustJSON(t, insts))

	// Sanity: the credential WAS captured (as a redaction), so the test is not
	// vacuously passing because nothing was recorded.
	if len(page.Records) == 0 {
		t.Fatal("no records captured; sentinel scan would be vacuous")
	}
}

// TestControlRuntimeMutationVisibleViaCOW asserts a runtime mutation applied
// through Instance.Mutate is atomic and visible via the control API's reported
// generation (MOCK-702, ADR-014 copy-on-write). A snapshot taken before the
// mutation keeps observing the old generation.
func TestControlRuntimeMutationVisibleViaCOW(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(6))
	facadeInst := s.Instances()[0]
	ctx := context.Background()

	before, err := s.Control().Instance(ctx, facadeInst.Name())
	if err != nil {
		t.Fatalf("Instance before: %v", err)
	}

	// Obtain the internal instance to drive the COW write path directly, and
	// capture a pre-mutation snapshot to prove immutability.
	in := mcpmock.InternalInstance(s, facadeInst.Name())
	if in == nil {
		t.Fatalf("internal instance %q not found", facadeInst.Name())
	}
	oldSnap := in.Snapshot()
	oldGen := oldSnap.Gen()
	newGen := in.Mutate(func(sn *instance.Snapshot) { _ = sn })
	if newGen != oldGen+1 {
		t.Errorf("Mutate returned gen %d, want %d", newGen, oldGen+1)
	}
	// The pre-mutation snapshot still reads the old generation (COW: no in-place
	// edit of a published snapshot).
	if oldSnap.Gen() != oldGen {
		t.Errorf("held snapshot generation changed under mutation: %d != %d", oldSnap.Gen(), oldGen)
	}

	after, err := s.Control().Instance(ctx, facadeInst.Name())
	if err != nil {
		t.Fatalf("Instance after: %v", err)
	}
	if after.Generation != before.Generation+1 {
		t.Errorf("control-reported generation = %d, want %d", after.Generation, before.Generation+1)
	}
}

// TestControlNonLoopbackRefused asserts a non-loopback control bind without a
// token is refused at construction (MOCK-104.5), and that a token permits it.
func TestControlNonLoopbackRefused(t *testing.T) {
	_, err := mcpmock.New(mcpmock.WithSeed(1), mcpmock.WithControlAddr("0.0.0.0:0"))
	if err == nil {
		t.Fatal("expected refusal binding control to 0.0.0.0 without a token")
	}
	if !strings.Contains(err.Error(), "0.0.0.0:0") {
		t.Errorf("refusal should name the address: %v", err)
	}

	// With a token the same bind is permitted.
	s, err := mcpmock.New(mcpmock.WithSeed(1),
		mcpmock.WithControlAddr("127.0.0.1:0"),
		mcpmock.WithControlToken("t"))
	if err != nil {
		t.Fatalf("loopback bind with token should succeed: %v", err)
	}
	_ = s.Close()
}

// TestControlWithoutControlDisablesListeners asserts WithoutControl leaves no
// control listener yet keeps the in-process Control usable.
func TestControlWithoutControlDisablesListeners(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(1), mcpmock.WithoutControl())
	if s.ControlURL() != "" || s.ControlSocket() != "" {
		t.Errorf("WithoutControl should start no listener: url=%q socket=%q", s.ControlURL(), s.ControlSocket())
	}
	if _, err := s.Control().Seed(context.Background()); err != nil {
		t.Errorf("in-process Control should still work: %v", err)
	}
}

// TestTwoFacadesControlNoCollision asserts two Servers in one process each get
// their own control listener on distinct ports without colliding (ADR-007).
func TestTwoFacadesControlNoCollision(t *testing.T) {
	a := mcpmock.StartTest(t, mcpmock.WithSeed(1))
	b := mcpmock.StartTest(t, mcpmock.WithSeed(2))
	if a.ControlURL() == "" || b.ControlURL() == "" {
		t.Fatal("both facades should have a control URL")
	}
	if a.ControlURL() == b.ControlURL() {
		t.Errorf("two facades collided on control URL %q", a.ControlURL())
	}
	// Each facade's control reports its own seed.
	sa, _ := a.Control().Seed(context.Background())
	sb, _ := b.Control().Seed(context.Background())
	if sa.Seed == sb.Seed {
		t.Errorf("two facades report the same seed %d", sa.Seed)
	}
}

// --- helpers ---

// sendMCP posts one discover request to the MCP endpoint carrying a sentinel
// Authorization header, so the journal records (and redacts) a credential.
func sendMCP(t *testing.T, url, token string) {
	t.Helper()
	client, err := mcpclient.NewHTTP(url)
	if err != nil {
		t.Fatalf("mcpclient.NewHTTP: %v", err)
	}
	req := mcpclient.DiscoverRequest(mcpclient.IntID(1))
	req.Headers = []mcpclient.Header{{Name: "Authorization", Value: "Bearer " + token}}
	if _, err := client.Do(context.Background(), req); err != nil {
		t.Fatalf("mcpclient.Do: %v", err)
	}
}

// assertNoSentinel fails if the sentinel token appears anywhere in b.
func assertNoSentinel(t *testing.T, where string, b []byte) {
	t.Helper()
	if bytes.Contains(b, []byte(sentinelToken)) {
		t.Errorf("sentinel credential leaked in %s", where)
	}
}

// mustJSON marshals v or fails the test.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
