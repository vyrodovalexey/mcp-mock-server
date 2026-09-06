package main

import (
	"log/slog"
	"testing"
)

// TestParseSeed covers the --seed parsing: empty means generate, a valid uint64
// is used, a bad value is an error (never a silent random default) (MOCK-704.1).
func TestParseSeed(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    uint64
		wantSet bool
		wantErr bool
	}{
		{"empty generates", "", 0, false, false},
		{"zero", "0", 0, true, false},
		{"max uint64", "18446744073709551615", 1<<64 - 1, true, false},
		{"typical", "12345", 12345, true, false},
		{"negative", "-1", 0, false, true},
		{"not a number", "abc", 0, false, true},
		{"overflow", "18446744073709551616", 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, set, err := parseSeed(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got != tc.want || set != tc.wantSet {
				t.Errorf("parseSeed(%q) = (%d,%v), want (%d,%v)", tc.raw, got, set, tc.want, tc.wantSet)
			}
		})
	}
}

// TestParseLogLevel covers the level mapping and its default and error paths.
func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		raw     string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"DEBUG", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"trace", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseLogLevel(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestParseTransports covers transport selection, including the empty-set and
// unknown-transport errors and both transports at once (criterion 5).
func TestParseTransports(t *testing.T) {
	cases := []struct {
		raw       string
		wantStdio bool
		wantHTTP  bool
		wantErr   bool
	}{
		{"http", false, true, false},
		{"stdio", true, false, false},
		{"stdio,http", true, true, false},
		{"http,stdio", true, true, false},
		{" http , stdio ", true, true, false},
		{"", false, false, true},
		{"grpc", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			ts, err := parseTransports(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if ts.stdio != tc.wantStdio || ts.http != tc.wantHTTP {
				t.Errorf("parseTransports(%q) = %+v, want stdio=%v http=%v", tc.raw, ts, tc.wantStdio, tc.wantHTTP)
			}
		})
	}
}

// TestRandomSeedDiffers asserts two generated seeds differ (MOCK-704.1: two runs
// without --seed differ). A collision is astronomically unlikely; a repeated
// draw catches a broken generator.
func TestRandomSeedDiffers(t *testing.T) {
	if randomSeed() == randomSeed() {
		t.Error("two random seeds are identical; generator is not random")
	}
}
