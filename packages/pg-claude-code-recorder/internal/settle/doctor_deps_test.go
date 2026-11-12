package settle

import (
	"context"
	"testing"
	"time"
)

// The Doctor runs from its own narrow dependencies, with no Settler: the
// store, the copier, a clock, the metrics and a few values and callbacks.
func TestDoctorRunsFromNarrowDependencies(t *testing.T) {
	e := newEnv(t, false)
	e.seed(5, time.Hour)
	var marked, stopChecks int
	doc := NewDoctor(DoctorDeps{
		Store: e.st, Copier: e.cp, Clock: e.clk, Obs: e.obs, ClaudeDir: e.claude,
		Mtime:     func(string) (time.Time, error) { return time.Time{}, nil },
		Stopping:  func() bool { stopChecks++; return false },
		OnFullRun: func() { marked++ },
	})
	ans, err := doc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ans.FilesChecked != 1 || len(ans.Mismatches) != 0 || ans.TookOver != nil {
		t.Fatalf("%+v", ans)
	}
	if marked != 1 {
		t.Fatalf("OnFullRun called %d times, want 1 (bytes_per_slice is 0: the synchronous run)", marked)
	}
	if got, ok := LoadDoctor(e.state); !ok || got.AtNS != e.clk.Now().UnixNano() {
		t.Fatalf("result file: %v %+v", ok, got)
	}
	// The rolling tick is off when bytes_per_slice is 0.
	if err := doc.Tick(context.Background()); err != nil || stopChecks != 1 {
		t.Fatalf("Tick: %v, %d stopping checks", err, stopChecks)
	}
}

// With a budget the Doctor completes a rolling cycle by itself and leaves the
// removal-gauge callback alone (only the synchronous run calls it).
func TestDoctorRollingCycleFromNarrowDependencies(t *testing.T) {
	e := newEnv(t, false)
	e.seed(5, time.Hour)
	marked := 0
	doc := NewDoctor(DoctorDeps{
		Store: e.st, Copier: e.cp, Clock: e.clk, Obs: e.obs, ClaudeDir: e.claude,
		Mtime:         func(string) (time.Time, error) { return time.Time{}, nil },
		Interval:      24 * time.Hour,
		BytesPerSlice: 1 << 30,
		SliceEvery:    time.Minute,
		OnFullRun:     func() { marked++ },
	})
	ans, err := doc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ans.FilesChecked != 1 || len(ans.Mismatches) != 0 || marked != 0 {
		t.Fatalf("%+v marked=%d", ans, marked)
	}
	if doc.loadCursor().Active {
		t.Fatal("the cycle is still open")
	}
}
