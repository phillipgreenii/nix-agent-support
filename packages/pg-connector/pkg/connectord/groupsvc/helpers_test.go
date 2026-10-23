package groupsvc

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The invented connectors of the shared tests: kind "alpha" and kind "beta".
// Their wire references look like "Zone-A/Item-B~7" (a "~" marks a reference; the
// part after it is a number); a store id has no "~". Stamps are opaque words.
const (
	groupSide    = "side"    // a timed background group
	groupStamped = "stamped" // a stamped background group with no time limit
	groupPassive = "passive" // a group the background cycle never touches
)

var (
	errTestUnknown = errors.New("test: unknown entity")
	errTestBadRef  = errors.New("test: malformed reference")
	errTestNoSuch  = errors.New("test: no such entity")
)

func specOf(kind, primary string) registry.Spec {
	return registry.Spec{
		EntityKind:   kind,
		PrimaryGroup: primary,
		Groups:       []string{primary, groupSide, groupStamped, groupPassive},
		FoldRef:      strings.ToLower,
	}
}

var (
	specAlpha = specOf("alpha", "core")
	specBeta  = specOf("beta", "hub")
)

// parseID is the invented connector's reference rule.
func parseID(id string) (string, bool, error) {
	_, num, found := strings.Cut(id, "~")
	if !found {
		return id, false, nil
	}
	if _, err := strconv.Atoi(num); err != nil {
		return "", true, errTestBadRef
	}
	return id, true, nil
}

func rulesTable() groupkit.Table {
	return groupkit.Table{Groups: []groupkit.GroupSpec{
		{Name: groupSide, TTL: 10 * time.Minute, Background: true},
		{Name: groupStamped, Stamped: true, Background: true},
		{Name: groupPassive, TTL: 5 * time.Minute},
	}}
}

// fakeAliases is an in-memory query.Aliases: a case-folded reference to an id.
type fakeAliases struct {
	mu      sync.Mutex
	byRef   map[string]query.Alias
	byID    map[string]string
	resolve int // the number of Resolve calls
}

var _ query.Aliases = (*fakeAliases)(nil)

func newFakeAliases() *fakeAliases {
	return &fakeAliases{byRef: map[string]query.Alias{}, byID: map[string]string{}}
}

func (a *fakeAliases) Put(_ context.Context, id, ref string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byRef[strings.ToLower(ref)] = query.Alias{ID: id, CanonicalRef: ref}
	a.byID[id] = ref
	return nil
}

func (a *fakeAliases) Resolve(_ context.Context, refs []string) (map[string]query.Alias, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolve++
	out := map[string]query.Alias{}
	for _, r := range refs {
		if al, ok := a.byRef[strings.ToLower(r)]; ok {
			out[r] = al
		}
	}
	return out, nil
}

func (a *fakeAliases) Ref(_ context.Context, id string) (string, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref, ok := a.byID[id]
	return ref, ok, nil
}

func (a *fakeAliases) resolveCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resolve
}

// fakeKeepAlive retains the ids it was told to and records accesses.
type fakeKeepAlive struct {
	mu     sync.Mutex
	retain map[string]bool
}

func (k *fakeKeepAlive) Retain(_ context.Context, id string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.retain[id], nil
}

func (k *fakeKeepAlive) Access(context.Context, string) error { return nil }

func (k *fakeKeepAlive) keep(ids ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, id := range ids {
		k.retain[id] = true
	}
}

// badContent is the primary content the fake describer cannot decode.
const badContent = "not-decodable"

type fakeDescriber struct{}

func (fakeDescriber) Describe(content []byte) (engine.Primary, error) {
	if string(content) == badContent {
		return engine.Primary{}, errors.New("test: cannot decode")
	}
	return engine.Primary{Ref: string(content), Stamp: "s1", Stamp2: "s2"}, nil
}

// fakeFetcher is a Fetcher that is never asked to fetch in these tests.
type fakeFetcher struct{ group string }

func (f fakeFetcher) Group() string { return f.group }
func (f fakeFetcher) MaxBatch() int { return 10 }
func (f fakeFetcher) Spend(governor.Class, int) governor.Request {
	return governor.Request{}
}

func (f fakeFetcher) Fetch(_ context.Context, _ CallMeta, targets []Target) []Outcome {
	return make([]Outcome, len(targets))
}

// hintLog records the origin hints.
type hintLog struct {
	mu    sync.Mutex
	calls [][3]string
}

func (h *hintLog) NoteOrigin(id, group, origin string) {
	h.mu.Lock()
	h.calls = append(h.calls, [3]string{id, group, origin})
	h.mu.Unlock()
}

func (h *hintLog) all() [][3]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][3]string(nil), h.calls...)
}

// rig is a Service over a real store and scheduler on a manual clock.
type rig struct {
	t       *testing.T
	svc     *Service
	st      *store.Store
	sched   *scheduler.Scheduler
	clk     *governor.ManualClock
	aliases *fakeAliases
	keep    *fakeKeepAlive
	hints   *hintLog
}

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newRig builds the shared Service for spec over st (nil opens a fresh store) and
// clk (nil starts a fresh manual clock). Until New exists this helper is the only
// way a test gets a Service; it registers a fetcher for the three non-primary
// groups the way Register will.
func newRig(t *testing.T, spec registry.Spec, st *store.Store, clk *governor.ManualClock) *rig {
	t.Helper()
	if st == nil {
		st = openStore(t)
	}
	if clk == nil {
		clk = governor.NewManualClock(t0)
	}
	sched := scheduler.New(scheduler.Options{Clock: clk, Workers: 1})
	r := &rig{
		t: t, st: st, sched: sched, clk: clk, aliases: newFakeAliases(),
		keep: &fakeKeepAlive{retain: map[string]bool{}}, hints: &hintLog{},
	}
	r.svc = &Service{
		cfg: Config{
			Spec: spec, Describer: fakeDescriber{}, Store: st, Scheduler: sched, Rules: rulesTable(),
			Aliases: r.aliases, KeepAlive: r.keep, Clock: clk, Expiry: time.Hour,
			NoSuchEntity: errTestNoSuch, BadRef: errTestBadRef, UnknownEntity: errTestUnknown, ParseID: parseID,
		},
		clock:    clk,
		fetchers: map[string]Fetcher{},
		hints:    r.hints,
	}
	r.svc.writes.init()
	for _, g := range []string{groupSide, groupStamped, groupPassive} {
		r.svc.fetchers[g] = fakeFetcher{g}
	}
	return r
}

func (r *rig) kind() string { return r.svc.cfg.Spec.EntityKind }

// seedEntity stores an entity row accessed at the given time.
func (r *rig) seedEntity(id string, access time.Time) {
	r.t.Helper()
	if err := r.st.TouchEntity(context.Background(), r.kind(), id, access); err != nil {
		r.t.Fatal(err)
	}
}

// seedPrimary stores the entity's primary content (accessed now) and returns nothing.
func (r *rig) seedPrimary(id, content string) {
	r.t.Helper()
	r.seedEntity(id, r.clk.Now())
	r.seedGroup(store.GroupRow{ID: id, Group: r.svc.cfg.Spec.PrimaryGroup, Content: []byte(content), Generation: 1})
}

func (r *rig) seedGroup(row store.GroupRow) {
	r.t.Helper()
	row.Kind = r.kind()
	if err := r.st.PutGroup(context.Background(), row); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) group(id, group string) (store.GroupRow, bool) {
	r.t.Helper()
	row, ok, err := r.st.GetGroup(context.Background(), r.kind(), id, group)
	if err != nil {
		r.t.Fatal(err)
	}
	return row, ok
}

func (r *rig) putQuery(key string, ids ...string) {
	r.t.Helper()
	if err := r.st.PutQuery(context.Background(), store.QueryRow{Key: key, Configured: true, IDs: ids, Complete: true}); err != nil {
		r.t.Fatal(err)
	}
}

// depth is how many tasks of the class are queued.
func (r *rig) depth(class governor.Class) int {
	return r.sched.State().Classes[int(class)-1].Depth
}

func queryRow(key string, ids ...string) store.QueryRow {
	return store.QueryRow{Key: key, IDs: ids}
}

// freshGroup is a stored row of the group as a fetch at the rig's now would leave it.
func freshGroup(r *rig, id, group, content string) store.GroupRow {
	fresh, maxAge := r.svc.cfg.Rules.Schedule(group, false, r.clk.Now())
	return store.GroupRow{
		ID: id, Group: group, Content: []byte(content), Fingerprint: groupkit.Fingerprint([]byte(content)),
		FetchedAt: r.clk.Now(), FreshUntil: fresh, MaxAgeAt: maxAge, Generation: 1,
	}
}

// staleGroup is freshGroup's counterpart past its fresh_until.
func staleGroup(r *rig, id, group, content string) store.GroupRow {
	row := freshGroup(r, id, group, content)
	row.FetchedAt = r.clk.Now().Add(-time.Hour)
	row.FreshUntil = r.clk.Now().Add(-time.Minute)
	row.MaxAgeAt = time.Time{}
	return row
}
