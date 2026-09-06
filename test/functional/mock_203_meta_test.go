//go:build functional

package functional_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// mock_203_meta_test.go — _meta validation (MOCK-203). Strict rejection in all
// three absence cases with -32602 + HTTP 400 and data.missing; and the
// observable lenient-vs-off distinction in the journal (DEF-005 guard).

// TestMOCK203_Strict_MissingProtocolVersion asserts the strict-default server
// rejects a request whose _meta.protocolVersion is absent with -32602 and,
// crucially, HTTP 400, naming the field in data.missing (203.1, 203.4).
func TestMOCK203_Strict_MissingProtocolVersion(t *testing.T) {
	t.Parallel()
	assertStrictReject(t, func(r *mcpclient.Request) { r.Meta.OmitProtocolVersion = true }, "protocolVersion")
}

// TestMOCK203_Strict_MissingClientCapabilities asserts the same for absent
// _meta.clientCapabilities (203.2, 203.4).
func TestMOCK203_Strict_MissingClientCapabilities(t *testing.T) {
	t.Parallel()
	assertStrictReject(t, func(r *mcpclient.Request) { r.Meta.OmitClientCapabilities = true }, "clientCapabilities")
}

// TestMOCK203_Strict_MetaAbsent asserts the same for _meta absent entirely
// (203.3): both required fields are named as missing.
func TestMOCK203_Strict_MetaAbsent(t *testing.T) {
	t.Parallel()
	assertStrictReject(t, func(r *mcpclient.Request) { r.Meta.Omit = true },
		"protocolVersion", "clientCapabilities")
}

// assertStrictReject drives a discover request mutated by mutate against a
// strict-default server and asserts -32602 + HTTP 400 + data.missing == want.
// The HTTP 400 is checked at the transport layer (a raw net/http round trip),
// because -32602 alone does not prove the status code half of 203.1.
func assertStrictReject(t *testing.T, mutate func(*mcpclient.Request), want ...string) {
	t.Helper()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)

	r := completeCallInfo(mcpclient.DiscoverRequest(mcpclient.IntID(1)))
	mutate(r)
	resp, err := c.Do(ctx(t), r)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	requireRPCError(t, resp, mcpclient.ErrCodeInvalidParams)

	missing := errorDataMissing(t, resp.Body)
	if !sameStringSet(missing, want) {
		t.Fatalf("data.missing = %v, want %v\nbody: %s", missing, want, resp.Body)
	}

	// HTTP 400 half of 203.1: re-send the same bytes over a raw net/http POST
	// so we can read the status code (mcpclient abstracts it away).
	assertHTTPStatus(t, inst.URL(), r, http.StatusBadRequest)
}

// TestMOCK203_LenientVsOff_ObservablyDistinct is the DEF-005 guard: the same
// missing-protocolVersion request is ANSWERED NORMALLY under both lenient and
// off, but the journal metaValidation record differs — lenient records
// outcome="tolerated" with the missing field computed, off records
// outcome="skipped" with missing NOT computed (203.7, 203.8). If these two modes
// ever become indistinguishable, the regression this test exists to catch has
// happened.
func TestMOCK203_LenientVsOff_ObservablyDistinct(t *testing.T) {
	t.Parallel()
	lenient := runMetaMode(t, "../../testdata/meta-lenient.yaml")
	off := runMetaMode(t, "../../testdata/meta-off.yaml")

	if lenient.Mode != "lenient" || off.Mode != "off" {
		t.Fatalf("modes not recorded: lenient=%q off=%q", lenient.Mode, off.Mode)
	}
	if lenient.Outcome != "tolerated" {
		t.Fatalf("lenient outcome = %q, want tolerated", lenient.Outcome)
	}
	if off.Outcome != "skipped" {
		t.Fatalf("off outcome = %q, want skipped", off.Outcome)
	}
	if len(lenient.Missing) == 0 {
		t.Fatalf("lenient must COMPUTE the missing set (203.7); got empty")
	}
	if len(off.Missing) != 0 {
		t.Fatalf("off must NOT compute the missing set (203.8); got %v", off.Missing)
	}
	// The two modes must not collapse into the same observable record.
	if lenient.Outcome == off.Outcome {
		t.Fatalf("lenient and off produced the same outcome %q — not observably distinct", lenient.Outcome)
	}
}

// metaOutcome is the journal metaValidation projection the mode test asserts on.
type metaOutcome struct {
	Mode    string
	Outcome string
	Missing []string
}

// runMetaMode loads a scenario file selecting a validateMeta mode, sends a
// discover request with protocolVersion omitted, and returns the metaValidation
// record captured for it. It asserts the request was ANSWERED (not rejected),
// which is the shared precondition of 203.7/203.8.
func runMetaMode(t *testing.T, file string) metaOutcome {
	t.Helper()
	s, err := mcpmock.NewFromFile(file, mcpmock.WithSeed(fixedSeed), mcpmock.WithAddr("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("load %s: %v", file, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start %s: %v", file, err)
	}
	inst := s.Instances()[0]
	c := newHTTPClient(t, inst)

	r := completeCallInfo(mcpclient.DiscoverRequest(mcpclient.IntID(1)))
	r.Meta.OmitProtocolVersion = true
	resp, err := c.Do(ctx(t), r)
	if err != nil {
		t.Fatalf("%s: do: %v", file, err)
	}
	requireResult(t, resp) // answered normally, not rejected

	var out metaOutcome
	var found bool
	inst.Journal().Iter(func(rec journalapi.Record) bool {
		if rec.MetaValidation == nil {
			return true
		}
		out = metaOutcome{
			Mode:    rec.MetaValidation.Mode,
			Outcome: rec.MetaValidation.Outcome,
			Missing: rec.MetaValidation.Missing,
		}
		found = true
		return false
	})
	if !found {
		t.Fatalf("%s: no metaValidation record in journal", file)
	}
	return out
}

// TestMOCK203_MetaInJournal asserts the decoded _meta appears in the journal with
// all keys preserved, including an unknown one (203.6).
func TestMOCK203_MetaInJournal(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)

	r := completeCallInfo(mcpclient.DiscoverRequest(mcpclient.IntID(1)))
	r.Meta.Extra = map[string]any{"x-unknown-meta-key": "kept"}
	if _, err := c.Do(ctx(t), r); err != nil {
		t.Fatalf("do: %v", err)
	}

	var sawUnknown bool
	inst.Journal().Iter(func(rec journalapi.Record) bool {
		if _, ok := rec.Meta["x-unknown-meta-key"]; ok {
			sawUnknown = true
			return false
		}
		return true
	})
	if !sawUnknown {
		t.Fatalf("unknown _meta key not preserved in journal (203.6)")
	}
}

// --- helpers ---

// errorDataMissing extracts error.data.missing from an error envelope.
func errorDataMissing(t *testing.T, body []byte) []string {
	t.Helper()
	var env struct {
		Error struct {
			Data struct {
				Missing []string `json:"missing"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode error data: %v\nbody: %s", err, body)
	}
	return env.Error.Data.Missing
}

// sameStringSet reports set equality ignoring order.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
