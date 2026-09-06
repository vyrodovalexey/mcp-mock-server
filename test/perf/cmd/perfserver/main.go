// Command perfserver is a TEST-ASSET load-target harness for TASK-029.
//
// It is not production code and is not part of the shipped module surface: it
// exists solely so k6 can drive the mcpmock facade over a real loopback socket
// under the GAP-006 / requirements-spec.md §9 measurement conditions. It starts
// exactly one Server via the public facade, prints the bound URL, and blocks
// until SIGINT/SIGTERM, then shuts down cleanly.
//
// Journaling mode is selected with -journal:
//
//	off   -> WithoutJournal()                    (MOCK-901 measurement mode)
//	full  -> WithJournal(100000-record ring,     (MOCK-902 measurement mode)
//	         bodies=full)
//	def   -> facade default (4096-record ring)
//
// It writes lifecycle logs to stderr only (ADR-011: never stdout), and prints
// one machine-readable line "PERFSERVER_URL=<url>" to stdout so the launcher can
// discover the ephemeral port.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "MCP HTTP listen address")
	journal := flag.String("journal", "off", "journal mode: off|full|def")
	seed := flag.Uint64("seed", 0x29, "deterministic seed")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	opts := []mcpmock.Option{
		mcpmock.WithAddr(*addr),
		mcpmock.WithSeed(*seed),
		mcpmock.WithLogger(logger),
	}
	switch *journal {
	case "off":
		opts = append(opts, mcpmock.WithoutJournal())
	case "full":
		opts = append(opts, mcpmock.WithJournal(journalapi.Config{
			Enabled:    true,
			MaxRecords: 100000,
			Mode:       journalapi.CaptureFull,
		}))
	case "def":
		// facade default ring
	default:
		fmt.Fprintf(os.Stderr, "unknown -journal %q\n", *journal)
		os.Exit(2)
	}

	srv, err := mcpmock.New(opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "new server: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}

	insts := srv.Instances()
	if len(insts) == 0 {
		fmt.Fprintln(os.Stderr, "no instances")
		os.Exit(1)
	}
	url := insts[0].URL()
	// stdout: single machine-readable discovery line.
	fmt.Printf("PERFSERVER_URL=%s\n", url)
	fmt.Printf("PERFSERVER_PID=%d\n", os.Getpid())
	fmt.Printf("PERFSERVER_GOMAXPROCS=%d\n", runtime.GOMAXPROCS(0))
	os.Stdout.Sync()

	logger.Info("perfserver ready", "url", url, "journal", *journal, "gomaxprocs", runtime.GOMAXPROCS(0))

	<-ctx.Done()
	logger.Info("perfserver stopping")
	shutdownCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
	}
}
