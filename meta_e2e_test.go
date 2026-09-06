package mcpmock_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// meta_e2e_test.go is the DEF-005 end-to-end proof: it drives requests THROUGH
// THE FACADE (not a unit-level validator call) and asserts that MOCK-203 strict
// _meta validation actually holds now that the injection seam is wired. Unit
// coverage of the validator lives in internal/modern; unit coverage of the
// injection path lives in internal/instance. What these tests add — and what
// was missing before the fix — is that an assembled mcpmock server rejects a
// request whose params._meta omits a required field, with the JSON-RPC and HTTP
// codes the requirement names.

// codeInvalidParams is the JSON-RPC -32602 code MOCK-203 requires for a missing
// required _meta field. It is transcribed here (this is a black-box facade test)
// rather than imported from internal/wire.
const codeInvalidParams = -32602

// postThroughFacade sends the marshalled bytes of req to url over real HTTP and
// returns the HTTP status and the decoded JSON-RPC envelope. It POSTs directly
// with net/http (rather than through mcpclient.Do) because the end-to-end
// assertion needs BOTH the HTTP status code (MOCK-203's "/ 400" half) and the
// JSON-RPC error code (the "-32602" half), and mcpclient.Response intentionally
// surfaces only the body.
func postThroughFacade(t *testing.T, url string, req *mcpclient.Request) (int, mcpclient.Envelope) {
	t.Helper()
	payload, err := req.Marshal()
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build http request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var env mcpclient.Envelope
	if len(body) > 0 {
		if uErr := json.Unmarshal(body, &env); uErr != nil {
			t.Fatalf("decode envelope from %q: %v", body, uErr)
		}
	}
	return resp.StatusCode, env
}

// startStrict starts a strict-mode server from the strict scenario file and
// returns its instance URL. It loads a real scenario FILE (NewFromFile) so the
// end-to-end path exercises scenario→switches.validateMeta→validator wiring
// exactly as a hub operator's file would.
func startStrict(t *testing.T) string {
	t.Helper()
	s, err := mcpmock.NewFromFile("testdata/meta-strict.yaml",
		mcpmock.WithSeed(1), mcpmock.WithAddr("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("NewFromFile(strict): %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	inst, ok := s.Instance("meta-strict")
	if !ok {
		t.Fatal("strict instance not found")
	}
	return inst.URL()
}

// TestMetaStrictRejectsMissingProtocolVersionThroughFacade is the headline
// DEF-005 assertion: a request whose _meta omits protocolVersion, sent through
// the assembled facade under strict mode, is rejected with -32602 AND HTTP 400,
// and error.data.missing names protocolVersion. Before the seam was wired the
// facade hardcoded AcceptAllMeta and this request would have returned 200.
func TestMetaStrictRejectsMissingProtocolVersionThroughFacade(t *testing.T) {
	url := startStrict(t)

	req := mcpclient.ToolsListRequest(mcpclient.IntID(1))
	req.Meta.OmitProtocolVersion = true

	status, env := postThroughFacade(t, url, req)

	if status != http.StatusBadRequest {
		t.Fatalf("HTTP status = %d, want 400 (MOCK-203)", status)
	}
	if env.Error == nil {
		t.Fatalf("expected a JSON-RPC error, got result: %+v", env)
	}
	if env.Error.Code != codeInvalidParams {
		t.Fatalf("error code = %d, want %d (-32602)", env.Error.Code, codeInvalidParams)
	}
	assertMissingContains(t, env.Error.Data, "protocolVersion")
}

// TestMetaStrictRejectsMissingClientCapabilitiesThroughFacade is the sibling
// case for the second required field (MOCK-203.2).
func TestMetaStrictRejectsMissingClientCapabilitiesThroughFacade(t *testing.T) {
	url := startStrict(t)

	req := mcpclient.ToolsListRequest(mcpclient.IntID(2))
	req.Meta.OmitClientCapabilities = true

	status, env := postThroughFacade(t, url, req)

	if status != http.StatusBadRequest {
		t.Fatalf("HTTP status = %d, want 400", status)
	}
	if env.Error == nil || env.Error.Code != codeInvalidParams {
		t.Fatalf("want -32602 error, got %+v", env)
	}
	assertMissingContains(t, env.Error.Data, "clientCapabilities")
}

// TestMetaStrictRejectsMissingMetaEntirelyThroughFacade covers _meta absent
// entirely (MOCK-203.3): both required fields are named as missing.
func TestMetaStrictRejectsMissingMetaEntirelyThroughFacade(t *testing.T) {
	url := startStrict(t)

	req := mcpclient.ToolsListRequest(mcpclient.IntID(3))
	req.Meta.Omit = true

	status, env := postThroughFacade(t, url, req)

	if status != http.StatusBadRequest {
		t.Fatalf("HTTP status = %d, want 400", status)
	}
	if env.Error == nil || env.Error.Code != codeInvalidParams {
		t.Fatalf("want -32602 error, got %+v", env)
	}
	assertMissingContains(t, env.Error.Data, "protocolVersion")
	assertMissingContains(t, env.Error.Data, "clientCapabilities")
}

// TestMetaStrictAcceptsCompleteMetaThroughFacade is the negative control: a
// complete _meta envelope passes stage 4 and yields a normal 200 result. Without
// it, a validator that rejected everything would also pass the rejection tests.
func TestMetaStrictAcceptsCompleteMetaThroughFacade(t *testing.T) {
	url := startStrict(t)

	// A fully-populated _meta (the mcpclient default for ToolsListRequest builds
	// protocolVersion + clientCapabilities + clientInfo).
	req := mcpclient.ToolsListRequest(mcpclient.IntID(4))
	req.Meta.ClientInfo = &mcpclient.ClientInfo{Name: "e2e", Version: "1"}

	status, env := postThroughFacade(t, url, req)

	if status != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200 for a complete _meta; env=%+v", status, env)
	}
	if env.Error != nil {
		t.Fatalf("complete _meta must be accepted, got error %+v", env.Error)
	}
}

// TestMetaModesSelectableFromScenarioFile proves all three modes are selectable
// from a scenario FILE and that lenient and off — which both ACCEPT a request
// missing protocolVersion — remain OBSERVABLY DISTINCT in the journal
// (AMEND-6): lenient records outcome="tolerated" with the missing field
// computed, off records outcome="skipped" with an empty missing set.
func TestMetaModesSelectableFromScenarioFile(t *testing.T) {
	cases := []struct {
		mode        string
		file        string
		instance    string
		wantStatus  int
		wantErrCode int // 0 means "no JSON-RPC error"
		wantOutcome string
		wantMissing []string
	}{
		{
			mode:        "strict",
			file:        "testdata/meta-strict.yaml",
			instance:    "meta-strict",
			wantStatus:  http.StatusBadRequest,
			wantErrCode: codeInvalidParams,
			wantOutcome: "rejected",
			wantMissing: []string{"protocolVersion"},
		},
		{
			mode:        "lenient",
			file:        "testdata/meta-lenient.yaml",
			instance:    "meta-lenient",
			wantStatus:  http.StatusOK,
			wantErrCode: 0,
			wantOutcome: "tolerated",
			wantMissing: []string{"protocolVersion"},
		},
		{
			mode:        "off",
			file:        "testdata/meta-off.yaml",
			instance:    "meta-off",
			wantStatus:  http.StatusOK,
			wantErrCode: 0,
			wantOutcome: "skipped",
			wantMissing: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			s, err := mcpmock.NewFromFile(tc.file,
				mcpmock.WithSeed(2), mcpmock.WithAddr("127.0.0.1:0"))
			if err != nil {
				t.Fatalf("NewFromFile(%s): %v", tc.file, err)
			}
			if err := s.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			inst, ok := s.Instance(tc.instance)
			if !ok {
				t.Fatalf("instance %q not found", tc.instance)
			}

			req := mcpclient.ToolsListRequest(mcpclient.IntID(1))
			req.Meta.OmitProtocolVersion = true

			status, env := postThroughFacade(t, inst.URL(), req)

			if status != tc.wantStatus {
				t.Fatalf("%s: HTTP status = %d, want %d", tc.mode, status, tc.wantStatus)
			}
			switch tc.wantErrCode {
			case 0:
				if env.Error != nil {
					t.Fatalf("%s: expected acceptance, got error %+v", tc.mode, env.Error)
				}
			default:
				if env.Error == nil || env.Error.Code != tc.wantErrCode {
					t.Fatalf("%s: want error code %d, got %+v", tc.mode, tc.wantErrCode, env)
				}
			}

			// The journal part is the observable distinction between lenient and
			// off, recorded through the wired validator on the assembled server.
			part := lastMetaValidation(t, inst.Journal())
			if part == nil {
				t.Fatalf("%s: request produced no metaValidation journal part", tc.mode)
			}
			if part.Mode != tc.mode {
				t.Errorf("%s: journal mode = %q, want %q", tc.mode, part.Mode, tc.mode)
			}
			if part.Outcome != tc.wantOutcome {
				t.Errorf("%s: journal outcome = %q, want %q", tc.mode, part.Outcome, tc.wantOutcome)
			}
			if !equalStrings(part.Missing, tc.wantMissing) {
				t.Errorf("%s: journal missing = %v, want %v", tc.mode, part.Missing, tc.wantMissing)
			}
		})
	}
}

// TestLenientAndOffAreObservablyDistinctAfterWiring pins the AMEND-6 requirement
// directly: the SAME missing-protocolVersion request, sent to a lenient server
// and to an off server through the facade, must produce journal parts that
// differ in both outcome and computed missing set — proving the wiring did not
// collapse the two modes into one.
func TestLenientAndOffAreObservablyDistinctAfterWiring(t *testing.T) {
	lenient := lastPartFor(t, "testdata/meta-lenient.yaml", "meta-lenient")
	off := lastPartFor(t, "testdata/meta-off.yaml", "meta-off")

	if lenient.Outcome == off.Outcome {
		t.Fatalf("lenient and off share outcome %q — modes not distinct", lenient.Outcome)
	}
	if lenient.Outcome != "tolerated" {
		t.Errorf("lenient outcome = %q, want tolerated", lenient.Outcome)
	}
	if off.Outcome != "skipped" {
		t.Errorf("off outcome = %q, want skipped", off.Outcome)
	}
	if len(lenient.Missing) == 0 {
		t.Error("lenient must compute the missing set (tolerate-and-record)")
	}
	if len(off.Missing) != 0 {
		t.Errorf("off must not compute a missing set, got %v", off.Missing)
	}
}

// lastPartFor starts a server from file, sends one missing-protocolVersion
// request, and returns the resulting metaValidation journal part.
func lastPartFor(t *testing.T, file, instance string) *journalapi.MetaValidationPart {
	t.Helper()
	s, err := mcpmock.NewFromFile(file, mcpmock.WithSeed(3), mcpmock.WithAddr("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("NewFromFile(%s): %v", file, err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	inst, ok := s.Instance(instance)
	if !ok {
		t.Fatalf("instance %q not found", instance)
	}
	req := mcpclient.ToolsListRequest(mcpclient.IntID(1))
	req.Meta.OmitProtocolVersion = true
	if _, _ = postThroughFacade(t, inst.URL(), req); true {
		// status/env asserted elsewhere; here we only want the journal side effect.
	}
	part := lastMetaValidation(t, inst.Journal())
	if part == nil {
		t.Fatalf("%s: no metaValidation part recorded", instance)
	}
	return part
}

// lastMetaValidation returns the metaValidation part of the most recent journal
// record that carries one, or nil if none does.
func lastMetaValidation(t *testing.T, v journalapi.View) *journalapi.MetaValidationPart {
	t.Helper()
	recs := v.Snapshot()
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].MetaValidation != nil {
			return recs[i].MetaValidation
		}
	}
	return nil
}

// assertMissingContains decodes error.data as {missing:[…]} and asserts field is
// present, proving the -32602 payload names the absent _meta field (MOCK-203.4).
func assertMissingContains(t *testing.T, data json.RawMessage, field string) {
	t.Helper()
	if len(data) == 0 {
		t.Fatalf("error.data absent; expected it to name missing field %q", field)
	}
	var payload struct {
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode error.data %q: %v", data, err)
	}
	for _, m := range payload.Missing {
		if m == field {
			return
		}
	}
	t.Fatalf("error.data.missing %v does not name %q", payload.Missing, field)
}

// equalStrings reports whether two string slices are element-wise equal, treating
// nil and empty as equal (an absent missing set and an empty one are the same
// observable "nothing missing").
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
