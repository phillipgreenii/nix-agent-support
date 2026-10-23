package query

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
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
		c.Tracker = noTracker{} // the members are not refreshed: only the membership rows are under test
	})
	r.run(t)
	return &engineRig{rig: r, conf: conf, log: lg}
}

// list sets the listing the next run reads. The scheduler goroutine may be
// inside fakeGH.Run, so the swap goes through the fake's lock.
func (r *rig) list(complete bool, ms ...mem) {
	r.gh.setAnswer(func(call) ([]byte, error) { return listBody(complete, ms...), nil })
}

// runKey runs the configured query "mine" once, through the scheduler (the
// rig's scheduler loop MUST be running).
func (r *rig) runKey(t *testing.T) store.QueryRow {
	t.Helper()
	row, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
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

// noTracker tracks nothing.
type noTracker struct{}

func (noTracker) Track(context.Context, []string) ([]string, error) { return nil, nil }

// The adapter resolves every previous member in ONE batched read of the alias
// table: a lookup per reference scans the whole table once each (the fold
// cannot use the alias_ref index), so with 150 previous members that was 150
// scans on every run of a query (pg2-6cwpf). The test runs on the real store and
// also pins the answer: the alias's own spelling, an unparsable reference
// reported and answered with an empty Alias, a reference no alias names absent,
// and a duplicate answered like the original.
func TestIdentityAliases_ResolveReadsTheAliasTableOnceForAllReferences(t *testing.T) {
	ctx := context.Background()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	counted := &scanCountingAliases{Store: st}
	lg := &logs{}
	al := NewAliases(entity.NewIdentity(counted, governor.NewManualClock(t0)), lg.log)

	const n = 150
	var prevIDs []string
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("Acme-Org/Widget#%d", i+1)
		if err := st.PutAlias(ctx, store.AliasRow{NodeID: fmt.Sprintf("PR_%03d", i), Ref: ref, SeenAt: t0}); err != nil {
			t.Fatal(err)
		}
		// Queries store the reference in lower case: the match is case-folded.
		prevIDs = append(prevIDs, strings.ToLower(ref))
	}
	// An unparsable id, a reference no alias names, and a duplicate of the first.
	prevIDs = append(prevIDs, "not a reference", "acme-org/unknown#1", prevIDs[0])

	got, err := al.Resolve(ctx, prevIDs)
	if err != nil {
		t.Fatal(err)
	}
	if scans := counted.scans.Load(); scans > 1 {
		t.Errorf("Resolve scanned the alias table %d times for %d references, want one scan for all of them", scans, len(prevIDs))
	}
	for i := 0; i < n; i++ {
		a, ok := got[prevIDs[i]]
		wantNode, wantRef := fmt.Sprintf("PR_%03d", i), fmt.Sprintf("Acme-Org/Widget#%d", i+1)
		if !ok || a.ID != wantNode || a.CanonicalRef != wantRef {
			t.Fatalf("alias of %q = %+v (%v), want node %s ref %s", prevIDs[i], a, ok, wantNode, wantRef)
		}
	}
	if a, ok := got["not a reference"]; !ok || a != (shared.Alias{}) {
		t.Errorf("unparsable reference = %+v (%v), want an empty Alias", a, ok)
	}
	if _, ok := got["acme-org/unknown#1"]; ok {
		t.Error("a reference no alias names must be absent")
	}
	if want := []string{"query_membership_unparsable_id"}; !slices.Equal(lg.seen(), want) {
		t.Errorf("log = %v, want %v", lg.seen(), want)
	}
}

func TestIdentityAliases_PutRecordsTheAliasAndRefusesAnUnparsableReference(t *testing.T) {
	ctx := context.Background()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	id := entity.NewIdentity(st, governor.NewManualClock(t0))
	al := NewAliases(id, nil)
	if err := al.Put(ctx, "PR_a", "Acme-Org/Widget#3"); err != nil {
		t.Fatal(err)
	}
	if ref, ok, err := id.Ref(ctx, "PR_a"); err != nil || !ok || ref.String() != "Acme-Org/Widget#3" {
		t.Errorf("alias of PR_a = %v %v %v", ref, ok, err)
	}
	if err := al.Put(ctx, "PR_b", "not a reference"); err == nil {
		t.Error("an unparsable reference must be refused")
	}
}
