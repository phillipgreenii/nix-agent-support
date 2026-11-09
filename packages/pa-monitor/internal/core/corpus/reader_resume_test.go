package corpus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
)

// A slice that cannot finish (a Scan's read timeout) must leave the cursor
// taken and the slices already done kept, so the next Scan resumes the
// inventory instead of restarting it.
func TestInventory_TimeoutKeepsProgressAndCursor(t *testing.T) {
	e := corpustest.Start(t)
	e.Register(111, "s1", cwd, "Alpha", true)
	e.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "hi"))
	e.Put(corpustest.StatusRel(cwd, "s1"), statusLine("s1", t0.Unix(), 10, t0.Unix()+3600))

	r := newReader(e.Facts)
	ctx := context.Background()
	c, err := e.Facts.Client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.bootstrap(ctx, c, t0.Add(-48*time.Hour), nil); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if r.cursor == "" {
		t.Fatal("cursor must be taken before the inventory")
	}
	total := len(r.pending)
	if total < 3 {
		t.Fatalf("expected status plus several transcript windows, got %d slices", total)
	}

	// The second slice times out.
	runs := 0
	r.gate = func() error {
		runs++
		if runs == 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := r.resumeInventory(ctx, c); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the timeout, got %v", err)
	}
	if got := len(r.pending); got != total-1 {
		t.Fatalf("pending after timeout = %d, want %d (first slice kept)", got, total-1)
	}
	if len(r.files) == 0 {
		t.Fatal("status file of the finished first slice was lost")
	}
	cursor := r.cursor

	// The next run continues from the failed slice, and never re-takes the cursor.
	runs = 100
	if err := r.resumeInventory(ctx, c); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(r.pending) != 0 || r.cursor != cursor {
		t.Fatalf("pending=%d cursor changed=%v", len(r.pending), r.cursor != cursor)
	}
	if runs != 100+total-1 {
		t.Fatalf("resume ran %d slices, want %d", runs-100, total-1)
	}
	kinds := map[fileKind]int{}
	for _, f := range r.files {
		kinds[f.kind]++
	}
	if kinds[kindStatus] != 1 {
		t.Fatalf("files by kind = %v, want the status file", kinds)
	}
}

func TestPlanInventory_WindowsCoverCutoffToNowNewestFirst(t *testing.T) {
	cutoff := t0
	plan := planInventory(cutoff, t0.Add(60*time.Hour), []string{"s1"})
	// status, 3 windows (24h+24h+open), active session
	if len(plan) != 5 {
		t.Fatalf("plan has %d slices, want 5", len(plan))
	}
	last := plan[1].All[1].Range
	if last.Lt != nil || last.Gte != t0.Add(48*time.Hour).Format(time.RFC3339) {
		t.Fatalf("newest window must be open-ended from 48h: %+v", last)
	}
}
