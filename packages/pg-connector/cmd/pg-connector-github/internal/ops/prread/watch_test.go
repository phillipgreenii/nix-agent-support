package prread

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The watch option of a direct read (--watch on pr show) against a REAL daemon
// store, a real change engine and the real Persister: the handler's plumbing is
// the thing under test, so the group engine alone is a fake, and it stores what
// the real one stores (the persisted summary) when it is asked for a PR the
// store has never seen.

// watchRig is a store, a change engine under a registration and a Persister.
type watchRig struct {
	st   *store.Store
	chg  *changes.Engine
	pers *summary.Persister
	ids  *entity.Identity
	clk  *governor.ManualClock
}

func newWatchRig(t *testing.T) *watchRig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	chg, err := changes.NewWithSpec(changes.Config{Clock: clk}, changes.Spec())
	if err != nil {
		t.Fatal(err)
	}
	ids := entity.NewIdentity(st, clk)
	pers := &summary.Persister{Store: st, Identity: ids, TTL: time.Minute, Clock: clk, Hook: chg.DetectSummary}
	return &watchRig{st: st, chg: chg, pers: pers, ids: ids, clk: clk}
}

// persist stores the canned summary as a refresh would: the first call is the
// first sighting (no row), a later call with a changed title is a change.
func (r *watchRig) persist(t *testing.T, mutate func(*summary.Summary)) summary.Summary {
	t.Helper()
	s := cannedSummary()
	if mutate != nil {
		mutate(&s)
	}
	if _, err := r.pers.Persist(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (r *watchRig) entity(t *testing.T) store.EntityRow {
	t.Helper()
	row, _, err := r.st.GetEntity(context.Background(), "pr", cannedSummary().NodeID)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (r *watchRig) rows(t *testing.T) []store.ChangeRow {
	t.Helper()
	rows, err := r.st.ChangesAfter(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// storeEngine is the group engine fake that behaves like the real one on the
// two points the watch depends on: it stores the PR's summary when the store has
// none (what a first read does), and it records whether the entity was already
// watched when it was called.
type storeEngine struct {
	rig            *watchRig
	t              *testing.T
	res            groupsvc.EnsureResult
	err            error
	calls          int
	watchedAtCall  bool
	persistIfFresh bool
}

func (e *storeEngine) Ensure(ctx context.Context, _ string, _ []string, _ groupsvc.EnsureOptions) (groupsvc.EnsureResult, error) {
	e.calls++
	e.watchedAtCall = !e.rig.entity(e.t).WatchedAt.IsZero()
	if e.err != nil {
		return groupsvc.EnsureResult{}, e.err
	}
	if e.persistIfFresh {
		if _, ok, _ := e.rig.st.GetGroup(ctx, "pr", cannedSummary().NodeID, summary.GroupName); !ok {
			e.rig.persist(e.t, nil)
		}
	}
	return e.res, nil
}

func filesResult(t *testing.T) groupsvc.EnsureResult {
	t.Helper()
	return groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
		fresh(t, groups.Files, prfacts.Files{Files: []schema.PRFile{{Path: "a.go", Additions: 3, Deletions: 1}}}),
	}}
}

func (r *watchRig) handler(eng Engine, op string, withWatcher bool) func(string) (any, error) {
	deps := Deps{Engine: eng, Clock: r.clk}
	if withWatcher {
		deps.Watcher = NewWatcher(r.chg, r.st, r.ids)
	}
	h := Handlers(deps)[op]
	return func(a string) (any, error) { return h.Handle(context.Background(), json.RawMessage(a)) }
}

func watchArgs(id string, watch string) string {
	return `{"id":"` + id + `","watch":` + watch + `}`
}

func TestAWatchReadOfAPRTheStoreHasNeverSeenWatchesItAndAppendsNoRow(t *testing.T) {
	r := newWatchRig(t)
	eng := &storeEngine{rig: r, t: t, res: filesResult(t), persistIfFresh: true}
	if _, err := r.handler(eng, OpFiles, true)(watchArgs(wireID, "true")); err != nil {
		t.Fatal(err)
	}
	row := r.entity(t)
	if row.WatchedAt.IsZero() || row.WatchedVia != registry.ViaDirect {
		t.Fatalf("the PR must be watched via a direct read, got watched_at %v via %q", row.WatchedAt, row.WatchedVia)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Fatalf("a watch-option read of a never-seen PR appended %d rows (its first fetch appends none): %+v", len(rows), rows)
	}
	if eng.watchedAtCall {
		t.Error("a PR the store had never seen cannot be watched before its read stored it")
	}
}

func TestAWatchReadOfAKnownPRWatchesItBeforeTheReadAndAppendsOneAddedRow(t *testing.T) {
	r := newWatchRig(t)
	r.persist(t, nil) // the daemon knows the PR: alias, entity and summary stored
	eng := &storeEngine{rig: r, t: t, res: filesResult(t)}
	if _, err := r.handler(eng, OpFiles, true)(watchArgs(wireID, "true")); err != nil {
		t.Fatal(err)
	}
	if !eng.watchedAtCall {
		t.Error("a known PR must be watched BEFORE the read, so a change the read finds is a row of a watched PR")
	}
	row := r.entity(t)
	if row.WatchedAt.IsZero() || row.WatchedVia != registry.ViaDirect {
		t.Fatalf("watched_at %v via %q", row.WatchedAt, row.WatchedVia)
	}
	rows := r.rows(t)
	if len(rows) != 1 || !slices.Equal(rows[0].Kinds, []string{"added"}) || !rows[0].Watched || rows[0].ID != wireID {
		t.Fatalf("a watch-option read of a known PR must append exactly one added row of the watched PR, got %+v", rows)
	}

	// The same read again changes nothing: the flag is set and the first cause wins.
	if _, err := r.handler(eng, OpFiles, true)(watchArgs(wireID, "true")); err != nil {
		t.Fatal(err)
	}
	if again := r.rows(t); len(again) != 1 {
		t.Errorf("a second watch-option read appended rows: %+v", again)
	}
}

func TestAWatchReadChangeIsARowOfAWatchedPR(t *testing.T) {
	// The ordering the design needs: the watch is set before the refresh the read
	// triggers, so the change that refresh finds is delivered.
	r := newWatchRig(t)
	r.persist(t, nil)
	eng := &storeEngine{rig: r, t: t, res: filesResult(t)}
	h := r.handler(&refreshingEngine{storeEngine: eng}, OpFiles, true)
	if _, err := h(watchArgs(wireID, "true")); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, row := range r.rows(t) {
		if !row.Watched {
			t.Errorf("row %d (%v) was appended with no watch snapshot", row.Seq, row.Kinds)
		}
		kinds = append(kinds, strings.Join(row.Kinds, ","))
	}
	if !slices.Contains(kinds, "added") || len(kinds) < 2 {
		t.Fatalf("rows = %v, want the added row and the change the read's refresh found", kinds)
	}
}

// refreshingEngine is storeEngine whose read finds a changed title, as a refresh
// of a stale summary does.
type refreshingEngine struct{ *storeEngine }

func (e *refreshingEngine) Ensure(ctx context.Context, id string, want []string, opt groupsvc.EnsureOptions) (groupsvc.EnsureResult, error) {
	res, err := e.storeEngine.Ensure(ctx, id, want, opt)
	e.rig.persist(e.t, func(s *summary.Summary) { s.Title = "A new title" })
	return res, err
}

func TestAReadWithoutTheWatchOptionWatchesNothingAndClearsNothing(t *testing.T) {
	r := newWatchRig(t)
	r.persist(t, nil)
	eng := &storeEngine{rig: r, t: t, res: filesResult(t)}
	if _, err := r.handler(eng, OpFiles, true)(`{"id":"` + wireID + `"}`); err != nil {
		t.Fatal(err)
	}
	if !r.entity(t).WatchedAt.IsZero() || len(r.rows(t)) != 0 {
		t.Fatalf("a read without the option must not watch: %+v, rows %+v", r.entity(t), r.rows(t))
	}
	// A watched PR stays watched across a read without the option, and false is the
	// same as absent.
	if _, err := r.handler(eng, OpFiles, true)(watchArgs(wireID, "true")); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{`{"id":"` + wireID + `"}`, watchArgs(wireID, "false"), watchArgs(wireID, "null")} {
		if _, err := r.handler(eng, OpFiles, true)(a); err != nil {
			t.Fatal(err)
		}
		if r.entity(t).WatchedAt.IsZero() {
			t.Fatalf("a read with %s cleared the watch", a)
		}
	}
	if n := len(r.rows(t)); n != 1 {
		t.Errorf("rows = %d, want only the one added row of the watch", n)
	}
}

func TestWatchIsAcceptedByEveryReadOp(t *testing.T) {
	for _, op := range []string{OpShow, OpFiles, OpCommits, OpReviewPending} {
		t.Run(op, func(t *testing.T) {
			r := newWatchRig(t)
			r.persist(t, nil)
			eng := &storeEngine{rig: r, t: t}
			switch op {
			case OpShow:
				eng.res = showResult(t)
			case OpFiles:
				eng.res = filesResult(t)
			case OpCommits:
				eng.res = groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
					fresh(t, groups.Commits, prfacts.Commits{Commits: []schema.PRCommit{{SHA: "abc", Author: "a", Message: "m"}}}),
				}}
			case OpReviewPending:
				eng.res = pendingResult(t, cannedSummary(), storedPending(t))
			}
			if _, err := r.handler(eng, op, true)(watchArgs(wireID, "true")); err != nil {
				t.Fatalf("%s with watch: %v", op, err)
			}
			if r.entity(t).WatchedAt.IsZero() {
				t.Errorf("%s with watch did not watch the PR", op)
			}
		})
	}
}

func TestAWatchThatIsNotABooleanIsInvalidArgumentAndNothingIsRead(t *testing.T) {
	r := newWatchRig(t)
	r.persist(t, nil)
	eng := &storeEngine{rig: r, t: t, res: filesResult(t)}
	for _, bad := range []string{`"yes"`, `1`, `[true]`, `{"a":1}`} {
		_, err := r.handler(eng, OpFiles, true)(watchArgs(wireID, bad))
		if err == nil || code(t, err) != "invalid_argument" {
			t.Errorf("watch %s = %v, want invalid_argument", bad, err)
		}
	}
	if eng.calls != 0 || !r.entity(t).WatchedAt.IsZero() {
		t.Errorf("a rejected request must neither read nor watch (engine calls %d)", eng.calls)
	}
}

func TestAWatchReadWithoutAWatcherIsUnavailableAndReadsNothing(t *testing.T) {
	r := newWatchRig(t)
	eng := &storeEngine{rig: r, t: t, res: filesResult(t)}
	_, err := r.handler(eng, OpFiles, false)(watchArgs(wireID, "true"))
	if err == nil || code(t, err) != "unavailable" || eng.calls != 0 {
		t.Fatalf("err = %v, engine calls = %d, want unavailable and no read", err, eng.calls)
	}
	// Without the option the same handler still answers.
	if _, err := r.handler(eng, OpFiles, false)(`{"id":"` + wireID + `"}`); err != nil {
		t.Errorf("a read without the option must not need a watcher: %v", err)
	}
}

func TestAWatchReadOfAPRThatIsGoneWatchesNothing(t *testing.T) {
	r := newWatchRig(t)
	eng := &storeEngine{rig: r, t: t, res: groupsvc.EnsureResult{Summary: summary.Summary{NodeID: cannedSummary().NodeID, Gone: true}}}
	_, err := r.handler(eng, OpFiles, true)(watchArgs(wireID, "true"))
	if err == nil || code(t, err) != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
	if !r.entity(t).WatchedAt.IsZero() {
		t.Error("a PR that does not exist must not be watched")
	}
}

func TestAFailedReadOfAPRTheStoreHasNeverSeenWatchesNothing(t *testing.T) {
	r := newWatchRig(t)
	eng := &storeEngine{rig: r, t: t, err: scriptout.WrapError(scriptout.ErrUnavailable, "the origin is down")}
	_, err := r.handler(eng, OpFiles, true)(watchArgs(wireID, "true"))
	if err == nil || code(t, err) != "unavailable" {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if got, _ := r.st.CountWatched(context.Background(), "pr"); got != 0 {
		t.Errorf("watched = %d, want 0", got)
	}
}
