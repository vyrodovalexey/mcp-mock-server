package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// hijackRestoreGrace bounds how long Restore waits for the drainer to flush on
// shutdown before it forcibly joins it (MOCK-508: a stalled drain must not hang
// the process).
const hijackRestoreGrace = 5 * time.Second

// hijackHandle wraps an installed [Hijack] with the small surface serve needs:
// the protocol writer to hand the stdio transport, and a restore that also
// reports a non-zero leak count. Keeping the wrapper here keeps serve.go free of
// the hijack's lifecycle details.
type hijackHandle struct {
	h *Hijack
}

// installHijack installs the ADR-011 stdout hijack, feeding leaked bytes to the
// bundle's mcpmock_stdout_leak_bytes_total counter. It returns a handle whose
// [hijackHandle.protoOut] is the captured real fd 1.
func installHijack(logger *slog.Logger, metrics *obs.Metrics) (*hijackHandle, error) {
	h, err := Install(logger, MetricsLeakCounter{Metrics: metrics})
	if err != nil {
		return nil, err
	}
	return &hijackHandle{h: h}, nil
}

// protoOut returns the captured real fd 1 to hand the stdio transport as its
// protocol writer.
func (hh *hijackHandle) protoOut() *os.File { return hh.h.ProtoOut() }

// restore reverses the hijack, restoring os.Stdout and joining the drainer. A
// non-zero leak count is reported to errOut so a corrupt run is visible; a
// clean run leaks nothing (MOCK-105.4). It is safe to call once via defer. The
// caller supplies the parent context so cancellation semantics are inherited
// (contextcheck); restore adds its own bounded deadline for the drain join.
func (hh *hijackHandle) restore(parent context.Context, errOut io.Writer) {
	ctx, cancel := context.WithTimeout(parent, hijackRestoreGrace)
	defer cancel()
	_ = hh.h.Restore(ctx)
	if n := hh.h.LeakBytes(); n > 0 {
		// os.Stdout has been restored, so this note goes to errOut (stderr).
		fmt.Fprintf(errOut, "mcpmock: WARNING: %d byte(s) leaked to stdout during stdio "+
			"serve (see stdout_leak log records)\n", n)
	}
}

// isValidationError reports whether err (a facade construction error) is a
// scenario validation failure, which the CLI maps to exit code 1 (MOCK-701).
// Any other error is an I/O/startup failure mapped to exit code 2.
func isValidationError(err error) bool {
	return errors.Is(err, mcpmock.ErrValidation)
}
