package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/vyrodovalexey/mcp-mock-server/internal/config"
)

// resolveScenarioRoot builds a bounded scenario root for extends resolution
// (MOCK-703.7), mirroring the facade's own resolution: an explicit --scenario-root
// wins, else the file's own directory is the root. It returns the loader's error
// unchanged so validate/serve can branch on it.
func resolveScenarioRoot(root, path string) (*config.ScenarioRoot, error) {
	if root == "" {
		root = dirOf(path)
	}
	sr, err := config.NewScenarioRoot(root)
	if err != nil {
		return nil, fmt.Errorf("mcpmock: scenario root: %w", err)
	}
	return sr, nil
}

// dirOf returns the directory portion of a path, or "." when there is none. It
// avoids importing path/filepath in two files for a single call and keeps the
// root-resolution rule identical to the facade's filepath.Dir.
func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		if i == 0 {
			return "/"
		}
		return path[:i]
	}
	return "."
}

// parseSeed parses a --seed value as an unsigned 64-bit integer. An empty value
// means "generate one"; a non-empty unparsable value is an error, never a silent
// default, so a typo does not quietly randomize a run the operator meant to pin.
func parseSeed(raw string) (seed uint64, set bool, err error) {
	if raw == "" {
		return 0, false, nil
	}
	n, perr := strconv.ParseUint(raw, 10, 64)
	if perr != nil {
		return 0, false, fmt.Errorf("--seed must be an unsigned 64-bit integer: %w", perr)
	}
	return n, true, nil
}

// randomSeed draws a cryptographically random root seed, used when --seed is
// absent so two runs without a seed differ (MOCK-704.1). It matches the facade's
// own generation so the CLI's printed seed equals the facade's effective seed.
func randomSeed() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic and effectively impossible on the
		// supported platforms; fall back to a fixed value rather than aborting.
		return 0
	}
	return binary.BigEndian.Uint64(b[:])
}

// parseLogLevel maps a --log-level string to an slog.Level, defaulting to INFO
// for an empty value and erroring on an unknown one.
func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("--log-level must be one of debug,info,warn,error; got %q", raw)
	}
}
