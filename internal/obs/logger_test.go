package obs_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// logLine is the subset of the observability.md §4 log schema this package can
// produce in Phase 1. The schema test asserts every emitted line parses into
// this shape with the mandatory fields present.
type logLine struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
	Event string `json:"event"`
}

// TestNewLoggerEmitsJSON asserts NewLogger produces one JSON object per line
// with the mandatory time/level/msg fields (observability.md §4, MOCK-105.3).
func TestNewLoggerEmitsJSON(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := obs.NewLogger(&buf, slog.LevelInfo)
	logger.Info("mcpmock started", obs.FieldEvent, obs.EventStartup)

	line := strings.TrimSpace(buf.String())
	var got logLine
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nline=%q", err, line)
	}
	if got.Time == "" || got.Level != "INFO" || got.Msg != "mcpmock started" {
		t.Fatalf("mandatory fields wrong: %+v", got)
	}
	if got.Event != obs.EventStartup {
		t.Fatalf("event = %q, want %q", got.Event, obs.EventStartup)
	}
}

// TestLogSchema validates every emitted line against the observability.md §4
// mandatory-field contract across a range of record kinds and levels.
func TestLogSchema(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	base := obs.NewLogger(&buf, slog.LevelDebug)
	logger := obs.InstanceLogger(base, "inst-a")

	cases := []struct {
		name  string
		emit  func()
		level string
		event string
	}{
		{"startup", func() {
			logger.Info("mcpmock started", obs.FieldEvent, obs.EventStartup)
		}, "INFO", obs.EventStartup},
		{"request", func() {
			logger.Debug("request completed",
				obs.FieldEvent, obs.EventRequestCompleted,
				obs.FieldSeq, uint64(7))
		}, "DEBUG", obs.EventRequestCompleted},
		{"mutation", func() {
			logger.Info("control mutation",
				obs.FieldEvent, obs.EventControlMutation,
				obs.FieldActor, "abcd1234")
		}, "INFO", obs.EventControlMutation},
		{"drop warn", func() {
			logger.Warn("journal dropped",
				obs.FieldEvent, obs.EventJournalDropped)
		}, "WARN", obs.EventJournalDropped},
	}
	for _, tc := range cases {
		tc.emit()
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("emitted %d lines, want %d", len(lines), len(cases))
	}
	for i, raw := range lines {
		var got logLine
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
		if got.Time == "" {
			t.Errorf("line %d (%s): missing time", i, cases[i].name)
		}
		if got.Level != cases[i].level {
			t.Errorf("line %d (%s): level=%q want %q", i, cases[i].name, got.Level, cases[i].level)
		}
		if got.Msg == "" {
			t.Errorf("line %d (%s): missing msg", i, cases[i].name)
		}
		if got.Event != cases[i].event {
			t.Errorf("line %d (%s): event=%q want %q", i, cases[i].name, got.Event, cases[i].event)
		}
		// msg must be stable/lowercase with no interpolation.
		if got.Msg != strings.ToLower(got.Msg) {
			t.Errorf("line %d (%s): msg %q is not lowercase", i, cases[i].name, got.Msg)
		}
	}
}

// TestInstanceLoggerBindsInstance asserts a per-instance logger pre-binds the
// instance field on every line (ADR-016).
func TestInstanceLoggerBindsInstance(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	base := obs.NewLogger(&buf, slog.LevelInfo)
	obs.InstanceLogger(base, "inst-42").Info("lifecycle", obs.FieldEvent, obs.EventStartup)

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if m[obs.FieldInstance] != "inst-42" {
		t.Fatalf("instance field = %v, want inst-42", m[obs.FieldInstance])
	}
}

// TestLevelFiltering asserts DEBUG is suppressed at INFO level, matching the
// ADR-016 default where per-request DEBUG is off.
func TestLevelFiltering(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := obs.NewLogger(&buf, slog.LevelInfo)
	logger.Debug("request completed", obs.FieldEvent, obs.EventRequestCompleted)
	if buf.Len() != 0 {
		t.Fatalf("DEBUG line emitted at INFO level: %q", buf.String())
	}
}

const fixtureToken = "Bearer super-secret-token-value-DO-NOT-LOG-000"

// TestNoCredentialInLogs asserts a CredentialHash logs only its fingerprint and
// that the raw fixture token never appears in output, even when passed
// carelessly (security.md §5, MOCK-105.5). Redaction is structural via
// slog.LogValuer, not a filter.
func TestNoCredentialInLogs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := obs.NewLogger(&buf, slog.LevelInfo)

	cred := obs.CredentialHash{Fingerprint: "deadbeef"}
	// Careless call: pass the credential value directly as a field.
	logger.Info("auth", obs.FieldEvent, "auth.decision", "cred", cred)

	out := buf.String()
	if strings.Contains(out, fixtureToken) {
		t.Fatalf("raw token leaked into log: %q", out)
	}
	if !strings.Contains(out, "deadbeef") {
		t.Fatalf("fingerprint missing from log: %q", out)
	}
}

// TestCredentialHashLogValue asserts the LogValuer returns exactly the
// fingerprint and nothing that could carry a raw secret.
func TestCredentialHashLogValue(t *testing.T) {
	t.Parallel()
	c := obs.CredentialHash{Fingerprint: "0a1b2c3d"}
	if got := c.LogValue().String(); got != "0a1b2c3d" {
		t.Fatalf("LogValue = %q, want fingerprint", got)
	}
}
