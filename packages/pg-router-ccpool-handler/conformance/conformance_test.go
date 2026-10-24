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
//
// Two request shapes are covered (docket pg2-12gr4, handler split). The NEW
// shape delivers the role as the `role` object ({name, config}) in the JSON
// request on stdin and runs against the two adapter binaries,
// pg-router-handler-command (dispatch) and pg-router-handler-ccpool (query);
// those tests carry no suffix. The OLD shape (today's path: the per-role JSON
// file named by --role-config, against the pg-router-ccpool-handler binary)
// keeps its own cases, suffixed OldPath, until the final cleanup packet
// (pg2-12gr4.10.17) removes the old binary and these cases with it.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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

// Names of the three binaries this suite builds, each the cmd/ directory of the
// same name in this module.
const (
	oldHandlerCmd     = "pg-router-ccpool-handler" // today's path; removed by pg2-12gr4.10.17
	commandAdapterCmd = "pg-router-handler-command"
	ccpoolAdapterCmd  = "pg-router-handler-ccpool"
)

// buildBinary compiles the REAL cmd/<cmd> main package (`go build -o <tmp>/<cmd>
// ./cmd/<cmd>`, run with the module root as the build's working directory) —
// the actual artifact operators run, not a package-level fake. Mirrors
// packages/pg-router/cmd/pg-router/e2e_test.go's own buildPgRouterBinary.
//
// The timeout is 5 minutes, not the more typical 120s (bead pg2-6pxs1):
// `nix flake check` observed this exact `go build` step killed at almost
// precisely the 120s mark on three independent, unrelated worktree checkouts
// while the machine was saturated by concurrent sessions/checks (load
// average 90-180). None of the landed diffs touched this package, so that
// was resource contention starving the build, not a real regression — a
// warm-cache build here normally finishes in ~1s. 5 minutes gives headroom
// under load while still failing fast on a genuine build break.
//
// Each binary is built ONCE per test process (pg2-3q2g6) and shared by every
// live test that names it: previously each caller ran its own `go build`, so
// under a saturated host the cold first compile and the later relinks each
// competed for CPU. All builds land in one temp dir, removed by TestMain.
func buildBinary(t *testing.T, cmdName string) string {
	t.Helper()
	bin, err := builds.build(cmdName)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return bin
}

// builds memoizes buildBinary's builds for the process, one entry per cmd/
// directory name.
var builds binaryBuilds

type binaryBuilds struct {
	mu      sync.Mutex
	dir     string
	results map[string]*buildResult
}

type buildResult struct {
	once sync.Once
	bin  string
	err  error
}

func (b *binaryBuilds) build(cmdName string) (string, error) {
	b.mu.Lock()
	if b.results == nil {
		b.results = map[string]*buildResult{}
	}
	r, ok := b.results[cmdName]
	if !ok {
		r = &buildResult{}
		b.results[cmdName] = r
	}
	b.mu.Unlock()

	r.once.Do(func() {
		moduleRoot, err := filepath.Abs("..")
		if err != nil {
			r.err = fmt.Errorf("resolve module root: %w", err)
			return
		}
		dir, err := b.buildDir()
		if err != nil {
			r.err = err
			return
		}
		bin := filepath.Join(dir, cmdName)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/"+cmdName)
		cmd.Dir = moduleRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			r.err = fmt.Errorf("go build ./cmd/%s: %w\n%s", cmdName, err, out)
			return
		}
		r.bin = bin
	})
	return r.bin, r.err
}

// buildDir returns the shared output dir, creating it on first use.
func (b *binaryBuilds) buildDir() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dir == "" {
		dir, err := os.MkdirTemp("", "pg-router-handler-conformance-")
		if err != nil {
			return "", fmt.Errorf("make build dir: %w", err)
		}
		b.dir = dir
	}
	return b.dir, nil
}

func TestMain(m *testing.M) {
	code := m.Run()
	if builds.dir != "" {
		_ = os.RemoveAll(builds.dir)
	}
	os.Exit(code)
}

// runBinary invokes bin's subcommand with request piped on stdin, returning
// stdout and the process's exit code. err is non-nil only when the process
// itself could not be started/awaited — an ExitError (nonzero exit) is
// reported through exitCode, matching conformance/driver's own
// CommandParticipant.Run contract (driver.go's doc comment).
func runBinary(t *testing.T, bin string, args []string, request []byte) (stdout []byte, exitCode int, err error) {
	t.Helper()
	stdout, _, exitCode, err = runBinaryStderr(t, bin, args, request)
	return stdout, exitCode, err
}

// runBinaryStderr is runBinary that also returns the process's stderr.
func runBinaryStderr(t *testing.T, bin string, args []string, request []byte) (stdout, stderr []byte, exitCode int, err error) {
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
		return out.Bytes(), errBuf.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(runErr, &exitErr); ok {
		return out.Bytes(), errBuf.Bytes(), exitErr.ExitCode(), nil
	}
	return out.Bytes(), errBuf.Bytes(), 0, runErr
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// commandRoleConfig is the [role.config] a command role carries on the new
// path, in the shape pg-router-handler-command decodes: the argv of a trivial,
// always-succeeding real subprocess.
func commandRoleConfig() map[string]any {
	return map[string]any{"command": map[string]any{"argv": []any{"true"}}}
}

// newShapeDispatchRequest builds a new-path handler.dispatch request: INTF-
// HANDLER's own golden request (never edited) plus the top-level role object
// {name, config} that a role with `handler` set delivers on stdin. The golden
// itself stays {schemaVersion, id, event}.
func newShapeDispatchRequest(name string, config map[string]any) map[string]any {
	req, err := conformance.Golden("handler.dispatch")
	if err != nil {
		panic(fmt.Sprintf("read handler.dispatch golden: %v", err))
	}
	req["role"] = map[string]any{"name": name, "config": config}
	return req
}

// liveDispatchNewShape runs pg-router-handler-command dispatch on req and
// returns the reply bytes, failing the test on any exit but ExitOK.
func liveDispatchNewShape(t *testing.T, req map[string]any) []byte {
	t.Helper()
	bin := buildBinary(t, commandAdapterCmd)
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	reply, code, err := runBinary(t, bin, []string{"dispatch"}, reqBytes)
	if err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("dispatch exit=%d, want %d; reply=%s", code, conformance.ExitOK, reply)
	}
	return reply
}

// TestLiveDispatch_command invokes pg-router-handler-command's `dispatch`
// subcommand with INTF-HANDLER's own golden handler.dispatch request plus the
// role object for a "command" role (name conformance-command, argv ["true"] —
// a trivial, always-succeeding real subprocess, so this proves the wire
// adapter end to end without needing a real ccpool/claude session) and checks
// the reply validates against handler.dispatch-reply. The role travels on
// stdin; there is no --role-config.
func TestLiveDispatch_command(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("`true` not on PATH; skipping live dispatch conformance check")
	}
	reply := liveDispatchNewShape(t, newShapeDispatchRequest("conformance-command", commandRoleConfig()))
	var v any
	if err := json.Unmarshal(reply, &v); err != nil {
		t.Fatalf("dispatch reply not JSON: %v; reply=%s", err, reply)
	}
	if err := conformance.Check("handler.dispatch-reply", v); err != nil {
		t.Fatalf("dispatch reply failed handler.dispatch-reply schema: %v; reply=%s", err, reply)
	}
}

// TestLiveDispatch_negative sends pg-router-handler-command malformed new-path
// dispatch requests and checks each is reported as a genuine failure — a
// nonzero exit with a diagnostic (an error reply on stdout or text on stderr) —
// rather than accepted: a request
// with no role object at all, and one whose role.config is not an object.
func TestLiveDispatch_negative(t *testing.T) {
	bin := buildBinary(t, commandAdapterCmd)

	noRole, err := conformance.Golden("handler.dispatch")
	if err != nil {
		t.Fatalf("read handler.dispatch golden: %v", err)
	}
	configNotObject := newShapeDispatchRequest("conformance-command", nil)
	configNotObject["role"].(map[string]any)["config"] = "not-an-object"

	for _, tc := range []struct {
		name string
		req  map[string]any
	}{
		{"no-role-object", noRole},
		{"role-config-not-an-object", configNotObject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqBytes, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			stdout, stderr, code, err := runBinaryStderr(t, bin, []string{"dispatch"}, reqBytes)
			if err != nil {
				t.Fatalf("run dispatch: %v", err)
			}
			if code == conformance.ExitOK {
				t.Fatalf("dispatch accepted a request that is malformed (%s); reply=%s", tc.name, stdout)
			}
			// The diagnostic is the INTF-HANDLER error reply on stdout
			// ({schemaVersion, error}); stderr MAY carry one too. Either way
			// a failure must say why.
			var reply struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(stdout, &reply)
			if reply.Error == "" && len(bytes.TrimSpace(stderr)) == 0 {
				t.Fatalf("dispatch exit=%d with no diagnostic (no error reply on stdout, empty stderr); stdout=%s", code, stdout)
			}
		})
	}
}

// TestLiveQuery invokes pg-router-handler-ccpool's `query` subcommand against
// INTF-SOURCE's own golden source.query request, with NO --query-config, and
// checks the reply validates against source.query-reply. The query subcommand
// is the INTF-SOURCE participant: it takes no role object and keeps today's
// request and reply. An unconfigured invocation always answers zero events
// (docket pg2-oju6w's Task 5.8 wired the beads-backed source in for real, but
// ONLY when --query-config names its filters — see query.go's own doc comment;
// this module's query_test.go covers that real path directly, against a fake
// beads.Runner), so this proves the wire adapter, not a real query result.
func TestLiveQuery(t *testing.T) {
	liveQuery(t, ccpoolAdapterCmd)
}

// liveQuery is TestLiveQuery's body for the binary built from cmd/<cmdName>.
func liveQuery(t *testing.T, cmdName string) {
	t.Helper()
	bin := buildBinary(t, cmdName)
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

// ---- Old path (today's): pg-router-ccpool-handler + --role-config. ----------
//
// These cases stay until packet cleanup-go (pg2-12gr4.10.17) removes the old
// binary; they keep proving the old path while roles still use it.

// TestLiveDispatch_commandOldPath is TestLiveDispatch_command on today's path:
// the old binary, the role in a per-role JSON file passed with --role-config.
func TestLiveDispatch_commandOldPath(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("`true` not on PATH; skipping live dispatch conformance check")
	}
	bin := buildBinary(t, oldHandlerCmd)
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

// TestLiveDispatch_negativeOldPath invokes the old binary's `dispatch` with a
// malformed request (the SAME "missing event" case driver.go's own
// negativeMatrix uses for handler.dispatch) and checks it reports a genuine
// failure — a nonzero exit — rather than accepting it.
func TestLiveDispatch_negativeOldPath(t *testing.T) {
	bin := buildBinary(t, oldHandlerCmd)
	req := []byte(`{"schemaVersion":"1","id":"h"}`) // missing "event"
	reply, code, err := runBinary(t, bin, []string{"dispatch"}, req)
	if err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
	if code == conformance.ExitOK {
		t.Fatalf("dispatch accepted a request missing \"event\"; reply=%s", reply)
	}
}

// TestLiveQueryOldPath is TestLiveQuery against the old binary, whose `query`
// subcommand is unchanged.
func TestLiveQueryOldPath(t *testing.T) {
	liveQuery(t, oldHandlerCmd)
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
