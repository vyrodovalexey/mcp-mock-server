package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunVersion asserts `mcpmock version` reports the linker-stamped build
// identity and exits zero (criterion 8). The default (unstamped) values are the
// honest go-run defaults.
func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if code := runVersion(&out, nil); code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	got := out.String()
	for _, want := range []string{version, commit, date} {
		if !strings.Contains(got, want) {
			t.Errorf("version output %q missing %q", got, want)
		}
	}
}

// TestBuildInfoStamped asserts the stamp variables flow through buildInfo, so a
// -ldflags override is reported verbatim.
func TestBuildInfoStamped(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })
	version, commit, date = "v1.2.3", "abc1234", "2026-01-02T03:04:05Z"

	bi := buildInfo()
	if bi.Version != "v1.2.3" || bi.Commit != "abc1234" || bi.Date != "2026-01-02T03:04:05Z" {
		t.Errorf("buildInfo() = %+v, want the stamped values", bi)
	}
}
