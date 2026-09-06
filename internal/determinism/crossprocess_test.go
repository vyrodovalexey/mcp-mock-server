package determinism_test

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
)

// childEnv, when set, makes the test binary print a deterministic fingerprint
// of the whole kernel and exit, instead of running the suite. This is how the
// cross-process test re-executes itself.
const childEnv = "DETERMINISM_CROSSPROCESS_CHILD"

// kernelFingerprint builds a stable string covering Root, a chain of Derive
// calls across several domains, an RNG head, and a VirtualClock instant. Any
// leak of nondeterminism — a map range, a wall-clock read, a per-process seed —
// would change this string between processes.
func kernelFingerprint() string {
	req := deriveRequestKey(fixedSeed, fixedInstance, fixedMethod, fixedRawID, fixedBody)

	var b strings.Builder
	root := determinism.Root(fixedSeed)
	fmt.Fprintf(&b, "root=%s\n", hex.EncodeToString(root[:]))
	for _, d := range determinism.AllDomains() {
		child := req.Derive(d, []byte("probe"))
		fmt.Fprintf(&b, "%s=%s\n", string(d), hex.EncodeToString(child[:]))
	}
	fmt.Fprintf(&b, "rnghead=%s\n", rngHead256(req))

	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	vc := determinism.NewVirtualClock(req, epoch)
	fmt.Fprintf(&b, "clock=%s\n", vc.Now().UTC().Format(time.RFC3339Nano))
	return b.String()
}

// TestMain intercepts a re-executed child and has it emit the fingerprint.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		fmt.Print(kernelFingerprint())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestCrossProcessAndGOMAXPROCSDeterminism re-runs this binary as child
// processes at GOMAXPROCS=1 and GOMAXPROCS=8 and asserts every child emits a
// byte-identical kernel fingerprint that also matches the in-process one
// (acceptance criterion 1: byte-stable across GOMAXPROCS and across separate
// process invocations). Crossing a real process boundary is the strongest form
// of the guarantee: each process gets a fresh Go map seed and scheduler, so any
// hidden dependence on either would diverge here.
func TestCrossProcessAndGOMAXPROCSDeterminism(t *testing.T) {
	t.Parallel()
	want := kernelFingerprint()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	for _, procs := range []string{"1", "8"} {
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
					t.Fatalf("GOMAXPROCS=%s run %d: kernel fingerprint differs from parent", procs, run)
				}
			}
		})
	}
}
