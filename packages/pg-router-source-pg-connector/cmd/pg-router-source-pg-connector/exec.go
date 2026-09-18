// exec.go: this adapter's own shared subprocess-invocation and outcome-
// classification helpers, used by every one of the three verbs
// (changes.go, sweep.go, list.go).
//
// pg-connector is execed as a subprocess (ambient $PATH binary, never a Go
// import) exactly as pg-connector's own registry.go execs its Tier-2
// backends by ambient binary name — this file is the same "one-shot exec,
// capture stdout/stderr separately, classify by exit code" shape as
// packages/pg-connector/pkg/scriptout/exec.go's runInvoke, adapted one
// layer up: that file classifies a Tier-2 BACKEND's 0/1 wire-protocol exit
// code; this file classifies pg-connector's OWN CLI exit code (0/1/2/3)
// into this adapter's distinct 0/1 scheme [design: section 6.1].
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// pgConnectorBinary is the ambient $PATH name this adapter execs. Never a
// compile-time import of packages/pg-connector [design: section 6.1].
const pgConnectorBinary = "pg-connector"

// execCmdFactory constructs the *exec.Cmd used to invoke pg-connector.
// Production code uses exec.CommandContext; tests swap this to spawn a
// reentrant test-helper process — the "stubbed exec" half of this
// packet's own "fake pg-connector binary or a stubbed exec" wire-double
// requirement, mirroring packages/pg-connector's own
// pkg/scriptout/exec.go execCmdFactory pattern exactly.
var execCmdFactory = exec.CommandContext

// pgConnectorResult is one subprocess invocation's raw, unclassified
// outcome: pg-connector's own captured stdout/stderr (never interleaved,
// so a caller can honor "print nothing [own stdout] on total failure;
// copy pg-connector's own stderr" without re-parsing a combined stream)
// plus its exit code.
type pgConnectorResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// runPgConnector execs pg-connector with args, inheriting this process's
// own environment unchanged plus any extraEnv entries appended (this
// packet's own --beads-dir -> PG_CONNECTOR_ISSUE_BEADS_DIR rule; see
// beadsDirEnv) [design: section 6.1]. The returned error is non-nil only
// when the child could never even be started (binary missing, permission
// denied, context already canceled, ...) — there is then no exit code to
// classify and no child stderr to trust.
func runPgConnector(ctx context.Context, args []string, extraEnv []string) (pgConnectorResult, error) {
	cmd := execCmdFactory(ctx, pgConnectorBinary, args...)
	if len(extraEnv) > 0 {
		// APPEND to whatever cmd.Env the factory already set, rather than
		// overwriting it: in production execCmdFactory is
		// exec.CommandContext, which leaves cmd.Env nil (meaning "inherit
		// os.Environ() implicitly"), so base resolves to os.Environ()
		// itself; in tests execCmdFactory is swapped to spawn a reentrant
		// helper process that has ALREADY populated cmd.Env with the
		// identity vars (GO_WANT_HELPER_PROCESS/GO_HELPER_BEHAVIOR) it
		// needs to recognize itself as the helper on re-exec — clobbering
		// that here would silently break every test that also exercises
		// --beads-dir.
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(append([]string{}, base...), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	res := pgConnectorResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}

	if runErr == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.exitCode = exitErr.ExitCode()
		return res, nil
	}
	return pgConnectorResult{}, fmt.Errorf("pg-connector: exec %v: %w", args, runErr)
}

// classifyExit maps pg-connector's own CLI exit-code taxonomy (its
// --help's documented Fan-out 0/2/3 and Targeted 0/1/4 schemes) onto this
// adapter's own distinct 2-value scheme [design: section 6.1, this
// packet's own "Outcome classification" Contract bullet]: exit 0 or 2 ->
// ok=true (the caller parses stdout and prints items, folding any
// degraded backend into the printed items' own metadata); exit 1 or 3 ->
// ok=false (print nothing on this adapter's own stdout; the caller
// copies pg-connector's own captured stderr to this adapter's stderr for
// diagnosis instead). pg-connector's exit 4 (not_found) is reserved for
// TARGETED ops resolving to exactly one backend (e.g. "pr show") and never
// appears on the fan-out "changes"/"list" verbs this adapter invokes, so
// this classification does not need a case for it; any exit code besides
// 0/1/2/3 is conservatively classified as a failure, the same side
// pg-connector's own exit 1 ("any other error") falls on.
func classifyExit(exitCode int) bool {
	switch exitCode {
	case 0, 2:
		return true
	default:
		return false
	}
}

// invokeOrFail runs pg-connector with args/extraEnv and applies
// classifyExit to its outcome, honoring this adapter's own contract on
// the failure path: nothing further is written by the CALLER on this
// adapter's own stdout, and pg-connector's own captured stderr (verbatim,
// if any) is copied to stderr before returning the shared errFailed
// sentinel — so every one of the three verbs shares one implementation
// of "MUST NOT propagate a pg-connector CLI exit code as its own"
// [design: section 6.1].
//
// On this same failure path, pg-connector's own real degraded/failed
// reason (sources[].reason) is ALSO forwarded to stderr, decoded from
// pg-connector's own stdout (bead pg2-wa5uk): pg-connector's own
// writeFanOutResult writes its wire body to stdout before ever checking
// the fan-out's exit code, so a "total failure" outcome (the exact
// observed case: a single degraded backend, rate-limited, no other
// healthy source) still carries the real cause on stdout even though the
// call as a whole failed — but pg-connector's own stderr is ALWAYS empty
// for that outcome by design (its own exitError doc comment: "carries a
// specific exit code without printing anything to stderr, since the JSON
// body on stdout is the reported outcome"). Without this, the failure
// path above forwarded nothing at all for that case, and every caller
// downstream (including pg-router's own producer-tick WARN log) saw only
// a bare "exit status N".
func invokeOrFail(ctx context.Context, stderr io.Writer, args []string, extraEnv []string) ([]byte, error) {
	res, err := runPgConnector(ctx, args, extraEnv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, errFailed
	}
	if !classifyExit(res.exitCode) {
		if len(res.stderr) > 0 {
			_, _ = stderr.Write(res.stderr)
		}
		if reasons := failureReasons(res.stdout); len(reasons) > 0 {
			fmt.Fprintln(stderr, strings.Join(reasons, "; "))
		}
		return nil, errFailed
	}
	return res.stdout, nil
}

// pgConnectorSourceRow is the minimal subset of a fan-out wire response's
// sources[] row this adapter needs to recover a degraded/failed
// backend's own reason text on the failure path (invokeOrFail above).
// The row's own identifying field is spelled "backend" in pg-connector's
// "changes" wire (changesSourceRow, changes.go) and "source" in every
// other fan-out verb's own SourceResult-shaped wire ("list", which
// list.go/sweep.go both invoke) — both are decoded here so this one
// extraction works across every verb this adapter invokes, without
// needing to know which shape a given call used [bead pg2-wa5uk].
type pgConnectorSourceRow struct {
	Backend string `json:"backend"`
	Source  string `json:"source"`
	Reason  string `json:"reason"`
}

// pgConnectorSourcesEnvelope is the minimal subset of pg-connector's own
// stdout JSON this adapter needs on its own failure path: just the
// sources[] array, decoded generically since invokeOrFail is shared by
// every verb and a Tier-1 CLI-level failure (no backend ever dispatched)
// carries no sources[] at all — that case simply yields zero rows below,
// not a decode error [bead pg2-wa5uk].
type pgConnectorSourcesEnvelope struct {
	Sources []pgConnectorSourceRow `json:"sources"`
}

// failureReasons extracts one "<backend>: <reason>" string per sources[]
// row that carries a non-empty reason, from pg-connector's own stdout on
// invokeOrFail's failure path. best-effort: a malformed/non-JSON stdout
// body (never actually emitted by pg-connector today, but not this
// adapter's job to assume) yields no reasons rather than an error, so a
// genuinely different failure mode never masks itself as "no reason
// available" [bead pg2-wa5uk].
func failureReasons(stdout []byte) []string {
	var env pgConnectorSourcesEnvelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		return nil
	}
	reasons := make([]string, 0, len(env.Sources))
	for _, s := range env.Sources {
		if s.Reason == "" {
			continue
		}
		backend := s.Backend
		if backend == "" {
			backend = s.Source
		}
		if backend == "" {
			reasons = append(reasons, s.Reason)
			continue
		}
		reasons = append(reasons, backend+": "+s.Reason)
	}
	return reasons
}

// beadsDirEnv builds the extraEnv slice runPgConnector appends when
// --beads-dir is given: PG_CONNECTOR_ISSUE_BEADS_DIR=<path>, and nothing
// at all when beadsDir is empty (never PG_CONNECTOR_ISSUE_BEADS_DIR="") —
// so the child falls back to whatever BEADS_DIR it inherited when
// --beads-dir was never passed [design: section 6.1, this packet's own
// Contract "PG_CONNECTOR_ISSUE_BEADS_DIR" bullet].
func beadsDirEnv(beadsDir string) []string {
	if beadsDir == "" {
		return nil
	}
	return []string{"PG_CONNECTOR_ISSUE_BEADS_DIR=" + beadsDir}
}
