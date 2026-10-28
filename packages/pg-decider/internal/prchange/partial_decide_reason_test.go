package prchange

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// The reason a partial decide gives. The decider writes one line per failed
// action (op, work item, rule, outcome, error text) next to its run counters
// line. pg-pr-change re-wraps those lines into its own failure line, and the
// backfill driver prints the reason of each failed PR on its progress line, so
// the check log of a scenario that ends `decide_partial=<repo#n>` names the
// failing action and its error. The driver is run over the real pg-pr-change
// code (Run, in process) with the package's double of the three tools.

const failedUpdateLine = "pg-decider: update bd-7a (rule anchor.backfill): failed: connector issue update: unsupported operation"

// partialDecideScript is a script whose apply fails one `update` action.
func partialDecideScript(stderr string) *script {
	return hydrated().
		on("pg-decider plan pr", ok(planWithWork)).
		on("pg-decider apply", exit(2, "", applyCounters+"\n"+stderr))
}

func TestAPartialDecideFailureLineNamesTheFailingActionAndItsError(t *testing.T) {
	s := partialDecideScript(failedUpdateLine + "\n")
	o, _, errb := opts(t, ModeApply, s)
	if got := Run(context.Background(), o); got != ExitDecidePartial {
		t.Fatalf("exit %d, want %d: %s", got, ExitDecidePartial, errb)
	}
	var failure string
	for _, l := range strings.Split(errb.String(), "\n") {
		if strings.HasPrefix(l, "pg-pr-change: ") && strings.Contains(l, "class=decide_partial") {
			failure = l
		}
	}
	for _, want := range []string{"update bd-7a", "anchor.backfill", "unsupported operation"} {
		if !strings.Contains(failure, want) {
			t.Errorf("failure line %q lacks %q", failure, want)
		}
	}
}

func TestTheBackfillProgressLineOfAPartialDecideNamesTheFailingAction(t *testing.T) {
	rec := filepath.Join(t.TempDir(), RunRecordFileName)
	var progress bytes.Buffer
	runner := func(ctx context.Context, c backfill.Call) backfill.Result {
		s := partialDecideScript(failedUpdateLine + "\n")
		o, out, errb := opts(t, c.Args[len(c.Args)-2], s)
		o.RawID = c.Args[len(c.Args)-1]
		o.RunRecordPath = rec
		o.LockWait = time.Second
		code := Run(ctx, o)
		return backfill.Result{Code: code, Stdout: []byte(out.String()), Stderr: []byte(errb.String())}
	}
	set := &backfill.Set{Contract: backfill.SetContract, Entries: []backfill.Entry{{PR: testID, State: backfill.StateOpen, From: []string{backfill.FromQuery}}}}
	res, err := backfill.Run(context.Background(), backfill.RunOptions{
		Mode: backfill.ModeApply, Set: set, RunRecord: rec, NoLimits: true,
		Rate:   rateOf(9000),
		Runner: runner, Progress: &progress, Sleep: func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != backfill.OutcomeFailed || len(res.Failed) != 1 || res.Failed[0].Class != "decide_partial" {
		t.Fatalf("result %+v: %s", res, res.Line)
	}
	if !strings.HasSuffix(res.Line, " decide_partial="+testID) {
		t.Fatalf("the last line changed shape: %q", res.Line)
	}
	for _, want := range []string{"fail " + testID + ": exit 30 (decide_partial): ", "update bd-7a", "anchor.backfill", "unsupported operation"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress %q lacks %q", progress.String(), want)
		}
	}
	if strings.Contains(progress.String(), "run-counters") {
		t.Errorf("the progress line shows the counters line instead of the reason: %q", progress.String())
	}
}

type rateOf int

func (r rateOf) Newest(context.Context) (backfill.Reading, error) {
	return backfill.Reading{Remaining: int(r), Source: "fake"}, nil
}
