//go:build e2e

package embed_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// This file holds the request-driving helpers the embed tests share. They use
// ONLY net/http and encoding/json from the standard library — no mcpmock server
// package, no test/mcpclient. That is deliberate: it proves an external consumer
// can drive the mock with nothing beyond the four public packages plus the
// standard library. The JSON-RPC envelope and the params._meta shape are
// transcribed by hand from the wire annex the same way test/mcpclient does
// (ADR-017), so this driver shares no encoder with the server.

const (
	// protocolRevision is the modern revision the mock speaks (annex §0).
	protocolRevision = "2026-07-28"
	// contentTypeJSON is the required request/response media type (annex 1.4).
	contentTypeJSON = "application/json"
)

// jsonRPCEnvelope is the minimal request envelope the driver marshals by hand.
type jsonRPCEnvelope struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

// jsonRPCResponse is the subset of a response the driver inspects.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// toolsListBody builds a strict-valid tools/list request: params._meta carries
// protocolVersion and clientCapabilities, which _meta validation requires
// (MOCK-203). Built by hand from the annex, never from server code.
func toolsListBody(id int) []byte {
	env := jsonRPCEnvelope{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/list",
		Params: map[string]any{
			"_meta": map[string]any{
				"protocolVersion":    protocolRevision,
				"clientCapabilities": map[string]any{},
				"clientInfo": map[string]any{
					"name":    "embed-e2e",
					"version": "0.0.0",
				},
			},
		},
	}
	b, err := json.Marshal(env)
	if err != nil { // marshalling a fixed literal cannot fail; guard anyway.
		panic(fmt.Sprintf("marshal tools/list: %v", err))
	}
	return b
}

// drive POSTs one JSON-RPC request to url with a bounded context and returns the
// parsed response. Every call is deadline-bounded: a hung server must fail the
// test, not hang CI. It uses a fresh client per call and closes idle connections
// so no keep-alive goroutine outlives the call.
func drive(t *testing.T, url string, body []byte, extraHeaders map[string]string) jsonRPCResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d, body %s", url, resp.StatusCode, raw)
	}

	var out jsonRPCResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	return out
}
