// ccpoolexec.go: this probe's own subprocess wrapper around ccpool's
// read-only `ccpool list` verb, scoped to pg-router-ccpool-handler's own
// session-metadata namespace via --filter pgrouter.pool=pg-router
// [design: Contract's "ccpool's own list CLI" bullet;
// packages/pg-router-ccpool-handler/internal/ccpool/meta.go for the
// pgrouter.pool key/PoolName value]. Never a compile-time import of
// packages/ccpool — this binary execs the ambient-$PATH `ccpool` binary,
// same "one-shot exec, capture stdout/stderr separately, classify by exit
// code" shape as connector.go's pg-connector wrapper (itself adapted from
// packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector/exec.go).
//
// The exact flag spelling here (--all --filter k=v --json, plus an
// optional --state) is confirmed against
// packages/ccpool/cmd/ccpool/contract_test.go and
// packages/ccpool/cmd/ccpool/list.go directly, per this packet's own
// Contract note that the design's illustrative -filter/-state (single-
// dash) spelling does not match this repo's real CLI.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// ccpoolBinary is the ambient $PATH name this probe execs.
const ccpoolBinary = "ccpool"

// pgRouterPoolFilter scopes every `ccpool list` call below to sessions
// pg-router-ccpool-handler dispatched [design: "ccpool-probe run checks"
// item 1, its parenthetical: "no new tagging scheme is needed"].
const pgRouterPoolFilter = "pgrouter.pool=pg-router"

// ccpoolExecCmdFactory constructs the *exec.Cmd used to invoke ccpool.
// Production code uses exec.CommandContext; tests swap this to spawn a
// reentrant test-helper process (testmain_test.go), mirroring connector.go's
// own execCmdFactory pattern — kept as a SEPARATE variable from
// execCmdFactory (connector.go) so a test can independently fake ccpool's
// and pg-connector's own behavior in the same run.
var ccpoolExecCmdFactory = exec.CommandContext

type ccpoolResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// runCcpool execs ccpool with args, inheriting this process's environment
// verbatim. The returned error is non-nil only when the child could never
// even be started.
func runCcpool(ctx context.Context, args []string) (ccpoolResult, error) {
	cmd := ccpoolExecCmdFactory(ctx, ccpoolBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	res := ccpoolResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}

	if runErr == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.exitCode = exitErr.ExitCode()
		return res, nil
	}
	return ccpoolResult{}, fmt.Errorf("ccpool: exec %v: %w", args, runErr)
}

var errCcpoolFailed = errors.New("ccpool-probe: ccpool call failed")

// ccpoolSessionRow is the subset of ccpool's own `list --json` listJSON
// shape (packages/ccpool/cmd/ccpool/list.go) this probe needs. Decoded
// generically since this probe has no compile-time dependency on
// packages/ccpool — extra fields on the real wire shape are ignored by
// encoding/json.
type ccpoolSessionRow struct {
	ExternalID string            `json:"external_id"`
	Name       string            `json:"name"`
	State      string            `json:"state"`
	CWD        string            `json:"cwd"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// listCcpoolSessions runs `ccpool list --all --filter pgrouter.pool=pg-router
// --json`, optionally narrowed by --state, and decodes the JSON array of
// rows. --all is required so a hidden-by-retention (but still real) row is
// not silently dropped from either check's own view [design: Contract's
// "ccpool's own list CLI" bullet].
func listCcpoolSessions(ctx context.Context, state string, warn func(string)) ([]ccpoolSessionRow, error) {
	args := []string{"list", "--all", "--filter", pgRouterPoolFilter, "--json"}
	if state != "" {
		args = append(args, "--state", state)
	}
	res, err := runCcpool(ctx, args)
	if err != nil {
		warn(err.Error())
		return nil, errCcpoolFailed
	}
	if res.exitCode != 0 {
		if len(res.stderr) > 0 {
			warn(string(res.stderr))
		} else {
			warn(fmt.Sprintf("ccpool: exit %d", res.exitCode))
		}
		return nil, errCcpoolFailed
	}
	var rows []ccpoolSessionRow
	if err := json.Unmarshal(res.stdout, &rows); err != nil {
		return nil, fmt.Errorf("ccpool-probe: decode ccpool list response: %w", err)
	}
	return rows, nil
}
