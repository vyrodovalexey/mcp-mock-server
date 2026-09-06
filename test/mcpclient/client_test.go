package mcpclient

import (
	"context"
	"encoding/json"
	"testing"
)

// decodeParamsMeta unmarshals a marshalled request and returns params and its
// _meta member (or nil, false when absent). It is a test helper that decodes
// the client's own output the way a server would see it.
func decodeParamsMeta(t *testing.T, raw []byte) (obj, params, meta map[string]any) {
	t.Helper()
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal request: %v\nraw: %s", err, raw)
	}
	if p, ok := obj["params"].(map[string]any); ok {
		params = p
		if m, ok := p["_meta"].(map[string]any); ok {
			meta = m
		}
	}
	return obj, params, meta
}

func TestRequestMarshalEnvelope(t *testing.T) {
	r := NewRequest(IntID(7), MethodDiscover, nil)
	raw, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	obj, _, _ := decodeParamsMeta(t, raw)
	if obj["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc: got %v, want 2.0", obj["jsonrpc"])
	}
	if obj["method"] != MethodDiscover {
		t.Errorf("method: got %v, want %s", obj["method"], MethodDiscover)
	}
	// Numeric id must survive as a JSON number, not a string.
	if _, ok := obj["id"].(float64); !ok {
		t.Errorf("id: got %T, want JSON number", obj["id"])
	}
}

// TestIDTypePreservation proves a numeric id and a string id of the same digits
// are distinguishable on the wire (the client controls the exact lexical form).
func TestIDTypePreservation(t *testing.T) {
	numRaw, _ := NewRequest(IntID(1), MethodToolsList, nil).Marshal()
	strRaw, _ := NewRequest(StringID("1"), MethodToolsList, nil).Marshal()

	var numObj, strObj map[string]json.RawMessage
	_ = json.Unmarshal(numRaw, &numObj)
	_ = json.Unmarshal(strRaw, &strObj)

	if string(numObj["id"]) != "1" {
		t.Errorf("numeric id raw: got %s, want 1", numObj["id"])
	}
	if string(strObj["id"]) != `"1"` {
		t.Errorf("string id raw: got %s, want \"1\"", strObj["id"])
	}
	if string(numObj["id"]) == string(strObj["id"]) {
		t.Error("numeric and string ids are indistinguishable on the wire")
	}
}

// TestMetaOmissionMatrix covers TASK-008 acceptance criterion 3: complete,
// protocolVersion absent, clientCapabilities absent, and _meta absent entirely.
func TestMetaOmissionMatrix(t *testing.T) {
	client := &Client{defaultClientInfo: defaultClientInfo()}

	t.Run("complete", func(t *testing.T) {
		r := client.bindDefault(DiscoverRequest(IntID(1)))
		raw, _ := r.Marshal()
		_, _, meta := decodeParamsMeta(t, raw)
		if meta == nil {
			t.Fatal("_meta absent, want present")
		}
		if _, ok := meta[MetaKeyProtocolVersion]; !ok {
			t.Error("protocolVersion absent from complete _meta")
		}
		if _, ok := meta[MetaKeyClientCapabilities]; !ok {
			t.Error("clientCapabilities absent from complete _meta")
		}
		if meta[MetaKeyProtocolVersion] != ProtocolRevision {
			t.Errorf("protocolVersion: got %v, want %s", meta[MetaKeyProtocolVersion], ProtocolRevision)
		}
	})

	t.Run("protocolVersionAbsent", func(t *testing.T) {
		r := DiscoverRequest(IntID(1))
		r.Meta.OmitProtocolVersion = true
		raw, _ := r.Marshal()
		_, _, meta := decodeParamsMeta(t, raw)
		if meta == nil {
			t.Fatal("_meta absent, want present")
		}
		if _, ok := meta[MetaKeyProtocolVersion]; ok {
			t.Error("protocolVersion present, want omitted")
		}
		if _, ok := meta[MetaKeyClientCapabilities]; !ok {
			t.Error("clientCapabilities should still be present")
		}
	})

	t.Run("clientCapabilitiesAbsent", func(t *testing.T) {
		r := DiscoverRequest(IntID(1))
		r.Meta.OmitClientCapabilities = true
		raw, _ := r.Marshal()
		_, _, meta := decodeParamsMeta(t, raw)
		if meta == nil {
			t.Fatal("_meta absent, want present")
		}
		if _, ok := meta[MetaKeyClientCapabilities]; ok {
			t.Error("clientCapabilities present, want omitted")
		}
	})

	t.Run("metaAbsentEntirely", func(t *testing.T) {
		r := DiscoverRequest(IntID(1))
		r.Meta.Omit = true
		raw, _ := r.Marshal()
		obj, _, meta := decodeParamsMeta(t, raw)
		if meta != nil {
			t.Error("_meta present, want absent")
		}
		// With no params and _meta omitted, params should be absent entirely,
		// which is itself a valid rejection-path input.
		if _, ok := obj["params"]; ok {
			t.Error("params present with no content, want absent")
		}
	})
}

// TestRawBodyMalformation proves malformed requests are a first-class
// capability (MOCK-203/204): RawBody is sent verbatim, bypassing all structured
// construction.
func TestRawBodyMalformation(t *testing.T) {
	malformed := []byte(`{"jsonrpc":"2.0","method":"tools/call","params":{`) // truncated
	r := &Request{RawBody: malformed}
	raw, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(malformed) {
		t.Errorf("RawBody not emitted verbatim: got %s", raw)
	}
	// It must NOT be valid JSON — that is the point.
	var v any
	if json.Unmarshal(raw, &v) == nil {
		t.Error("expected malformed body to be invalid JSON")
	}
}

// TestToolsCallParams checks the tools/call params carry name and arguments per
// builtin-tools.md §1.3, and that nil args omits the arguments key.
func TestToolsCallParams(t *testing.T) {
	r := ToolsCallRequest(IntID(1), "echo", map[string]any{"message": "hi"})
	raw, _ := r.Marshal()
	_, params, _ := decodeParamsMeta(t, raw)
	if params[ParamsKeyName] != "echo" {
		t.Errorf("name: got %v, want echo", params[ParamsKeyName])
	}
	args, ok := params[ParamsKeyArguments].(map[string]any)
	if !ok || args["message"] != "hi" {
		t.Errorf("arguments: got %v", params[ParamsKeyArguments])
	}

	rNil := ToolsCallRequest(IntID(1), "echo", nil)
	rawNil, _ := rNil.Marshal()
	_, paramsNil, _ := decodeParamsMeta(t, rawNil)
	if _, ok := paramsNil[ParamsKeyArguments]; ok {
		t.Error("arguments present for nil args, want absent")
	}
}

// TestExtraMetaKeysPreserved proves unknown _meta keys survive (annex 2.11).
func TestExtraMetaKeysPreserved(t *testing.T) {
	r := DiscoverRequest(IntID(1))
	r.Meta.Extra = map[string]any{"io.example/custom": "keep-me"}
	raw, _ := r.Marshal()
	_, _, meta := decodeParamsMeta(t, raw)
	if meta["io.example/custom"] != "keep-me" {
		t.Errorf("unknown _meta key not preserved: %v", meta)
	}
}

func TestDoNilRequest(t *testing.T) {
	c := &Client{tr: nil}
	if _, err := c.Do(context.Background(), nil); err == nil {
		t.Error("expected error for nil request")
	}
}
