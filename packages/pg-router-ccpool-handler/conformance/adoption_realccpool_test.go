package conformance

// Adoption across the handler split's cutover, against REAL ccpool and REAL
// tmux (global rule V-3: a mock of either does not count; docket pg2-12gr4,
// ADR 0085's Addendum, docs/runbooks/handler-split-consumer-checklist.md row 7
// and step 5 of "Moving one role").
//
// Sessions outlive a daemon restart (ADR 0085): when the daemon restarts, the
// dispatch that started a session dies with it and the session keeps running.
// When a role family moves to the new adapter (pg-router-handler-ccpool, role
// object on stdin) the redelivered dispatch MUST find that session and launch
// nothing beside it; and when the move is reverted, the old handler
// (pg-router-ccpool-handler, --role-config) MUST do the same for a session the
// new adapter started. Each test starts a session through one binary, kills that
// dispatch the way a restart does (SIGKILL of the whole process group; the
// session lives on in its own tmux server), redelivers the SAME event to the
// other binary and asserts, from the pool's own listing and the handler's event
// log, that the session was adopted: still exactly one session for the bead,
// the same external id, route "adopted" and not "launched", and a supervision
// lease the adopting dispatch refreshed.
//
// No daemon runs: both binaries are driven directly with the golden-shaped
// dispatch request. bd is a small script first on PATH (the nix check's sandbox
// has no bd and needs no dolt); ccpool and tmux are real, in a sandbox pool and
// tmux socket under a scratch directory, with the fake-claude stub as claude.
// The sandbox mirrors internal/executor's prior_sessions_realccpool_test.go (its
// harness is in another package's _test.go, so it cannot be imported): HOME,
// XDG_*, CCPOOL_REGISTRY_DIR and TMUX_TMPDIR all point into the scratch
// directory, the inherited CCPOOL_*, BEADS_*, OTEL_* and TMUX* variables are
// cleared, and every tmux server under the scratch directory is killed at the
// end. Nothing of the operator's pools, registry or tmux is touched.
//
// The tests need ccpool and tmux on PATH. Without them they SKIP, unless
// PG_ROUTER_CCPOOL_HANDLER_REQUIRE_CCPOOL is set (the nix check
// checks.<system>.pg-router-ccpool-handler-go-tests sets it), which makes the
// absence a failure, so a green check means they ran.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router/conformance"
)

const (
	// requireRealCCPoolEnv turns a missing ccpool or tmux from a skip into a
	// failure (the same variable internal/executor's real-ccpool tests honor).
	requireRealCCPoolEnv = "PG_ROUTER_CCPOOL_HANDLER_REQUIRE_CCPOOL"

	adoptBead  = "adopt-bead-1"
	adoptRole  = "adopt-role"
	adoptEvent = "evt-adopt-1"
	adoptActor = "adopt-actor"

	// adoptLeaseTTL is the supervision-lease TTL the sandbox's launch config
	// sets (config.Config.LeaseTTL; internal/executor/lease.go refreshes the
	// lease to now+LeaseTTL every poll). It is far under the initial lease a
	// launch stamps (launch + ccpool.EnsureTimeout + LeaseTTL), so a lease at
	// most this far ahead proves an adopting dispatch refreshed it.
	adoptLeaseTTL = 30 * time.Second
	// adoptWait bounds every wait on a real session or handler process.
	adoptWait = 90 * time.Second
)

// adoptFakeClaude stands in for claude inside a real ccpool session: it fires
// ccpool's SessionStart hook (the session reaches ready), then reads its input;
// /exit (ccpool's close) ends it, and every other line (the dispatch's prompt)
// is recorded in the transcript and leaves the turn running (ccpool's reply
// already marked it working). Same stub as internal/executor's real-ccpool
// tests, reduced to what these tests use.
const adoptFakeClaude = `#!/bin/sh
uuid=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) uuid="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cc="${CCPOOL_BIN:-ccpool}"
transcript="${FAKE_CLAUDE_DIR:-/tmp}/fake-claude-$uuid.jsonl"
: > "$transcript"
payload() { printf '{"session_id":"%s","transcript_path":"%s","cwd":"%s","hook_event_name":"%s"}' "$uuid" "$transcript" "$PWD" "$1"; }
payload SessionStart | "$cc" hook start >/dev/null 2>&1
while IFS= read -r line; do
  case "$line" in
    */exit*) exit 0 ;;
    *) printf '{"type":"user","message":{"role":"user","content":"prompt"}}\n' >> "$transcript" ;;
  esac
done
`

// adoptFakeBD is the bd the handlers call: every call is logged, show answers
// the bead's status from a file the test writes (in_progress until it writes
// closed), and every other call succeeds silently. The handler passes
// --actor <role actor> first, which the script drops.
const adoptFakeBD = `#!/bin/sh
echo "$*" >> "$FAKE_BD_DIR/calls.log"
while [ "$1" = "--actor" ]; do shift 2; done
case "$1" in
  show)
    st=$(cat "$FAKE_BD_DIR/status" 2>/dev/null || echo in_progress)
    printf '{"id":"%s","status":"%s","assignee":"` + adoptActor + `","labels":[],"comment_count":0}\n' "$2" "$st"
    ;;
esac
exit 0
`

// adoptSandbox is the hermetic environment of one test: a scratch directory
// holding the pool, the tmux socket directory, the fake bd, the handler state
// and the launch config.
type adoptSandbox struct {
	t        *testing.T
	root     string // short, so tmux socket paths fit
	tmuxDir  string
	poolDir  string
	bdDir    string // FAKE_BD_DIR: status file and call log
	fakeBin  string // directory first on the handlers' PATH, holding bd
	stateDir string // the handlers' state directory: events.jsonl, session locks
	cfgPath  string // the launch config JSON both binaries read
	cc       *ccpool.CLIRunner
}

// shortTempRoot returns a temp base short enough that a tmux socket under it
// stays inside the unix socket path limit (104 bytes on macOS, whose default
// $TMPDIR is already about 50).
func shortTempRoot() string {
	if base := os.TempDir(); len(base) <= 40 {
		return base
	}
	return "/tmp"
}

func newAdoptSandbox(t *testing.T) *adoptSandbox {
	t.Helper()
	ccpoolBin, err1 := exec.LookPath("ccpool")
	_, err2 := exec.LookPath("tmux")
	if err1 != nil || err2 != nil {
		msg := fmt.Sprintf("real ccpool tests need ccpool and tmux on PATH (ccpool: %v, tmux: %v)", err1, err2)
		if os.Getenv(requireRealCCPoolEnv) != "" {
			t.Fatal(msg + "; " + requireRealCCPoolEnv + " is set, so this is a failure, not a skip")
		}
		t.Skip(msg)
	}
	root, err := os.MkdirTemp(shortTempRoot(), "adopt-")
	if err != nil {
		t.Fatal(err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	sb := &adoptSandbox{
		t: t, root: root,
		tmuxDir:  filepath.Join(root, "tmux"),
		poolDir:  filepath.Join(root, "pool"),
		bdDir:    filepath.Join(root, "bd"),
		fakeBin:  filepath.Join(root, "fakebin"),
		stateDir: filepath.Join(root, "state-handler"),
		cfgPath:  filepath.Join(root, "launch-config.json"),
	}
	// Registered before any t.Setenv below, so it runs after the environment is
	// restored (cleanups are LIFO) and after every handler process is killed; it
	// addresses the sandbox explicitly, not through the environment.
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Cleanup(sb.killTmux)

	// Clear every inherited variable that could aim a ccpool, tmux or bd call at
	// something live, or export telemetry.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CCPOOL_") || strings.HasPrefix(k, "BEADS_") || strings.HasPrefix(k, "OTEL_") || k == "TMUX" || k == "TMUX_PANE" {
			t.Setenv(k, "") // registers the restore
			_ = os.Unsetenv(k)
		}
	}
	for k, sub := range map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "cfg", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state",
		"XDG_RUNTIME_DIR": "run", "CCPOOL_REGISTRY_DIR": "reg", "TMUX_TMPDIR": "tmux",
		"FAKE_CLAUDE_DIR": "transcripts", "FAKE_BD_DIR": "bd",
	} {
		d := filepath.Join(root, sub)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, d)
	}
	t.Setenv("CCPOOL_BIN", ccpoolBin)
	t.Setenv("CCPOOL_POOL", sb.poolDir)
	for _, d := range []string{sb.poolDir, sb.fakeBin, sb.stateDir, filepath.Join(root, "work"), filepath.Join(root, "wt"), filepath.Join(root, "plugin")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fakeClaude := filepath.Join(root, "fake-claude")
	for path, body := range map[string]string{fakeClaude: adoptFakeClaude, filepath.Join(sb.fakeBin, "bd"): adoptFakeBD} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	poolCfg := fmt.Sprintf(`[pool]
max_sessions = 50
auto_reap = false
[claude]
bin = %q
plugin_dir = %q
[wait]
timeout = "8s"
[notify]
adapter = "none"
[usage_gate]
enabled = false
[retry]
enabled = false
`, fakeClaude, filepath.Join(root, "plugin"))
	if err := os.WriteFile(filepath.Join(sb.poolDir, "config.toml"), []byte(poolCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// The handler-wide launch config both binaries read through --config (the
	// durations are nanosecond integers on the wire). Unset fields keep
	// config.Default().
	launchCfg := map[string]any{
		"RepoRoot":            filepath.Join(root, "work"),
		"WorktreeDir":         filepath.Join(root, "wt"),
		"PollInterval":        int64(50 * time.Millisecond),
		"LeaseTTL":            int64(adoptLeaseTTL),
		"WorktreeQuietWindow": int64(100 * time.Millisecond),
		"ConfirmIngest":       0, // the fake never starts a real model turn
		"OriginProbe":         map[string]any{"stateDir": sb.stateDir},
	}
	raw, err := json.Marshal(launchCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sb.cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sb.cc = ccpool.NewCLIRunnerForPool(config.Default(), sb.poolDir)
	return sb
}

// killTmux kills every tmux server whose socket lives in the sandbox.
func (sb *adoptSandbox) killTmux() {
	socks, _ := filepath.Glob(filepath.Join(sb.tmuxDir, "tmux-*", "*"))
	for _, s := range socks {
		_ = exec.Command("tmux", "-S", s, "kill-server").Run()
	}
}

// setBeadStatus writes the status the fake bd answers `show` with.
func (sb *adoptSandbox) setBeadStatus(status string) {
	sb.t.Helper()
	if err := os.WriteFile(filepath.Join(sb.bdDir, "status"), []byte(status), 0o600); err != nil {
		sb.t.Fatal(err)
	}
}

// beadSessions returns every session row of the sandbox pool that records the
// test's bead, live or not.
func (sb *adoptSandbox) beadSessions() []ccpool.Session {
	sb.t.Helper()
	all, err := sb.cc.List(context.Background())
	if err != nil {
		sb.t.Fatalf("ccpool list: %v", err)
	}
	var out []ccpool.Session
	for _, s := range all {
		if s.Meta[ccpool.MetaKeyBead] == adoptBead {
			out = append(out, s)
		}
	}
	return out
}

// eventRecords returns the handlers' event-log records of the given kind.
func (sb *adoptSandbox) eventRecords(kind string) []map[string]any {
	sb.t.Helper()
	f, err := os.Open(filepath.Join(sb.stateDir, "events.jsonl"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec["kind"] == kind {
			out = append(out, rec)
		}
	}
	return out
}

// handlerProc is one running dispatch process, in a process group of its own so
// a kill takes its children (bd, ccpool) with it, as a restart's group kill
// would.
type handlerProc struct {
	name string
	cmd  *exec.Cmd
	out  bytes.Buffer // stdout: the reply; read only once done is closed
	err  bytes.Buffer // stderr; read only once done is closed
	done chan struct{}
}

// start runs bin with args, the request on stdin, and the fake bd first on PATH.
func (sb *adoptSandbox) start(name, bin string, args []string, request []byte) *handlerProc {
	sb.t.Helper()
	p := &handlerProc{name: name, done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = append(os.Environ(), "PATH="+sb.fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	p.cmd.Stdin = bytes.NewReader(request)
	p.cmd.Stdout, p.cmd.Stderr = &p.out, &p.err
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := p.cmd.Start(); err != nil {
		sb.t.Fatalf("start %s: %v", name, err)
	}
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()
	sb.t.Cleanup(func() { p.kill() })
	return p
}

// kill ends the process group with SIGKILL and waits for the process to be reaped.
func (p *handlerProc) kill() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
	}
}

func (p *handlerProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// report is the process's stdout and stderr, for a failure message; call it only
// once the process has exited.
func (p *handlerProc) report() string {
	return fmt.Sprintf("%s exited %d\nstdout: %s\nstderr: %s", p.name, p.cmd.ProcessState.ExitCode(), p.out.String(), p.err.String())
}

// waitFor polls cond every 50 ms until it holds, failing the test with what
// after timeout, or at once (with the process's output) when p exits first.
func (sb *adoptSandbox) waitFor(what string, p *handlerProc, cond func() bool) {
	sb.t.Helper()
	deadline := time.Now().Add(adoptWait)
	for !cond() {
		if p != nil && p.exited() {
			if cond() { // the process may have finished and left the state we wait for
				return
			}
			sb.t.Fatalf("%s: %s ended before %s", p.name, p.report(), what)
		}
		if time.Now().After(deadline) {
			sb.t.Fatalf("timed out after %s waiting for %s; sessions: %+v", adoptWait, what, sb.beadSessions())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// adoptCCPoolSettings are the ccpool role settings both shapes carry (the same
// field names in the old per-role file and the new [role.config]); isolation
// "none" runs the session in the repo root, so no worktree is made.
func adoptCCPoolSettings() map[string]any {
	return map[string]any{
		"actor":          adoptActor,
		"promptBody":     "Work bead {{.BeadID}}",
		"completion":     "close-only",
		"onFailure":      "unclaim",
		"onDispatchFail": "unclaim",
		"isolation":      map[string]any{"type": "none"},
	}
}

// adoptRequest is the golden handler.dispatch body with the event filled the
// way the old dispatch reads it (the item's fields at the payload's top
// level), for the one bead and event both dispatches use. role, when non-nil,
// is the new shape's role object.
func adoptRequest(t *testing.T, role map[string]any) []byte {
	t.Helper()
	req, err := conformance.Golden("handler.dispatch")
	if err != nil {
		t.Fatalf("read handler.dispatch golden: %v", err)
	}
	ev := req["event"].(map[string]any)
	ev["id"] = adoptEvent
	ev["type"] = "adopt.ready"
	ev["payload"] = map[string]any{"id": adoptBead, "type": "task", "title": "adoption test bead"}
	if role != nil {
		req["role"] = role
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return b
}

// handlerShape is one of the two ways a dispatch is delivered: today's
// pg-router-ccpool-handler with the per-role JSON named by --role-config, or the
// session adapter pg-router-handler-ccpool with the role object on stdin.
type handlerShape struct {
	name string
	// start runs one dispatch of the test's bead, role and event.
	start func(t *testing.T, sb *adoptSandbox) *handlerProc
}

func oldShape() handlerShape {
	return handlerShape{name: "old handler (pg-router-ccpool-handler dispatch --role-config)", start: func(t *testing.T, sb *adoptSandbox) *handlerProc {
		bin := buildBinary(t, oldHandlerCmd)
		roleFile := filepath.Join(sb.root, "role.json")
		raw, err := json.Marshal(map[string]any{
			"name": adoptRole, "type": "ccpool", "survivesShutdown": true, "ccpool": adoptCCPoolSettings(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(roleFile, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return sb.start("old handler", bin, []string{"dispatch", "--role-config", roleFile, "--config", sb.cfgPath}, adoptRequest(t, nil))
	}}
}

func newShape() handlerShape {
	return handlerShape{name: "new adapter (pg-router-handler-ccpool dispatch, role on stdin)", start: func(t *testing.T, sb *adoptSandbox) *handlerProc {
		bin := buildBinary(t, ccpoolAdapterCmd)
		role := map[string]any{"name": adoptRole, "config": map[string]any{"ccpool": adoptCCPoolSettings()}}
		return sb.start("new adapter", bin, []string{"dispatch", "--config", sb.cfgPath}, adoptRequest(t, role))
	}}
}

// adoptedExternalID is the shape of the per-attempt external id both binaries
// stamp: <prefix><role>-<bead>-<stamp> (dispatchlib.StampExternalID), which is
// also the session's tmux name in the pool listing.
var adoptedExternalID = regexp.MustCompile(`^pg-router-` + adoptRole + `-` + adoptBead + `-\d{8}T\d{6}\.\d{9}$`)

// runAdoption starts a session with starter, kills that dispatch as a daemon
// restart does, redelivers the same event to adopter, and asserts the adoption.
func runAdoption(t *testing.T, starter, adopter handlerShape) {
	// Build both binaries BEFORE the sandbox re-points HOME and the XDG
	// directories: `go build` under the sandbox's HOME would fill a fresh
	// GOPATH (a read-only module cache) inside the scratch directory, and
	// refetch every module. The builds are memoized per process.
	buildBinary(t, oldHandlerCmd)
	buildBinary(t, ccpoolAdapterCmd)
	sb := newAdoptSandbox(t)

	// 1. The starter launches the session and delivers the prompt (working).
	p1 := starter.start(t, sb)
	var original ccpool.Session
	sb.waitFor("the "+starter.name+" session to be working", p1, func() bool {
		for _, s := range sb.beadSessions() {
			if s.Live && s.State == ccpool.StateWorking {
				original = s
				return true
			}
		}
		return false
	})
	if !adoptedExternalID.MatchString(original.ExternalID) {
		t.Fatalf("the started session's external id %q does not have the shape <prefix><role>-<bead>-<stamp>", original.ExternalID)
	}
	if got := original.Meta[ccpool.MetaKeyEventID]; got != adoptEvent {
		t.Fatalf("the started session must record the event id %q for the redelivery to match; got %q", adoptEvent, got)
	}

	// 2. A daemon restart ends the dispatch; the session lives on.
	p1.kill()
	if s := sb.beadSessions(); len(s) != 1 || !s[0].Live || s[0].State != ccpool.StateWorking {
		t.Fatalf("the session must survive its dispatch's death, still working; got %+v", s)
	}
	launchedBefore := len(sb.eventRecords(executor.DispatchStartKind))

	// 3. The same event is redelivered to the other binary.
	redeliveredAt := time.Now()
	p2 := adopter.start(t, sb)
	sb.waitFor("the redelivered dispatch to be accepted", p2, func() bool {
		return len(sb.eventRecords(executor.DispatchStartKind)) > launchedBefore
	})

	// 4. It found the session by its stable identity and launched nothing.
	rec := sb.eventRecords(executor.DispatchStartKind)[launchedBefore]
	if rec["route"] != executor.RouteAdopted || rec["session"] != original.ExternalID {
		t.Fatalf("the %s must adopt session %s (route %q); its dispatch_start record is %v",
			adopter.name, original.ExternalID, executor.RouteAdopted, rec)
	}
	launched := 0
	for _, r := range sb.eventRecords(executor.DispatchStartKind) {
		if r["route"] == executor.RouteLaunched {
			launched++
		}
	}
	if launched != 1 {
		t.Fatalf("exactly one dispatch may have launched a session (the starter's); got %d dispatch_start records: %v",
			launched, sb.eventRecords(executor.DispatchStartKind))
	}
	rows := sb.beadSessions()
	if len(rows) != 1 || rows[0].ExternalID != original.ExternalID {
		t.Fatalf("exactly one session must exist for bead %s and role %s, the original %s; got %+v", adoptBead, adoptRole, original.ExternalID, rows)
	}
	t.Logf("session %s started by the %s was adopted by the %s (route %s)", original.ExternalID, starter.name, adopter.name, rec["route"])

	// 5. The supervision lease is fresh: the adopting dispatch refreshed it to
	// now + LeaseTTL, which a launch's own lease (launch + EnsureTimeout + TTL)
	// is far beyond; one second of slack covers the lease's RFC3339 resolution.
	sb.waitFor("the adopting dispatch's lease refresh", p2, func() bool {
		until, ok := sb.beadSessions()[0].LeaseUntil()
		return ok && !until.Before(time.Now().Add(-time.Second)) && until.Before(time.Now().Add(adoptLeaseTTL+time.Second))
	})
	if until, _ := sb.beadSessions()[0].LeaseUntil(); !until.After(redeliveredAt) {
		t.Errorf("the lease must be fresh after the redelivery; lease_until=%s, redelivered at %s", until, redeliveredAt)
	}

	// 6. The adopted dispatch runs to the session's outcome: the bead closes and
	// the dispatch replies with success.
	if p2.exited() {
		t.Fatalf("the adopting dispatch ended before the bead closed: %s", p2.report())
	}
	sb.setBeadStatus("closed")
	select {
	case <-p2.done:
	case <-time.After(adoptWait):
		t.Fatalf("the adopting dispatch did not finish after the bead closed; sessions: %+v", sb.beadSessions())
	}
	if code := p2.cmd.ProcessState.ExitCode(); code != conformance.ExitOK {
		t.Fatalf("the adopting dispatch must succeed: %s", p2.report())
	}
	var reply any
	if err := json.Unmarshal(p2.out.Bytes(), &reply); err != nil {
		t.Fatalf("the adopting dispatch's reply is not JSON: %v\n%s", err, p2.out.String())
	}
	if err := conformance.Check("handler.dispatch-reply", reply); err != nil {
		t.Fatalf("the adopting dispatch's reply fails handler.dispatch-reply: %v\n%s", err, p2.out.String())
	}
	if rows := sb.beadSessions(); len(rows) != 1 || rows[0].ExternalID != original.ExternalID {
		t.Errorf("no second session may exist after the dispatch settles; got %+v", rows)
	}
}

// TestAdoptOldHandlerSessionByNewAdapter is the cutover case (ADR 0085's
// Addendum): a session the old handler started is adopted, across a daemon
// restart, by the new ccpool adapter's redelivered dispatch of the same event.
func TestAdoptOldHandlerSessionByNewAdapter(t *testing.T) {
	runAdoption(t, oldShape(), newShape())
}

// TestRollbackNewAdapterSessionAdoptedByOldHandler is the rollback case: after
// a role family's move is reverted, the old handler (the only binary the
// simulation runs for the redelivery) adopts a session the new adapter started.
func TestRollbackNewAdapterSessionAdoptedByOldHandler(t *testing.T) {
	runAdoption(t, newShape(), oldShape())
}
