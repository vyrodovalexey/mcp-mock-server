package control

import _ "embed"

// OpenAPISpec is the control API contract document, served verbatim at
// GET /v1/openapi.yaml (MOCK-104.1). It is an embedded copy of
// specification/contracts/control-api.openapi.yaml; go:embed cannot reach
// outside the package directory, so the copy lives here and
// TestEmbeddedOpenAPIMatchesSpec asserts it is byte-identical to the
// authoritative file (the same drift-guard pattern TASK-006 uses for
// scenario.schema.json). Changing the contract means updating both, and the
// test fails loudly if they diverge.
//
//go:embed control-api.openapi.yaml
var OpenAPISpec []byte
