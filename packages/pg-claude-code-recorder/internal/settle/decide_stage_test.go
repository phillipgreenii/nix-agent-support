package settle

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// decideStage is pure: these tables build no store, copier or parser.
func TestDecideStageTable(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	const after, recheck = time.Hour, 10 * time.Minute
	old := now.Add(-2 * time.Hour)            // quiet longer than settle.after
	fresh := now.Add(-time.Minute)            // quiet shorter than settle.after
	sinceNS := now.Add(-time.Hour).UnixNano() // the hold began after old, before fresh

	type facts struct {
		live      bool
		row       string // "" means no row
		force     bool
		changed   bool
		newest    time.Time
		checkedAt time.Time
		lowSpace  bool
	}
	tests := []struct {
		name string
		in   facts
		// want
		clear    string
		proceed  bool
		wasHeld  bool
		stage    string
		skipped  string
		reason   string
		noReads  []string // reads that must not happen
		wantRead []string // reads that must happen
	}{
		{name: "live, no row", in: facts{live: true}, stage: StageLive, skipped: "live process", noReads: []string{"changed", "newest", "checked", "low"}},
		{name: "live, settled row clears", in: facts{live: true, row: StageSettled}, clear: "process", stage: StageLive, skipped: "live process", noReads: []string{"changed", "newest"}},
		{name: "live, held row clears", in: facts{live: true, row: StageHeld}, clear: "process", stage: StageLive, skipped: "live process", noReads: []string{"newest"}},
		{name: "removed reappears and then settles", in: facts{row: store.StageRemoved, newest: old}, clear: "path_reappeared", proceed: true, stage: StageQuiet, wantRead: []string{"newest"}},
		{name: "removed reappears but is still fresh", in: facts{row: store.StageRemoved, newest: fresh}, clear: "path_reappeared", stage: StageQuiet, skipped: "quiet age below settle.after"},
		{name: "removal pending skips", in: facts{row: store.StageRemovalPending}, stage: store.StageRemovalPending, skipped: "removal or extract in progress", noReads: []string{"changed", "newest"}},
		{name: "extracting skips", in: facts{row: store.StageExtracting}, stage: store.StageExtracting, skipped: "removal or extract in progress", noReads: []string{"changed", "newest"}},
		{name: "settled and unchanged skips", in: facts{row: StageSettled}, stage: StageSettled, skipped: "settled", noReads: []string{"newest"}, wantRead: []string{"changed"}},
		{name: "settled with new writes clears and settles", in: facts{row: StageSettled, changed: true, newest: old}, clear: "new_writes", proceed: true, stage: StageQuiet},
		{name: "settled with new writes clears but is fresh", in: facts{row: StageSettled, changed: true, newest: fresh}, clear: "new_writes", stage: StageQuiet, skipped: "quiet age below settle.after"},
		{name: "held with a write after the hold clears", in: facts{row: StageHeld, newest: now.Add(-time.Minute)}, clear: "new_writes", stage: StageQuiet, skipped: "quiet age below settle.after", noReads: []string{"checked"}},
		{name: "held, no new writes, recheck not due, skips", in: facts{row: StageHeld, newest: old, checkedAt: now.Add(-time.Minute)}, stage: StageHeld, skipped: "held, recheck not due", reason: "held-reason"},
		{name: "held, recheck not due but forced, settles as held", in: facts{row: StageHeld, newest: old, checkedAt: now.Add(-time.Minute), force: true}, proceed: true, wasHeld: true, stage: StageQuiet},
		{name: "held, never checked, settles as held", in: facts{row: StageHeld, newest: old}, proceed: true, wasHeld: true, stage: StageQuiet},
		{name: "held, recheck due, settles as held", in: facts{row: StageHeld, newest: old, checkedAt: now.Add(-recheck)}, proceed: true, wasHeld: true, stage: StageQuiet},
		{name: "no row, quiet long enough, settles", in: facts{newest: old}, proceed: true, stage: StageQuiet, noReads: []string{"changed", "checked", "low"}},
		{name: "no row, fresh, skips", in: facts{newest: fresh}, stage: StageQuiet, skipped: "quiet age below settle.after", wantRead: []string{"low"}, noReads: []string{"changed", "checked"}},
		{name: "no row, fresh, low space waives the gate", in: facts{newest: fresh, lowSpace: true}, proceed: true, stage: StageQuiet},
		{name: "no row, exactly settle.after, settles", in: facts{newest: now.Add(-after)}, proceed: true, stage: StageQuiet, noReads: []string{"low"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reads := map[string]int{}
			f := stageFacts{
				Live:    tc.in.live,
				HasRow:  tc.in.row != "",
				Row:     store.StageRow{Session: "s", Stage: tc.in.row, Reason: "held-reason", SinceNS: sinceNS},
				Force:   tc.in.force,
				Now:     now,
				After:   after,
				Recheck: recheck,
				ChangedSinceSettle: func() bool {
					reads["changed"]++
					return tc.in.changed
				},
				Newest:    func() time.Time { reads["newest"]++; return tc.in.newest },
				CheckedAt: func() time.Time { reads["checked"]++; return tc.in.checkedAt },
				LowSpace:  func() bool { reads["low"]++; return tc.in.lowSpace },
			}
			d := decideStage("s", f)
			if d.Clear != tc.clear || d.Proceed != tc.proceed || d.WasHeld != tc.wasHeld {
				t.Fatalf("clear %q proceed %v wasHeld %v, want %q %v %v", d.Clear, d.Proceed, d.WasHeld, tc.clear, tc.proceed, tc.wasHeld)
			}
			if d.Out.Session != "s" || d.Out.Stage != tc.stage || d.Out.Skipped != tc.skipped || d.Out.Reason != tc.reason {
				t.Fatalf("out %+v, want stage %q skipped %q reason %q", d.Out, tc.stage, tc.skipped, tc.reason)
			}
			for _, r := range tc.noReads {
				if reads[r] != 0 {
					t.Errorf("read %q %d times, want none", r, reads[r])
				}
			}
			for _, r := range tc.wantRead {
				if reads[r] == 0 {
					t.Errorf("did not read %q", r)
				}
			}
			for r, n := range reads {
				if n > 1 {
					t.Errorf("read %q %d times, want at most once", r, n)
				}
			}
		})
	}
}
