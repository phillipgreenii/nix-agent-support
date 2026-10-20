package query

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// mem is one listed member of a fake listing.
type mem struct {
	id, repo string
	n        int
}

var (
	memA = mem{"PR_a", "Acme-Org/Widget", 3}
	memB = mem{"PR_b", "acme-org/gadget", 7}
	memC = mem{"PR_c", "Acme-Org/Widget", 1}
	memD = mem{"PR_d", "Acme-Org/Widget", 9}
)

// listBody is the search answer for ms; a listing that is not complete reports
// more results than GitHub's search limit.
func listBody(complete bool, ms ...mem) []byte {
	count := len(ms)
	if !complete {
		count = SearchResultLimit + 1
	}
	var nodes []string
	for _, m := range ms {
		nodes = append(nodes, fmt.Sprintf(`{"id":%q,"number":%d,"repository":{"nameWithOwner":%q}}`, m.id, m.n, m.repo))
	}
	return []byte(fmt.Sprintf(`{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},"search":{"issueCount":%d,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[%s]}}}`,
		count, strings.Join(nodes, ",")))
}

// fakeConfirmer answers each node id from a table and records every call.
type fakeConfirmer struct {
	mu    sync.Mutex
	by    map[string]Confirmation
	err   error
	calls []string
}

func (c *fakeConfirmer) Confirm(_ context.Context, node, _ string) (Confirmation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, node)
	if c.err != nil {
		return Confirmation{}, c.err
	}
	return c.by[node], nil
}

func (c *fakeConfirmer) called() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// set answers node with conf. The scheduler goroutine may be inside Confirm, so
// the table is only written under mu.
func (c *fakeConfirmer) set(node string, conf Confirmation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.by[node] = conf
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
		e, err := changes.New(changes.Config{Clock: c.Clock})
		if err != nil {
			t.Fatal(err)
		}
		c.Changes, c.Confirm, c.Log = e, conf, lg.log
	})
	return &engineRig{rig: r, conf: conf, log: lg}
}

// list sets the listing the next run reads. The scheduler goroutine may be
// inside fakeGH.Run, so the swap goes through the fake's lock.
func (r *rig) list(complete bool, ms ...mem) {
	r.gh.setAnswer(func(call) ([]byte, error) { return listBody(complete, ms...), nil })
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
func fieldOf(t *testing.T, row store.ChangeRow, name string) (classify.Field, bool) {
	t.Helper()
	fs, err := classify.DecodeFields(row.Fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Name == name {
			return f, true
		}
	}
	return classify.Field{}, false
}

func ids(ms ...mem) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = fmt.Sprintf("%s#%d", m.repo, m.n)
	}
	return out
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
	if got := r.row(t, "mine"); !got.BaselineAt.Equal(t0) || !slices.Equal(got.IDs, ids(memA, memB, memC)) {
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
	id := entity.NewIdentity(r.st, r.clk)
	sched := scheduler.New(scheduler.Options{Clock: r.clk, Gate: r.gov, Workers: 1, PollInterval: 5 * time.Millisecond, RetryDelay: time.Minute})
	e, err := changes.New(changes.Config{Clock: r.clk})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{
		Settings: r.cfg, Store: r.st, Identity: id, Tracker: r.svc, GH: r.gh, Governor: r.gov,
		Feed: ratefeed.New(r.gov, r.gh, r.clk), Scheduler: sched, Clock: r.clk, Changes: e, Confirm: r.conf,
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

func TestMembership_ABrandNewPRInABaselinedQueryIsRecordedAndItsEntityCreated(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	ctx := context.Background()
	if _, ok, _ := r.st.GetEntity(ctx, entity.KindPR, memC.id); ok {
		t.Fatal("setup: the new PR must have no entity row yet")
	}
	r.list(true, memA, memB, memC)
	r.runKey(t)
	rows := r.rows(t)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one entered_query", rows)
	}
	row := rows[0]
	if !slices.Equal(row.Kinds, []string{classify.KindEnteredQuery}) || row.Origin != "query" || row.ID != "Acme-Org/Widget#1" {
		t.Errorf("row = %+v", row)
	}
	if f, ok := fieldOf(t, row, "query:mine"); !ok || string(f.After) != `"mine"` {
		t.Errorf("query field = %+v %v, want after \"mine\"", f, ok)
	}
	if _, ok, err := r.st.GetEntity(ctx, entity.KindPR, memC.id); err != nil || !ok {
		t.Errorf("the new PR's entity row: ok=%v err=%v, want it created", ok, err)
	}
	if got := r.row(t, "mine"); !slices.Equal(got.IDs, ids(memA, memB, memC)) {
		t.Errorf("stored ids = %v", got.IDs)
	}
}

func TestMembership_ARepositoryRenameIsNeitherAnArrivalNorADeparture(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	renamed := mem{memA.id, "Acme-Org/Renamed", memA.n}
	r.list(true, renamed, memB)
	row := r.runKey(t)
	if !slices.Equal(row.IDs, ids(renamed, memB)) {
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
	if rows := r.rows(t); len(rows) != 1 || !slices.Equal(rows[0].Kinds, []string{classify.KindEnteredQuery}) {
		t.Errorf("rows after a later arrival = %+v, want exactly the arrival", rows)
	}
}

func TestMembership_ARenameWhoseAliasMovedBeforeTheRunNeverBlocksTheRun(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB)
	ctx := context.Background()
	// Another path (a summary refresh) already moved the alias in place.
	newRef, err := entity.NewRef("Acme-Org/Renamed", memA.n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.id.Put(ctx, memA.id, newRef); err != nil {
		t.Fatal(err)
	}
	renamed := mem{memA.id, "Acme-Org/Renamed", memA.n}
	r.list(true, renamed, memB)
	r.runKey(t) // must not fail
	if !slices.Equal(r.log.seen(), []string{"query_membership_no_alias"}) {
		t.Errorf("log = %v, want the previous id with no alias reported once", r.log.seen())
	}
	for _, row := range r.rows(t) {
		if slices.Contains(row.Kinds, classify.KindLeftQuery) {
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
	ctx := context.Background()
	// The alias of A moved to another reference, so the previous id cannot be
	// resolved; the new listing shows A under its old reference again, in a
	// different case.
	other, _ := entity.NewRef("Acme-Org/Elsewhere", 3)
	if _, err := r.id.Put(ctx, memA.id, other); err != nil {
		t.Fatal(err)
	}
	again := mem{memA.id, "acme-org/widget", memA.n}
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
		t.Errorf("rows = %+v, want no left_query from a truncated listing", rows)
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
				t.Fatalf("rows = %+v, want one left_query", rows)
			}
			if !slices.Equal(rows[0].Kinds, []string{classify.KindLeftQuery}) || rows[0].ID != "Acme-Org/Widget#1" || rows[0].Origin != "query" {
				t.Errorf("row = %+v", rows[0])
			}
			if f, ok := fieldOf(t, rows[0], "query:mine"); !ok || string(f.Before) != `"mine"` {
				t.Errorf("query field = %+v %v", f, ok)
			}
			if f, ok := fieldOf(t, rows[0], StateFieldPrefix+"mine"); !ok || string(f.After) != fmt.Sprintf("%q", state) {
				t.Errorf("state field = %+v %v, want after %q", f, ok, state)
			}
			if !slices.Equal(row.IDs, ids(memA, memB)) {
				t.Errorf("stored ids = %v", row.IDs)
			}
		})
	}
}

func TestMembership_AGoneConfirmationAppendsNoLeftQuery(t *testing.T) {
	r := newEngineRig(t)
	r.baselined(t, memA, memB, memC)
	r.conf.set(memC.id, Confirmation{Gone: true})
	r.list(true, memA, memB)
	row := r.runKey(t)
	if calls := r.conf.called(); len(calls) != 1 {
		t.Errorf("confirmer calls = %v, want one", calls)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the entity's tombstone path logs removed", rows)
	}
	if !slices.Equal(row.IDs, ids(memA, memB)) {
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
	if !slices.Equal(row.IDs, ids(memA, memB, memC)) || !strings.Contains(row.LastError, "origin said no") || !row.BaselineAt.Equal(baseline) {
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
	if len(rows) != 1 || !slices.Equal(rows[0].Kinds, []string{classify.KindLeftQuery}) || rows[0].ID != "Acme-Org/Widget#1" {
		t.Fatalf("rows = %+v, want the one left_query", rows)
	}
	if got := r.row(t, "mine"); !slices.Equal(got.IDs, ids(memA, memB)) || got.LastError != "" {
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
	if row := r.row(t, "mine"); !slices.Equal(row.IDs, ids(memA, memB, memC)) {
		t.Errorf("stored ids = %v, want the previous row kept", row.IDs)
	}
}

func TestMembership_WithoutAConfirmerADepartureIsNeitherRecordedNorDropped(t *testing.T) {
	r := newRigWith(t, func(c *Config) {
		e, err := changes.New(changes.Config{Clock: c.Clock})
		if err != nil {
			t.Fatal(err)
		}
		c.Changes = e
	})
	r.list(true, memA, memB, memC)
	r.runKey(t)
	r.list(true, memA, memB, memD) // D arrives, C departs
	if _, err := r.runner.run(context.Background(), Key{Name: "mine"}, governor.ClassQuery); err == nil {
		t.Fatal("a departure that cannot be confirmed must fail the run")
	}
	if row := r.row(t, "mine"); !slices.Equal(row.IDs, ids(memA, memB, memC)) {
		t.Errorf("stored ids = %v, want the previous row kept", row.IDs)
	}
	if rows := r.rows(t); len(rows) != 0 {
		t.Errorf("rows = %+v, want nothing stored with the result", rows)
	}
	// Arrivals alone (no departure) are still recorded.
	r.list(true, memA, memB, memC, memD)
	r.runKey(t)
	if rows := r.rows(t); len(rows) != 1 || !slices.Equal(rows[0].Kinds, []string{classify.KindEnteredQuery}) {
		t.Errorf("rows = %+v, want D's arrival", rows)
	}
}

// failingChanges appends through the engine, then fails on its nth call.
type failingChanges struct {
	inner Changes
	n     int
	calls int
}

func (f *failingChanges) AppendMembership(ctx context.Context, tx *store.Tx, m changes.Membership) error {
	f.calls++
	if f.calls == f.n {
		return errors.New("append failed")
	}
	return f.inner.AppendMembership(ctx, tx, m)
}

func TestMembership_TheStoredRowAndTheAppendedRowsAreVisibleTogetherOrNotAtAll(t *testing.T) {
	var fc *failingChanges
	r := newRigWith(t, func(c *Config) {
		e, err := changes.New(changes.Config{Clock: c.Clock})
		if err != nil {
			t.Fatal(err)
		}
		fc = &failingChanges{inner: e, n: 2}
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
	if row := r.row(t, "mine"); !slices.Equal(row.IDs, ids(memA)) {
		t.Errorf("stored ids = %v, want the previous row", row.IDs)
	}
	if _, ok, _ := r.st.GetEntity(context.Background(), entity.KindPR, memB.id); ok {
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

func TestMembership_WithChangesNilTheRunnerStoresWhatMainStoresPlusTheBaseline(t *testing.T) {
	r := newRig(t)
	r.list(true, memA, memB)
	first := r.runKey(t) // at t0
	r.list(true, memB, memC)
	second := r.runKey(t) // a minute later
	want := store.QueryRow{
		Key: "mine", Configured: true, IDs: ids(memB, memC), Complete: true,
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
}

func TestMembership_ADuplicateOrUnparsablePreviousIDNeverBlocksTheRun(t *testing.T) {
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
	if !slices.Equal(r.log.seen(), []string{"query_membership_unparsable_id"}) {
		t.Errorf("log = %v", r.log.seen())
	}
	if rows := r.rows(t); len(rows) != 0 || len(r.conf.called()) != 0 {
		t.Errorf("rows = %+v, confirms = %v, want none", rows, r.conf.called())
	}
}

// refFold is what the comparison of references rests on.
func TestRefFold_FoldsOwnerAndRepoButNotTheNumber(t *testing.T) {
	if refFold("Acme-Org/Widget#3") != refFold("acme-org/widget#3") {
		t.Error("owner and repository compare case-insensitively")
	}
	if refFold("Acme-Org/Widget#3") == refFold("Acme-Org/Widget#4") {
		t.Error("the number compares exactly")
	}
}

func TestMembershipPlan_IsEmptyWithoutABaseline(t *testing.T) {
	r := newEngineRig(t)
	plan, err := r.runner.planMembership(context.Background(), Key{Name: "mine"},
		store.QueryRow{Key: "mine", IDs: ids(memA), Complete: true}, listing{members: nil, complete: true})
	if err != nil || len(plan.arrivals) != 0 || len(plan.departures) != 0 {
		t.Errorf("plan = %+v err = %v, want empty without a baseline", plan, err)
	}
}

// scanCountingAliases is the real store with a count of the alias-by-reference
// reads it served. Each AliasByRefFold is one scan of the alias table (the fold
// cannot use the alias_ref index) and each AliasesByRefFold is one scan per
// chunk of 500 references, so the count is the number of scans.
type scanCountingAliases struct {
	*store.Store
	scans atomic.Int64
}

func (c *scanCountingAliases) AliasByRefFold(ctx context.Context, ref string) (store.AliasRow, bool, error) {
	c.scans.Add(1)
	return c.Store.AliasByRefFold(ctx, ref)
}

func (c *scanCountingAliases) AliasesByRefFold(ctx context.Context, refs []string) (map[string]store.AliasRow, error) {
	c.scans.Add(int64((len(refs) + 499) / 500))
	return c.Store.AliasesByRefFold(ctx, refs)
}

// planMembership must not scan the alias table once per previous member: with
// 150 previous members that was 150 scans on every run of a query (pg2-6cwpf).
// The test runs on the real store and also pins the plan's content: the
// departures are the resolvable previous members the listing no longer shows,
// in previous order, with the alias's own spelling and each node once.
func TestMembershipPlan_ReadsTheAliasTableOnceForAllPreviousMembers(t *testing.T) {
	ctx := context.Background()
	var counted *scanCountingAliases
	lg := &logs{}
	r := newRigWith(t, func(c *Config) {
		counted = &scanCountingAliases{Store: c.Store.(*store.Store)}
		c.Identity = entity.NewIdentity(counted, c.Clock)
		c.Log = lg.log
	})

	const n = 150
	var prevIDs []string
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("Acme-Org/Widget#%d", i+1)
		if err := r.st.PutAlias(ctx, store.AliasRow{NodeID: fmt.Sprintf("PR_%03d", i), Ref: ref, SeenAt: t0}); err != nil {
			t.Fatal(err)
		}
		// Queries store the reference in lower case: the match is case-folded.
		prevIDs = append(prevIDs, strings.ToLower(ref))
	}
	// An unparsable id, a reference no alias names, and a duplicate of the first.
	prevIDs = append(prevIDs, "not a reference", "acme-org/unknown#1", prevIDs[0])

	// The listing still shows the first 100; the other 50 departed.
	var members []member
	for i := 0; i < 100; i++ {
		ref, err := entity.ParseRef(fmt.Sprintf("Acme-Org/Widget#%d", i+1))
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, member{nodeID: fmt.Sprintf("PR_%03d", i), ref: ref})
	}

	prev := store.QueryRow{Key: "mine", IDs: prevIDs, Complete: true, BaselineAt: t0}
	plan, err := r.runner.planMembership(ctx, Key{Name: "mine"}, prev, listing{members: members, complete: true})
	if err != nil {
		t.Fatal(err)
	}

	if scans := counted.scans.Load(); scans > 1 {
		t.Errorf("planMembership scanned the alias table %d times for %d previous members, want one scan for all of them", scans, len(prevIDs))
	}
	if len(plan.arrivals) != 0 || len(plan.departures) != n-100 {
		t.Fatalf("plan = %d arrivals, %d departures, want 0 and %d", len(plan.arrivals), len(plan.departures), n-100)
	}
	for i, d := range plan.departures {
		wantNode, wantRef := fmt.Sprintf("PR_%03d", 100+i), fmt.Sprintf("Acme-Org/Widget#%d", 101+i)
		if d.node != wantNode || d.ref != wantRef {
			t.Fatalf("departure %d = %+v, want node %s ref %s", i, d, wantNode, wantRef)
		}
	}
	if want := []string{"query_membership_unparsable_id", "query_membership_no_alias"}; !slices.Equal(lg.seen(), want) {
		t.Errorf("log = %v, want %v", lg.seen(), want)
	}
}

// The fixtures are swapped by the test goroutine while the scheduler goroutine
// is inside them (the fake clock's Advance makes a refresh due, and the
// scheduler's own timer runs it). Both fakes MUST take their lock to swap, or
// the race detector fires here regardless of scheduling luck.
func TestFixtures_SwapWhileTheSchedulerGoroutineReadsThem(t *testing.T) {
	gh := &fakeGH{answer: func(call) ([]byte, error) { return nil, nil }}
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
			_, _ = gh.Run(context.Background(), "-f", "q=x")
			_, _ = conf.Confirm(context.Background(), memC.id, "")
		}
	}()
	for i := range 500 {
		gh.setAnswer(func(call) ([]byte, error) { return listBody(true, memA), nil })
		conf.set(memC.id, Confirmation{State: fmt.Sprint(i)})
	}
	close(stop)
	wg.Wait()
}
