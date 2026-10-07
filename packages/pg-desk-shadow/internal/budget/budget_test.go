package budget

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

func row(at time.Time, remaining int, reset time.Time) string {
	return fmt.Sprintf(`{"time":%q,"op":"list","graphql_remaining":%d,"graphql_reset_at":%q,"graphql_cost":1}`, at.Format(time.RFC3339Nano), remaining, reset.Format(time.RFC3339))
}

func TestFloor(t *testing.T) {
	for _, c := range []struct{ per, margin, want int }{{50, 200, 2000}, {50, 0, 2000}, {100, 200, 2400}, {0, 0, 2000}} {
		if got := Floor(c.per, c.margin); got != c.want {
			t.Errorf("Floor(%d,%d) = %d, want %d", c.per, c.margin, got, c.want)
		}
	}
}

func TestParseReadingsIgnoresStartAndHeartbeatRows(t *testing.T) {
	in := strings.Join([]string{
		`{"time":"2026-01-05T09:59:00Z","level":"info","msg":"files still running","op":"files","phase":"heartbeat"}`,
		row(now.Add(-time.Minute), 3000, now.Add(10*time.Minute)),
		`garbage`,
	}, "\n")
	rs := ParseReadings(strings.NewReader(in), "live")
	if len(rs) != 1 || rs[0].Remaining != 3000 {
		t.Fatalf("readings = %+v", rs)
	}
}

func TestDecideAtFloorAndBelow(t *testing.T) {
	reset := now.Add(10 * time.Minute)
	at := func(n int) Decision {
		return Decide(now, 2000, []Reading{{Time: now.Add(-time.Minute), Remaining: n, ResetAt: reset, Source: "live"}})
	}
	if d := at(2000); d.Skip || !d.Known {
		t.Errorf("at the floor the tick must run: %+v", d)
	}
	if d := at(1999); !d.Skip {
		t.Errorf("floor-1 must skip: %+v", d)
	}
}

func TestDecideMinimumAcrossWindowsNewestPerWindow(t *testing.T) {
	r1, r2 := now.Add(5*time.Minute), now.Add(15*time.Minute)
	d := Decide(now, 2000, []Reading{
		{Time: now.Add(-3 * time.Minute), Remaining: 1500, ResetAt: r1, Source: "live"},    // older reading, same window: superseded
		{Time: now.Add(-1 * time.Minute), Remaining: 3500, ResetAt: r1, Source: "scratch"}, // newest of window 1
		{Time: now.Add(-2 * time.Minute), Remaining: 2100, ResetAt: r2, Source: "live"},    // window 2
	})
	if d.Remaining != 2100 || d.Skip || d.Source != "live" {
		t.Errorf("decision = %+v, want min across windows 2100 from live and no skip", d)
	}
	d = Decide(now, 2000, []Reading{
		{Time: now.Add(-1 * time.Minute), Remaining: 3500, ResetAt: r1, Source: "scratch"},
		{Time: now.Add(-2 * time.Minute), Remaining: 1900, ResetAt: r2, Source: "live"},
	})
	if !d.Skip || d.Remaining != 1900 {
		t.Errorf("the lowest window must decide: %+v", d)
	}
}

func TestDecideLivePrecedenceOverStaleScratch(t *testing.T) {
	reset := now.Add(10 * time.Minute)
	d := Decide(now, 2000, []Reading{
		{Time: now.Add(-5 * time.Minute), Remaining: 1200, ResetAt: reset, Source: "scratch"},
		{Time: now.Add(-1 * time.Minute), Remaining: 2600, ResetAt: reset, Source: "live"},
	})
	if d.Skip || d.Source != "live" || d.Remaining != 2600 {
		t.Errorf("the newest reading of the window wins whichever log supplied it: %+v", d)
	}
}

func TestDecideDeadlockCases(t *testing.T) {
	// No readings at all (empty scratch log, quiet live log): proceed.
	if d := Decide(now, 2000, nil); d.Skip || d.Known {
		t.Errorf("no reading must not skip: %+v", d)
	}
	reset := now.Add(-3 * time.Second) // reset passed, still inside the 5 s grace
	low := []Reading{{Time: now.Add(-20 * time.Minute), Remaining: 100, ResetAt: reset, Source: "scratch"}}
	if d := Decide(now, 2000, low); !d.Skip {
		t.Errorf("inside the grace the old window still counts: %+v", d)
	}
	// After reset+5s the reading is void: skip, then resume (the deadlock case).
	later := now.Add(3 * time.Second)
	if d := Decide(later, 2000, low); d.Skip || d.Known {
		t.Errorf("after reset+5s the reading is void and the guard must resume: %+v", d)
	}
}

func TestGatherReadsLiveThenRotatedAndScratch(t *testing.T) {
	dir := t.TempDir()
	live, scratchLog := filepath.Join(dir, "events.jsonl"), filepath.Join(dir, "scratch.jsonl")
	reset := now.Add(10 * time.Minute)
	// The live file was just rotated: only the .1 has readings in the window.
	if err := os.WriteFile(live, []byte(`{"time":"2026-01-05T09:59:50Z","op":"list","phase":"start"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live+".1", []byte(row(now.Add(-2*time.Minute), 2500, reset)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scratchLog, []byte(row(now.Add(-30*time.Second), 2400, reset)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := Gather(now, DefaultWindow, live, scratchLog)
	if err != nil || len(rs) != 2 {
		t.Fatalf("Gather = %+v, %v", rs, err)
	}
	if d := Decide(now, 2000, rs); d.Remaining != 2400 || d.Source != "scratch" {
		t.Errorf("decision %+v", d)
	}
	// Missing files are fine.
	if rs, err := Gather(now, DefaultWindow, filepath.Join(dir, "none"), ""); err != nil || len(rs) != 0 {
		t.Errorf("missing logs: %v %v", rs, err)
	}
}
