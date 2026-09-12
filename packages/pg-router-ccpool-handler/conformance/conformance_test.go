// Package conformance is this module's own conformance-suite invocation
// (docket pg2-oju6w Task 5.2/5.3's "Test:" file, folded per the operator's
// 2026-09-11 decision): it proves packages/pg-router-ccpool-handler's real,
// compiled binary — not a package-level fake — passes INTF-HANDLER's
// positive+negative checks, per ADR 0065's Acceptance section ("the handler
// module imports the conformance package's driver and passes the
// INTF-HANDLER invoking check against itself").
//
// Two layers: the STATIC schema/golden checks (packages/pg-router/conformance/
// driver.Run, which iterates every message type regardless of Target — dispatch
// and query included — via golden fixtures and the negative matrix, no live
// process needed), and a LIVE layer that execs the real compiled binary for
// `dispatch` and `query`, feeding it the exact golden requests over stdin/stdout
// and checking the reply against its own reply schema — the one thing
// driver.Run cannot do today (driver.Target carries no Handler slot, only
// MonRead/Query/Command; see driver.go's own doc comment). The live `dispatch`
// case uses a "command" role (argv ["true"]) rather than a real ccpool
// session — this module's own executor tests already cover ccpool's
// business logic end to end against fakes (internal/executor/ccpool_test.go);
// this suite's job is proving the WIRE ADAPTER around it, not re-proving
// ccpool's behavior.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/conformance/driver"
)

// TestStaticSchemaChecks runs driver.Run's golden/negative-matrix suite —
// the same static checks INV-INTF-2 requires of every implementer, applied
// here to this module's own INTF-HANDLER/INTF-SOURCE message types
// (handler.dispatch{,-reply}, source.query{,-reply}, and every other
// registered schema pg-router's own suite already covers). No live
// participant is needed for this layer (driver.Target{} is enough); the
// live binary is exercised separately below.
func TestStaticSchemaChecks(t *testing.T) {
	results := driver.Run(context.Background(), driver.Target{})
	for _, r := range results {
		if r.Skipped || r.Busy {
			continue
		}
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}

// buildHandlerBinary compiles the REAL cmd/pg-router-ccpool-handler main
// package (`go build -o <tmp> ./cmd/pg-router-ccpool-handler`, run with the
// module root as the build's working directory) — the actual artifact
// operators run, not a package-level fake. Mirrors
// packages/pg-router/cmd/pg-router/e2e_test.go's own buildPgRouterBinary.
func buildHandlerBinary(t *testing.T) string {
	t.Helper()
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "pg-router-ccpool-handler")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/pg-router-ccpool-handler")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/pg-router-ccpool-handler: %v\n%s", err, out)
	}
	return bin
}

// runBinary invokes bin's subcommand with request piped on stdin, returning
// stdout and the process's exit code. err is non-nil only when the process
// itself could not be started/awaited — an ExitError (nonzero exit) is
// reported through exitCode, matching conformance/driver's own
// CommandParticipant.Run contract (driver.go's doc comment).
func runBinary(t *testing.T, bin string, args []string, request []byte) (stdout []byte, exitCode int, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(request)
	var out bytes.Buffer
	cmd.Stdout = &out
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr == nil {
		return out.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(runErr, &exitErr); ok {
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	return out.Bytes(), 0, runErr
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// TestLiveDispatch_command invokes the real binary's `dispatch` subcommand
// against INTF-HANDLER's own golden handler.dispatch request (a "command"
// role, argv ["true"] — a trivial, always-succeeding real subprocess, so
// this proves the wire adapter end to end without needing a real ccpool/
// claude session) and checks the reply validates against
// handler.dispatch-reply.
func TestLiveDispatch_command(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("`true` not on PATH; skipping live dispatch conformance check")
	}
	bin := buildHandlerBinary(t)
	roleConfig := writeRoleConfig(t, `{"name":"conformance-command","type":"command","command":{"argv":["true"]}}`)

	req, err := conformance.Golden("handler.dispatch")
	if err != nil {
		t.Fatalf("read handler.dispatch golden: %v", err)
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	reply, code, err := runBinary(t, bin, []string{"dispatch", "--role-config", roleConfig}, reqBytes)
	if err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("dispatch exit=%d, want %d; reply=%s", code, conformance.ExitOK, reply)
	}
	var v any
	if err := json.Unmarshal(reply, &v); err != nil {
		t.Fatalf("dispatch reply not JSON: %v; reply=%s", err, reply)
	}
	if err := conformance.Check("handler.dispatch-reply", v); err != nil {
		t.Fatalf("dispatch reply failed handler.dispatch-reply schema: %v; reply=%s", err, reply)
	}
}

// TestLiveDispatch_negative invokes `dispatch` with a malformed request (the
// SAME "missing event" case driver.go's own negativeMatrix uses for
// handler.dispatch) and checks the binary reports it as a genuine failure —
// a nonzero exit — rather than accepting it.
func TestLiveDispatch_negative(t *testing.T) {
	bin := buildHandlerBinary(t)
	req := []byte(`{"schemaVersion":"1","id":"h"}`) // missing "event"
	reply, code, err := runBinary(t, bin, []string{"dispatch"}, req)
	if err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
	if code == conformance.ExitOK {
		t.Fatalf("dispatch accepted a request missing \"event\"; reply=%s", reply)
	}
}

// TestLiveQuery invokes the real binary's `query` subcommand against
// INTF-SOURCE's own golden source.query request and checks the reply
// validates against source.query-reply. This module's `query` always
// answers zero events today (docket pg2-oju6w's Task 5.8 wires the built-in
// beads source for real — query.go's own doc comment), so this proves the
// wire adapter, not a real query result.
func TestLiveQuery(t *testing.T) {
	bin := buildHandlerBinary(t)
	req, err := conformance.Golden("source.query")
	if err != nil {
		t.Fatalf("read source.query golden: %v", err)
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	reply, code, err := runBinary(t, bin, []string{"query"}, reqBytes)
	if err != nil {
		t.Fatalf("run query: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("query exit=%d, want %d; reply=%s", code, conformance.ExitOK, reply)
	}
	var v any
	if err := json.Unmarshal(reply, &v); err != nil {
		t.Fatalf("query reply not JSON: %v; reply=%s", err, reply)
	}
	if err := conformance.Check("source.query-reply", v); err != nil {
		t.Fatalf("query reply failed source.query-reply schema: %v; reply=%s", err, reply)
	}
}

// writeRoleConfig writes contents to a fresh temp file and returns its path.
func writeRoleConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "role.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write role config: %v", err)
	}
	return path
}
