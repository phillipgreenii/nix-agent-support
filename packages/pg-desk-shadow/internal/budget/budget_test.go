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

// originRow is a daemon kind=origin row: no graphql_reset_at; remaining only
// for the graphql bucket.
func originRow(at time.Time, bucket string, remaining *int) string {
	if remaining == nil {
		return fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"list","bucket":%q,"graphql_cost":0,"rest_requests":2,"rest_remaining":4000}`, at.Format(time.RFC3339Nano), bucket)
	}
	return fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"list","bucket":%q,"graphql_cost":3,"graphql_remaining":%d}`, at.Format(time.RFC3339Nano), bucket, *remaining)
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
	if err := os.WriteFile(scratchLog, []byte(originRow(now.Add(-30*time.Second), "graphql", &[]int{2400}[0])+"\n"), 0o600); err != nil {
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

func TestParseReadingsScratchTakesOriginRowsOnly(t *testing.T) {
	n := func(v int) *int { return &v }
	in := strings.Join([]string{
		originRow(now.Add(-3*time.Minute), "graphql", n(2500)),
		originRow(now.Add(-2*time.Minute), "rest_core", nil), // REST bucket: no reading
		// A kind=request row and a kind-less row never yield a scratch reading,
		// even when they carry the live-format fields.
		fmt.Sprintf(`{"time":%q,"kind":"request","graphql_remaining":10,"graphql_reset_at":%q}`, now.Add(-time.Minute).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339)),
		row(now.Add(-time.Minute), 10, now.Add(time.Hour)),
		// An origin row with no usable time is skipped.
		`{"time":"nope","kind":"origin","bucket":"graphql","graphql_cost":1,"graphql_remaining":5}`,
		originRow(now.Add(-time.Minute), "graphql", n(2300)),
	}, "\n")
	rs := ParseReadings(strings.NewReader(in), "scratch")
	if len(rs) != 2 || rs[0].Remaining != 2500 || rs[1].Remaining != 2300 {
		t.Fatalf("readings = %+v", rs)
	}
	// One window for the whole call: newest reading's time plus one hour.
	want := now.Add(-time.Minute).Add(time.Hour)
	for _, r := range rs {
		if !r.ResetAt.Equal(want) || r.Source != "scratch" {
			t.Errorf("reading %+v, want ResetAt %v and source scratch", r, want)
		}
	}
	// The same fixture parsed as a live source keeps the old rule, unchanged:
	// it never looks at kind, takes rows with graphql_reset_at, and ignores the
	// origin rows (which have none).
	live := ParseReadings(strings.NewReader(in), "live")
	if len(live) != 2 || live[0].Remaining != 10 || live[1].Remaining != 10 {
		t.Errorf("live readings = %+v, want the two rows with graphql_reset_at", live)
	}
}

func TestScratchOriginReadingDecidesAndLiveOldFormatStillDoes(t *testing.T) {
	dir := t.TempDir()
	scratchLog, live := filepath.Join(dir, "scratch.jsonl"), filepath.Join(dir, "live.jsonl")
	n := 1500
	if err := os.WriteFile(scratchLog, []byte(originRow(now.Add(-time.Minute), "graphql", &n)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := Gather(now, DefaultWindow, "", scratchLog)
	if err != nil || len(rs) != 1 || rs[0].Source != "scratch" {
		t.Fatalf("Gather = %+v, %v", rs, err)
	}
	if d := Decide(now, 2000, rs); !d.Known || !d.Skip || d.Remaining != 1500 || d.Source != "scratch" {
		t.Errorf("a fresh origin reading below the floor must skip: %+v", d)
	}
	// The live side keeps the old format and still yields a reading.
	if err := os.WriteFile(live, []byte(row(now.Add(-time.Minute), 1800, now.Add(10*time.Minute))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err = Gather(now, DefaultWindow, live, "")
	if err != nil || len(rs) != 1 || rs[0].Source != "live" || rs[0].Remaining != 1800 {
		t.Fatalf("live Gather = %+v, %v", rs, err)
	}
	// A rest-bucket origin row yields no reading, so the guard proceeds.
	if err := os.WriteFile(scratchLog, []byte(originRow(now.Add(-time.Minute), "rest_core", nil)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err = Gather(now, DefaultWindow, "", scratchLog)
	if err != nil || len(rs) != 0 {
		t.Fatalf("rest origin row: %+v, %v", rs, err)
	}
	if d := Decide(now, 2000, rs); d.Known || d.Skip {
		t.Errorf("a missing reading must not skip a slot: %+v", d)
	}
}

func TestScratchOriginReadingOlderThanTheWindowNeverDecides(t *testing.T) {
	dir := t.TempDir()
	scratchLog := filepath.Join(dir, "scratch.jsonl")
	n := 100
	body := originRow(now.Add(-DefaultWindow-time.Second), "graphql", &n) + "\n"
	if err := os.WriteFile(scratchLog, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := Gather(now, DefaultWindow, "", scratchLog)
	if err != nil || len(rs) != 0 {
		t.Fatalf("a reading older than DefaultWindow must be dropped: %+v, %v", rs, err)
	}
	if d := Decide(now, 2000, rs); d.Skip {
		t.Errorf("an aged-out reading must not skip a slot: %+v", d)
	}
	// Just inside the window it still decides.
	body += originRow(now.Add(-time.Minute), "graphql", &n) + "\n"
	if err := os.WriteFile(scratchLog, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, _ = Gather(now, DefaultWindow, "", scratchLog)
	if len(rs) != 1 || !Decide(now, 2000, rs).Skip {
		t.Errorf("readings = %+v", rs)
	}
}
