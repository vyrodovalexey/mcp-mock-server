package control

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// fakeBackend is an in-memory [Backend] for handler tests. It records the last
// query it received so a test can assert the handler parsed and forwarded every
// filter dimension, and it holds a fixed record set the journal ops read from.
type fakeBackend struct {
	instances []InstanceInfo
	records   []journalapi.Record
	seed      SeedInfo
	health    Health
	lastQuery journalapi.Query
	notFound  bool
	cleared   bool
}

func (b *fakeBackend) Instances(context.Context) ([]InstanceInfo, error) { return b.instances, nil }

func (b *fakeBackend) Instance(_ context.Context, name string) (InstanceInfo, error) {
	for _, in := range b.instances {
		if in.Name == name {
			return in, nil
		}
	}
	return InstanceInfo{}, ErrNotFound
}

func (b *fakeBackend) Seed(context.Context) (SeedInfo, error) { return b.seed, nil }
func (b *fakeBackend) Health(context.Context) (Health, error) { return b.health, nil }
func (b *fakeBackend) For(name string) InstanceBackend        { return &fakeInstance{b: b, name: name} }

type fakeInstance struct {
	b    *fakeBackend
	name string
}

func (fi *fakeInstance) exists() bool {
	for _, in := range fi.b.instances {
		if in.Name == fi.name {
			return true
		}
	}
	return false
}

func (fi *fakeInstance) Journal(_ context.Context, q journalapi.Query) (journalapi.Page, error) {
	if !fi.exists() {
		return journalapi.Page{}, ErrNotFound
	}
	fi.b.lastQuery = q
	var recs []journalapi.Record
	for _, r := range fi.b.records {
		if q.Selector.Matches(r) && r.Seq > q.After {
			recs = append(recs, r)
		}
	}
	return journalapi.Page{Records: recs, Total: len(recs)}, nil
}

func (fi *fakeInstance) JournalStream(
	_ context.Context, q journalapi.Query,
) (iter.Seq2[journalapi.Record, error], error) {
	if !fi.exists() {
		return nil, ErrNotFound
	}
	fi.b.lastQuery = q
	return func(yield func(journalapi.Record, error) bool) {
		for _, r := range fi.b.records {
			if !q.Selector.Matches(r) {
				continue
			}
			if !yield(r, nil) {
				return
			}
		}
	}, nil
}

func (fi *fakeInstance) Correlations(context.Context, journalapi.Query) ([]journalapi.Correlation, error) {
	if !fi.exists() {
		return nil, ErrNotFound
	}
	return nil, nil
}

func (fi *fakeInstance) ClearJournal(context.Context) error {
	if !fi.exists() {
		return ErrNotFound
	}
	fi.b.cleared = true
	return nil
}

// compile-time assertion that the fake satisfies the port.
var _ Backend = (*fakeBackend)(nil)

func newTestHandler() (*Handler, *fakeBackend) {
	b := &fakeBackend{
		instances: []InstanceInfo{{Name: "a", MountPath: "/mcp", Era: "modern", Generation: 3}},
		seed:      SeedInfo{Seed: 42, Source: "flag"},
		health:    Health{Status: "ok", Instances: 1, Seed: 42},
		records: []journalapi.Record{
			{Seq: 1, Instance: "a", Transport: journalapi.TransportHTTP,
				JSONRPC:     journalapi.JSONRPCPart{Method: "tools/list", Name: "x"},
				WallTime:    time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
				Correlation: journalapi.CorrelationPart{TraceID: "t1"}},
			{Seq: 2, Instance: "a", Transport: journalapi.TransportStdio,
				JSONRPC:     journalapi.JSONRPCPart{Method: "tools/call", Name: "echo"},
				WallTime:    time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
				Correlation: journalapi.CorrelationPart{TraceID: "t2"}},
		},
	}
	return NewHandler(b, OpenAPISpec), b
}

func doReq(t *testing.T, h *Handler, method, target, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, http.NoBody)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestHandlerRoutesAndStatuses covers the Phase 1 routes and their status codes.
func TestHandlerRoutesAndStatuses(t *testing.T) {
	h, _ := newTestHandler()
	tests := []struct {
		name   string
		method string
		target string
		want   int
	}{
		{"list instances", http.MethodGet, "/v1/instances", http.StatusOK},
		{"get instance", http.MethodGet, "/v1/instances/a", http.StatusOK},
		{"unknown instance 404", http.MethodGet, "/v1/instances/zzz", http.StatusNotFound},
		{"seed", http.MethodGet, "/v1/seed", http.StatusOK},
		{"health", http.MethodGet, "/v1/health", http.StatusOK},
		{"journal", http.MethodGet, "/v1/instances/a/journal", http.StatusOK},
		{"correlations", http.MethodGet, "/v1/instances/a/journal/correlations", http.StatusOK},
		{"clear journal 204", http.MethodDelete, "/v1/instances/a/journal", http.StatusNoContent},
		{"openapi", http.MethodGet, "/v1/openapi.yaml", http.StatusOK},
		{"unknown path 404", http.MethodGet, "/v1/nope", http.StatusNotFound},
		{"wrong method 405", http.MethodPost, "/v1/instances", http.StatusMethodNotAllowed},
		{"missing prefix 404", http.MethodGet, "/instances", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := doReq(t, h, tt.method, tt.target, "")
			if rr.Code != tt.want {
				t.Errorf("%s %s: status = %d, want %d (body %s)", tt.method, tt.target, rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}

// TestJournalFiltersOverHTTP asserts every MOCK-602.4 filter dimension parses
// and reaches the backend as a Selector, and that they AND-combine.
func TestJournalFiltersOverHTTP(t *testing.T) {
	tests := []struct {
		name  string
		query string
		check func(t *testing.T, q journalapi.Query)
	}{
		{"method glob", "method=tools/*", func(t *testing.T, q journalapi.Query) {
			if q.Method != "tools/*" {
				t.Errorf("method = %q", q.Method)
			}
		}},
		{"name", "name=echo", func(t *testing.T, q journalapi.Query) {
			if q.Name != "echo" {
				t.Errorf("name = %q", q.Name)
			}
		}},
		{"transport", "transport=stdio", func(t *testing.T, q journalapi.Query) {
			if q.Transport != "stdio" {
				t.Errorf("transport = %q", q.Transport)
			}
		}},
		{"since/until time range", "since=2026-01-01T12:30:00Z&until=2026-01-01T14:00:00Z", func(t *testing.T, q journalapi.Query) {
			if q.Since.IsZero() || q.Until.IsZero() {
				t.Errorf("since/until not parsed: %v %v", q.Since, q.Until)
			}
		}},
		{"correlationId maps to chain/trace", "correlationId=c1", func(t *testing.T, q journalapi.Query) {
			if q.ChainID != "c1" {
				t.Errorf("correlationId did not map: %q", q.ChainID)
			}
		}},
		{"traceId", "traceId=t2", func(t *testing.T, q journalapi.Query) {
			if q.TraceID != "t2" {
				t.Errorf("traceId = %q", q.TraceID)
			}
		}},
		{"status", "status=200", func(t *testing.T, q journalapi.Query) {
			if q.StatusCode == nil || *q.StatusCode != 200 {
				t.Errorf("status not parsed: %v", q.StatusCode)
			}
		}},
		{"faultRule", "faultRule=r1", func(t *testing.T, q journalapi.Query) {
			if q.FaultRule != "r1" {
				t.Errorf("faultRule = %q", q.FaultRule)
			}
		}},
		{"AND combine method+transport", "method=tools/call&transport=stdio", func(t *testing.T, q journalapi.Query) {
			if q.Method != "tools/call" || q.Transport != "stdio" {
				t.Errorf("AND filters not both set: %+v", q.Selector)
			}
		}},
		{"consistent flag", "consistent=true", func(t *testing.T, q journalapi.Query) {
			if !q.Consistent {
				t.Error("consistent not set")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, b := newTestHandler()
			rr := doReq(t, h, http.MethodGet, "/v1/instances/a/journal?"+tt.query, "")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
			}
			tt.check(t, b.lastQuery)
		})
	}
}

// TestJournalInvalidQueryIsValidationError asserts an unparsable numeric/time
// parameter yields a 422 validation_failed error object.
func TestJournalInvalidQueryIsValidationError(t *testing.T) {
	h, _ := newTestHandler()
	for _, q := range []string{"status=notanint", "since=notatime", "after=-1", "limit=-5"} {
		rr := doReq(t, h, http.MethodGet, "/v1/instances/a/journal?"+q, "")
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("query %q: status = %d, want 422", q, rr.Code)
		}
		var eo errorObject
		if err := json.Unmarshal(rr.Body.Bytes(), &eo); err != nil {
			t.Fatalf("decode error object: %v", err)
		}
		if eo.Code != codeValidationFailed {
			t.Errorf("query %q: code = %q, want %q", q, eo.Code, codeValidationFailed)
		}
	}
}

// TestJournalNDJSONStreams asserts the NDJSON form is one record per line, both
// via Accept and ?format=ndjson (MOCK-602.2).
func TestJournalNDJSONStreams(t *testing.T) {
	for _, sel := range []struct{ name, accept, target string }{
		{"accept header", contentTypeNDJSON, "/v1/instances/a/journal"},
		{"format param", "", "/v1/instances/a/journal?format=ndjson"},
	} {
		t.Run(sel.name, func(t *testing.T) {
			h, _ := newTestHandler()
			rr := doReq(t, h, http.MethodGet, sel.target, sel.accept)
			if ct := rr.Header().Get("Content-Type"); ct != contentTypeNDJSON {
				t.Errorf("content-type = %q, want %q", ct, contentTypeNDJSON)
			}
			lines := strings.Split(strings.TrimRight(rr.Body.String(), "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("expected 2 NDJSON lines, got %d: %q", len(lines), rr.Body.String())
			}
			for _, l := range lines {
				var r journalapi.Record
				if err := json.Unmarshal([]byte(l), &r); err != nil {
					t.Errorf("line not valid JSON record: %v (%q)", err, l)
				}
			}
		})
	}
}

// TestErrorObjectShape asserts a 4xx body conforms to the OpenAPI error schema:
// a code from the catalogue and a message.
func TestErrorObjectShape(t *testing.T) {
	h, _ := newTestHandler()
	rr := doReq(t, h, http.MethodGet, "/v1/instances/zzz", "")
	var eo errorObject
	if err := json.Unmarshal(rr.Body.Bytes(), &eo); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if eo.Code != codeNotFound {
		t.Errorf("code = %q, want %q", eo.Code, codeNotFound)
	}
	if eo.Message == "" {
		t.Error("message must not be empty")
	}
}

// TestCheckBind covers the MOCK-104.5 refusal matrix: a non-loopback bind
// without a token is refused; loopback or a token is allowed.
func TestCheckBind(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		token   bool
		wantErr bool
	}{
		{"loopback ipv4 no token ok", "127.0.0.1:9091", false, false},
		{"loopback name no token ok", "localhost:9091", false, false},
		{"loopback ipv6 no token ok", "[::1]:9091", false, false},
		{"wildcard no token refused", "0.0.0.0:9091", false, true},
		{"public no token refused", "10.0.0.5:9091", false, true},
		{"empty host no token refused", ":9091", false, true},
		{"public with token ok", "10.0.0.5:9091", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckBind(tt.addr, tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckBind(%q, token=%v) err = %v, wantErr = %v", tt.addr, tt.token, err, tt.wantErr)
			}
			if tt.wantErr && err != nil {
				var be *BindError
				if !strings.Contains(err.Error(), tt.addr) {
					t.Errorf("bind error should name the address %q: %v", tt.addr, err)
				}
				_ = be
			}
		})
	}
}
