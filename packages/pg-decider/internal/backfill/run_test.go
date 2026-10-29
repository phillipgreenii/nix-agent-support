package backfill

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newFake(t *testing.T, rec string) *fakeChange {
	return &fakeChange{t: t, path: rec, now: clock(), script: map[string]func(string) recLine{}, exit: map[string]int{}}
}

func TestRunStopsAtTheFloorAndReportsFLOORWithItsOwnExitCode(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	set := testSet("o/r#1", "o/r#2", "o/r#3", "o/r#4")
	// The budget is fine for two PRs, then falls under the floor.
	res, err := Run(context.Background(), newRun(t, ModePlan, set, rec, f, &fakeRate{vals: []int{5000, 4000, 2999}}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeFloor || res.Code != ExitFloor {
		t.Fatalf("outcome %s code %d: %s", res.Outcome, res.Code, res.Line)
	}
	if res.Counts.Done != 2 || res.Counts.Remaining != 2 || len(f.calls) != 2 {
		t.Fatalf("counts %+v calls %v", res.Counts, f.calls)
	}
	if !strings.HasPrefix(res.Line, "FLOOR ") || !strings.Contains(res.Line, "floor=3000 newest=2999") {
		t.Fatalf("last line %q", res.Line)
	}
}

func TestRunExitCodesAreDistinctAndNeverOneOrNine(t *testing.T) {
	codes := map[int]string{ExitDone: "DONE", ExitFloor: "FLOOR", ExitFailed: "FAILED", ExitStopped: "FAILED-stopped", ExitUsage: "usage", ExitSummaryFailed: "summary"}
	if len(codes) != 6 {
		t.Fatalf("two outcomes share an exit code: %v", codes)
	}
	for c := range codes {
		if c == 1 || c == 9 {
			t.Fatalf("exit code %d is reserved", c)
		}
	}
}

func TestResumeSkipsAPRWithAResultOfTheSameModeAndAModeSwitchRedoesAll(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	set := testSet("o/r#1", "o/r#2", "o/r#3")
	rate := &fakeRate{vals: []int{9000}}
	// An interrupted plan pass: only #1 finished.
	opts := newRun(t, ModePlan, testSet("o/r#1"), rec, f, rate)
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	res, err := Run(context.Background(), newRun(t, ModePlan, set, rec, f, rate))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDone || res.Counts.Skipped != 1 || res.Counts.Done != 2 {
		t.Fatalf("resume: %+v %s", res.Counts, res.Line)
	}
	if f.countOf("plan o/r#1") != 0 {
		t.Fatalf("a finished PR was redone: %v", f.calls)
	}
	// A plan-mode result never satisfies an apply-mode resume.
	f.calls = nil
	o := newRun(t, ModeApply, set, rec, f, rate)
	o.NoLimits = true
	res, err = Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts.Skipped != 0 || res.Counts.Done != 3 || len(f.calls) != 3 {
		t.Fatalf("mode switch: %+v calls %v", res.Counts, f.calls)
	}
}

func TestANewPassIgnoresTheRecordsOfEarlierPasses(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	set := testSet("o/r#1", "o/r#2")
	rate := &fakeRate{vals: []int{9000}}
	for i, newPass := range []bool{false, false, true} {
		f.calls = nil
		o := newRun(t, ModePlan, set, rec, f, rate)
		o.NewPass = newPass
		res, err := Run(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 1 {
			want = 2 // the second run of the same pass skips both
		}
		if res.Counts.Skipped != want {
			t.Fatalf("run %d (new pass %v): skipped %d, want %d: %s", i, newPass, res.Counts.Skipped, want, res.Line)
		}
	}
	if f.countOf("plan") != 2 {
		t.Fatalf("the new pass did not redo the set: %v", f.calls)
	}
}

func TestTransientAndDegradedRunsAreRecordedNamedAndDoNotStopTheRun(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	f.exit["o/r#2"], f.exit["o/r#3"] = 20, 21
	set := testSet("o/r#1", "o/r#2", "o/r#3", "o/r#4")
	res, err := Run(context.Background(), newRun(t, ModePlan, set, rec, f, &fakeRate{vals: []int{9000}}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeFailed || res.Code != ExitFailed || res.Stop != "" {
		t.Fatalf("outcome %s code %d stop %q: %s", res.Outcome, res.Code, res.Stop, res.Line)
	}
	if len(f.calls) != 4 {
		t.Fatalf("the run stopped early: %v", f.calls)
	}
	for _, w := range []string{"transient=o/r#2", "degraded=o/r#3", "done=2", "failed=2"} {
		if !strings.Contains(res.Line, w) {
			t.Errorf("last line lacks %q: %s", w, res.Line)
		}
	}
	// A re-run resumes: the finished PRs are skipped, the failed ones redone.
	f.calls, f.exit = nil, map[string]int{}
	res, err = Run(context.Background(), newRun(t, ModePlan, set, rec, f, &fakeRate{vals: []int{9000}}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDone || len(f.calls) != 2 || res.Counts.Skipped != 2 {
		t.Fatalf("re-run: %s calls %v", res.Line, f.calls)
	}
}

func TestAnUnauthenticatedOrDecideFailedRunStopsAtOnce(t *testing.T) {
	for code, class := range map[int]string{23: "unauthenticated", 31: "decide_failed"} {
		rec := tmpRecord(t)
		f := newFake(t, rec)
		f.exit["o/r#2"] = code
		res, err := Run(context.Background(), newRun(t, ModePlan, testSet("o/r#1", "o/r#2", "o/r#3"), rec, f, &fakeRate{vals: []int{9000}}))
		if err != nil {
			t.Fatal(err)
		}
		if res.Code != ExitStopped || !strings.HasPrefix(res.Line, "FAILED ") || !strings.Contains(res.Line, "stop=") || !strings.Contains(res.Line, class) {
			t.Fatalf("%s: code %d line %s", class, res.Code, res.Line)
		}
		if len(f.calls) != 2 || res.Counts.Remaining != 1 {
			t.Fatalf("%s: the run went on after the stop: %v %+v", class, f.calls, res.Counts)
		}
	}
}

func TestANotFoundResultIsFinalAndCountedApart(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	f.exit["o/r#2"] = 22
	set := testSet("o/r#1", "o/r#2")
	res, err := Run(context.Background(), newRun(t, ModePlan, set, rec, f, &fakeRate{vals: []int{9000}}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDone || res.Counts.NotFound != 1 {
		t.Fatalf("%s", res.Line)
	}
	f.calls = nil
	if _, err := Run(context.Background(), newRun(t, ModePlan, set, rec, f, &fakeRate{vals: []int{9000}})); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a not-found PR was run again: %v", f.calls)
	}
}

func createRec(kind string) func(string) recLine {
	return func(mode string) recLine {
		a := []Planned{{Rule: "anchor.lazy", Kind: kind, Op: "create"}}
		r := recLine{exit: 0, actions: a}
		if mode == ModeApply {
			r.applied = map[string]int{"anchor.lazy": 1}
			r.residual = intp(0)
		}
		return r
	}
}

func TestTheCreateLimitIsAStopCriterionCheckedAfterEachPRNotACap(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	for _, p := range []string{"o/r#1", "o/r#2", "o/r#3", "o/r#4"} {
		f.script[p] = createRec("anchor")
	}
	set := testSet("o/r#1", "o/r#2", "o/r#3", "o/r#4")
	o := newRun(t, ModeApply, set, rec, f, &fakeRate{vals: []int{9000}})
	o.Limits = map[string]int{"anchor": 2}
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	// The third PR's create was made (nothing prevents it), and the run stopped
	// right after it.
	if len(f.calls) != 3 || res.Code != ExitStopped || !strings.Contains(res.Stop, "create-limit") || !strings.Contains(res.Stop, "kind anchor has 3 creates, the expected number is 2") {
		t.Fatalf("calls %v code %d stop %q", f.calls, res.Code, res.Stop)
	}
	if !strings.HasPrefix(res.Line, "FAILED ") {
		t.Fatalf("line %q", res.Line)
	}
}

func TestACreateOfAKindWithNoLimitExceedsALimitOfZeroAndANoLimitRunIsExplicit(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	f.script["o/r#1"] = createRec("fix-ci")
	o := newRun(t, ModeApply, testSet("o/r#1", "o/r#2"), rec, f, &fakeRate{vals: []int{9000}})
	o.Limits = map[string]int{"anchor": 5}
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != ExitStopped || !strings.Contains(res.Stop, "kind fix-ci has 1 creates, the expected number is 0") {
		t.Fatalf("code %d stop %q", res.Code, res.Stop)
	}
	// No limits and no explicit opt-out: refused before anything runs.
	f2 := newFake(t, tmpRecord(t))
	o2 := newRun(t, ModeApply, testSet("o/r#1"), f2.path, f2, &fakeRate{vals: []int{9000}})
	if _, err := Run(context.Background(), o2); err == nil || !strings.Contains(err.Error(), "create limits") || len(f2.calls) != 0 {
		t.Fatalf("an apply pass without limits: err %v calls %v", err, f2.calls)
	}
}

func TestACreateLimitMeetingTheExpectedNumberDoesNotStopTheRun(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	for _, p := range []string{"o/r#1", "o/r#2"} {
		f.script[p] = createRec("anchor")
	}
	o := newRun(t, ModeApply, testSet("o/r#1", "o/r#2"), rec, f, &fakeRate{vals: []int{9000}})
	o.Limits = map[string]int{"anchor": 2}
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDone || res.Code != ExitDone {
		t.Fatalf("%s", res.Line)
	}
}

func TestAnUnreadableRateSignalFailsClosedAndNothingRuns(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	res, err := Run(context.Background(), newRun(t, ModePlan, testSet("o/r#1"), rec, f, &fakeRate{err: context.DeadlineExceeded}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != ExitStopped || !strings.Contains(res.Stop, "rate-signal-unavailable") || len(f.calls) != 0 {
		t.Fatalf("code %d stop %q calls %v", res.Code, res.Stop, f.calls)
	}
}

func TestACommandThatCannotBeStartedStopsTheRun(t *testing.T) {
	rec := tmpRecord(t)
	o := newRun(t, ModePlan, testSet("o/r#1", "o/r#2"), rec, newFake(t, rec), &fakeRate{vals: []int{9000}})
	n := 0
	o.Runner = func(context.Context, Call) Result { n++; return Result{Code: -1, StartErr: context.Canceled} }
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != ExitStopped || n != 1 {
		t.Fatalf("code %d calls %d: %s", res.Code, n, res.Line)
	}
}

func TestRunRefusesABadInvocation(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	good := newRun(t, ModePlan, testSet("o/r#1"), rec, f, &fakeRate{vals: []int{9000}})
	for name, mut := range map[string]func(*RunOptions){
		"a mode that is neither": func(o *RunOptions) { o.Mode = "sync" },
		"an empty set":           func(o *RunOptions) { o.Set = testSet() },
		"no rate source":         func(o *RunOptions) { o.Rate = nil },
		"no run record":          func(o *RunOptions) { o.RunRecord = "" },
	} {
		o := good
		mut(&o)
		if _, err := Run(context.Background(), o); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("a refused invocation ran the command: %v", f.calls)
	}
}

func TestThePaceWaitsBetweenPRsAndNotAfterTheLast(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	o := newRun(t, ModePlan, testSet("o/r#1", "o/r#2", "o/r#3"), rec, f, &fakeRate{vals: []int{9000}})
	o.Pace = 5 * time.Second
	var waits []time.Duration
	o.Sleep = func(_ context.Context, d time.Duration) { waits = append(waits, d) }
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 {
		t.Fatalf("waits %v, want two (between three PRs)", waits)
	}
}

func TestFailureReasonPrefersTheFailureLineOverTheCountersLine(t *testing.T) {
	counters := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"o/r#1","rules":{}}`
	failure := "pg-pr-change: the decider applied only part of its plan (stage=decide, class=decide_partial): update bd-7a (rule anchor.backfill): failed: unsupported: run pg-decider plan pr o/r#1"
	cases := []struct {
		name, stderr, want string
	}{
		{"counters first", counters + "\n" + failure + "\n", failure},
		{"no failure line: first line that is not the counters", counters + "\nsomething broke\n", "something broke"},
		{"only the counters line", counters + "\n", counters},
		{"empty", "", ""},
		{"error prefix", "Error: boom\n", "boom"},
	}
	for _, c := range cases {
		if got := FailureReason(c.stderr); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFailureReasonIsBoundedAndRedactsCredentials(t *testing.T) {
	long := FailureReason("pg-pr-change: x (stage=decide, class=decide_partial): " + strings.Repeat("é", 1000))
	if len(long) > MaxReasonBytes+len("...") || !strings.HasSuffix(long, "...") {
		t.Fatalf("not bounded: %d bytes", len(long))
	}
	got := FailureReason("pg-pr-change: x (stage=decide, class=decide_partial): fetch https://user:hunter2@host/x failed: Authorization: Bearer abc123 token=zzz9 password: pw1")
	for _, secret := range []string{"hunter2", "abc123", "zzz9", "pw1"} {
		if strings.Contains(got, secret) {
			t.Errorf("reason leaks %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "failed:") {
		t.Errorf("redaction ate the surrounding text: %s", got)
	}
}

func TestAPlanResultCarryingAPlanErrorIsNotFinalAndAResumeRunsThePRAgain(t *testing.T) {
	rec := tmpRecord(t)
	f := newFake(t, rec)
	f.script["o/r#1"] = func(string) recLine { return recLine{planErr: "work-items lookup timed out"} }
	set := testSet("o/r#1", "o/r#2")
	rate := &fakeRate{vals: []int{9000}}
	if _, err := Run(context.Background(), newRun(t, ModePlan, set, rec, f, rate)); err != nil {
		t.Fatal(err)
	}
	// The lookup recovers.
	delete(f.script, "o/r#1")
	f.calls = nil
	res, err := Run(context.Background(), newRun(t, ModePlan, set, rec, f, rate))
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts.Skipped != 1 || res.Counts.Done != 1 || f.countOf("plan") != 1 {
		t.Fatalf("the resume should skip #2 and retry #1: %s %v", res.Line, f.calls)
	}
	// An apply-mode record is untouched by the rule: only a plan result is retried.
	lg, _ := ReadLog(rec)
	if !finished(lg, ModePlan, "o/r#1", 0) {
		t.Fatal("the retried plan is not final")
	}
}

func TestASetFilePassDoesNotMoveTheResumePointerOfAnotherSet(t *testing.T) {
	rec := tmpRecord(t)
	dir := t.TempDir()
	real, small := filepath.Join(dir, "set.json"), filepath.Join(dir, "copy.json")
	f := newFake(t, rec)
	set := testSet("o/r#1", "o/r#2", "o/r#3")
	rate := &fakeRate{vals: []int{9000}}
	run := func(s *Set, file string, newPass bool) *RunResult {
		o := newRun(t, ModePlan, s, rec, f, rate)
		o.SetFile, o.NewPass = file, newPass
		res, err := Run(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	run(set, real, false)
	// A new pass over a one-PR copy, as the operator does to retry a few PRs.
	if res := run(testSet("o/r#2"), small, true); res.Counts.Done != 1 {
		t.Fatalf("%s", res.Line)
	}
	// A plain resume over the real set must resume: everything has a result.
	f.calls = nil
	res := run(set, real, false)
	if res.Counts.Skipped != 3 || f.countOf("plan") != 0 {
		t.Fatalf("the copy's pass moved the real set's resume pointer: %s", res.Line)
	}
	// The copy's own pointer is its own: a plain run over it skips its PR.
	if res := run(testSet("o/r#2"), small, false); res.Counts.Skipped != 1 {
		t.Fatalf("%s", res.Line)
	}
	// A new pass of the real set still redoes all of it.
	if res := run(set, real, true); res.Counts.Done != 3 {
		t.Fatalf("%s", res.Line)
	}
}
