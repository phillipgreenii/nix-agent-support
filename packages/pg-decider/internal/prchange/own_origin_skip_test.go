package prchange

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/apply"
)

// The own-origin guard (INV-SKIP-1, pg2-aoehwj): a delivery caused by the
// decider's own annotation write ends before the lock.

func TestOwnOriginIsTheDeciderOrigin(t *testing.T) {
	if got := apply.Origin("pr"); got != "decider:pr-decider" {
		t.Fatalf("apply.Origin(pr) = %q, want decider:pr-decider", got)
	}
}

func TestOwnOriginDeliverySkipsBeforeTheLock(t *testing.T) {
	for _, mode := range []string{ModeApply, ModePlan} {
		t.Run(mode, func(t *testing.T) {
			s := &script{} // lane-record only (opts adds it); any other call fails the test
			o, out, errb := opts(t, mode, s)
			o.Origin = apply.Origin("pr")
			o.Seq = "<no value>"
			o.RawID = testID + "@0123456789ab"
			o.RunRecordPath = filepath.Join(t.TempDir(), "runs.jsonl")
			// Negative control: another process holds the per-PR lock for longer than
			// the lock wait. A run that reached the lock would time out (exit 75);
			// the skip returns at once.
			held, err := AcquireLock(context.Background(), o.LockDir, testID, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			o.LockWait = 10 * time.Second

			t0 := time.Now()
			if got := Run(context.Background(), o); got != ExitOK {
				t.Fatalf("exit %d, want 0: %s", got, errb)
			}
			if d := time.Since(t0); d > 2*time.Second {
				t.Errorf("the skip took %s: it waited at the lock", d)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want none", out)
			}

			// No hydrate, view read, decide or apply: only the skip counter call.
			if len(s.calls) != 1 || !strings.HasPrefix(s.calls[0], "pg-desk pr lane-record") {
				t.Fatalf("calls = %q, want exactly the lane-record skip counter", s.calls)
			}
			lanes := laneCalls(t, s)
			if len(lanes) != 1 || lanes[0].SkipReason != SkipOwnOrigin || lanes[0].PR != testID || lanes[0].Mode != mode {
				t.Fatalf("lane documents = %+v, want one skip of %s", lanes, testID)
			}
			// Nothing that writes lane state rides along.
			if in := lanes[0]; in.Residual != nil || in.LastOutcome != "" || len(in.Decisions) != 0 || in.Timings != nil || len(in.Events) != 0 {
				t.Errorf("the skip document carries run state: %+v", in)
			}

			recs := readRecords(t, o.RunRecordPath)
			if len(recs) != 1 {
				t.Fatalf("run records = %d, want 1", len(recs))
			}
			r := recs[0]
			if !r.Skipped || r.SkipReason != "own-origin" || r.Origin != apply.Origin("pr") || r.SkipCounter != "recorded" ||
				r.ExitCode != 0 || r.Lane.Result != "not_run" || r.Decide != nil || r.Hydrate.Result != hydrateNotRun || r.FinishedAt == "" {
				t.Errorf("record = %+v", r)
			}
			if r.Entity != testID || r.EventID != testID+"@0123456789ab" || r.Mode != mode {
				t.Errorf("record identity = %q %q %q", r.Entity, r.EventID, r.Mode)
			}
		})
	}
}

// A failure to bump the skip counter never fails the command; the record says so.
func TestOwnOriginSkipSurvivesACounterFailure(t *testing.T) {
	s := (&script{}).on("pg-desk pr lane-record", exit(1, "", "unknown field skip_reason"))
	o, _, errb := opts(t, ModeApply, s)
	o.Origin = apply.Origin("pr")
	o.RunRecordPath = filepath.Join(t.TempDir(), "runs.jsonl")
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	recs := readRecords(t, o.RunRecordPath)
	if len(recs) != 1 || !recs[0].Skipped || recs[0].SkipCounter != "failed" {
		t.Fatalf("records = %+v, want one skipped record with skip_counter failed", recs)
	}
}

// Every other origin runs: pg-desk (operator verbs, focus), free text, another
// type's decider, and every absent-meaning text.
func TestOtherOriginsRunTheDecider(t *testing.T) {
	for _, origin := range []string{
		"pg-desk", "free text", "decider:issue-decider", "decider:pr-decider-x", "Decider:pr-decider",
		"", "   ", "<no value>", "<nil>", "null", "NULL",
	} {
		t.Run("origin "+origin, func(t *testing.T) {
			s := hydrated().on("pg-decider plan pr", ok(planEmpty)).on("pg-decider apply", ok(planEmpty))
			o, _, errb := opts(t, ModeApply, s)
			o.Origin = origin
			o.RunRecordPath = filepath.Join(t.TempDir(), "runs.jsonl")
			if got := Run(context.Background(), o); got != ExitOK {
				t.Fatalf("exit %d: %s", got, errb)
			}
			if !s.called("pg-desk pr refresh") || !s.called("pg-decider apply pr") {
				t.Fatalf("the run did not hydrate and decide: %v", s.calls)
			}
			recs := readRecords(t, o.RunRecordPath)
			if len(recs) != 1 || recs[0].Skipped || recs[0].SkipReason != "" || recs[0].Lane.Result != "recorded" {
				t.Fatalf("record = %+v, want a normal recorded run", recs)
			}
			for _, in := range laneCalls(t, s) {
				if in.SkipReason != "" {
					t.Fatalf("a run wrote a skip document: %+v", in)
				}
			}
		})
	}
}

func TestNormalizeOriginAbsentTexts(t *testing.T) {
	for in, want := range map[string]string{
		"": "", " ": "", "<no value>": "", "<nil>": "", "null": "", " Null ": "",
		"pg-desk": "pg-desk", " decider:pr-decider ": "decider:pr-decider",
	} {
		if got := normalizeOrigin(in); got != want {
			t.Errorf("normalizeOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}
