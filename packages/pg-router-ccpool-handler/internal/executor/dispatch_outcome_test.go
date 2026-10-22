package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/query"
)

// Tests for the outcome records of INV-CCH-35 (ruling 33): dispatch_start is
// written when the payload STARTS, and every accepted dispatch gets exactly one
// outcome record after it: dispatch_result when it failed (INV-CCH-9),
// dispatch_end for every other end, success included.

// wantCounts fails unless the log holds exactly want[kind] records of each dispatch kind.
func wantCounts(t *testing.T, path string, want map[string]int) {
	t.Helper()
	for _, kind := range []string{DispatchStartKind, DispatchEndKind, dispatchResultKind, DispatchDeclineKind} {
		if got := len(readRecords(t, path, kind)); got != want[kind] {
			t.Errorf("%s records = %d, want %d", kind, got, want[kind])
		}
	}
}

// lineIndex returns the 0-based line of the first record of kind in the log, or -1.
func lineIndex(t *testing.T, path, kind string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.Contains(line, `"kind":"`+kind+`"`) {
			return i
		}
	}
	return -1
}

// The payload starts only after dispatch_start is in the log: the payload itself
// reads the log and reports whether the record was already there.
func TestCommandDispatch_dispatchStartIsWrittenBeforeThePayloadStarts(t *testing.T) {
	log, path := newTestLog(t)
	seen := filepath.Join(t.TempDir(), "seen")
	role := shCommandRole(`grep -c '"kind":"dispatch_start"' "$1" > "$2"`)
	role.Command.Argv = append(role.Command.Argv, "payload", path, seen)
	if err := dispatchCommand(t, role, log); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "1" {
		t.Errorf("dispatch_start records visible to the payload at its start = %q, want 1", got)
	}
}

// A payload that hangs and is cancelled still has its dispatch_start, and its one
// outcome record is dispatch_end "interrupted" (a cancelled dispatch is not a
// session failure), after the start.
func TestCommandDispatch_hungCancelledPayloadKeepsItsDispatchStart(t *testing.T) {
	log, path := newTestLog(t)
	role := shCommandRole("sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		d := DispatchContext{Role: role, Item: item.Item{ID: "b-1", Type: "task"}}
		_, err := commandExecutor{}.Dispatch(ctx, d, Deps{Cmd: query.OSCommander{}, Log: log})
		done <- err
	}()
	// The payload is hung once dispatch_start is in the log.
	deadline := time.Now().Add(20 * time.Second)
	for len(readRecordsTolerant(path, DispatchStartKind)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no dispatch_start while the payload is running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled payload must return an error")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the cancelled payload did not return")
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1, DispatchEndKind: 1})
	if rec := readRecords(t, path, DispatchEndKind)[0]; rec["outcome"] != OutcomeInterrupted {
		t.Errorf("outcome = %v, want %q", rec["outcome"], OutcomeInterrupted)
	}
	if lineIndex(t, path, DispatchStartKind) > lineIndex(t, path, DispatchEndKind) {
		t.Error("dispatch_start must come before the outcome record")
	}
}

// readRecordsTolerant is readRecords for a log being written concurrently: a
// missing file or a half-written last line reads as no record.
func readRecordsTolerant(path, kind string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"kind":"`+kind+`"`) {
			out = append(out, line)
		}
	}
	return out
}

// A command role's busy exit 9 is a decline: dispatch_start is written (the
// command had to run to say so) and the executor writes no outcome of its own,
// because the cmd layer's dispatch_decline is the outcome.
func TestCommandDispatch_exit9WritesDispatchStartAndNoExecutorOutcome(t *testing.T) {
	log, path := newTestLog(t)
	err := dispatchCommand(t, shCommandRole("exit 9"), log)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1})
}

// A successful command dispatch writes exactly one outcome, dispatch_end
// "completed", after its dispatch_start, and no failure record.
func TestCommandDispatch_successWritesOneDispatchEnd(t *testing.T) {
	log, path := newTestLog(t)
	if err := dispatchCommand(t, shCommandRole("exit 0"), log); err != nil {
		t.Fatal(err)
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1, DispatchEndKind: 1})
	rec := readRecords(t, path, DispatchEndKind)[0]
	if rec["outcome"] != OutcomeCompleted || rec["role"] != "cmdrole" || rec["item_id"] != "b-1" || rec["item_type"] != "task" {
		t.Errorf("record = %v", rec)
	}
	if _, has := rec["session"]; has {
		t.Errorf("a command role has no session: %v", rec)
	}
	if lineIndex(t, path, DispatchStartKind) > lineIndex(t, path, DispatchEndKind) {
		t.Error("dispatch_start must come before dispatch_end")
	}
}

// A failing command dispatch writes exactly one outcome, the dispatch_result
// failure record with the fields its readers use (INV-CCH-9), classified from its
// stderr, redacted and bounded.
func TestCommandDispatch_failureWritesOneDispatchResultWithReaderFields(t *testing.T) {
	log, path := newTestLog(t)
	err := dispatchCommand(t, shCommandRole(`echo 'fatal: could not read from remote repository https://user:hunter2@example.com/r.git' >&2; exit 3`), log)
	if err == nil {
		t.Fatal("a failing command must error")
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1, dispatchResultKind: 1})
	rec := readRecords(t, path, dispatchResultKind)[0]
	for _, f := range []string{"role", "pool", "bead", "session", "failure_signature", "signature_evidence"} {
		if _, ok := rec[f]; !ok {
			t.Errorf("failure record lacks field %q: %v", f, rec)
		}
	}
	if rec["role"] != "cmdrole" || rec["bead"] != "b-1" {
		t.Errorf("record = %v", rec)
	}
	if sig, _ := rec["failure_signature"].(string); !failsig.Signature(sig).Valid() || sig != string(failsig.GitAuth) {
		t.Errorf("failure_signature = %v, want %q", rec["failure_signature"], failsig.GitAuth)
	}
	ev, _ := rec["signature_evidence"].(string)
	if ev == "" || len(ev) > failsig.MaxEvidenceLen || strings.Contains(ev, "hunter2") {
		t.Errorf("signature_evidence = %q, want redacted, bounded and never empty", ev)
	}
	if lineIndex(t, path, DispatchStartKind) > lineIndex(t, path, dispatchResultKind) {
		t.Error("dispatch_start must come before the outcome record")
	}
}

// A command payload that cannot start is still an accepted dispatch (its
// dispatch_start is written before the start) and ends in a failure record.
func TestCommandDispatch_payloadThatCannotStartWritesStartThenFailure(t *testing.T) {
	log, path := newTestLog(t)
	role := shCommandRole("")
	role.Command.Argv = []string{filepath.Join(t.TempDir(), "no-such-command")}
	if err := dispatchCommand(t, role, log); err == nil {
		t.Fatal("a missing command must error")
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1, dispatchResultKind: 1})
	ev, _ := readRecords(t, path, dispatchResultKind)[0]["signature_evidence"].(string)
	if ev == "" {
		t.Error("signature_evidence must never be empty")
	}
}

// A successful ccpool dispatch (launched) writes exactly one dispatch_end
// "completed" naming the session, after its dispatch_start.
func TestDispatch_ccpoolSuccessWritesOneDispatchEnd(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	log, path := newTestLog(t)
	deps := newExec(cc, bd, cfg).deps
	deps.Log = log
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w", Type: "task"}}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1, DispatchEndKind: 1})
	rec := readRecords(t, path, DispatchEndKind)[0]
	if rec["outcome"] != OutcomeCompleted || rec["session"] != "pg-router-worker-zr-w" || rec["item_id"] != "zr-w" || rec["role"] != d.Role.Name {
		t.Errorf("record = %v", rec)
	}
	if got := dispatchKindsInOrder(t, path); got[0] != DispatchStartKind || got[len(got)-1] != DispatchEndKind {
		t.Errorf("dispatch records in order = %v, want dispatch_start first and dispatch_end last", got)
	}
}

// dispatchKindsInOrder returns the dispatch_* kinds in log order.
func dispatchKindsInOrder(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		for _, kind := range []string{DispatchStartKind, DispatchEndKind, dispatchResultKind, DispatchDeclineKind} {
			if strings.Contains(line, `"kind":"`+kind+`"`) {
				out = append(out, kind)
			}
		}
	}
	return out
}

// A session that dies under the dispatch (the wait path) writes exactly one
// outcome, the dispatch_result failure record with the readers' fields, and no
// dispatch_end.
func TestDispatch_ccpoolSessionDeathWritesOneDispatchResult(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := noBudget(reviewRole(cfg))
	display := role.DisplayName(cfg.SessionPrefix, "bead-1")
	dead := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateErrored, CloseReason: "handler",
		Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:bead-1", ccpool.MetaKeyHeadSHA: "h1"},
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bead-1": {"in_progress"}}}
	turn := dead
	turn.Live, turn.State, turn.CloseReason = true, ccpool.StateWorking, ""
	fake := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{turn}, {dead}}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:bead-1",
		Item: item.Item{ID: "bead-1", Type: "task", Metadata: map[string]any{"repo": "example/repo", "pr_number": "7", "head_sha": "h1"}},
	}
	log, path := newTestLog(t)
	deps := newExec(fake, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Log = log
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err == nil {
		t.Fatal("a dead session must fail the dispatch")
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1, dispatchResultKind: 1})
	rec := readRecords(t, path, dispatchResultKind)[0]
	for _, f := range []string{"role", "pool", "bead", "session", "failure_signature", "signature_evidence"} {
		if _, ok := rec[f]; !ok {
			t.Errorf("failure record lacks field %q: %v", f, rec)
		}
	}
	if ev, _ := rec["signature_evidence"].(string); ev == "" {
		t.Error("signature_evidence must never be empty")
	}
	if got := dispatchKindsInOrder(t, path); got[0] != DispatchStartKind {
		t.Errorf("dispatch records in order = %v, want dispatch_start first", got)
	}
}

// A failure after the dispatch was accepted but before the wait (the nudge's
// send failed) still writes exactly one outcome: the dispatch_result failure record.
func TestDispatch_ccpoolSendFailureWritesOneDispatchResult(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend, ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateReady}}}}
	log, path := newTestLog(t)
	deps := newExec(cc, bd, cfg).deps
	deps.Log = log
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w", Type: "task"}}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err == nil {
		t.Fatal("a send failure must error")
	}
	wantCounts(t, path, map[string]int{DispatchStartKind: 1, dispatchResultKind: 1})
	rec := readRecords(t, path, dispatchResultKind)[0]
	if rec["session"] != "pg-router-worker-zr-w" || rec["bead"] != "zr-w" || rec["pool"] != "default" {
		t.Errorf("record = %v", rec)
	}
	if sig, _ := rec["failure_signature"].(string); !failsig.Signature(sig).Valid() {
		t.Errorf("failure_signature = %v, want a member of the closed set", rec["failure_signature"])
	}
}

// A dispatch that was never accepted (a failed launch is a decline) has no
// dispatch_start, so the executor writes no outcome for it either.
func TestDispatch_ccpoolLaunchFailureWritesNoOutcome(t *testing.T) {
	cc := &dtest.FakeCC{EnsureErr: context.DeadlineExceeded}
	h := newAbandonedHarness(t, cc, workerRole)
	log, path := newTestLog(t)
	h.run.deps.Log = log
	_ = h.dispatch(context.Background())
	if b, _ := os.ReadFile(path); strings.Contains(string(b), `"kind":"dispatch_`) {
		t.Errorf("a declined launch must write no executor dispatch record: %s", b)
	}
}
