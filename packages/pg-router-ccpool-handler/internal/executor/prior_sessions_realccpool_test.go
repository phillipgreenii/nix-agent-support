package executor

// Hermetic tests of INV-CCH-30 (ADR 0094, bead pg2-qaeb8) against the REAL
// ccpool binary (global rule V-3: a fake ccpool does not count). The fake-ccpool
// tests in ccpool_test.go (TestPlanPriorSessions et al.) pin the same decision
// table fast; these prove it against ccpool's own store, metadata, liveness and
// close semantics.
//
// Isolation (bead pg2-7jaxd: never touch the operator's pools or the shared
// `-L ccpool` tmux socket). Every ccpool call runs in POOL-DIR mode
// (CCPOOL_POOL=<sandbox>/pools/<case>), so the store, logs, locks and config
// live in the sandbox and each pool's tmux socket is the pool's own derived name
// (cc-<hash>), never "ccpool". On top of that the process environment is
// re-pointed at the sandbox for the whole test: TMUX_TMPDIR (so even a
// default-named socket would be a different socket file), HOME (trust writes),
// XDG_* and CCPOOL_REGISTRY_DIR (registry enrolment), and the inherited
// CCPOOL_*, TMUX*, BEADS_* and OTEL_* variables are cleared (no live pool, no
// live tmux server, no telemetry export). Each pool's config replaces `claude`
// with a tiny fake that only fires ccpool's own hooks, and disables the usage
// gate (no monitor call), notifications and the retry actuator. Every tmux
// server under the sandbox is killed at the end, and the sandbox is removed.
//
// The tests need `ccpool` and `tmux` on PATH. Without them they SKIP, unless
// PG_ROUTER_CCPOOL_HANDLER_REQUIRE_CCPOOL=1 (set by the nix check
// checks.<system>.pg-router-ccpool-handler-go-tests), which turns the skip into
// a failure, so a green check means these ran.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
)

// requireRealCCPoolEnv turns a missing ccpool/tmux from a skip into a failure.
const requireRealCCPoolEnv = "PG_ROUTER_CCPOOL_HANDLER_REQUIRE_CCPOOL"

// realLaunchWait is each pool's `ccpool new` wait for ready. A fake session is
// ready in well under a second; the headroom keeps a loaded machine from
// failing a launch the test expects to succeed. lateStart (seconds) is past it.
const (
	realLaunchWait = 8 * time.Second
	lateStart      = "10"
)

// realParallel caps how many cases drive ccpool at once.
const realParallel = 3

// realFakeClaude stands in for claude inside a real ccpool session. It fires
// ccpool's SessionStart hook (the session reaches ready) unless
// FAKE_CLAUDE_NO_START=1 (a launch that never becomes ready), after
// FAKE_CLAUDE_START_DELAY seconds when set (a launch that reaches ready only
// after `ccpool new` gave up on it), then reads its input: /exit (ccpool's
// close) ends the process, a line carrying FAKECC_STOP ends the turn through
// the Stop hook (idle), and every other line (the dispatch's prompt) is
// recorded in the transcript and leaves the turn running, as ccpool's reply
// already marked it working.
const realFakeClaude = `#!/bin/sh
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
if [ "${FAKE_CLAUDE_NO_START:-}" != "1" ]; then
  sleep "${FAKE_CLAUDE_START_DELAY:-0}"
  payload SessionStart | "$cc" hook start >/dev/null 2>&1
fi
while IFS= read -r line; do
  case "$line" in
    */exit*) exit 0 ;;
    *FAKECC_STOP*)
      printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"OK-DONE"}]}}\n' >> "$transcript"
      payload Stop | "$cc" hook stop >/dev/null 2>&1 ;;
    *)
      printf '{"type":"user","message":{"role":"user","content":"prompt"}}\n' >> "$transcript" ;;
  esac
done
`

// realSandbox is the per-test hermetic environment the pools live in.
type realSandbox struct {
	root    string // short, so tmux socket paths fit
	tmuxDir string // TMUX_TMPDIR
	fake    string // the fake claude
	pools   atomic.Int32
}

// shortTempRoot returns a temp base short enough that a tmux socket under it
// stays well inside the unix socket path limit (104 bytes on macOS, whose
// default $TMPDIR is already ~50).
func shortTempRoot() string {
	if base := os.TempDir(); len(base) <= 40 {
		return base
	}
	return "/tmp"
}

func newRealSandbox(t *testing.T) *realSandbox {
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
	root, err := os.MkdirTemp(shortTempRoot(), "cch-")
	if err != nil {
		t.Fatal(err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	sb := &realSandbox{root: root, tmuxDir: filepath.Join(root, "tmux"), fake: filepath.Join(root, "bin", "fake-claude")}
	// Registered before any t.Setenv below, so they run AFTER the restores
	// (cleanups are LIFO) and after every parallel case; they address the
	// sandbox explicitly, not through the environment.
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
	dirs := map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "cfg", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state",
		"XDG_RUNTIME_DIR": "run", "CCPOOL_REGISTRY_DIR": "reg", "TMUX_TMPDIR": "tmux", "FAKE_CLAUDE_DIR": "transcripts",
	}
	for k, sub := range dirs {
		d := filepath.Join(root, sub)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, d)
	}
	t.Setenv("CCPOOL_BIN", ccpoolBin)
	for _, d := range []string{"bin", "plugin", "locks", "pools", "work"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(sb.fake, []byte(realFakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	return sb
}

// killTmux kills every tmux server whose socket lives in the sandbox.
func (sb *realSandbox) killTmux() {
	socks, _ := filepath.Glob(filepath.Join(sb.tmuxDir, "tmux-*", "*"))
	for _, s := range socks {
		_ = exec.Command("tmux", "-S", s, "kill-server").Run()
	}
}

// realPool is one hermetic ccpool pool driven through the real binary, owned
// by one case.
type realPool struct {
	t        *testing.T
	sb       *realSandbox
	poolDir  string // CCPOOL_POOL
	repoRoot string // the sessions' working directory (isolation "none")
	lockDir  string // the per-session flocks (per pool: cases reuse session ids)
	cc       *ccpool.CLIRunner
	cfg      config.Config
}

// pool creates a fresh pool of its own (store, config, tmux socket) for t.
func (sb *realSandbox) pool(t *testing.T) *realPool {
	t.Helper()
	n := sb.pools.Add(1)
	p := &realPool{
		t: t, sb: sb,
		poolDir:  filepath.Join(sb.root, "pools", fmt.Sprintf("p%d", n)),
		repoRoot: filepath.Join(sb.root, "work", fmt.Sprintf("p%d", n)),
		lockDir:  filepath.Join(sb.root, "locks", fmt.Sprintf("p%d", n)),
	}
	for _, d := range []string{p.poolDir, p.repoRoot, p.lockDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
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
timeout = %q
[notify]
adapter = "none"
[usage_gate]
enabled = false
[retry]
enabled = false
`, sb.fake, filepath.Join(sb.root, "plugin"), realLaunchWait.String())
	if err := os.WriteFile(filepath.Join(p.poolDir, "config.toml"), []byte(poolCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RepoRoot = p.repoRoot
	cfg.WorktreeDir = filepath.Join(sb.root, "wt")
	cfg.PollInterval = 50 * time.Millisecond
	cfg.MaxWait = 30 * time.Second
	cfg.LeaseTTL = time.Minute
	cfg.WorktreeQuietWindow = 100 * time.Millisecond
	cfg.ConfirmIngest = 0 // the fake never starts a real model turn
	p.cfg = cfg
	p.cc = ccpool.NewCLIRunnerForPool(cfg, p.poolDir)
	return p
}

func (p *realPool) ctx() context.Context { return context.Background() }

// launch starts a real session named name with the given dispatch meta (and
// extra session env), the way an earlier dispatch would have.
func (p *realPool) launch(id, name string, meta, env map[string]string) error {
	p.t.Helper()
	if env == nil {
		env = map[string]string{}
	}
	return p.cc.Ensure(p.ctx(), id, name, p.repoRoot, env, meta)
}

func (p *realPool) mustLaunch(id, name string, meta map[string]string) {
	p.t.Helper()
	if err := p.launch(id, name, meta, nil); err != nil {
		p.t.Fatalf("launch %s: %v", id, err)
	}
	p.wantState(id, ccpool.StateReady, true)
}

// work delivers a line to the session through ccpool reply, which marks it working.
func (p *realPool) work(id string) {
	p.t.Helper()
	if err := p.cc.Send(p.ctx(), id, "FAKECC_HOLD", ccpool.ModeNoWait); err != nil {
		p.t.Fatalf("reply %s: %v", id, err)
	}
	p.wantState(id, ccpool.StateWorking, true)
}

// hook fires one of ccpool's own hooks for the session, as claude would.
func (p *realPool) hook(id, event, notificationType string) {
	p.t.Helper()
	s := p.mustRow(id)
	payload, _ := json.Marshal(map[string]string{
		"session_id": s.ClaudeSessionID, "transcript_path": s.TranscriptPath,
		"hook_event_name": event, "notification_type": notificationType,
	})
	cmd := exec.Command("ccpool", "hook", event)
	cmd.Env = append(os.Environ(), "CCPOOL_POOL="+p.poolDir, "CCPOOL_EXTERNAL_ID="+id)
	cmd.Stdin = strings.NewReader(string(payload))
	if out, err := cmd.CombinedOutput(); err != nil {
		p.t.Fatalf("ccpool hook %s %s: %v\n%s", event, id, err, out)
	}
}

func (p *realPool) idle(id string) {
	p.t.Helper()
	p.hook(id, "stop", "")
	p.wantState(id, ccpool.StateIdle, true)
}

func (p *realPool) needsInput(id string) {
	p.t.Helper()
	p.hook(id, "notify", "permission_prompt")
	p.wantState(id, ccpool.StateNeedsInput, true)
}

// closeAsHandler closes the session the way the handler's settled close does
// (non-purge, reason handler): its tmux session ends and the row stays.
func (p *realPool) closeAsHandler(id string) {
	p.t.Helper()
	if err := p.cc.Close(p.ctx(), id, false); err != nil {
		p.t.Fatalf("close %s: %v", id, err)
	}
	s := p.mustRow(id)
	if s.Live || s.CloseReason != "handler" {
		p.t.Fatalf("precondition: %s must be closed by the handler and no longer live; live=%v close_reason=%q", id, s.Live, s.CloseReason)
	}
}

func (p *realPool) list() []ccpool.Session {
	p.t.Helper()
	sessions, err := p.cc.List(p.ctx())
	if err != nil {
		p.t.Fatalf("ccpool list: %v", err)
	}
	return sessions
}

func (p *realPool) row(id string) (ccpool.Session, bool) {
	p.t.Helper()
	for _, s := range p.list() {
		if s.ExternalID == id {
			return s, true
		}
	}
	return ccpool.Session{}, false
}

func (p *realPool) mustRow(id string) ccpool.Session {
	p.t.Helper()
	s, ok := p.row(id)
	if !ok {
		p.t.Fatalf("session %s is not in the pool", id)
	}
	return s
}

func (p *realPool) wantState(id string, st ccpool.SessionState, live bool) {
	p.t.Helper()
	deadline := time.Now().Add(realLaunchWait + 10*time.Second)
	for {
		s, ok := p.row(id)
		if ok && s.State == st && s.Live == live {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("precondition: session %s must be %s (live=%v); got present=%v %+v", id, st, live, ok, s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ids returns the external ids of every session in the pool.
func (p *realPool) ids() []string {
	p.t.Helper()
	var ids []string
	for _, s := range p.list() {
		ids = append(ids, s.ExternalID)
	}
	return ids
}

// wantUnchanged asserts that a row a case set up is exactly as it was before a
// decline: same state, liveness, close reason and lease.
func (p *realPool) wantUnchanged(before ccpool.Session) {
	p.t.Helper()
	after := p.mustRow(before.ExternalID)
	if after.State != before.State || after.Live != before.Live || after.CloseReason != before.CloseReason ||
		after.Meta[ccpool.MetaKeyLeaseUntil] != before.Meta[ccpool.MetaKeyLeaseUntil] {
		p.t.Errorf("a decline must leave %s untouched; before=%+v after=%+v", before.ExternalID, before, after)
	}
}

// wantReplaced asserts that the leftover old was closed (non-purge, reason
// handler, no longer live) and that fresh was launched and prompted.
func (p *realPool) wantReplaced(old, fresh string) {
	p.t.Helper()
	o := p.mustRow(old)
	if o.Live || o.CloseReason != "handler" {
		p.t.Errorf("the leftover must be closed by the handler (kept, not purged); got live=%v close_reason=%q", o.Live, o.CloseReason)
	}
	f, ok := p.row(fresh)
	if !ok {
		p.t.Fatalf("the dispatch must launch its own session after closing the leftover; pool holds %v", p.ids())
	}
	if !f.Live || f.State != ccpool.StateWorking {
		p.t.Errorf("the fresh session must have been prompted (live, working); got live=%v state=%s", f.Live, f.State)
	}
}

// meta builds the dispatch meta an earlier dispatch of bead/role stamped,
// launched at launched with a lease ending at leaseUntil. An empty eventID or
// head omits its key.
func (p *realPool) meta(bead, role, eventID, head string, launched, leaseUntil time.Time) map[string]string {
	m := ccpool.DispatchMeta(bead, role, launched, p.cfg.LeaseTTL, eventID, head)
	m[ccpool.MetaKeyLeaseUntil] = ccpool.FormatMetaTime(leaseUntil)
	return m
}

// realRole returns a close-only ccpool role that runs in the repo root
// (isolation none) with no budget unless the caller sets one.
func realRole(name string) roles.Role {
	return roles.Role{
		Name: name, Type: "ccpool",
		CCPool: &roles.CCPoolConfig{
			Actor: "pgii-pool__" + name, Completion: roles.CloseOnly, OnFailure: roles.Unclaim, OnDispatchFail: roles.DispatchUnclaim,
			PromptBody: "Work bead {{.BeadID}}.", Prompt: mustParsePrompt(name, "Work bead {{.BeadID}}."),
			Isolation: roles.IsolationConfig{Type: "none"},
		},
	}
}

// dispatch runs one real Dispatch against the pool with a scripted bead.
func (p *realPool) dispatch(role roles.Role, bead, eventID, head, ext string, status ...string) (report.Result, error) {
	p.t.Helper()
	return p.dispatchAt(0, p.cc, role, bead, eventID, head, ext, status...)
}

// dispatchAt is dispatch through cc on a clock running ahead of real time by
// offset (every row then reads offset older); 0 is the real clock.
func (p *realPool) dispatchAt(offset time.Duration, cc ccpool.Runner, role roles.Role, bead, eventID, head, ext string, status ...string) (report.Result, error) {
	p.t.Helper()
	it := item.Item{ID: bead}
	if head != "" {
		it.Metadata = map[string]any{"head_sha": head}
	}
	deps := Deps{
		CC: cc, BD: &dtest.ScriptBD{StatusSeq: map[string][]string{bead: status}}, Cfg: p.cfg,
		ExternalID: ext, LockDir: p.lockDir,
		UsageReader: &dtest.RampReader{Seq: []usage.Snapshot{{}}},
		Git:         &dtest.NoopGit{}, GitOpener: (&dtest.NoopGitOpener{}).Open,
	}
	if offset != 0 {
		deps.Now = func() time.Time { return time.Now().Add(offset) }
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return ccpoolExecutor{}.Dispatch(ctx, DispatchContext{Role: role, Item: it, EventID: eventID}, deps)
}

// TestPriorSessions_realCCPool drives INV-CCH-30's decision table through the
// real ccpool binary: the settled success row and a new head (the case that
// produced every false budget stop) and each case of the rule. Each case has a
// pool of its own; a few run at a time.
func TestPriorSessions_realCCPool(t *testing.T) {
	sb := newRealSandbox(t)
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	sem := make(chan struct{}, realParallel)
	run := func(name string, f func(t *testing.T, p *realPool)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sem <- struct{}{}
			defer func() { <-sem }()
			f(t, sb.pool(t))
		})
	}
	const bead = "rc-1"

	// INV-CCH-30's headline: a settled success row (its dispatch completed and the
	// handler closed it) for the SAME event id. Whether the head advanced (a
	// re-review, pg2-afre3 / pg2-pq2ej) or not (the candidate ADR 0094 rejected),
	// the dispatch runs in a fresh session with its own launch time; absorbing
	// would measure the 25-minute budget from the row's launch five hours ago and
	// hard-stop at once.
	for _, head := range []string{"h2", "h1"} {
		run("settled success row, head "+head+": fresh session", func(t *testing.T, p *realPool) {
			role := realRole("review")
			role.CCPool.Budget = budget.Budget{Time: 25 * time.Minute, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00}}
			name := role.DisplayName(p.cfg.SessionPrefix, bead)
			event := "review.ready:" + bead
			p.mustLaunch("old", name, p.meta(bead, "review", event, "h1", now.Add(-5*time.Hour), now.Add(-4*time.Hour)))
			p.work("old")
			p.idle("old")
			p.closeAsHandler("old")

			_, err := p.dispatch(role, bead, event, head, "new", "in_progress", "closed")
			if errors.Is(err, watchdog.ErrBudgetExceeded) {
				t.Fatalf("the dispatch absorbed the settled row and hard-stopped on its old launch time: %v", err)
			}
			if err != nil {
				t.Fatalf("the fresh session must run to completion, got %v", err)
			}
			fresh, ok := p.row("new")
			if !ok {
				t.Fatalf("no fresh session was launched; pool holds %v", p.ids())
			}
			if fresh.Name != name || fresh.Meta[ccpool.MetaKeyEventID] != event || fresh.Meta[ccpool.MetaKeyHeadSHA] != head {
				t.Errorf("the fresh session must carry the stable name, this event and this head; got %+v", fresh)
			}
			if at, ok := fresh.LaunchedAt(); !ok || at.Before(now.Add(-time.Minute)) {
				t.Errorf("the fresh session must start its own budget clock now, got launched_at=%q", fresh.Meta[ccpool.MetaKeyLaunchedAt])
			}
			if old := p.mustRow("old"); old.Live || old.CloseReason != "handler" || old.State != ccpool.StateIdle {
				t.Errorf("the settled row must be left as it was; got %+v", old)
			}
		})
	}

	// The one exception: the same event's turn in progress (working, or paused
	// for a person), nobody closed it, bead open. Its handler is gone (lease
	// expired), as after a crash or a daemon restart. The redelivery absorbs it:
	// no second session, the absorbed session's lease taken over.
	for _, st := range []ccpool.SessionState{ccpool.StateWorking, ccpool.StateNeedsInput} {
		run("same event's turn "+string(st)+": absorbed", func(t *testing.T, p *realPool) {
			role := realRole("worker")
			name := role.DisplayName(p.cfg.SessionPrefix, bead)
			p.mustLaunch("cur", name, p.meta(bead, "worker", "evt-1", "", now.Add(-time.Minute), past))
			p.work("cur")
			if st == ccpool.StateNeedsInput {
				p.needsInput("cur")
			}
			if _, err := p.dispatch(role, bead, "evt-1", "", "new", "in_progress", "in_progress", "closed"); err != nil {
				t.Fatalf("the redelivery must be absorbed and report the session's success, got %v", err)
			}
			if _, launched := p.row("new"); launched {
				t.Errorf("absorbing must not launch a second session; pool holds %v", p.ids())
			}
			if lease, ok := p.mustRow("cur").LeaseUntil(); !ok || !lease.After(now) {
				t.Errorf("the absorbing dispatch must take over the session's lease; lease_until=%v ok=%v", lease, ok)
			}
		})
	}

	// Any other attempt in flight: decline session-in-flight, launch nothing,
	// touch nothing.
	for _, tc := range []struct {
		name  string
		setup func(p *realPool, name string) // leaves the in-flight session "cur"
		bead  []string                       // the bead's status sequence (nil: in_progress)
	}{
		{"another event's turn in progress", func(p *realPool, name string) {
			p.mustLaunch("cur", name, p.meta(bead, "worker", "evt-earlier", "", now, future))
			p.work("cur")
		}, nil},
		{"another event's turn paused for a person", func(p *realPool, name string) {
			p.mustLaunch("cur", name, p.meta(bead, "worker", "evt-earlier", "", now, past))
			p.work("cur")
			p.needsInput("cur")
		}, nil},
		{"a turn that records no event id", func(p *realPool, name string) {
			p.mustLaunch("cur", name, p.meta(bead, "worker", "", "", now, past))
			p.work("cur")
		}, nil},
		{"the same event's turn, bead already closed", func(p *realPool, name string) {
			p.mustLaunch("cur", name, p.meta(bead, "worker", "evt-1", "", now, past))
			p.work("cur")
		}, []string{"closed"}},
		{"a session still being launched (ready, lease held)", func(p *realPool, name string) {
			p.mustLaunch("cur", name, p.meta(bead, "worker", "evt-1", "", now, future))
		}, nil},
		{"a turn that ended while its handler still settles it (lease held)", func(p *realPool, name string) {
			p.mustLaunch("cur", name, p.meta(bead, "worker", "evt-1", "", now, future))
			p.work("cur")
			p.idle("cur")
		}, nil},
		{"a closed row whose handler still holds the lease", func(p *realPool, name string) {
			p.mustLaunch("cur", name, p.meta(bead, "worker", "evt-1", "", now, future))
			p.work("cur")
			p.idle("cur")
			p.closeAsHandler("cur")
		}, nil},
	} {
		run("decline: "+tc.name, func(t *testing.T, p *realPool) {
			role := realRole("worker")
			tc.setup(p, role.DisplayName(p.cfg.SessionPrefix, bead))
			before := p.mustRow("cur")
			status := tc.bead
			if status == nil {
				status = []string{"in_progress"}
			}
			if _, err := p.dispatch(role, bead, "evt-1", "", "new", status...); !errors.Is(err, ErrSessionInFlight) {
				t.Fatalf("err = %v, want ErrSessionInFlight", err)
			}
			if _, launched := p.row("new"); launched {
				t.Errorf("a decline launches nothing; pool holds %v", p.ids())
			}
			p.wantUnchanged(before)
		})
	}

	// A pool whose sessions cannot be read counts as one in flight but declines with its own
	// ErrPoolUnreadable, not ErrSessionInFlight (bead pg2-s3pjt): a directory
	// that is not a ccpool pool makes the real `ccpool list` fail.
	run("decline: a pool that cannot be read", func(t *testing.T, p *realPool) {
		if err := os.WriteFile(filepath.Join(p.poolDir, "foreign.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := p.cc.List(p.ctx()); err == nil {
			t.Fatal("precondition: the real ccpool must refuse to list a directory that is not a pool")
		}
		if _, err := p.dispatch(realRole("worker"), bead, "evt-1", "", "new", "in_progress"); !errors.Is(err, ErrPoolUnreadable) {
			t.Fatalf("err = %v, want ErrPoolUnreadable", err)
		}
	})

	// A leftover nobody supervises (live, no turn, lease expired: its handler
	// died before sending the prompt) is closed, without purging, and replaced by
	// the dispatch's own session, which gets the prompt.
	run("leftover ready, lease expired: closed and replaced", func(t *testing.T, p *realPool) {
		role := realRole("worker")
		p.mustLaunch("old", role.DisplayName(p.cfg.SessionPrefix, bead), p.meta(bead, "worker", "evt-earlier", "", now.Add(-time.Hour), past))
		if _, err := p.dispatch(role, bead, "evt-1", "", "new", "in_progress", "closed"); err != nil {
			t.Fatalf("the replacing dispatch must succeed, got %v", err)
		}
		p.wantReplaced("old", "new")
	})

	// A launch that fails still carries its dispatch metadata, the supervision
	// lease included: `ccpool new --meta` is applied when the row is inserted,
	// before the launch wait (bead pg2-7139f; INV-CCH-18), not once the session is
	// ready. So a session still starting, and what a launch that never got ready
	// (or got ready only after `ccpool new` gave up) leaves behind, is held by
	// its lease: a dispatch declines while it lasts, and once it has expired the
	// row is a leftover, closed and replaced.
	for _, tc := range []struct {
		name  string
		env   map[string]string
		state ccpool.SessionState
	}{
		{"a launch that never reached ready (starting)", map[string]string{"FAKE_CLAUDE_NO_START": "1"}, ccpool.StateStarting},
		{"a launch that reached ready after it was given up (ready)", map[string]string{"FAKE_CLAUDE_START_DELAY": lateStart}, ccpool.StateReady},
	} {
		run("leased failed launch, "+tc.name+": declined while the lease holds, then replaced", func(t *testing.T, p *realPool) {
			role := realRole("worker")
			meta := ccpool.DispatchMeta(bead, "worker", time.Now(), p.cfg.LeaseTTL, "evt-earlier", "")
			if err := p.launch("old", role.DisplayName(p.cfg.SessionPrefix, bead), meta, tc.env); err == nil {
				t.Fatal("precondition: a launch that does not reach ready in time must fail")
			}
			p.wantState("old", tc.state, true)
			before := p.mustRow("old")
			if len(before.Meta) == 0 && os.Getenv(requireRealCCPoolEnv) == "" {
				// The ccpool on PATH predates bead pg2-7139f (--meta applied only at
				// ready). The nix check builds ccpool from this repo and sets the REQUIRE
				// variable, so there it is a failure, not a skip.
				t.Skip("the ccpool on PATH applies --meta only once the session is ready (it predates pg2-7139f)")
			}
			if _, ok := before.LeaseUntil(); !ok || before.Meta[ccpool.MetaKeyBead] != bead {
				t.Fatalf("a failed launch must still carry the dispatch metadata ccpool new was given (lease included); got %v", before.Meta)
			}
			if _, err := p.dispatch(role, bead, "evt-1", "", "early", "in_progress"); !errors.Is(err, ErrSessionInFlight) {
				t.Fatalf("while the launch's lease holds the row is supervised: err = %v, want ErrSessionInFlight", err)
			}
			if _, launched := p.row("early"); launched {
				t.Errorf("a decline launches nothing; pool holds %v", p.ids())
			}
			p.wantUnchanged(before)
			pastLease := ccpool.EnsureTimeout + p.cfg.LeaseTTL + time.Minute
			if _, err := p.dispatchAt(pastLease, p.cc, role, bead, "evt-1", "", "new", "in_progress", "closed"); err != nil {
				t.Fatalf("once the lease has expired the leftover is replaced: got %v", err)
			}
			p.wantReplaced("old", "new")
		})
	}

	// A row with NO lease: one launched before ccpool applied --meta at insert
	// (a ccpool older than bead pg2-7139f), or by a handler whose LeaseTTL is not
	// positive, which omits the lease keys. It launches with no metadata at all
	// here. Such a live starting or ready row may still be inside its launch, so
	// inside the launch window a dispatch declines; past it, it is a leftover,
	// closed and replaced. This is the executor's one-launch-window fallback
	// (launchWindow), kept for those rows.
	for _, tc := range []struct {
		name  string
		env   map[string]string
		state ccpool.SessionState
	}{
		{"a launch that never reached ready (starting)", map[string]string{"FAKE_CLAUDE_NO_START": "1"}, ccpool.StateStarting},
		{"a launch that reached ready after it was given up (ready)", map[string]string{"FAKE_CLAUDE_START_DELAY": lateStart}, ccpool.StateReady},
	} {
		run("unleased leftover, "+tc.name+": declined inside the launch window, then replaced", func(t *testing.T, p *realPool) {
			role := realRole("worker")
			if err := p.launch("old", role.DisplayName(p.cfg.SessionPrefix, bead), nil, tc.env); err == nil {
				t.Fatal("precondition: a launch that does not reach ready in time must fail")
			}
			p.wantState("old", tc.state, true)
			before := p.mustRow("old")
			if len(before.Meta) != 0 {
				t.Fatalf("precondition: a launch given no metadata carries none; got %v", before.Meta)
			}
			if _, err := p.dispatch(role, bead, "evt-1", "", "early", "in_progress"); !errors.Is(err, ErrSessionInFlight) {
				t.Fatalf("inside the launch window the row may still be launching: err = %v, want ErrSessionInFlight", err)
			}
			if _, launched := p.row("early"); launched {
				t.Errorf("a decline launches nothing; pool holds %v", p.ids())
			}
			p.wantUnchanged(before)
			pastWindow := ccpool.EnsureTimeout + p.cfg.LeaseTTL + time.Minute
			if _, err := p.dispatchAt(pastWindow, p.cc, role, bead, "evt-1", "", "new", "in_progress", "closed"); err != nil {
				t.Fatalf("past the launch window the leftover is replaced: got %v", err)
			}
			p.wantReplaced("old", "new")
		})
	}
}
