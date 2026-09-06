package journalapi

import "time"

// Default journal bounds and timeouts (ADR-005).
const (
	// DefaultMaxRecords is the default record-count bound before overflow.
	DefaultMaxRecords = 100_000
	// DefaultMaxBytes is the default byte-budget bound before overflow
	// (256 MiB).
	DefaultMaxBytes int64 = 256 << 20
	// DefaultBlockTimeout is the default bound on the block overflow policy,
	// after which it degrades to drop-oldest (ADR-005; GAP-017).
	DefaultBlockTimeout = 100 * time.Millisecond
)

// CaptureMode selects how much of each body the journal retains (ADR-005). The
// zero value is [CaptureFull].
//
// Stability: v0. Values are stable strings; the set is closed for Phase 1.
type CaptureMode string

// CaptureMode values.
const (
	// CaptureFull retains the body verbatim.
	CaptureFull CaptureMode = "full"
	// CaptureTruncate retains a prefix plus a SHA-256 of the whole plus the
	// original length. The prefix length is carried in [Config.TruncateBytes].
	CaptureTruncate CaptureMode = "truncate"
	// CaptureDigest retains only the SHA-256 and the length.
	CaptureDigest CaptureMode = "digest"
	// CaptureOff retains neither body nor digest.
	CaptureOff CaptureMode = "off"
)

// OverflowPolicy selects what happens when a bound is reached (ADR-005,
// MOCK-902). The zero value is [OverflowDropOldest].
//
// Stability: v0.
type OverflowPolicy string

// OverflowPolicy values.
const (
	// OverflowDropOldest overwrites the oldest record, increments the dropped
	// counter, and leaves a [Record.Dropped] marker so a reader sees the gap.
	OverflowDropOldest OverflowPolicy = "drop-oldest"
	// OverflowBlock waits up to [Config.BlockTimeout] for a reader to drain,
	// then degrades to drop-oldest (incrementing a distinct counter).
	OverflowBlock OverflowPolicy = "block"
	// OverflowError rejects the request with a 503 and a journalled reason.
	OverflowError OverflowPolicy = "error"
)

// Config is the journal capture configuration for one instance (ADR-005). It is
// a value with no mandatory constructor; the disabled path checks [Config.Enabled]
// before doing any work, so journaling-off allocates nothing (MOCK-901).
//
// Stability: v0.
type Config struct {
	// Enabled turns capture on. When false the write path is a single bool
	// check and no [Record] is constructed.
	Enabled bool
	// Mode is the body capture mode; see [CaptureMode]. Empty means
	// [CaptureFull].
	Mode CaptureMode
	// TruncateBytes is the prefix length retained under [CaptureTruncate];
	// ignored otherwise.
	TruncateBytes int
	// MaxRecords bounds the record count; zero means [DefaultMaxRecords].
	MaxRecords int
	// MaxBytes bounds the total retained bytes; zero means [DefaultMaxBytes].
	MaxBytes int64
	// Overflow selects the policy when a bound is reached; empty means
	// [OverflowDropOldest].
	Overflow OverflowPolicy
	// BlockTimeout bounds [OverflowBlock]; zero means [DefaultBlockTimeout].
	BlockTimeout time.Duration
	// RedactHeaders lists additional header names (case-insensitive) whose
	// values are redacted at capture, beyond the always-redacted set in
	// security.md §5.
	RedactHeaders []string
}

// WithDefaults returns a copy of c with zero-valued fields replaced by their
// documented defaults. Callers building a storage ring use it so the contract's
// defaults live in one place.
func (c Config) WithDefaults() Config {
	if c.Mode == "" {
		c.Mode = CaptureFull
	}
	if c.MaxRecords == 0 {
		c.MaxRecords = DefaultMaxRecords
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = DefaultMaxBytes
	}
	if c.Overflow == "" {
		c.Overflow = OverflowDropOldest
	}
	if c.BlockTimeout == 0 {
		c.BlockTimeout = DefaultBlockTimeout
	}
	return c
}
