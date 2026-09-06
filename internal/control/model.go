package control

import (
	"context"
	"iter"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// Backend is the process-scoped control operation set — the ADR-015 single
// definition of the operations, expressed as a Go interface so the HTTP handler,
// the HTTP client and the CLI are all transports over it. The root mcpmock
// package aliases this as mcpmock.Control (contracts/library-api.md §4).
//
// It is safe for concurrent use. It is consumer-only: callers use it, they do
// not implement it outside this module.
type Backend interface {
	// Instances lists every logical instance in stable configuration order.
	Instances(ctx context.Context) ([]InstanceInfo, error)
	// Instance returns the named instance, or a not-found error.
	Instance(ctx context.Context, name string) (InstanceInfo, error)
	// Seed returns the effective root seed and its source (MOCK-704.2).
	Seed(ctx context.Context) (SeedInfo, error)
	// Health returns process health.
	Health(ctx context.Context) (Health, error)
	// For returns the control operations scoped to one instance.
	For(name string) InstanceBackend
}

// InstanceBackend is the per-instance control operation set (aliased in the root
// package as mcpmock.InstanceControl). Mutations are serialized per instance
// (ADR-014); it is safe for concurrent use.
type InstanceBackend interface {
	// Journal returns one page of the request journal matching q, in Seq order
	// (MOCK-602.1/602.4/602.7).
	Journal(ctx context.Context, q journalapi.Query) (journalapi.Page, error)
	// JournalStream returns an iterator over the matching records in Seq order
	// without materializing the journal (MOCK-602.2).
	JournalStream(ctx context.Context, q journalapi.Query) (iter.Seq2[journalapi.Record, error], error)
	// Correlations groups the matching records into MRTR chains (MOCK-605).
	Correlations(ctx context.Context, q journalapi.Query) ([]journalapi.Correlation, error)
	// ClearJournal empties the instance's journal (MOCK-602.6/702.7).
	ClearJournal(ctx context.Context) error
}

// InstanceInfo is the read model of one logical instance
// (contracts/control-api.openapi.yaml #/components/schemas/InstanceInfo).
type InstanceInfo struct {
	// Name is the instance name.
	Name string `json:"name"`
	// MountPath is the path prefix the instance is served under.
	MountPath string `json:"mountPath"`
	// URL is the base URL a client POSTs to, or "" for a stdio-only instance.
	URL string `json:"url,omitempty"`
	// Era is the resolved protocol era ("modern" in Phase 1).
	Era string `json:"era"`
	// Generation is the current configuration snapshot generation (ADR-014).
	Generation uint64 `json:"generation"`
	// Journal summarizes the instance's journal ring.
	Journal JournalInfo `json:"journal"`
	// Hostile reports the ADR-018 hostile-corpus gate state; false in Phase 1.
	Hostile bool `json:"hostile"`
}

// JournalInfo summarizes an instance's journal ring for [InstanceInfo].
type JournalInfo struct {
	// Records is the number of records currently held.
	Records int `json:"records"`
	// Dropped is the number of records lost to overflow (ADR-005).
	Dropped uint64 `json:"dropped"`
	// Bytes is the retained-byte estimate against the byte budget.
	Bytes int64 `json:"bytes"`
}

// SeedInfo is the effective root seed and its source (GET /v1/seed,
// MOCK-704.2).
type SeedInfo struct {
	// Seed is the effective root seed.
	Seed uint64 `json:"seed"`
	// Source is where the seed came from: "flag", "scenario" or "random".
	Source string `json:"source"`
}

// Health is process health (GET /v1/health,
// contracts/control-api.openapi.yaml #/components/schemas/Health).
type Health struct {
	// Status is "ok" or "degraded".
	Status string `json:"status"`
	// Instances is the number of logical instances.
	Instances int `json:"instances"`
	// UptimeSeconds is the process uptime in seconds.
	UptimeSeconds float64 `json:"uptimeSeconds"`
	// Seed is the effective root seed.
	Seed uint64 `json:"seed"`
	// SafeMode reports the ADR-018 safe-mode state; false in Phase 1.
	SafeMode bool `json:"safeMode"`
}
