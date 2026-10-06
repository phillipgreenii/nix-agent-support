package changes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// fakeLister answers every ListFingerprints call with one fixed result.
type fakeLister struct {
	res gather.ListFingerprintsResult
	err error
}

func (f fakeLister) ListFingerprints(context.Context, string, string) (gather.ListFingerprintsResult, error) {
	return f.res, f.err
}

// queryLister answers per watched query and counts the calls it receives.
type queryLister map[string]fakeLister

func (q queryLister) ListFingerprints(_ context.Context, _, query string) (gather.ListFingerprintsResult, error) {
	return q[query].res, q[query].err
}

type fakeHydrator struct{ calls []string }

func (f *fakeHydrator) RunEntityChange(_ context.Context, _ string, id string, _ gather.ChangeKind, _ pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	f.calls = append(f.calls, id)
	return pipeline.EntityChangeResult{}, nil
}

func seededEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	st := store.OpenNewSchemaForTest(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	// The seeded records are newer than reconcile_age (30m) at the engine's
	// clock, so the local reconcile tier leaves these entities alone.
	for _, id := range []string{"bd-1", "bd-2"} {
		if _, err := st.WriteEntityStateWithLog(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, "2026-10-01T11:00:00Z", true, []string{"reconcile"}, "sweep", "2026-10-01T11:55:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open"}
	return &Engine{
		Cfg: cfg, Store: st, Hydrator: &fakeHydrator{},
		Lister: fakeLister{res: okListing()},
		Now:    func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
	}, st
}

func cursorOf(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	cs, err := st.ListConsumers()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name {
			return c.Cursor
		}
	}
	return -1
}

// A flush that fails MUST leave the cursor where it was: the same records are
// re-delivered by the next call.
func TestRunDoesNotAdvanceCursorWhenTheFlushFails(t *testing.T) {
	e, st := seededEngine(t)
	opts := Options{EntityType: "issue", Consumer: "router"}
	_, err := e.Run(context.Background(), opts, func(Envelope) error { return errors.New("broken pipe") })
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("err = %v", err)
	}
	if got := cursorOf(t, st, "router"); got != 0 {
		t.Fatalf("cursor = %d after a failed flush, want 0", got)
	}

	var env Envelope
	if _, err := e.Run(context.Background(), opts, func(v Envelope) error { env = v; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 2 || cursorOf(t, st, "router") != env.Cursor.To {
		t.Errorf("redelivery = %+v, cursor %d", env, cursorOf(t, st, "router"))
	}
}

// A quiet listing (nothing listed, nothing persisted) hydrates nothing and
// writes nothing: the seeded entities were hydrated within sweep.max_age and
// logged within reconcile_age, so neither sweep tier touches them.
func TestRunQuietListingHydratesNothing(t *testing.T) {
	e, _ := seededEngine(t)
	h := e.Hydrator.(*fakeHydrator)
	if _, err := e.Run(context.Background(), Options{EntityType: "issue", Consumer: "router"}, func(Envelope) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 {
		t.Errorf("hydrated %v", h.calls)
	}
}

func TestSelectQueries(t *testing.T) {
	cfg := &config.Config{}
	cfg.Watch.PR.Queries = []string{"mine", "team"}
	if got, err := SelectQueries(cfg, "pr", ""); err != nil || len(got) != 2 {
		t.Errorf("all = %v, %v", got, err)
	}
	if got, err := SelectQueries(cfg, "pr", "team"); err != nil || len(got) != 1 || got[0] != "team" {
		t.Errorf("one = %v, %v", got, err)
	}
	if _, err := SelectQueries(cfg, "pr", "nope"); err == nil {
		t.Error("an unknown query was accepted")
	}
	if _, err := SelectQueries(cfg, "issue", ""); err == nil || !strings.Contains(err.Error(), "watch.issue.queries") {
		t.Errorf("no queries: %v", err)
	}
}

// --- the age sweep's two tiers ---

// seedTierRow stores an active issue with the given hydrated_at, list_fp and
// (when logAt is non-empty) one change_log row stamped logAt; an empty logAt
// writes no kinds, so the entity has NO change_log row.
func seedTierRow(t *testing.T, st *store.Store, id, hydratedAt, listFP, logAt string) {
	t.Helper()
	var kinds []string
	if logAt != "" {
		kinds = []string{"reconcile"}
	}
	fp := listFP
	if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{"k":1}`, AsOf: "x"},
		0, hydratedAt, true, &fp, kinds, "sweep", logAt); err != nil {
		t.Fatal(err)
	}
}

// noConnectorLister fails the test if any pg-connector call is made.
type noConnectorLister struct{ t *testing.T }

func (p noConnectorLister) ListFingerprints(context.Context, string, string) (gather.ListFingerprintsResult, error) {
	p.t.Fatal("the local reconcile tier made a connector call")
	return gather.ListFingerprintsResult{}, nil
}

func historyOf(t *testing.T, st *store.Store, id string) []store.ChangeRecord {
	t.Helper()
	h, err := st.ListEntityHistory("o/r", "issue", id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A local-tier record needs no remote call and no hydration; it appends one
// reconcile record and leaves hydrated_at, active, list_fp and the snapshot
// alone while advancing the version [design 3.3 step 5].
func TestReconcileLocalRecordsWithoutRemoteCallOrHydration(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = noConnectorLister{t}
	seedTierRow(t, st, "bd-1", "2026-10-01T11:58:00Z", "F1", "2026-10-01T09:00:00Z") // log 3h old
	before, _, _ := st.GetEntity("o/r", "issue", "bd-1")

	if err := e.reconcileLocal(context.Background(), "issue", &pollState{hydrated: map[string]error{}}); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 {
		t.Errorf("hydrator called for %v, want zero hydrations", h.calls)
	}
	after, found, err := st.GetEntity("o/r", "issue", "bd-1")
	if err != nil || !found {
		t.Fatalf("GetEntity = %v, %v", found, err)
	}
	if after.HydratedAt != before.HydratedAt || after.Inactive || after.ListFP != "F1" || after.Facts != before.Facts {
		t.Errorf("row changed beyond the version: before %+v after %+v", before, after)
	}
	if after.Version != before.Version+1 {
		t.Errorf("version = %d, want %d", after.Version, before.Version+1)
	}
	hist := historyOf(t, st, "bd-1")
	if len(hist) != 2 {
		t.Fatalf("change_log has %d rows, want 2 (seed + local reconcile)", len(hist))
	}
	if got := hist[0]; !reflect.DeepEqual(got.Kinds, []string{"reconcile"}) || got.Origin != OriginLocalReconcile ||
		got.Version != after.Version || got.At != "2026-10-01T12:00:00Z" {
		t.Errorf("newest record = %+v", got)
	}

	// The record restarted the local age: a second pass emits nothing more.
	if err := e.reconcileLocal(context.Background(), "issue", &pollState{hydrated: map[string]error{}}); err != nil {
		t.Fatal(err)
	}
	if got := len(historyOf(t, st, "bd-1")); got != 2 {
		t.Errorf("second pass left %d rows, want 2", got)
	}
}

// A real call delivers the local-tier record to the consumer and hydrates
// nothing for it.
func TestRunDeliversLocalReconcileWithoutHydration(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: okListing()}
	seedTierRow(t, st, "bd-1", "2026-10-01T11:58:00Z", "F1", "2026-10-01T09:00:00Z")

	env, out, err := runTick(t, e)
	if err != nil || out.Partial {
		t.Fatalf("run = %+v, %v", out, err)
	}
	if len(h.calls) != 0 {
		t.Errorf("hydrated %v", h.calls)
	}
	var origins []string
	for _, r := range env.Records {
		origins = append(origins, r.Origin)
	}
	if !reflect.DeepEqual(origins, []string{"sweep", OriginLocalReconcile}) {
		t.Errorf("record origins = %v, want the seed then one local-reconcile", origins)
	}
}

// Both tiers are capped per poll and the cap carries work forward: 88
// entities reset together fall due over several polls, never all at once and
// never twice.
func TestLocalTierCapCarriesWorkForward(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: okListing()}
	cap := 20
	e.Cfg.Sweep.MaxPerPoll = &cap
	var ids []string
	for i := 0; i < 88; i++ {
		id := fmt.Sprintf("bd-%02d", i)
		ids = append(ids, id)
		seedTierRow(t, st, id, "2026-10-01T11:58:00Z", "F", "2026-10-01T10:00:00Z") // all reset together, 2h ago
	}

	seen := map[string]bool{}
	for poll, want := range []int{20, 20, 20, 20, 8, 0} {
		before := map[string]int{}
		for _, id := range ids {
			before[id] = len(historyOf(t, st, id))
		}
		if _, _, err := runTick(t, e); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, id := range ids {
			if d := len(historyOf(t, st, id)) - before[id]; d > 0 {
				if d != 1 || seen[id] {
					t.Fatalf("poll %d: %s got %d new records (seen before: %v)", poll, id, d, seen[id])
				}
				seen[id] = true
				n++
			}
		}
		if n != want {
			t.Errorf("poll %d reconciled %d entities, want %d", poll, n, want)
		}
	}
	if len(seen) != 88 {
		t.Errorf("%d of 88 reconciled, want every one exactly once", len(seen))
	}
	if len(h.calls) != 0 {
		t.Errorf("hydrator called %d times, want zero", len(h.calls))
	}
}

// The remote tier re-hydrates entities older than sweep.max_age plus jitter,
// capped per poll and oldest first; what the cap leaves is selected next poll.
func TestRemoteTierCapCarriesWorkForward(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: okListing()}
	cap := 10
	e.Cfg.Sweep.MaxPerPoll = &cap
	hydrated := "2026-10-01T00:00:00Z" // 12h before the engine clock: due at any jitter
	for i := 0; i < 25; i++ {
		// A recent log row keeps the local tier out of this test.
		seedTierRow(t, st, fmt.Sprintf("bd-%02d", i), hydrated, "F", "2026-10-01T11:59:00Z")
	}
	seedTierRow(t, st, "recent", "2026-10-01T06:30:00Z", "F", "2026-10-01T11:59:00Z") // 5h30m: not due

	var batches [][]string
	for range 4 {
		h.calls = nil
		if _, _, err := runTick(t, e); err != nil {
			t.Fatal(err)
		}
		batches = append(batches, append([]string(nil), h.calls...))
	}
	if len(batches[0]) != 10 || len(batches[1]) != 10 || len(batches[2]) != 5 || len(batches[3]) != 0 {
		t.Fatalf("batch sizes = %d,%d,%d,%d, want 10,10,5,0", len(batches[0]), len(batches[1]), len(batches[2]), len(batches[3]))
	}
	seen := map[string]bool{}
	for _, b := range batches {
		for _, id := range b {
			if seen[id] || id == "recent" {
				t.Fatalf("%s re-hydrated or not due", id)
			}
			seen[id] = true
			if h.origins[id] != OriginSweep || h.changes[id] != gather.ChangeSweep {
				t.Errorf("%s hydrated as %s/%s", id, h.origins[id], h.changes[id])
			}
		}
	}
	// Oldest first by due time: with equal hydrated_at that is smallest jitter first.
	first := append([]string(nil), batches[0]...)
	all := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		all = append(all, fmt.Sprintf("bd-%02d", i))
	}
	sort.Slice(all, func(i, j int) bool {
		oi, oj := jitterOffset(all[i], 6*time.Hour), jitterOffset(all[j], 6*time.Hour)
		if oi != oj {
			return oi < oj
		}
		return all[i] < all[j]
	})
	if !reflect.DeepEqual(first, all[:10]) {
		t.Errorf("first batch = %v, want the 10 earliest-due %v", first, all[:10])
	}
}
