package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend is anything that can answer one scriptout wire request the same
// way a real Tier-2 backend binary does: given the raw JSON request bytes
// that would normally be written to its stdin, return the raw stdout
// bytes and the process's own exit code. Both a real compiled backend
// binary (ExecBackend, below) and an in-process DispatchTable double
// (TableBackend, below) implement it, so driver.Run's case set exercises
// either one identically — "a new backend implementation (real or fake)
// can be run against it" (bead pg2-7vgn5's acceptance criterion).
type Backend interface {
	Invoke(ctx context.Context, request []byte) (stdout []byte, exitCode int, err error)
}

// ExecBackend runs a real compiled Tier-2 backend binary, spawning one
// process per Invoke call — the exact invocation
// pkg/scriptout/exec.go's runInvoke performs against a real backend, with
// one difference: exec.go deliberately does not consult the process's own
// exit code (only the stdout JSON is its contract; see its package doc
// comment), whereas ExecBackend surfaces the exit code too, since
// asserting on it is exactly what this conformance suite exists to do.
type ExecBackend struct {
	// Binary is the backend executable's path (absolute, or resolved via
	// $PATH).
	Binary string
	// Args are extra command-line arguments passed to Binary. A real
	// Tier-2 backend binary takes none (it only ever reads stdin, matching
	// exec.go's own zero-arg invocation) — Args exists for a test double
	// built from a self-re-exec'd test binary, which needs e.g.
	// -test.run=... to target the right helper-process test function.
	Args []string
}

// Invoke implements Backend by spawning Binary, writing request to its
// stdin, and capturing stdout plus the real process exit code. err is
// non-nil only for a failure to spawn/communicate with the process at
// all (e.g. the binary does not exist) — a nonzero *backend* exit code is
// reported via exitCode, not err, so a caller can assert on it the same
// way for both branches of Backend.
func (b ExecBackend) Invoke(ctx context.Context, request []byte) (stdout []byte, exitCode int, err error) {
	if b.Binary == "" {
		return nil, 0, errors.New("conformance: ExecBackend.Binary is empty")
	}
	cmd := exec.CommandContext(ctx, b.Binary, b.Args...)
	cmd.Stdin = bytes.NewReader(request)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr == nil {
		return out.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	// Failed to even start/communicate with the process (binary missing,
	// permission denied, ...) — this is not a backend-reported exit code
	// at all, so it is surfaced as err rather than a synthetic exitCode.
	return out.Bytes(), -1, fmt.Errorf("conformance: exec %s: %w (stderr: %s)", b.Binary, runErr, bytes.TrimSpace(stderr.Bytes()))
}

// TableBackend runs an in-process scriptout.DispatchTable double via
// scriptout.ServeOne — no subprocess, no compiled binary required. This
// is what lets a backend author's own unit tests (which typically build a
// DispatchTable and a handful of fake handlers) run the SAME conformance
// suite ExecBackend runs against a real binary, closing exactly the gap
// the design doc's Appendix A named: nothing structurally prevented a
// backend's unit tests from all passing against a fake shape no real
// backend implements, because nothing independent of the backend's own
// tests ever checked that fake shape against a canonical spec.
type TableBackend struct {
	Table scriptout.DispatchTable
}

// Invoke implements Backend by running Table through scriptout.ServeOne
// against in-memory buffers. It never returns a non-nil err: ServeOne
// always produces some exit code and (best-effort) some stdout bytes,
// mirroring how a real backend process always exits with SOME code even
// when it produced no valid output.
func (b TableBackend) Invoke(_ context.Context, request []byte) (stdout []byte, exitCode int, err error) {
	var out bytes.Buffer
	code := scriptout.ServeOne(b.Table, bytes.NewReader(request), &out)
	return out.Bytes(), code, nil
}
