package groupsvc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const node = "PR_1"

// fetchers maps a group to its fake.
type fetchers map[string]*groupsvctest.Fetcher

func register(r *groupsvctest.Rig, names ...string) fetchers {
	out := fetchers{}
	for _, g := range names {
		out[g] = r.Register(groupsvctest.NewFetcher(g))
	}
	return out
}

func (f fetchers) total() int {
	n := 0
	for _, x := range f {
		n += x.CallCount()
	}
	return n
}

// newGov returns a governor with the given GraphQL cap and the whole cap open to
// the background (share 1).
func newGov(t *testing.T, clk *governor.ManualClock, graphqlCap int64) *governor.Governor {
	t.Helper()
	g, err := governor.New(governor.Config{
		GraphQL: governor.Limits{Cap: graphqlCap}, RESTCore: governor.Limits{Cap: 100}, RESTSearch: governor.Limits{Cap: 100},
		BackgroundShare: 1,
	}, governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func spent(g *governor.Governor) int64 {
	bs, _ := g.State().Bucket(governor.BucketGraphQL)
	return bs.Spent
}

// ensure runs Ensure with a generous deadline and fails the test on an error.
func ensure(t *testing.T, r *groupsvctest.Rig, id string, want []string, opt groupsvc.EnsureOptions) groupsvc.EnsureResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := r.Svc.Ensure(ctx, id, want, opt)
	if err != nil {
		t.Fatalf("Ensure(%s, %v) = %v", id, want, err)
	}
	return res
}

func result(t *testing.T, res groupsvc.EnsureResult, group string) groupsvc.GroupResult {
	t.Helper()
	for _, g := range res.Groups {
		if g.Group == group {
			return g
		}
	}
	t.Fatalf("no result for group %s in %+v", group, res.Groups)
	return groupsvc.GroupResult{}
}

// freshRow is a stored row of the group as a fetch at the rig's now would leave
// it, for the head and base of the origin's current summary.
func freshRow(r *groupsvctest.Rig, id, group, content string) store.GroupRow {
	sum := r.Origin.Summary(id)
	fresh, maxAge := r.Rules.Schedule(group, false, r.Clock.Now())
	return store.GroupRow{
		ID: id, Group: group, Content: []byte(content), Fingerprint: groups.Fingerprint([]byte(content)),
		FetchedAt: r.Clock.Now(), FreshUntil: fresh, MaxAgeAt: maxAge,
		FetchedForHead: sum.HeadRefOid, FetchedForBase: sum.BaseRefOid, Generation: 1,
	}
}

// staleRow is freshRow's counterpart past its fresh_until (but inside expiry).
func staleRow(r *groupsvctest.Rig, id, group, content string) store.GroupRow {
	row := freshRow(r, id, group, content)
	row.FetchedAt = r.Clock.Now().Add(-time.Hour)
	row.FreshUntil = r.Clock.Now().Add(-time.Minute)
	row.MaxAgeAt = time.Time{}
	return row
}

// seed stores the summary and the rows of an entity accessed now.
func seed(r *groupsvctest.Rig, id string, rows ...store.GroupRow) {
	r.SeedSummary(id, r.Clock.Now())
	for _, row := range rows {
		r.SeedGroup(row)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// observer records Served and Revalidated calls.
type observer struct {
	mu     sync.Mutex
	served map[string][]time.Duration
	reval  [][2]string
}

func newObserver() *observer { return &observer{served: map[string][]time.Duration{}} }

func (o *observer) Served(group string, staleness time.Duration) {
	o.mu.Lock()
	o.served[group] = append(o.served[group], staleness)
	o.mu.Unlock()
}

func (o *observer) Revalidated(group, result string) {
	o.mu.Lock()
	o.reval = append(o.reval, [2]string{group, result})
	o.mu.Unlock()
}

func (o *observer) servedOf(group string) []time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]time.Duration(nil), o.served[group]...)
}

// marker records the authentication markers.
type marker struct {
	mu      sync.Mutex
	marks   int
	clears  int
	lastErr error
}

func (m *marker) MarkUnauthenticated(err error) {
	m.mu.Lock()
	m.marks++
	m.lastErr = err
	m.mu.Unlock()
}

func (m *marker) Clear() {
	m.mu.Lock()
	m.clears++
	m.mu.Unlock()
}

func (m *marker) counts() (marks, clears int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.marks, m.clears
}
