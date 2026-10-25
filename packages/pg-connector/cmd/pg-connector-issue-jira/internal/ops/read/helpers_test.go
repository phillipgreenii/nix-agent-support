package read

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

const (
	issueKey   = "ABC-1"
	summaryTTL = time.Minute
	detailTTL  = 10 * time.Minute
	testExpiry = 24 * time.Hour
)

// fakeOrigin is the state shared by the fakes: what the origin holds for each
// issue, whether it can be reached, and how many calls reached it.
type fakeOrigin struct {
	mu sync.Mutex
	// issues is what the origin shows; an issue absent from it is not found.
	issues map[string]schema.Issue
	// descriptions is the origin's description of each issue.
	descriptions map[string]string
	// err, when set, is what every origin call fails with.
	err error
	// summaryCalls and detailCalls count the calls that reached the origin.
	summaryCalls, detailCalls int
	// watchedAtSummaryFetch records whether the issue was watched when the
	// summary was fetched (the ordering of a one-off watch).
	watchedAtSummaryFetch map[string]bool
}

func newFakeOrigin() *fakeOrigin {
	return &fakeOrigin{
		issues:                map[string]schema.Issue{},
		descriptions:          map[string]string{},
		watchedAtSummaryFetch: map[string]bool{},
	}
}

func (o *fakeOrigin) set(iss schema.Issue, description string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.issues[iss.ID] = iss
	o.descriptions[iss.ID] = description
}

func (o *fakeOrigin) setErr(err error) {
	o.mu.Lock()
	o.err = err
	o.mu.Unlock()
}

func (o *fakeOrigin) counts() (summaryCalls, detailCalls int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.summaryCalls, o.detailCalls
}

// rig is a temp store, a manual clock, the real change engine and fakes of the
// two group services and the query runner, around the handlers.
type rig struct {
	t       *testing.T
	st      *store.Store
	clk     *governor.ManualClock
	origin  *fakeOrigin
	chg     *changes.Engine
	auth    *origin.AuthState
	queries *fakeQueries
	summary *fakeSummary
	detail  *fakeDetail
	ops     map[string]scriptout.OpHandler
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	chg, err := changes.New(changes.Config{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, clk: clk, origin: newFakeOrigin(), chg: chg, auth: origin.NewAuthState()}
	r.queries = &fakeQueries{r: r, defined: map[string]bool{"mine": true}}
	r.summary, r.detail = &fakeSummary{r: r}, &fakeDetail{r: r}
	r.ops = Handlers(Deps{
		Auth:    r.auth,
		Summary: r.summary,
		Detail:  r.detail,
		Runner:  r.queries,
		Chg:     chg,
		Store:   st,
		Expiry:  testExpiry,
		Clock:   clk,
	})
	return r
}

// call runs an op with args and returns its answer.
func (r *rig) call(op, args string) (any, error) {
	r.t.Helper()
	h, ok := r.ops[op]
	if !ok {
		r.t.Fatalf("no handler for %q", op)
	}
	return h.Handle(context.Background(), json.RawMessage(args))
}

// issueOf is a schema.Issue as the origin shows it.
func issueOf(id, title string) schema.Issue {
	return schema.Issue{
		ID: id, Title: title, State: "To Do", URL: "https://example.invalid/" + id,
		StatusCategory: "new", CommentCount: 2, LatestCommentAt: "2026-10-04T10:00:00.000+0000",
		AsOf: "ignored", Stale: true,
	}
}

// putSummary stores an issue's entity row and summary group as a fetch at
// fetchedAt would.
func (r *rig) putSummary(iss schema.Issue, fetchedAt time.Time) {
	r.t.Helper()
	content, err := json.Marshal(groups.SummaryContent{Issue: iss})
	if err != nil {
		r.t.Fatal(err)
	}
	r.putEntity(iss.ID)
	if err := r.st.PutGroup(context.Background(), store.GroupRow{
		Kind: groups.EntityKind, ID: iss.ID, Group: groups.Summary, Content: content, Fingerprint: "f",
		FetchedAt: fetchedAt, FreshUntil: fetchedAt.Add(summaryTTL), Generation: 1,
	}); err != nil {
		r.t.Fatal(err)
	}
}

// putDetail stores an issue's detail group.
func (r *rig) putDetail(id string, d groups.DetailContent, fetchedAt time.Time) {
	r.t.Helper()
	content, err := json.Marshal(d)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.PutGroup(context.Background(), store.GroupRow{
		Kind: groups.EntityKind, ID: id, Group: groups.Detail, Content: content, Fingerprint: "f",
		FetchedAt: fetchedAt, FreshUntil: fetchedAt.Add(detailTTL), Generation: 1,
	}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) putEntity(id string) {
	r.t.Helper()
	ent, ok, err := r.st.GetEntity(context.Background(), groups.EntityKind, id)
	if err != nil {
		r.t.Fatal(err)
	}
	if !ok {
		ent = store.EntityRow{Kind: groups.EntityKind, ID: id, Version: 1, LastAccess: r.clk.Now(), KeepAlive: true}
	}
	if err := r.st.PutEntity(context.Background(), ent); err != nil {
		r.t.Fatal(err)
	}
}

// seed stores a known, fresh issue with its detail.
func (r *rig) seed(id, title, description string) {
	r.t.Helper()
	iss := issueOf(id, title)
	r.putSummary(iss, r.clk.Now())
	r.putDetail(id, groups.DetailContent{
		Description: description, Deps: []schema.IssueDependency{{ID: "ABC-9", Type: "blocks"}},
		StatusChangedAt: "2026-10-01T00:00:00Z", OperatorUpdatedAt: "2026-10-02T00:00:00Z",
	}, r.clk.Now())
	r.origin.set(iss, description)
}

func (r *rig) isWatched(id string) bool {
	r.t.Helper()
	ent, ok, err := r.st.GetEntity(context.Background(), groups.EntityKind, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return ok && !ent.WatchedAt.IsZero()
}

func (r *rig) rows(id string) []store.ChangeRow {
	r.t.Helper()
	rows, err := r.st.LastChanges(context.Background(), groups.EntityKind, id, 50)
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

// fakeSummary is a summary.Service with the same contract over the store: a
// non-Jira key is not_found, a stored summary inside fresh_until is a local hit,
// otherwise it asks the fake origin and stores what it shows.
type fakeSummary struct {
	r     *rig
	mu    sync.Mutex
	calls []summary.EnsureOptions
}

func (f *fakeSummary) Ensure(ctx context.Context, key string, opts summary.EnsureOptions) (summary.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, opts)
	f.mu.Unlock()
	return f.ensure(ctx, key, opts)
}

// ensure is Ensure without the call record: the detail fake makes the summary
// current through it, as the shared group service does for a primary group.
func (f *fakeSummary) ensure(ctx context.Context, key string, opts summary.EnsureOptions) (summary.Result, error) {
	r := f.r
	if !internal.IsJiraKey(key) {
		return summary.Result{}, scriptout.WrapError(scriptout.ErrNotFound, "not a key")
	}
	id := groups.FoldRef(key)
	now := r.clk.Now()
	row, have, err := r.st.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	if err != nil {
		return summary.Result{}, err
	}
	var stored groups.SummaryContent
	if have {
		_ = json.Unmarshal(row.Content, &stored)
	}
	if have && !opts.Fresh && row.FreshUntil.After(now) {
		return summary.Result{Content: stored, Row: row, AgeSeconds: int64(now.Sub(row.FetchedAt) / time.Second)}, nil
	}
	o := r.origin
	o.mu.Lock()
	failure := o.err
	iss, shown := o.issues[id]
	if failure == nil {
		o.summaryCalls++
		o.watchedAtSummaryFetch[id] = r.isWatched(id)
	}
	o.mu.Unlock()
	if failure != nil {
		if have && now.Sub(row.FetchedAt) <= testExpiry {
			return summary.Result{Content: stored, Row: row, Stale: true, AgeSeconds: int64(now.Sub(row.FetchedAt) / time.Second)}, nil
		}
		return summary.Result{}, scriptout.WrapError(scriptout.ErrUnavailable,
			"issue "+id+" has no stored summary and the origin cannot be asked: "+failure.Error())
	}
	if !shown {
		return summary.Result{}, scriptout.WrapError(scriptout.ErrNotFound, "issue "+id+" not found")
	}
	r.putSummary(iss, now)
	row, _, _ = r.st.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	return summary.Result{Content: groups.SummaryContent{Issue: iss}, Row: row, FetchedNow: true}, nil
}

// fakeDetail is the detail Ensure with the same contract over the store.
type fakeDetail struct {
	r     *rig
	mu    sync.Mutex
	calls []groupsvc.EnsureOptions
}

func (f *fakeDetail) Ensure(ctx context.Context, id string, want []string, opt groupsvc.EnsureOptions) (groupsvc.EnsureResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, opt)
	f.mu.Unlock()
	r := f.r
	// The shared service makes the primary group current first: it fetches the
	// summary when it is past fresh_until, and always when the call is forced.
	psum, perr := r.summary.ensure(ctx, id, summary.EnsureOptions{Fresh: opt.Fresh})
	now := r.clk.Now()
	row, have, err := r.st.GetGroup(ctx, groups.EntityKind, id, groups.Detail)
	if err != nil {
		return groupsvc.EnsureResult{}, err
	}
	if perr != nil && !errors.Is(perr, scriptout.ErrUnavailable) {
		return groupsvc.EnsureResult{}, perr
	}
	primaryFailed := perr != nil || psum.Stale
	res := func(g groupsvc.GroupResult) (groupsvc.EnsureResult, error) {
		g.Group = groups.Detail
		return groupsvc.EnsureResult{Groups: []groupsvc.GroupResult{g}}, nil
	}
	if primaryFailed {
		// Nothing is judged fresh against a primary that could not be refreshed.
		cause := perr
		if cause == nil {
			r.origin.mu.Lock()
			cause = r.origin.err
			r.origin.mu.Unlock()
		}
		if have && now.Sub(row.FetchedAt) <= testExpiry {
			return res(groupsvc.GroupResult{Row: row, Status: groupsvc.Stale, Err: cause})
		}
		return res(groupsvc.GroupResult{Status: groupsvc.Unavailable, Err: scriptout.WrapError(scriptout.ErrUnavailable, cause.Error())})
	}
	if have && !opt.Fresh && row.FreshUntil.After(now) {
		return res(groupsvc.GroupResult{Row: row, Status: groupsvc.Fresh})
	}
	o := r.origin
	o.mu.Lock()
	failure := o.err
	desc := o.descriptions[id]
	if failure == nil {
		o.detailCalls++
	}
	o.mu.Unlock()
	if failure != nil {
		if have && now.Sub(row.FetchedAt) <= testExpiry {
			return res(groupsvc.GroupResult{Row: row, Status: groupsvc.Stale, Err: failure})
		}
		return res(groupsvc.GroupResult{Status: groupsvc.Unavailable, Err: scriptout.WrapError(scriptout.ErrUnavailable, failure.Error())})
	}
	r.putDetail(id, groups.DetailContent{Description: desc}, now)
	row, _, _ = r.st.GetGroup(ctx, groups.EntityKind, id, groups.Detail)
	return res(groupsvc.GroupResult{Row: row, Status: groupsvc.Fresh, FromOrigin: true})
}

// fakeQueries is the query runner over the store: a defined name has a stored
// row (or none) and Fetch re-runs it against what the fake holds.
type fakeQueries struct {
	r       *rig
	mu      sync.Mutex
	defined map[string]bool
	// result is what a re-run stores.
	result  []string
	partial bool
	err     error
	fetches int
}

func (q *fakeQueries) Expr(name string) ([]string, bool) {
	if !q.defined[name] {
		return nil, false
	}
	return []string{"project = ABC"}, true
}

func (q *fakeQueries) Row(ctx context.Context, key lq.Key) (store.QueryRow, bool, error) {
	return q.r.st.GetQuery(ctx, key.String())
}

func (q *fakeQueries) Fetch(ctx context.Context, key lq.Key, _ ...lq.FetchOptions) (store.QueryRow, error) {
	q.mu.Lock()
	q.fetches++
	err, ids, partial := q.err, q.result, q.partial
	q.mu.Unlock()
	if err != nil {
		return store.QueryRow{}, err
	}
	now := q.r.clk.Now()
	row := store.QueryRow{
		Key: key.String(), Configured: true, IDs: ids, Complete: !partial,
		FetchedAt: now, FreshUntil: now.Add(5 * time.Minute), LastSuccessAt: now,
	}
	if err := q.r.st.PutQuery(ctx, row); err != nil {
		return store.QueryRow{}, err
	}
	return row, nil
}

func (q *fakeQueries) fetchCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.fetches
}

// putQuery stores a query row fetched at fetchedAt with a ttl of five minutes.
func (r *rig) putQuery(name string, complete bool, fetchedAt time.Time, ids ...string) {
	r.t.Helper()
	if err := r.st.PutQuery(context.Background(), store.QueryRow{
		Key: name, Configured: true, IDs: ids, Complete: complete,
		FetchedAt: fetchedAt, FreshUntil: fetchedAt.Add(5 * time.Minute), LastSuccessAt: fetchedAt,
	}); err != nil {
		r.t.Fatal(err)
	}
}

func rowAt(fetched time.Time) store.GroupRow {
	return store.GroupRow{FetchedAt: fetched, FreshUntil: fetched.Add(time.Minute)}
}
