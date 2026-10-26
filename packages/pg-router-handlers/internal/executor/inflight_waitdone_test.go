package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
)

// An unanswered tool call is activity (INV-CCH-28, pg2-r4ig2). These tests
// drive waitDone itself against a REAL transcript tree on disk: an idle drain
// orchestrator whose subagent transcript was last written longer ago than the
// quiet window.

// transcriptLine is one transcript line holding a single content block.
func transcriptLine(t *testing.T, typ string, block map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": typ, "message": map[string]any{"role": typ, "content": []map[string]any{block}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func bashUse(t *testing.T, id string, timeoutMs int) string {
	input := map[string]any{"command": "git commit -m x"}
	if timeoutMs > 0 {
		input["timeout"] = timeoutMs
	}
	return transcriptLine(t, "assistant", map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": input})
}

func resultFor(t *testing.T, id string) string {
	return transcriptLine(t, "user", map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"})
}

// sessionTree writes <dir>/s.jsonl (the orchestrator, its own turn ended) and
// <dir>/s/subagents/agent-1.jsonl (lines), both stamped mtime. It returns the
// session transcript path.
func sessionTree(t *testing.T, mtime time.Time, subagentLines ...string) string {
	t.Helper()
	dir := t.TempDir()
	tr := filepath.Join(dir, "s.jsonl")
	sub := filepath.Join(dir, "s", "subagents", "agent-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(sub), 0o755); err != nil {
		t.Fatal(err)
	}
	orch := transcriptLine(t, "assistant", map[string]any{"type": "text", "text": "waiting for the subagent"})
	for p, body := range map[string]string{tr: orch, sub: strings.Join(subagentLines, "\n")} {
		if err := os.WriteFile(p, []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

// inflightResult is what inflightRun observed.
type inflightResult struct {
	err    error
	cc     *dtest.FakeCC
	bd     *dtest.ScriptBD
	endAge time.Duration // clock - mtime when waitDone returned
}

// inflightRun drives a close-or-release drain waitDone whose orchestrator is
// idle and whose bead stays claimed, with the manual clock starting `age` after
// the transcripts' mtime. cutoff > 0 cancels the wait once the clock reaches
// mtime+cutoff (the "still waiting" probe); 0 lets it run to its end.
func inflightRun(t *testing.T, mtime time.Time, transcript string, age, cutoff time.Duration) inflightResult {
	t.Helper()
	cfg := fastCfg()
	cfg.PollInterval = time.Second
	cfg.MaxWait = 24 * time.Hour
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress"}},
		AssigneeSeq: map[string][]string{relBead: {relActor}},
	}
	row := idleRow()
	row.TranscriptPath = transcript
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}
	clk := &dtest.ManualClock{T: mtime.Add(age)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adv := clk.TickAdvancing()
	e := &ccpoolRun{deps: Deps{
		CC: cc, BD: bd, Cfg: cfg, Now: clk.Now,
		Tick: func(c context.Context, d time.Duration) error {
			err := adv(c, d)
			if cutoff > 0 && !clk.Now().Before(mtime.Add(cutoff)) {
				cancel()
			}
			return err
		},
	}}
	err := e.waitDone(ctx, nil, releaseDispatch(cfg), relSession)
	return inflightResult{err: err, cc: cc, bd: bd, endAge: clk.Now().Sub(mtime)}
}

func assertEnded(t *testing.T, r inflightResult) {
	t.Helper()
	if r.err == nil || !strings.Contains(r.err.Error(), "session exited before completing") {
		t.Fatalf("got %v, want the session-idle failure", r.err)
	}
	if len(r.cc.Closed) != 1 || r.cc.Closed[0] != relSession {
		t.Errorf("the dead session must be closed; Closed=%v", r.cc.Closed)
	}
	if !hasUpdateContaining(r.bd, "--add-label human") {
		t.Errorf("the bead must be parked human; updates=%v", r.bd.Updates)
	}
}

// AC 1: a call inside its declared timeout (400 s) is NOT an end, even though
// every transcript is older than the 2-minute window.
func TestWaitDoneRelease_unansweredBashInsideTimeoutIsNotEnded(t *testing.T) {
	mtime := time.Unix(1_000_000, 0)
	tr := sessionTree(t, mtime, bashUse(t, "toolu_1", 400000))
	// Cancel at 400 s: past the 2 m window and past the 3 m start age, still
	// inside timeout + grace.
	r := inflightRun(t, mtime, tr, 3*time.Minute, 400*time.Second)
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("got %v, want the wait still running when cancelled", r.err)
	}
	if len(r.cc.Closed) != 0 {
		t.Errorf("no CC.Close while the call is in flight; Closed=%v", r.cc.Closed)
	}
	if len(r.bd.Updates) != 0 {
		t.Errorf("no claim release, human label or park note while the call is in flight; updates=%v", r.bd.Updates)
	}
}

// AC 2a: the call's timeout plus grace elapsed with still no result: ended and
// handled as today (session-idle failure, bead parked human).
func TestWaitDoneRelease_unansweredBashPastTimeoutPlusGraceIsEnded(t *testing.T) {
	mtime := time.Unix(1_000_000, 0)
	tr := sessionTree(t, mtime, bashUse(t, "toolu_1", 400000))
	r := inflightRun(t, mtime, tr, 3*time.Minute, 0)
	assertEnded(t, r)
	if want := 400*time.Second + inflightGrace; r.endAge < want {
		t.Errorf("ended %v after the last write, before timeout+grace %v", r.endAge, want)
	}
}

// AC 2b: a finished tool_result older than the window is ended at once.
func TestWaitDoneRelease_finishedResultOlderThanWindowIsEnded(t *testing.T) {
	mtime := time.Unix(1_000_000, 0)
	tr := sessionTree(t, mtime, bashUse(t, "toolu_1", 400000), resultFor(t, "toolu_1"))
	r := inflightRun(t, mtime, tr, 3*time.Minute, 0)
	assertEnded(t, r)
	if r.endAge > 3*time.Minute+10*time.Second {
		t.Errorf("a dead session must be detected at once, took until %v", r.endAge)
	}
}

// A call with no declared timeout defaults to the Bash tool's 120 s.
func TestWaitDoneRelease_unansweredBashDefaultTimeoutIs120s(t *testing.T) {
	mtime := time.Unix(1_000_000, 0)
	tr := sessionTree(t, mtime, bashUse(t, "toolu_1", 0))
	r := inflightRun(t, mtime, tr, 3*time.Minute, 0)
	assertEnded(t, r)
	// Already past 120 s + grace at the 3 m start: ended on the first poll.
	if r.endAge > 3*time.Minute+10*time.Second {
		t.Errorf("default-timeout call must not extend the wait, took until %v", r.endAge)
	}
}

// AC 3: post-completion cleanup keeps the plain window: an unanswered call must
// not hold a finished dispatch's pool slot, and the drain window stays 2 m.
func TestInFlightCall_doesNotAffectPostCompletionCleanup(t *testing.T) {
	mtime := time.Unix(1_000_000, 0)
	tr := sessionTree(t, mtime, bashUse(t, "toolu_1", 600000))
	cfg := overrideCfg()
	e, opener := newExecWithOpener(quietCC(), &dtest.ScriptBD{}, cfg)
	e.deps.CC = &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "s", State: ccpool.StateIdle, TranscriptPath: tr}}}}
	clk := &dtest.ManualClock{T: mtime.Add(3 * time.Minute)}
	e.deps.Now, e.deps.Tick = clk.Now, clk.TickAdvancing()
	e.cleanupWorktree(context.Background(), releaseRole(cfg).CCPool, "s", "zr-w", "/tmp/wt/zr-w")
	if len(opener.WTM.Calls) == 0 {
		t.Fatal("worktree removal must not wait for an unanswered call once the bead completed")
	}
	if got := clk.Now().Sub(mtime); got != 3*time.Minute {
		t.Errorf("cleanup waited until %v; the plain 2m window was already satisfied", got)
	}
	if w := e.quietWindow(releaseRole(cfg).CCPool); w != 2*time.Minute {
		t.Errorf("drain quiet window = %v, want the unchanged 2m default", w)
	}
}
