package ordered_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/ordered"
)

// The environment variable a re-executed child looks for. When set, the test
// binary prints a deterministic fingerprint of a freshly built Map/Set/Slice
// and exits, instead of running the normal suite.
const childEnv = "ORDERED_CROSSPROCESS_CHILD"

// fingerprint builds the canonical containers and returns a stable string that
// encodes their iteration order. Any leak of Go map randomness would change
// this string from run to run.
func fingerprint() string {
	const n = 128
	m := ordered.NewMap[string, int](n)
	s := ordered.NewSet[int](n)
	sl := ordered.NewSlice[[2]string](n)
	for i := 0; i < n; i++ {
		m.Set(fmt.Sprintf("k-%03d", (i*37+11)%n), i)
		s.Add((i*53 + 7) % (n * 2))
		sl.Append([2]string{fmt.Sprintf("H-%d", i%3), fmt.Sprintf("v%d", i)})
	}
	mb, _ := json.Marshal(m)
	sb, _ := json.Marshal(s)
	slb, _ := json.Marshal(sl)
	return string(mb) + "\n" + string(sb) + "\n" + string(slb)
}

// TestMain intercepts the re-executed child before the normal test runner.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		fmt.Print(fingerprint())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestCrossProcessAndGOMAXPROCSDeterminism runs this same binary as child
// processes with GOMAXPROCS=1 and GOMAXPROCS=8 and asserts every child emits a
// byte-identical fingerprint, and that it matches the in-process fingerprint.
//
// This is the strongest form of the determinism acceptance criterion: it
// crosses a real process boundary (fresh map seed per process) and two
// GOMAXPROCS settings, exactly the axes ADR-003's determinism suite varies. A
// map-iteration leak that happened to look stable within one process would
// still diverge here.
func TestCrossProcessAndGOMAXPROCSDeterminism(t *testing.T) {
	t.Parallel()
	want := fingerprint()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	for _, procs := range []string{"1", "8"} {
		procs := procs
		t.Run("GOMAXPROCS="+procs, func(t *testing.T) {
			t.Parallel()
			for run := 0; run < 3; run++ {
				cmd := exec.Command(exe, "-test.run=TestMain") //nolint:gosec // exe is our own test binary
				cmd.Env = append(os.Environ(),
					childEnv+"=1",
					"GOMAXPROCS="+procs,
				)
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("child run %d: %v", run, err)
				}
				if got := strings.TrimRight(string(out), "\n"); got != strings.TrimRight(want, "\n") {
					t.Fatalf("GOMAXPROCS=%s run %d: fingerprint differs from parent", procs, run)
				}
			}
		})
	}
}
