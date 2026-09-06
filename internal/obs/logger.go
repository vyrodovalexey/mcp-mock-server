package obs

import (
	"io"
	"log/slog"
)

// Event identifiers used on the mandatory "event" log field (observability.md
// §4.1). They are stable, machine-readable, lowercase and dotted, so a log
// consumer can group by event independently of the human-readable "msg".
const (
	// EventStartup is emitted once at process start (observability.md §4.3).
	EventStartup = "startup"
	// EventRequestCompleted marks a completed request (DEBUG level).
	EventRequestCompleted = "request.completed"
	// EventControlMutation marks a control-API mutation of a Snapshot.
	EventControlMutation = "control.mutation"
	// EventFaultFired marks a fault rule firing.
	EventFaultFired = "fault.fired"
	// EventStdoutLeak marks a byte written to the hijacked stdout (ADR-011).
	EventStdoutLeak = "stdout_leak"
	// EventJournalDropped marks a dropped journal record (the honesty event).
	EventJournalDropped = "journal.dropped"
	// EventHostileArmed marks a hostile-mode arming (ADR-018).
	EventHostileArmed = "hostile.armed"
	// EventOTELExportFailed marks an OTLP export failure; never fatal.
	EventOTELExportFailed = "otel.export_failed"
)

// Log field keys defined by observability.md §4.1. Using constants keeps field
// names consistent across the module and stops a typo producing a field the
// TestLogSchema contract does not know about.
const (
	// FieldEvent is the mandatory machine-readable event id.
	FieldEvent = "event"
	// FieldInstance is the logical instance name, pre-bound on instance loggers.
	FieldInstance = "instance"
	// FieldSeq is the journal sequence number, joining a log line to the journal.
	FieldSeq = "seq"
	// FieldTraceID is the W3C trace id, joining a log line to a trace.
	FieldTraceID = "trace_id"
	// FieldSpanID is the W3C span id.
	FieldSpanID = "span_id"
	// FieldActor is a control-API actor token fingerprint, never the token.
	FieldActor = "actor"
	// FieldError carries an error string on WARN/ERROR records.
	FieldError = "error"
	// FieldHint carries an optional requirement id or remedy.
	FieldHint = "hint"
)

// NewLogger returns a *slog.Logger writing newline-delimited JSON to w at the
// given minimum level. It is the only logger constructor in the module.
//
// The caller supplies the writer explicitly: cmd/mcpmock passes os.Stderr and
// an embedded test passes its own buffer. There is deliberately no default to
// os.Stdout — stdout is the stdio transport's frame channel (ADR-011), and a
// logger over it would corrupt the protocol stream. slog.SetDefault is never
// called (ADR-007); this logger is owned by its [Bundle] and passed explicitly.
//
// The handler adds a stable ReplaceAttr that leaves the standard slog keys
// (time, level, msg) intact, so every emitted line matches the
// observability.md §4 schema.
func NewLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	if w == nil {
		// A nil writer would panic on first use; discard is the safe,
		// non-stdout fallback. cmd/mcpmock never passes nil.
		w = io.Discard
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
	})
	return slog.New(handler)
}

// InstanceLogger derives a per-instance logger from base with the instance name
// pre-bound as the "instance" field (ADR-016). Deriving with With is cheap and
// allocation-light, and it means every line an instance emits carries its
// identity without the hot path having to add it.
func InstanceLogger(base *slog.Logger, instance string) *slog.Logger {
	return base.With(slog.String(FieldInstance, instance))
}

// CredentialHash is a keyed hash of a presented credential (security.md §5). It
// exists so a credential can be logged safely: it implements [slog.LogValuer]
// to yield only its fingerprint, so a log call that passes a CredentialHash —
// even carelessly — can never emit the raw secret. Construct it from the keyed
// hash produced on the journal capture path; this package never sees, stores or
// derives the raw credential itself.
type CredentialHash struct {
	// Fingerprint is the non-reversible identifier of a credential, typically
	// the first bytes of a keyed HMAC rendered as hex. It is safe to log.
	Fingerprint string
}

// LogValue implements [slog.LogValuer]. It returns only the fingerprint, so the
// raw credential is unrepresentable in a log record by construction rather than
// by convention.
func (c CredentialHash) LogValue() slog.Value {
	return slog.StringValue(c.Fingerprint)
}

// Compile-time assurance that CredentialHash satisfies slog.LogValuer, so the
// structural redaction guarantee cannot silently regress.
var _ slog.LogValuer = CredentialHash{}
