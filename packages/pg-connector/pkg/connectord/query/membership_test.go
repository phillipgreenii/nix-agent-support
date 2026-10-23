package query

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// fakeConfirmer answers each id from a table and records every call.
type fakeConfirmer struct {
	mu    sync.Mutex
	by    map[string]Confirmation
	err   error
	calls []string
}

func (c *fakeConfirmer) Confirm(_ context.Context, id, _ string) (Confirmation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, id)
	if c.err != nil {
		return Confirmation{}, c.err
	}
	return c.by[id], nil
}

func (c *fakeConfirmer) called() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// set answers id with conf. The scheduler goroutine may be inside Confirm, so
// the table is only written under mu.
func (c *fakeConfirmer) set(id string, conf Confirmation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.by[id] = conf
}

func (c *fakeConfirmer) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// logs captures the runner's log events.
type logs struct {
	mu     sync.Mutex
	events []string
}

func (l *logs) log(event string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *logs) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// engineRig is a rig whose runner records membership changes through the real
// change engine over the rig's real store.
type engineRig struct {
	*rig
	conf *fakeConfirmer
	log  *logs
}

func newEngineRig(t *testing.T) *engineRig {
	t.Helper()
	conf := &fakeConfirmer{by: map[string]Confirmation{}}
	lg := &logs{}
	r := newRigWith(t, func(c *Config) {
		c.Changes, c.Confirmer, c.Log = newEngine(t, c.Clock), conf, lg.log
	})
	return &engineRig{rig: r, conf: conf, log: lg}
}

// list sets the listing the next run reads. The scheduler goroutine may be
// inside the fake lister, so the swap goes through the fake's lock.
func (r *rig) list(complete bool, ms ...mem) {
	r.lister.setAnswer(func(listCall) (Listing, error) { return listingOf(complete, ms...), nil })
	// The aliases follow the listing the way the origin's own identity does: a
	// member the origin knows under a reference has that alias.
}

// runKey runs the configured query "mine" once, directly.
func (r *rig) runKey(t *testing.T) store.QueryRow {
	t.Helper()
	row, err := r.runner.run(context.Background(), Key{Name: "mine"}, governor.ClassQuery)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	r.clk.Advance(time.Minute)
	return row
}

// rows is the whole change log in order.
func (r *rig) rows(t *testing.T) []store.ChangeRow {
	t.Helper()
	rows, err := r.st.ChangesAfter(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// fieldOf is the named field of a change row.
func fieldOf(t *testing.T, row store.ChangeRow, name string) (engine.Field, bool) {
	t.Helper()
	fs, err := engine.DecodeFields(row.Fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Name == name {
			return f, true
		}
	}
	return engine.Field{}, false
}

// baselined runs "mine" once over ms as a complete listing (the baseline).
func (r *engineRig) baselined(t *testing.T, ms ...mem) {
	t.Helper()
	r.list(true, ms...)
	row := r.runKey(t)
	if row.BaselineAt.IsZero() || len(r.rows(t)) != 0 {
		t.Fatalf("setup: baseline = %v, rows = %d", row.BaselineAt, len(r.rows(t)))
	}
}

func TestMembership_AFirstCompleteRunIsTheBaselineAndAppendsNothing(t *testing.T) {
	r := newEngineRig(t)
	r.list(true, memA, memB, memC)
	row := r.runKey(t)
	if !row.BaselineAt.Equal(t0) {
		t.Errorf("baseline = %v, want %v", row.BaselineAt, t0)
	}
	if got := r.row(t, "mine"); !got.BaselineAt.Equal(t0) || !slices.Equal(got.IDs, refs(memA, memB, memC)) {
		t.Errorf("stored row = %+v", got)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("a baseline run appended %d rows, want none", len(rows))
	}
	if len(r.conf.called()) != 0 {
		t.Error("the baseline run must not confirm anything")
	}
}

func TestMembership_ATruncatedFirstRunStoresNoBaselineAndTheNextCompleteRunIsIt(t *testing.T) {
	r := newEngineRig(t)
	r.list(false, memA)
	r.runKey(t)
	if got := r.row(t, "mine"); !got.BaselineAt.IsZero() || got.Complete {
		t.Fatalf("row after a truncated first run = %+v, want no baseline", got)
	}
	r.list(true, memA, memB)
	row := r.runKey(t)
	if row.BaselineAt.IsZero() {
		t.Fatal("the first complete run must set the baseline")
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("the run that sets the baseline appended %d rows (B is not an arrival), want none", len(rows))
	}
}

func TestMembership_TheBaselineSurvivesATruncatedRunAndANewRunner(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB) // at t0
	r.list(false, memA, memB)
	r.runKey(t)
	if got := r.row(t, "mine"); !got.BaselineAt.Equal(t0) {
		t.Errorf("baseline = %v after a truncated run, want %v kept", got.BaselineAt, t0)
	}

	// A new Runner over the same store (a restart) neither re-baselines nor
	// logs a burst: a later complete run changes nothing.
	sched := scheduler.New(scheduler.Options{Clock: r.clk, Gate: r.gov, Workers: 1, PollInterval: 5 * time.Millisecond, RetryDelay: time.Minute})
	second, err := New(Config{
		Spec: testSpec(), StateFieldPrefix: statePrefix, Bucket: bucketInvent,
		Settings: r.runner.cfg.Settings, Store: r.st, Aliases: r.aliases, Tracker: r.tracker, Lister: r.lister,
		Scheduler: sched, Clock: r.clk, Changes: newEngine(t, r.clk), Confirmer: r.conf,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.list(true, memA, memB)
	if _, err := second.run(context.Background(), Key{Name: "mine"}, governor.ClassQuery); err != nil {
		t.Fatal(err)
	}
	if got := r.row(t, "mine"); !got.BaselineAt.Equal(t0) {
		t.Errorf("baseline = %v after a new Runner, want the original %v", got.BaselineAt, t0)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %d after a restart over an unchanged membership, want none", len(rows))
	}
}

func TestMembership_ABrandNewMemberInABaselinedQueryIsRecordedAndItsEntityCreated(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	ctx := context.Background()
	if _, ok, _ := r.st.GetEntity(ctx, kindWidget, memC.id); ok {
		t.Fatal("setup: the new member must have no entity row yet")
	}
	r.list(true, memA, memB, memC)
	r.runKey(t)
	rows := r.rows(t)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one arrival", rows)
	}
	row := rows[0]
	if !slices.Equal(row.Kinds, []string{changeJoined}) || row.Origin != "query" || row.ID != memC.ref {
		t.Errorf("row = %+v", row)
	}
	if f, ok := fieldOf(t, row, "query:mine"); !ok || string(f.After) != `"mine"` {
		t.Errorf("query field = %+v %v, want after \"mine\"", f, ok)
	}
	if _, ok, err := r.st.GetEntity(ctx, kindWidget, memC.id); err != nil || !ok {
		t.Errorf("the new member's entity row: ok=%v err=%v, want it created", ok, err)
	}
	if got := r.row(t, "mine"); !slices.Equal(got.IDs, refs(memA, memB, memC)) {
		t.Errorf("stored ids = %v", got.IDs)
	}
}

func TestMembership_ARenameIsNeitherAnArrivalNorADeparture(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	renamed := mem{memA.id, "Example-Org/Renamed#3"}
	r.list(true, renamed, memB)
	row := r.runKey(t)
	if !slices.Equal(row.IDs, refs(renamed, memB)) {
		t.Errorf("ids = %v, want the new reference stored", row.IDs)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("a rename appended %+v, want nothing", rows)
	}
	if len(r.conf.called()) != 0 {
		t.Errorf("a rename confirmed %v, want no read", r.conf.called())
	}
	// It does not block later runs, which then compare the new reference.
	r.list(true, renamed, memB, memC)
	r.runKey(t)
	if rows := r.rows(t); len(rows) != 1 || !slices.Equal(rows[0].Kinds, []string{changeJoined}) {
		t.Errorf("rows after a later arrival = %+v, want exactly the arrival", rows)
	}
}

func TestMembership_ARenameWhoseAliasMovedBeforeTheRunNeverBlocksTheRun(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	// Another path already moved the alias in place.
	renamed := mem{memA.id, "Example-Org/Renamed#3"}
	r.aliases.set(renamed)
	r.list(true, renamed, memB)
	r.runKey(t) // must not fail
	if !slices.Equal(r.log.seen(), []string{"query_membership_no_alias"}) {
		t.Errorf("log = %v, want the previous id with no alias reported once", r.log.seen())
	}
	for _, row := range r.rows(t) {
		if slices.Contains(row.Kinds, changeLeft) {
			t.Errorf("an id with no alias became a departure: %+v", row)
		}
	}
	if len(r.conf.called()) != 0 {
		t.Errorf("confirmed %v, want no read for an id with no alias", r.conf.called())
	}
	// The accepted residue is at most the one false arrival of the rename the
	// run could not see; a later run is quiet.
	before := len(r.rows(t))
	r.runKey(t)
	if after := len(r.rows(t)); after != before {
		t.Errorf("a later unchanged run appended %d rows", after-before)
	}
}

func TestMembership_AListingWhoseReferenceEqualsAPreviousIDIgnoringCaseIsPresent(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	// The alias of A moved to another reference, so the previous id cannot be
	// resolved; the new listing shows A under its old reference again, in a
	// different case.
	r.aliases.set(mem{memA.id, "Example-Org/Elsewhere#3"})
	again := mem{memA.id, "example-org/thing#3"}
	r.list(true, again, memB)
	r.runKey(t)
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the reference equals a previous id case-insensitively", rows)
	}
}

func TestMembership_ARunWithATruncatedPreviousListingRecordsNoArrival(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA)
	r.list(false, memA, memB) // the previous row was complete: B is an arrival
	r.runKey(t)
	if rows := r.rows(t); len(rows) != 1 {
		t.Fatalf("rows = %d, want B's arrival", len(rows))
	}
	r.list(true, memA, memB, memC) // the previous row is truncated: C cannot be told from unseen
	r.runKey(t)
	if rows := r.rows(t); len(rows) != 1 {
		t.Errorf("rows = %d after a run over a truncated previous listing, want still 1", len(rows))
	}
}

func TestMembership_ATruncatedListingRecordsNoDeparture(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB, memC)
	r.list(false, memA)
	r.runKey(t)
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want no departure from a truncated listing", rows)
	}
	if len(r.conf.called()) != 0 {
		t.Errorf("confirmed %v, want no read for a departure from a truncated listing", r.conf.called())
	}
}

func TestMembership_ADepartureFromACompleteListingIsConfirmedOnceAndCarriesTheState(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED", "OPEN"} {
		t.Run(state, func(t *testing.T) {
			r := newEngineRig(t)
			r.baselined(t, memA, memB, memC)
			r.conf.set(memC.id, Confirmation{State: state})
			r.list(true, memA, memB)
			row := r.runKey(t)
			if calls := r.conf.called(); !slices.Equal(calls, []string{memC.id}) {
				t.Errorf("confirmer calls = %v, want exactly one for %s", calls, memC.id)
			}
			rows := r.rows(t)
			if len(rows) != 1 {
				t.Fatalf("rows = %+v, want one departure", rows)
			}
			if !slices.Equal(rows[0].Kinds, []string{changeLeft}) || rows[0].ID != memC.ref || rows[0].Origin != "query" {
				t.Errorf("row = %+v", rows[0])
			}
			if f, ok := fieldOf(t, rows[0], "query:mine"); !ok || string(f.Before) != `"mine"` {
				t.Errorf("query field = %+v %v", f, ok)
			}
			if f, ok := fieldOf(t, rows[0], statePrefix+"mine"); !ok || string(f.After) != fmt.Sprintf("%q", state) {
				t.Errorf("state field = %+v %v, want after %q", f, ok, state)
			}
			if !slices.Equal(row.IDs, refs(memA, memB)) {
				t.Errorf("stored ids = %v", row.IDs)
			}
		})
	}
}

func TestMembership_AGoneConfirmationAppendsNoDeparture(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB, memC)
	r.conf.set(memC.id, Confirmation{Gone: true})
	r.list(true, memA, memB)
	row := r.runKey(t)
	if calls := r.conf.called(); len(calls) != 1 {
		t.Errorf("confirmer calls = %v, want one", calls)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the entity's tombstone path logs the removal", rows)
	}
	if !slices.Equal(row.IDs, refs(memA, memB)) {
		t.Errorf("stored ids = %v, want the departure stored", row.IDs)
	}
}

func TestMembership_AFailedConfirmationFailsTheRunKeepsThePreviousRowAndIsRetried(t *testing.T) {
	r := newEngineRig(t)
	r.run(t)
	ctx := context.Background()
	r.list(true, memA, memB, memC)
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	baseline := r.row(t, "mine").BaselineAt
	if baseline.IsZero() {
		t.Fatal("setup: no baseline")
	}
	r.clk.Advance(2 * time.Minute)
	r.conf.set(memC.id, Confirmation{State: "MERGED"})
	r.conf.setErr(errors.New("confirm: origin said no"))
	r.list(true, memA, memB)
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err == nil || !strings.Contains(err.Error(), "origin said no") {
		t.Fatalf("err = %v, want the confirmation failure", err)
	}
	row := r.row(t, "mine")
	if !slices.Equal(row.IDs, refs(memA, memB, memC)) || !strings.Contains(row.LastError, "origin said no") || !row.BaselineAt.Equal(baseline) {
		t.Errorf("row = %+v, want the previous ids and baseline with last_error", row)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want none while the departure is unconfirmed", rows)
	}

	// The next run recomputes the same departure and appends it.
	r.conf.setErr(nil)
	r.clk.Advance(2 * time.Minute) // past the scheduler's retry hold
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	rows := r.rows(t)
	if len(rows) != 1 || !slices.Equal(rows[0].Kinds, []string{changeLeft}) || rows[0].ID != memC.ref {
		t.Fatalf("rows = %+v, want the one departure", rows)
	}
	if got := r.row(t, "mine"); !slices.Equal(got.IDs, refs(memA, memB)) || got.LastError != "" {
		t.Errorf("row after the retry = %+v", got)
	}
	if calls := r.conf.called(); len(calls) != 2 {
		t.Errorf("confirmer calls = %v, want one per run", calls)
	}
}

func TestMembership_ABudgetRefusedConfirmationIsAFailedRunNotADroppedDeparture(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB, memC)
	refusal := &governor.Refusal{Kind: governor.RefusedCap}
	r.conf.setErr(refusal)
	r.list(true, memA, memB)
	_, err := r.runner.run(context.Background(), Key{Name: "mine"}, governor.ClassQuery)
	var got *governor.Refusal
	if !errors.As(err, &got) {
		t.Fatalf("err = %v, want the refusal visible through the wrap", err)
	}
	if row := r.row(t, "mine"); !slices.Equal(row.IDs, refs(memA, memB, memC)) {
		t.Errorf("stored ids = %v, want the previous row kept", row.IDs)
	}
}

func TestMembership_WithoutAConfirmerADepartureIsNeitherRecordedNorDropped(t *testing.T) {
	r := newRigWith(t, func(c *Config) { c.Changes = newEngine(t, c.Clock) })
	r.list(true, memA, memB, memC)
	r.runKey(t)
	r.list(true, memA, memB, memD) // D arrives, C departs
	if _, err := r.runner.run(context.Background(), Key{Name: "mine"}, governor.ClassQuery); err == nil {
		t.Fatal("a departure that cannot be confirmed must fail the run")
	}
	if row := r.row(t, "mine"); !slices.Equal(row.IDs, refs(memA, memB, memC)) {
		t.Errorf("stored ids = %v, want the previous row kept", row.IDs)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want nothing stored with the result", rows)
	}
	// Arrivals alone (no departure) are still recorded.
	r.list(true, memA, memB, memC, memD)
	r.runKey(t)
	if rows := r.rows(t); len(rows) != 1 || !slices.Equal(rows[0].Kinds, []string{changeJoined}) {
		t.Errorf("rows = %+v, want D's arrival", rows)
	}
}

// failingChanges appends through the engine, then fails on its nth call.
type failingChanges struct {
	inner Changes
	n     int
	calls int
}

func (f *failingChanges) AppendMembership(ctx context.Context, tx *store.Tx, m engine.Membership) error {
	f.calls++
	if f.calls == f.n {
		return errors.New("append failed")
	}
	return f.inner.AppendMembership(ctx, tx, m)
}

func TestMembership_TheStoredRowAndTheAppendedRowsAreVisibleTogetherOrNotAtAll(t *testing.T) {
	var fc *failingChanges
	r := newRigWith(t, func(c *Config) {
		fc = &failingChanges{inner: newEngine(t, c.Clock), n: 2}
		c.Changes = fc
	})
	r.list(true, memA)
	r.runKey(t) // the baseline
	r.list(true, memA, memB, memC)
	if _, err := r.runner.run(context.Background(), Key{Name: "mine"}, governor.ClassQuery); err == nil || !strings.Contains(err.Error(), "append failed") {
		t.Fatalf("err = %v, want the append failure", err)
	}
	if fc.calls != 2 {
		t.Fatalf("append calls = %d, want 2 (the first one went through before the failure)", fc.calls)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want the first row rolled back with the transaction", rows)
	}
	if row := r.row(t, "mine"); !slices.Equal(row.IDs, refs(memA)) {
		t.Errorf("stored ids = %v, want the previous row", row.IDs)
	}
	if _, ok, _ := r.st.GetEntity(context.Background(), kindWidget, memB.id); ok {
		t.Error("the entity row EnsureEntity made must roll back too")
	}
}

func TestMembership_AnAdhocKeyRecordsNoBaselineAndNoRows(t *testing.T) {
	r := newEngineRig(t)
	key := Key{Name: "mine", Args: map[string]string{"label": "bug"}}
	ctx := context.Background()
	r.list(true, memA, memB, memC)
	if _, err := r.runner.run(ctx, key, governor.ClassInteractive); err != nil {
		t.Fatal(err)
	}
	r.list(true, memA, memD)
	row, err := r.runner.run(ctx, key, governor.ClassInteractive)
	if err != nil {
		t.Fatal(err)
	}
	if !row.BaselineAt.IsZero() || r.row(t, key.String()).Configured || !r.row(t, key.String()).BaselineAt.IsZero() {
		t.Errorf("ad-hoc row = %+v, want no baseline and not configured", r.row(t, key.String()))
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want none for an ad-hoc key", rows)
	}
	if len(r.conf.called()) != 0 {
		t.Errorf("confirmer calls = %v, want none", r.conf.called())
	}
}

func TestMembership_WithChangesNilTheRunnerStoresTheRowPlusTheBaseline(t *testing.T) {
	r := newRig(t)
	r.list(true, memA, memB)
	first := r.runKey(t) // at t0
	r.list(true, memB, memC)
	second := r.runKey(t) // a minute later
	want := store.QueryRow{
		Key: "mine", Configured: true, IDs: refs(memB, memC), Complete: true,
		FetchedAt: t0.Add(time.Minute), FreshUntil: t0.Add(2 * time.Minute), LastSuccessAt: t0.Add(time.Minute),
		BaselineAt: t0,
	}
	got := r.row(t, "mine")
	if got.Key != want.Key || got.Configured != want.Configured || !slices.Equal(got.IDs, want.IDs) || got.Complete != want.Complete ||
		!got.FetchedAt.Equal(want.FetchedAt) || !got.FreshUntil.Equal(want.FreshUntil) || !got.LastSuccessAt.Equal(want.LastSuccessAt) ||
		got.LastError != "" || !got.BaselineAt.Equal(want.BaselineAt) {
		t.Errorf("stored row = %+v, want %+v", got, want)
	}
	if !first.BaselineAt.Equal(t0) || !second.BaselineAt.Equal(t0) {
		t.Errorf("baselines = %v, %v, want the first complete run's time kept", first.BaselineAt, second.BaselineAt)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want none with Changes nil", rows)
	}
	if r.aliases.resolveCalls() != 0 {
		t.Errorf("aliases resolved %d times, want none with Changes nil", r.aliases.resolveCalls())
	}
}

func TestMembership_ADuplicateOrUnresolvablePreviousIDNeverBlocksTheRun(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	ctx := context.Background()
	prev := r.row(t, "mine")
	prev.IDs = append(prev.IDs, "not a reference", prev.IDs[0])
	if err := r.st.PutQuery(ctx, prev); err != nil {
		t.Fatal(err)
	}
	r.list(true, memA, memB)
	r.runKey(t)
	if !slices.Equal(r.log.seen(), []string{"query_membership_no_alias"}) {
		t.Errorf("log = %v", r.log.seen())
	}
	if rows := r.rows(t); len(rows) != 0 || len(r.conf.called()) != 0 {
		t.Errorf("rows = %+v, confirms = %v, want none", rows, r.conf.called())
	}
}

// An Alias with an empty id names a reference the aliases implementation has
// already reported: the runner skips it without a report of its own.
func TestMembership_AnAliasWithoutAnIDIsSkippedSilently(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	r.runner.cfg.Aliases = skippingAliases{inner: r.aliases, skip: memA.ref}
	r.list(true, memA, memB)
	r.runKey(t)
	if seen := r.log.seen(); len(seen) != 0 {
		t.Errorf("log = %v, want nothing for a reference the aliases already reported", seen)
	}
	if rows := r.rows(t); len(rows) != 0 || len(r.conf.called()) != 0 {
		t.Errorf("rows = %+v, confirms = %v, want none", rows, r.conf.called())
	}
}

type skippingAliases struct {
	inner Aliases
	skip  string
}

func (s skippingAliases) Put(ctx context.Context, id, ref string) error {
	return s.inner.Put(ctx, id, ref)
}

func (s skippingAliases) Resolve(ctx context.Context, refs []string) (map[string]Alias, error) {
	out, err := s.inner.Resolve(ctx, refs)
	if err != nil {
		return nil, err
	}
	out[s.skip] = Alias{}
	return out, nil
}

func TestFold_IsTheSpecsFoldAndTheIdentityWithoutOne(t *testing.T) {
	r := newRig(t)
	if r.runner.fold("Example-Org/Thing#3") != r.runner.fold("example-org/thing#3") {
		t.Error("the spec's fold compares the part before the number case-insensitively")
	}
	if r.runner.fold("Example-Org/Thing#3") == r.runner.fold("Example-Org/Thing#4") {
		t.Error("the number compares exactly")
	}
	bare := newRigWith(t, func(c *Config) {
		c.Spec.FoldRef = nil
		c.Changes = newEngine(t, c.Clock)
	})
	if got := bare.runner.fold("Example-Org/Thing#3"); got != "Example-Org/Thing#3" {
		t.Errorf("fold without a spec fold = %q, want the identity", got)
	}
}

func TestMembershipPlan_IsEmptyWithoutABaseline(t *testing.T) {
	r := newEngineRig(t)
	plan, err := r.runner.planMembership(context.Background(), Key{Name: "mine"},
		store.QueryRow{Key: "mine", IDs: refs(memA), Complete: true}, Listing{Complete: true})
	if err != nil || len(plan.arrivals) != 0 || len(plan.departures) != 0 {
		t.Errorf("plan = %+v err = %v, want empty without a baseline", plan, err)
	}
}

// planMembership must resolve every previous member in ONE Resolve call: a call
// per previous member can scan the whole alias table once each (pg2-6cwpf). The
// test also pins the plan's content: the departures are the resolvable previous
// members the listing no longer shows, in previous order, with the alias's own
// spelling and each id once.
func TestMembershipPlan_ResolvesAllPreviousMembersInOneCall(t *testing.T) {
	ctx := context.Background()
	lg := &logs{}
	r := newRigWith(t, func(c *Config) {
		c.Changes = newEngine(t, c.Clock)
		c.Log = lg.log
	})

	const n = 150
	var prevIDs []string
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("Example-Org/Thing#%d", i+1)
		r.aliases.set(mem{fmt.Sprintf("w-%03d", i), ref})
		// Queries store the reference in lower case: the match is case-folded.
		prevIDs = append(prevIDs, strings.ToLower(ref))
	}
	// A reference nothing resolves, one no alias names, and a duplicate of the first.
	prevIDs = append(prevIDs, "not a reference", "example-org/unknown#1", prevIDs[0])

	// The listing still shows the first 100; the other 50 departed.
	var members []Member
	for i := 0; i < 100; i++ {
		members = append(members, Member{ID: fmt.Sprintf("w-%03d", i), Ref: fmt.Sprintf("Example-Org/Thing#%d", i+1)})
	}

	prev := store.QueryRow{Key: "mine", IDs: prevIDs, Complete: true, BaselineAt: t0}
	plan, err := r.runner.planMembership(ctx, Key{Name: "mine"}, prev, Listing{Members: members, Complete: true})
	if err != nil {
		t.Fatal(err)
	}

	if calls := r.aliases.resolveCalls(); calls != 1 {
		t.Errorf("planMembership resolved %d times for %d previous members, want one call for all of them", calls, len(prevIDs))
	}
	if len(plan.arrivals) != 0 || len(plan.departures) != n-100 {
		t.Fatalf("plan = %d arrivals, %d departures, want 0 and %d", len(plan.arrivals), len(plan.departures), n-100)
	}
	for i, d := range plan.departures {
		wantID, wantRef := fmt.Sprintf("w-%03d", 100+i), fmt.Sprintf("Example-Org/Thing#%d", 101+i)
		if d.id != wantID || d.ref != wantRef {
			t.Fatalf("departure %d = %+v, want id %s ref %s", i, d, wantID, wantRef)
		}
	}
	if want := []string{"query_membership_no_alias", "query_membership_no_alias"}; !slices.Equal(lg.seen(), want) {
		t.Errorf("log = %v, want %v", lg.seen(), want)
	}
}

// The fixtures are swapped by the test goroutine while the scheduler goroutine
// is inside them. Both fakes MUST take their lock to swap, or the race detector
// fires here regardless of scheduling luck.
func TestFixtures_SwapWhileTheSchedulerGoroutineReadsThem(t *testing.T) {
	l := &fakeLister{answer: func(listCall) (Listing, error) { return Listing{}, nil }}
	conf := &fakeConfirmer{by: map[string]Confirmation{}}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = l.List(context.Background(), Key{Name: "x"}, nil, CallMeta{})
			_, _ = conf.Confirm(context.Background(), memC.id, "")
		}
	}()
	for i := range 500 {
		l.setAnswer(func(listCall) (Listing, error) { return listingOf(true, memA), nil })
		conf.set(memC.id, Confirmation{State: strconv.Itoa(i)})
	}
	close(stop)
	wg.Wait()
}
