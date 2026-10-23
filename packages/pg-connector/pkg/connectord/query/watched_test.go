package query

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The watch marking of the runner and the task shape of a fetch that carries the
// watch option.

// watchedSettings marks every configured query of the wrapped settings watched.
type watchedSettings struct{ Settings }

func (watchedSettings) Watched(string) bool { return true }

func watchedEngineRig(t *testing.T, adHoc bool) *engineRig {
	t.Helper()
	conf := &fakeConfirmer{by: map[string]Confirmation{}}
	r := newRigWith(t, func(c *Config) {
		c.Settings = watchedSettings{c.Settings}
		c.Changes, c.Confirmer, c.AdHocWatch = newEngine(t, c.Clock), conf, adHoc
	})
	return &engineRig{rig: r, conf: conf, log: &logs{}}
}

func TestWatched_TheRunMarksArrivalsAndOthersInOneTransactionAndAppendsNoRowOnItsOwn(t *testing.T) {
	r := watchedEngineRig(t, false)
	r.list(true, memA, memB)
	r.runKey(t) // the baseline run marks both without a row
	if rows := r.rows(t); len(rows) != 0 {
		t.Fatalf("the baseline run appended %d rows", len(rows))
	}
	for _, m := range []mem{memA, memB} {
		if !r.watchedWith(t, m.id, registry.ViaQuery+queryMine) {
			t.Errorf("%s is not watched through the query after the baseline run", m.id)
		}
	}

	// An arrival is marked through its own arrival row; a member that was already
	// there stays as it was.
	r.list(true, memA, memB, memC)
	r.runKey(t)
	rows := r.rows(t)
	if len(rows) != 1 || rows[0].ID != memC.ref || !rows[0].Watched {
		t.Fatalf("rows = %+v, want the one arrival row of %s with the snapshot set", rows, memC.ref)
	}
	if !r.watchedWith(t, memC.id, registry.ViaQuery+queryMine) {
		t.Errorf("the arrival %s is not watched", memC.id)
	}
}

func (r *engineRig) watchedWith(t *testing.T, id, via string) bool {
	t.Helper()
	ent, ok, err := r.st.GetEntity(context.Background(), kindWidget, id)
	if err != nil {
		t.Fatal(err)
	}
	return ok && !ent.WatchedAt.IsZero() && ent.WatchedVia == via
}

func TestWatched_AnUnwatchedQueryMarksNothingAndKeepsTheStorelessShortcut(t *testing.T) {
	r := newEngineRig(t) // the settings mark nothing watched
	r.list(true, memA)
	r.runKey(t)
	r.list(true, memA, memB)
	r.runKey(t)
	for _, m := range []mem{memA, memB} {
		if ent, ok, err := r.st.GetEntity(context.Background(), kindWidget, m.id); err != nil {
			t.Fatal(err)
		} else if ok && !ent.WatchedAt.IsZero() {
			t.Errorf("%s was marked watched by an unwatched query", m.id)
		}
	}
}

func TestFetch_TheWatchOptionIsRefusedUnlessTheRegistrationAllowsIt(t *testing.T) {
	r := watchedEngineRig(t, false)
	r.list(true, memA)
	_, err := r.runner.Fetch(context.Background(), Key{Name: queryMine}, FetchOptions{Watch: true})
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("Fetch with the watch option = %v, want invalid_argument", err)
	}
	if got := r.lister.count(); got != 0 {
		t.Errorf("a refused fetch listed %d times", got)
	}
}

func TestFetch_AWatchRunIsItsOwnTaskAndNeverJoinsAPlainRunOfTheSameKey(t *testing.T) {
	key := Key{Name: queryMine}
	plain := taskOf(key, governor.ClassInteractive, t0)
	watch := watchTaskOf(key)
	if plain.Entity == watch.Entity {
		t.Fatalf("the watch task shares the entity %q of the plain run", plain.Entity)
	}
	if watch.Class != governor.ClassInteractive || watch.Kind != Kind || watch.Group != Group {
		t.Errorf("watch task = %+v, want an interactive query task", watch)
	}
	for name, task := range map[string]scheduler.Task{"plain": plain, "watch": watch} {
		if got := keyOf(task); got.String() != key.String() {
			t.Errorf("%s: keyOf = %q, want %q", name, got, key)
		}
	}
	if watchOf(plain) || !watchOf(watch) {
		t.Errorf("watchOf(plain) = %v, watchOf(watch) = %v", watchOf(plain), watchOf(watch))
	}
	// An ad-hoc key keeps its arguments in the watch payload.
	adhoc := Key{Name: queryMine, Args: map[string]string{"label": "bug"}}
	if got := keyOf(watchTaskOf(adhoc)); got.String() != adhoc.String() {
		t.Errorf("keyOf(ad-hoc watch task) = %q, want %q", got, adhoc)
	}
	if watchTaskOf(adhoc).Entity == watchTaskOf(key).Entity {
		t.Error("two keys share one watch entity")
	}

	// Through the scheduler: the two fetches are two flights.
	r := watchedEngineRig(t, true)
	r.run(t)
	f1, err := r.sched.SubmitInteractive(plain)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := r.sched.SubmitInteractive(watch)
	if err != nil {
		t.Fatal(err)
	}
	if f1 == f2 {
		t.Error("the watch fetch joined the flight of the plain run")
	}
	<-f1.Done()
	<-f2.Done()
	if got := r.lister.count(); got != 2 {
		t.Errorf("origin lists = %d, want one per run", got)
	}
}
