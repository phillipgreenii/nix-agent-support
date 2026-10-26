package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeBd is a tracker double that understands the bd calls the rollback makes
// (list, comments, comments add, update, close, reopen). The real bd runs in the
// end-to-end drill (internal/prchange); this one lets a test make the tracker
// misbehave (ignore an update) and read what was asked.
type fakeBd struct {
	items    map[string]*Item
	comments map[string][]fakeComment
	calls    [][]string
	// ignoreUpdates makes `update` succeed and change nothing.
	ignoreUpdates bool
	// now stamps the comments added through bd (zero: the test clock t0 plus an hour).
	now time.Time
}

type fakeComment struct {
	id, text string
	at       time.Time
}

func newFakeBd() *fakeBd {
	return &fakeBd{items: map[string]*Item{}, comments: map[string][]fakeComment{}}
}

func (f *fakeBd) put(it Item) *Item {
	cp := it
	f.items[it.ID] = &cp
	return &cp
}

func (f *fakeBd) audit(id, op, rule string, at time.Time) {
	f.comments[id] = append(f.comments[id], fakeComment{
		id:   fmt.Sprintf("c%d", len(f.comments[id])+1),
		text: fmt.Sprintf("%s\nrule: %s\nop: %s\nfacts: {}\nseq: none\nat: %s\n", AuditHeader, rule, op, at.UTC().Format(time.RFC3339)),
	})
}

func (f *fakeBd) run(_ context.Context, c Call) Result {
	args := c.Args
	if len(args) >= 2 && args[0] == "--actor" {
		args = args[2:]
	}
	f.calls = append(f.calls, args)
	out := func(v any) Result {
		b, _ := json.Marshal(map[string]any{"data": v})
		return Result{Stdout: string(b)}
	}
	switch args[0] {
	case "list":
		var all []Item
		for _, it := range f.items {
			all = append(all, *it)
		}
		return out(all)
	case "comments":
		if args[1] == "add" {
			f.comments[args[2]] = append(f.comments[args[2]], fakeComment{id: "n" + strconv.Itoa(len(f.comments[args[2]])), text: args[3], at: f.now})
			return Result{}
		}
		var cs []map[string]string
		for _, c := range f.comments[args[1]] {
			at := c.at
			if at.IsZero() {
				at = t0
			}
			cs = append(cs, map[string]string{"id": c.id, "text": c.text, "created_at": at.UTC().Format(time.RFC3339)})
		}
		return out(cs)
	case "close":
		f.items[args[1]].Status = "closed"
	case "reopen":
		f.items[args[1]].Status = "open"
	case "update":
		if f.ignoreUpdates {
			return Result{}
		}
		it := f.items[args[1]]
		for i := 2; i < len(args); i += 2 {
			v := args[i+1]
			switch args[i] {
			case "--title":
				it.Title = v
			case "--priority":
				it.Priority, _ = strconv.Atoi(v)
			case "--description":
				it.Description = v
			case "--add-label":
				it.Labels = append(it.Labels, v)
			case "--remove-label":
				var keep []string
				for _, l := range it.Labels {
					if l != v {
						keep = append(keep, l)
					}
				}
				it.Labels = keep
			case "--set-metadata":
				k, val, _ := strings.Cut(v, "=")
				if it.Metadata == nil {
					it.Metadata = map[string]any{}
				}
				it.Metadata[k] = val
			case "--unset-metadata":
				delete(it.Metadata, v)
			default:
				return Result{Code: 2, Stderr: "fake bd: unknown flag " + args[i]}
			}
		}
	default:
		return Result{Code: 2, Stderr: "fake bd: unknown verb " + args[0]}
	}
	return Result{}
}

func (f *fakeBd) tracker() Tracker { return Tracker{Dir: "/x", Runner: f.run} }

func (f *fakeBd) writeCalls() int {
	n := 0
	for _, c := range f.calls {
		switch c[0] {
		case "close", "reopen", "update":
			n++
		case "comments":
			if c[1] == "add" {
				n++
			}
		}
	}
	return n
}

var t0 = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// drillTracker is a tracker holding what the first apply leaves: an anchor the
// lane updated, closed and reopened (so it ends open), a review item the lane
// updated and a review item the lane created. It returns the before-image.
func drillTracker(t *testing.T) (*fakeBd, *Snapshot) {
	t.Helper()
	f := newFakeBd()
	f.now = t0.Add(time.Hour)
	f.put(Item{ID: "a-1", Title: "acme/api#1: anchor", Status: "open", Priority: 2, Labels: []string{"x"}, Metadata: map[string]any{"pr_number": "1"}, UpdatedAt: t0.Add(-time.Hour).Format(time.RFC3339)})
	f.put(Item{ID: "r-1", Title: "review-pr: acme/api#1 @ aaaa", Status: "open", Priority: 2, Metadata: map[string]any{"head_sha": "aaaa"}})
	f.put(Item{ID: "z-1", Title: "untouched", Status: "open", Priority: 3, UpdatedAt: t0.Add(-time.Hour).Format(time.RFC3339)})
	snap, err := f.tracker().TakeSnapshot(context.Background(), func() time.Time { return t0.Add(-time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	// The lane's writes, and the audit comment each left.
	a := f.items["a-1"]
	a.Metadata["dedup_key"] = "pr:N1:anchor"
	a.Priority = 1
	a.Labels = []string{"x", "p1"}
	a.UpdatedAt = t0.Add(5 * time.Minute).Format(time.RFC3339)
	f.audit("a-1", "update", "adoption", t0.Add(1*time.Minute))
	f.audit("a-1", "close", "closure", t0.Add(3*time.Minute))
	f.audit("a-1", "reopen", "reopen", t0.Add(4*time.Minute))
	r := f.items["r-1"]
	r.Title, r.Metadata = "review-pr: acme/api#1 @ bbbb", map[string]any{"head_sha": "bbbb", "dedup_key": "k"}
	r.UpdatedAt = t0.Add(2 * time.Minute).Format(time.RFC3339)
	f.audit("r-1", "update", "review.head-advanced", t0.Add(2*time.Minute))
	f.put(Item{ID: "r-2", Title: "review-pr: acme/api#1 @ cccc", Status: "open", Priority: 2, UpdatedAt: t0.Add(2 * time.Minute).Format(time.RFC3339)})
	f.audit("r-2", "create", "review.head-advanced", t0.Add(2*time.Minute+30*time.Second))
	return f, snap
}

func plan(t *testing.T, f *fakeBd, snap *Snapshot, bound time.Time) (*RollbackPlan, error) {
	t.Helper()
	tr := f.tracker()
	items, err := tr.ListItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ws, err := tr.ListAuditWrites(context.Background(), items, bound)
	if err != nil {
		t.Fatal(err)
	}
	return PlanRollback(snap, bound, items, ws)
}

func TestParseAuditReadsTheDeciderCommentAndNothingElse(t *testing.T) {
	rule, op, at, ok := ParseAudit("pg-decider audit\nrule: review.head-advanced\nop: create\nfacts: {}\nseq: 4\nat: 2026-10-05T12:00:00Z\n")
	if !ok || rule != "review.head-advanced" || op != "create" || !at.Equal(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("%q %q %v %v", rule, op, at, ok)
	}
	for _, bad := range []string{"", "hello", "pg-decider audit\nrule: r\nop: create\n", "not pg-decider audit\nrule: r\nop: x\nat: 2026-10-05T12:00:00Z"} {
		if _, _, _, ok := ParseAudit(bad); ok {
			t.Errorf("%q was read as an audit comment", bad)
		}
	}
}

func TestTheRollbackListsEveryWriteByTimestampAndRevertsEachOne(t *testing.T) {
	f, snap := drillTracker(t)
	p, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, w := range p.Writes {
		ops = append(ops, w.Item+" "+w.Op)
	}
	want := "a-1 update|r-1 update|r-2 create|a-1 close|a-1 reopen"
	if got := strings.Join(ops, "|"); got != want {
		t.Fatalf("listing %q, want %q", got, want)
	}
	before := f.writeCalls()
	done, problems := p.Apply(context.Background(), f.tracker())
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if done == 0 || f.writeCalls() == before {
		t.Fatalf("nothing was reverted (done=%d)", done)
	}
	// The anchor is back as the before-image has it: fields restored, and the
	// close and reopen cancelled out.
	a := f.items["a-1"]
	if a.Status != "open" || a.Priority != 2 || a.Metadata["dedup_key"] != nil || strings.Join(a.Labels, ",") != "x" {
		t.Errorf("anchor: %+v", a)
	}
	if r := f.items["r-1"]; r.Title != "review-pr: acme/api#1 @ aaaa" || r.Metadata["head_sha"] != "aaaa" || r.Metadata["dedup_key"] != nil {
		t.Errorf("review item: %+v", r)
	}
	if f.items["r-2"].Status != "closed" {
		t.Errorf("the created item is %q, want closed", f.items["r-2"].Status)
	}
	if z := f.items["z-1"]; z.Priority != 3 || z.Status != "open" {
		t.Errorf("an item the lane never wrote changed: %+v", z)
	}
	for _, c := range f.calls {
		if len(c) > 1 && c[0] != "list" && c[0] != "comments" && c[1] == "z-1" {
			t.Errorf("a write reached the untouched item: %v", c)
		}
	}
}

func TestTheRollbackLeavesWhatAPersonChangedSinceTheLaneWroteIt(t *testing.T) {
	f, snap := drillTracker(t)
	// The lane closed and reopened the anchor; a person has closed it since. Closing
	// it again is no revert of the reopen, and reopening it would undo the person's
	// close: the status is left alone, the fields are still restored.
	f.items["a-1"].Status = "closed"
	p, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, s := range p.Steps {
		if s.Item == "a-1" {
			kinds = append(kinds, s.Kind)
		}
	}
	if got := strings.Join(kinds, ","); got != "restore,leave,leave" {
		t.Fatalf("steps for the anchor: %s", got)
	}
	if _, problems := p.Apply(context.Background(), f.tracker()); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
	if a := f.items["a-1"]; a.Status != "closed" || a.Priority != 2 {
		t.Errorf("anchor %+v: want the person's close kept and the fields restored", a)
	}
}

func TestAnItemAlreadyClosedByAPersonIsNotReopenedOrClosedTwice(t *testing.T) {
	f, snap := drillTracker(t)
	f.items["r-2"].Status = "closed" // a person closed the item the lane created
	p, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Steps {
		if s.Item == "r-2" {
			t.Errorf("a step for r-2, already closed: %+v", s)
		}
	}
}

func TestASnapshotTakenAfterTheFlipIsRefusedAndNothingIsWritten(t *testing.T) {
	f, snap := drillTracker(t)
	snap.T = t0.Add(time.Second)
	before := f.writeCalls()
	if _, err := plan(t, f, snap, t0); !errors.Is(err, ErrNoBeforeImage) {
		t.Fatalf("err %v, want ErrNoBeforeImage", err)
	}
	if f.writeCalls() != before {
		t.Fatal("a refused rollback wrote")
	}
}

func TestASnapshotThatDoesNotHoldAnItemTheLaneUpdatedIsRefused(t *testing.T) {
	f, snap := drillTracker(t)
	var keep []Item
	for _, it := range snap.Items {
		if it.ID != "r-1" {
			keep = append(keep, it)
		}
	}
	snap.Items = keep
	_, err := plan(t, f, snap, t0)
	if !errors.Is(err, ErrNoBeforeImage) || !strings.Contains(err.Error(), "r-1") {
		t.Fatalf("err %v", err)
	}
}

func TestASnapshotThatAlreadyHoldsAnItemTheLaneCreatedIsRefused(t *testing.T) {
	f, snap := drillTracker(t)
	snap.Items = append(snap.Items, *f.items["r-2"])
	if _, err := plan(t, f, snap, t0); !errors.Is(err, ErrNoBeforeImage) {
		t.Fatalf("err %v", err)
	}
}

func TestWritesBeforeTheBoundAreNotListed(t *testing.T) {
	f, snap := drillTracker(t)
	f.audit("z-1", "update", "old", t0.Add(-2*time.Hour))
	f.items["z-1"].UpdatedAt = t0.Add(time.Minute).Format(time.RFC3339)
	p, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range p.Writes {
		if w.Item == "z-1" {
			t.Errorf("a write stamped before the bound is listed: %+v", w)
		}
	}
}

func TestAnUpdateThatTheTrackerSilentlyDroppedFailsTheVerification(t *testing.T) {
	f, snap := drillTracker(t)
	p, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	f.ignoreUpdates = true
	_, problems := p.Apply(context.Background(), f.tracker())
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "differs from the before-image") {
		t.Fatalf("problems %v", problems)
	}
}

func TestADryRunPlanWritesNothing(t *testing.T) {
	f, snap := drillTracker(t)
	before := f.writeCalls()
	if _, err := plan(t, f, snap, t0); err != nil {
		t.Fatal(err)
	}
	if f.writeCalls() != before {
		t.Fatal("planning wrote")
	}
}

func TestTakeSnapshotFailsClosedOnAnEmptyTracker(t *testing.T) {
	f := newFakeBd()
	if _, err := f.tracker().TakeSnapshot(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "no work item") {
		t.Fatalf("err %v", err)
	}
}

func TestSnapshotFileRoundTripsAndAMissingOneIsRefused(t *testing.T) {
	f, snap := drillTracker(t)
	_ = f
	p := t.TempDir() + "/s.json"
	if _, err := LoadSnapshot(p); !errors.Is(err, ErrNoBeforeImage) {
		t.Fatalf("missing: %v", err)
	}
	if err := WriteSnapshot(p, snap); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(p)
	if err != nil || len(got.Items) != len(snap.Items) || !got.T.Equal(snap.T) {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestASecondRollbackListsNothingAndWritesNothing(t *testing.T) {
	f, snap := drillTracker(t)
	p, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, problems := p.Apply(context.Background(), f.tracker()); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
	p2, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Writes) != 0 || len(p2.Steps) != 0 {
		t.Fatalf("the second rollback lists %d writes and %d steps", len(p2.Writes), len(p2.Steps))
	}
}

func TestAnItemWhoseStepFailedGetsNoRollbackNoteAndIsListedAgain(t *testing.T) {
	f, snap := drillTracker(t)
	p, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	tr := f.tracker()
	inner := tr.Runner
	tr.Runner = func(ctx context.Context, c Call) Result {
		if len(c.Args) > 3 && c.Args[2] == "close" && c.Args[3] == "r-2" {
			return Result{Code: 1, Stderr: "boom"}
		}
		return inner(ctx, c)
	}
	_, problems := p.Apply(context.Background(), tr)
	if len(problems) == 0 {
		t.Fatal("a failed step was not reported")
	}
	p2, err := plan(t, f, snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	var again []string
	for _, w := range p2.Writes {
		again = append(again, w.Item)
	}
	if got := strings.Join(again, ","); got != "r-2" {
		t.Fatalf("listed again: %q, want only r-2", got)
	}
}
