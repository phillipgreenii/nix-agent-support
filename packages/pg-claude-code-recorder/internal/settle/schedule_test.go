package settle

import (
	"testing"
	"time"
)

// The durations are arbitrary but MUST differ from each other (After 10m,
// Recheck 3m, RemoveAfter 30m) and from the boundary step, or a swapped
// duration would give the same deadline and the test would not notice. The
// step is one second: it is the smallest probe that is far from time.Time
// resolution noise, and anything near a whole duration would cross into a
// different case instead of probing the boundary.
const (
	schedAfter       = 10 * time.Minute
	schedRecheck     = 3 * time.Minute
	schedRemoveAfter = 30 * time.Minute
	schedStep        = time.Second
)

var schedNow = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

func schedBase(stage string) schedInput {
	return schedInput{
		stage: stage, now: schedNow, removeEnable: true,
		removeAfter: schedRemoveAfter, recheck: schedRecheck, after: schedAfter,
		newest:    func() time.Time { panic("newest read when the stage does not need it") },
		checkedAt: func() time.Time { panic("checkedAt read when the stage does not need it") },
	}
}

func TestScheduleDecision(t *testing.T) {
	newestAt := func(ago time.Duration) func() time.Time {
		return func() time.Time { return schedNow.Add(-ago) }
	}
	never := func() time.Time { return time.Time{} }
	tests := []struct {
		name    string
		mod     func(in *schedInput)
		wantAct schedAction
		wantAt  time.Time
	}{
		{"errored pushes now whatever the stage", func(in *schedInput) { in.errored = true; in.stage = StageSettled }, schedPush, schedNow},
		{"quiet waits After past newest", func(in *schedInput) { in.newest = newestAt(time.Minute) }, schedPush, schedNow.Add(-time.Minute + schedAfter)},
		{"no stage waits After past newest", func(in *schedInput) { in.stage = ""; in.newest = newestAt(time.Minute) }, schedPush, schedNow.Add(-time.Minute + schedAfter)},
		{"quiet already past due is pushed at its past time", func(in *schedInput) { in.newest = newestAt(2 * schedAfter) }, schedPush, schedNow.Add(-schedAfter)},
		{"held with no check waits Recheck from now", func(in *schedInput) { in.stage = StageHeld; in.checkedAt = never }, schedPush, schedNow.Add(schedRecheck)},
		{"held waits Recheck from the last check", func(in *schedInput) {
			in.stage = StageHeld
			in.checkedAt = func() time.Time { return schedNow.Add(-schedStep) }
		}, schedPush, schedNow.Add(-schedStep + schedRecheck)},
		{"settled with removal off has no deadline", func(in *schedInput) { in.stage = StageSettled; in.removeEnable = false }, schedNone, time.Time{}},
		{"settled pushes a removal deadline", func(in *schedInput) { in.stage = StageSettled; in.newest = newestAt(time.Minute) }, schedPushRemoval, schedNow.Add(-time.Minute + schedRemoveAfter)},
		{"settled one under due with a removal pass is pushed", func(in *schedInput) {
			in.stage = StageSettled
			in.removalPass = true
			in.newest = newestAt(schedRemoveAfter - schedStep)
		}, schedPushRemoval, schedNow.Add(schedStep)},
		{"settled exactly due with a removal pass runs now", func(in *schedInput) {
			in.stage = StageSettled
			in.removalPass = true
			in.newest = newestAt(schedRemoveAfter)
		}, schedRemovalNow, schedNow},
		{"settled one over due with a removal pass runs now", func(in *schedInput) {
			in.stage = StageSettled
			in.removalPass = true
			in.newest = newestAt(schedRemoveAfter + schedStep)
		}, schedRemovalNow, schedNow.Add(-schedStep)},
		{"settled already due without a removal pass is pushed", func(in *schedInput) {
			in.stage = StageSettled
			in.newest = newestAt(schedRemoveAfter + schedStep)
		}, schedPushRemoval, schedNow.Add(-schedStep)},
		{"other stages have no deadline", func(in *schedInput) { in.stage = StageLive }, schedNone, time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := schedBase(StageQuiet)
			tc.mod(&in)
			act, at := scheduleDecision(in)
			if act != tc.wantAct {
				t.Fatalf("action = %d, want %d", act, tc.wantAct)
			}
			if !at.Equal(tc.wantAt) {
				t.Fatalf("at = %v, want %v", at, tc.wantAt)
			}
		})
	}
}
