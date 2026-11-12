package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// stubBd is a fake bd: a script found on PATH. It keeps its beads in
// $BD_STUB_DIR. BD_STUB_FAIL makes create fail before it files anything;
// BD_STUB_FAIL_AFTER makes create file the bead and then fail, as a crash
// between the tracker call and the row update would.
const stubBd = `#!/bin/sh
store="$BD_STUB_DIR"
if [ "$1" = "-C" ]; then echo "tracker $2" >> "$store/calls"; shift 2; fi
cmd=$1; shift
echo "$cmd" >> "$store/calls"
case $cmd in
list)
  key=""
  while [ $# -gt 0 ]; do if [ "$1" = "--external-ref" ]; then key=$2; shift; fi; shift; done
  if [ -f "$store/beads" ] && grep -q "|$key|" "$store/beads"; then
    id=$(grep "|$key|" "$store/beads" | head -1 | cut -d'|' -f1)
    printf '[{"id":"%s"}]\n' "$id"
  else
    echo '[]'
  fi ;;
create)
  key=""; label=""; title=""
  while [ $# -gt 0 ]; do
    case $1 in
      --external-ref) key=$2; shift ;;
      -l) label=$2; shift ;;
      --) shift; title=$1; break ;;
    esac
    shift
  done
  body=$(cat)
  if [ -n "$BD_STUB_FAIL" ]; then echo boom >&2; exit 1; fi
  n=$(( $(cat "$store/beads" 2>/dev/null | wc -l) + 1 ))
  id="stub-$n"
  echo "$id|$key|$label|$title" >> "$store/beads"
  printf '%s' "$body" > "$store/body-$n"
  if [ -n "$BD_STUB_FAIL_AFTER" ]; then echo crashed >&2; exit 1; fi
  echo "$id" ;;
esac
`

func installStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(stubBd), 0o755); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_STUB_DIR", store)
	t.Setenv("BD_STUB_FAIL", "")
	t.Setenv("BD_STUB_FAIL_AFTER", "")
	return store
}

func beadCount(t *testing.T, store string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(store, "beads"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// adopted seeds one item, shows it and adopts it with a label; it returns the decision.
func adopted(t *testing.T, l *Ledger, label string) (Item, Decision) {
	t.Helper()
	run := startRun(t, l, "agent-workflow", "demo")
	f := finding("alpha")
	f.Targets = []string{"pkg/a.md"}
	fileOnce(t, l, run, 12, []validate.EvidenceRow{evidence("alpha", nil, "")}, f)
	it := firstPending(t, l)
	if _, err := l.ShowItems(ctx, []int64{it.Number}); err != nil {
		t.Fatal(err)
	}
	res, err := l.RuleItems(ctx, []RuleRequest{{Number: it.Number, Ruling: RulingAdopted, TargetTracker: "/tmp/tracker", RepoLabel: label}}, 12)
	if err != nil {
		t.Fatal(err)
	}
	d, err := l.GetDecision(ctx, res[0].DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	return it, d
}

func firstPending(t *testing.T, l *Ledger) Item {
	t.Helper()
	items, err := l.PendingItems(ctx)
	if err != nil || len(items) == 0 {
		t.Fatalf("no pending items: %v", err)
	}
	return items[0]
}

func TestAdoptedFilesOneBeadAndIsIdempotent(t *testing.T) {
	store := installStub(t)
	l, c := openClock(t)
	_, d := adopted(t, l, "agent-support")
	// The ruling is stored first, with the bead still pending and a key.
	if d.BeadState != BeadPending || d.BeadIdempotencyKey == "" {
		t.Fatalf("stored decision: %+v", d)
	}
	if beadCount(t, store) != 0 {
		t.Fatal("a bead was filed before filing ran")
	}

	// A crash after the tracker call, before the row update: the claim is
	// held, the bead exists, the row still says filing.
	claimed, ok, err := l.claimDecision(ctx, d.ID)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	f := BdFiler{}
	if _, err := f.Create(ctx, BeadRequest{Key: claimed.BeadIdempotencyKey, Tracker: claimed.TargetTracker, Label: "agent-support", Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if beadCount(t, store) != 1 {
		t.Fatal("setup: expected one bead")
	}
	// Before the lease expires nobody else files it.
	res, err := FileAdoptedBeads(ctx, l, f)
	if err != nil || res.Filed != 0 {
		t.Fatalf("held lease: %+v %v", res, err)
	}
	c.Advance(BeadLease + time.Second)
	res, err = FileAdoptedBeads(ctx, l, f)
	if err != nil || res.Filed != 1 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if n := beadCount(t, store); n != 1 {
		t.Fatalf("retry filed a second bead: %d beads", n)
	}
	got, _ := l.GetDecision(ctx, d.ID)
	if got.BeadState != BeadFiled || got.WorkBead != "stub-1" {
		t.Fatalf("decision after retry: %+v", got)
	}
	// Running again is a no-op.
	if res, _ := FileAdoptedBeads(ctx, l, f); res.Filed != 0 || beadCount(t, store) != 1 {
		t.Fatalf("second pass: %+v", res)
	}
}

func TestAdoptedBeadBodyAndLabel(t *testing.T) {
	store := installStub(t)
	l, _ := openClock(t)
	adopted(t, l, "agent-support")
	if res, err := FileAdoptedBeads(ctx, l, BdFiler{}); err != nil || res.Filed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	body, _ := os.ReadFile(filepath.Join(store, "body-1"))
	if !strings.HasPrefix(string(body), "Untrusted origin: written by an automated analyst") {
		t.Errorf("body head: %q", firstLine(string(body)))
	}
	if !strings.Contains(string(body), "Item 1") && !strings.Contains(string(body), "Item ") {
		t.Errorf("body lacks the rendered item")
	}
	beads, _ := os.ReadFile(filepath.Join(store, "beads"))
	if !strings.Contains(string(beads), "|agent-support|") {
		t.Errorf("label not passed: %s", beads)
	}
	calls, _ := os.ReadFile(filepath.Join(store, "calls"))
	if !strings.Contains(string(calls), "tracker /tmp/tracker") {
		t.Errorf("tracker root not passed: %s", calls)
	}
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

func TestBdFailureLeavesRulingPendingThenFailsAfterFive(t *testing.T) {
	installStub(t)
	t.Setenv("BD_STUB_FAIL", "1")
	l, _ := openClock(t)
	it, d := adopted(t, l, "agent-support")
	for i := 1; i <= MaxBeadAttempts; i++ {
		res, err := FileAdoptedBeads(ctx, l, BdFiler{})
		if err != nil || res.Failed != 1 {
			t.Fatalf("attempt %d: %+v %v", i, res, err)
		}
		got, _ := l.GetDecision(ctx, d.ID)
		want := BeadPending
		if i == MaxBeadAttempts {
			want = BeadFailed
		}
		if got.BeadState != want || got.BeadAttempts != i {
			t.Fatalf("after attempt %d: state %s attempts %d, want %s", i, got.BeadState, got.BeadAttempts, want)
		}
	}
	// The ruling still stands.
	if tp, _ := l.GetTopic(ctx, d.TopicID); tp.State != StateAdopted {
		t.Errorf("topic state %s", tp.State)
	}
	if n, _ := l.OutboxFailed(ctx); n != 1 {
		t.Errorf("outbox_failed %d", n)
	}
	// Failed is terminal: maintain's retry does not touch it.
	if ids, _ := l.DueBeadDecisions(ctx); len(ids) != 0 {
		t.Errorf("failed decision is still due: %v", ids)
	}
	// Reopen resets it.
	if _, err := l.ShowItems(ctx, []int64{it.Number}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RuleItems(ctx, []RuleRequest{{Number: it.Number, Ruling: RulingReopen}}, 12); err != nil {
		t.Fatal(err)
	}
	got, _ := l.GetDecision(ctx, d.ID)
	if got.BeadState != BeadNone || got.BeadAttempts != 0 {
		t.Errorf("reopen left %s attempts %d", got.BeadState, got.BeadAttempts)
	}
	if n, _ := l.OutboxFailed(ctx); n != 0 {
		t.Errorf("outbox_failed after reopen: %d", n)
	}
}

func TestBeadCreatedThenCrashDoesNotDuplicate(t *testing.T) {
	store := installStub(t)
	t.Setenv("BD_STUB_FAIL_AFTER", "1")
	l, _ := openClock(t)
	adopted(t, l, "agent-support")
	if res, _ := FileAdoptedBeads(ctx, l, BdFiler{}); res.Failed != 1 {
		t.Fatalf("first pass: %+v", res)
	}
	t.Setenv("BD_STUB_FAIL_AFTER", "")
	res, err := FileAdoptedBeads(ctx, l, BdFiler{})
	if err != nil || res.Filed != 1 || beadCount(t, store) != 1 {
		t.Fatalf("second pass %+v %v, beads %d", res, err, beadCount(t, store))
	}
}

func TestMissingRepoLabelFailsVisibly(t *testing.T) {
	store := installStub(t)
	l, _ := openClock(t)
	_, d := adopted(t, l, "")
	res, _ := FileAdoptedBeads(ctx, l, BdFiler{})
	if res.Failed != 1 || beadCount(t, store) != 0 {
		t.Fatalf("%+v beads %d", res, beadCount(t, store))
	}
	if got, _ := l.GetDecision(ctx, d.ID); got.BeadAttempts != 1 || got.BeadState != BeadPending {
		t.Errorf("%+v", got)
	}
}

func TestClaimIsCompareAndSet(t *testing.T) {
	installStub(t)
	l, c := openClock(t)
	_, d := adopted(t, l, "x")
	if _, ok, _ := l.claimDecision(ctx, d.ID); !ok {
		t.Fatal("first claim failed")
	}
	if _, ok, _ := l.claimDecision(ctx, d.ID); ok {
		t.Fatal("a held claim was taken twice")
	}
	c.Advance(BeadLease - time.Second)
	if _, ok, _ := l.claimDecision(ctx, d.ID); ok {
		t.Fatal("claim taken before its lease expired")
	}
	c.Advance(2 * time.Second)
	got, ok, _ := l.claimDecision(ctx, d.ID)
	if !ok || got.BeadAttempts != 2 {
		t.Fatalf("expired lease not reclaimable: %v %+v", ok, got)
	}
}

func TestRuleNeedsFreshView(t *testing.T) {
	l, _ := openClock(t)
	run := startRun(t, l, "agent-workflow", "demo")
	fileOnce(t, l, run, 12, []validate.EvidenceRow{evidence("a", nil, ""), evidence("b", nil, "")}, finding("a"), finding("b"))
	items, _ := l.PendingItems(ctx)
	a, b := items[0].Number, items[1].Number
	rq := func(n int64) []RuleRequest { return []RuleRequest{{Number: n, Ruling: RulingRejected}} }
	if _, err := l.RuleItems(ctx, rq(a), 12); !errors.Is(err, ErrNotShown) {
		t.Fatalf("no view: %v", err)
	}
	if _, err := l.ShowItems(ctx, []int64{a, b}); err != nil {
		t.Fatal(err)
	}
	// The item changes after it was shown: auto-defer bumps the revision.
	c := l.now
	l.SetClock(func() time.Time { return c().Add(40 * 24 * time.Hour) })
	if _, err := l.AutoDefer(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RuleItems(ctx, rq(a), 12); !errors.Is(err, ErrStaleView) {
		t.Fatalf("stale view: %v", err)
	}
	// Nothing changed for the failed call.
	if tp, _ := l.GetTopic(ctx, items[0].TopicID); tp.State != StateDeferred {
		t.Errorf("state %s", tp.State)
	}
	if _, err := l.RuleItems(ctx, rq(99), 12); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestRuleSeveralIdsAndIdempotent(t *testing.T) {
	l, _ := openClock(t)
	run := startRun(t, l, "agent-workflow", "demo")
	fileOnce(t, l, run, 12, []validate.EvidenceRow{evidence("a", nil, ""), evidence("b", nil, "")}, finding("a"), finding("b"))
	items, _ := l.PendingItems(ctx)
	ids := []int64{items[0].Number, items[1].Number}
	if _, err := l.ShowItems(ctx, ids); err != nil {
		t.Fatal(err)
	}
	reqs := []RuleRequest{{Number: ids[0], Ruling: RulingRejected}, {Number: ids[1], Ruling: RulingRejected}}
	res, err := l.RuleItems(ctx, reqs, 12)
	if err != nil || len(res) != 2 || res[0].State != StateRejected || res[1].State != StateRejected {
		t.Fatalf("%+v %v", res, err)
	}
	// The same ruling again is idempotent, even though the views are now stale.
	res, err = l.RuleItems(ctx, reqs, 12)
	if err != nil || !res[0].Unchanged || !res[1].Unchanged {
		t.Fatalf("repeat: %+v %v", res, err)
	}
	var n int
	_ = l.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM decision`).Scan(&n)
	if n != 2 {
		t.Errorf("decisions %d", n)
	}
}

func TestRulePromotesWaiting(t *testing.T) {
	l, _ := openClock(t)
	run := startRun(t, l, "agent-workflow", "demo")
	fileOnce(t, l, run, 1, []validate.EvidenceRow{evidence("a", nil, ""), evidence("b", nil, "")}, finding("a"), finding("b"))
	c, _ := l.Counts(ctx)
	if c.Pending != 1 || c.Waiting != 1 {
		t.Fatalf("setup %+v", c)
	}
	it := firstPending(t, l)
	_, _ = l.ShowItems(ctx, []int64{it.Number})
	if _, err := l.RuleItems(ctx, []RuleRequest{{Number: it.Number, Ruling: RulingRejected}}, 1); err != nil {
		t.Fatal(err)
	}
	c, _ = l.Counts(ctx)
	if c.Pending != 1 || c.Waiting != 0 {
		t.Errorf("after rule %+v", c)
	}
}

func TestStaleLensesPerSchedule(t *testing.T) {
	l, c := openClock(t)
	mk := func(lens, sched string, from, to time.Time, end time.Time) {
		err := l.WithTx(ctx, func(tx *Tx) error {
			r, err := tx.InsertRun(ctx, Run{
				Lens: lens, Scope: "s", ScheduleID: sched, PID: 1, StartedAt: t0,
				WindowFrom: from.Format(time.RFC3339), WindowTo: to.Format(time.RFC3339),
			})
			if err != nil {
				return err
			}
			r.State, r.EndedAt = RunFiled, end
			return tx.UpdateRun(ctx, r)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	// lens a: a daily schedule last filed 3 days ago (stale), a weekly one filed yesterday.
	mk("a", "daily", t0.Add(-4*day), t0.Add(-3*day), t0.Add(-3*day))
	mk("a", "weekly", t0.Add(-8*day), t0.Add(-1*day), t0.Add(-1*day))
	// lens b: weekly, filed 10 days ago, window 7 days: not past 2x.
	mk("b", "weekly", t0.Add(-17*day), t0.Add(-10*day), t0.Add(-10*day))
	// lens c: no schedule id: ignored.
	mk("c", "", t0.Add(-40*day), t0.Add(-39*day), t0.Add(-39*day))
	c.Advance(0)
	got, err := l.StaleLenses(ctx)
	if err != nil || len(got) != 1 || got[0] != "a" {
		t.Fatalf("stale lenses %v %v", got, err)
	}
}

func TestValidCondition(t *testing.T) {
	for s, want := range map[string]bool{
		"date:2026-12-01": true, "target-changed": true, "cost:5; version-changed": true,
		"when the moon is full": false, "": false, "date:": false,
	} {
		if ValidCondition(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}
