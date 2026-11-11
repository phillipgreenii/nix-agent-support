package settle

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// perFileLastSeen is the previous implementation, kept as the oracle: three
// statements for every file.
func perFileLastSeen(st *store.Store, f store.File) time.Time {
	ns := f.FirstSeen
	var lc int64
	if err := st.Read().QueryRow(`SELECT last_copy_ns FROM files WHERE id=?`, f.ID).Scan(&lc); err == nil {
		ns = max(ns, lc)
	}
	if sn, err := st.Snapshots(f.ID); err == nil {
		for _, x := range sn {
			ns = max(ns, x.SeenNS)
		}
	}
	if ts, err := st.Tombstones(f.ID); err == nil {
		for _, t := range ts {
			ns = max(ns, t)
		}
	}
	return time.Unix(0, ns)
}

// lastSeenFixture creates n single-file conversations that cycle through the
// shapes lastSeen distinguishes: copy commits only, a snapshot version, a
// tombstone newer than the rest, and a file with no history at all.
func lastSeenFixture(e *csEnv, n int) map[string]*conv {
	groups := map[string]*conv{}
	for i := 0; i < n; i++ {
		f := e.appendFile(1, int64(10+i))
		switch i % 4 {
		case 1:
			if err := e.st.Commit(&store.Rec{T: store.TSnap, At: int64(1000 + i), File: f.ID, Version: 1, Hash: 7, Size: 5}); err != nil {
				e.t.Fatal(err)
			}
		case 2:
			if err := e.st.Commit(&store.Rec{T: store.TTomb, At: int64(5000 + i), File: f.ID, Path: "/c/x"}); err != nil {
				e.t.Fatal(err)
			}
		case 3:
			f = store.File{ID: e.st.NewFileID(), Mode: "append", FirstSeen: 77}
		}
		// Read the row back so FirstSeen is the stored one.
		if rows, err := e.st.AllFiles(); err == nil {
			for _, r := range rows {
				if r.ID == f.ID {
					f = r
				}
			}
		}
		session := fmt.Sprintf("s%03d", i)
		groups[session] = &conv{session: session, files: []store.File{f}}
	}
	return groups
}

func TestLastSeenBatchedEqualsPerFileComputation(t *testing.T) {
	e := newCSEnv(t)
	groups := lastSeenFixture(e, 20)
	e.s.prefetchLastSeen(context.Background(), groups, map[string]bool{}, map[string]bool{})
	if e.s.seen == nil {
		t.Fatal("prefetch left the cache empty")
	}
	distinct := map[int64]bool{}
	for _, c := range groups {
		for _, f := range c.files {
			got, want := e.s.lastSeen(context.Background(), f), perFileLastSeen(e.st, f)
			if !got.Equal(want) {
				t.Errorf("file %d: batched %v, per file %v", f.ID, got.UnixNano(), want.UnixNano())
			}
			distinct[want.UnixNano()] = true
		}
	}
	if len(distinct) < 10 {
		t.Fatalf("fixture too uniform: %d distinct times", len(distinct))
	}
	// Without the cache the method is the per-file computation.
	e.s.seen = nil
	for _, c := range groups {
		for _, f := range c.files {
			if got, want := e.s.lastSeen(context.Background(), f), perFileLastSeen(e.st, f); !got.Equal(want) {
				t.Errorf("uncached file %d: %v, want %v", f.ID, got.UnixNano(), want.UnixNano())
			}
		}
	}
}

func TestLastSeenSkipsLiveAndUnchangedConversations(t *testing.T) {
	e := newCSEnv(t)
	groups := lastSeenFixture(e, 8)
	live := map[string]bool{"s000": true}
	unchanged := map[string]bool{"s001": true}
	e.s.prefetchLastSeen(context.Background(), groups, live, unchanged)
	if _, ok := e.s.seen[groups["s000"].files[0].ID]; ok {
		t.Error("live conversation was prefetched")
	}
	if _, ok := e.s.seen[groups["s001"].files[0].ID]; ok {
		t.Error("prefilter-skipped conversation was prefetched")
	}
	if _, ok := e.s.seen[groups["s002"].files[0].ID]; !ok {
		t.Error("evaluated conversation was not prefetched")
	}
}

// One statement loads every file's last-seen time, and reading them costs none,
// so the count per tick does not grow with the number of files.
func TestLastSeenStatementCountIsConstant(t *testing.T) {
	count := func(n int) (prefetch, reads int64) {
		e := newCSEnv(t)
		groups := lastSeenFixture(e, n)
		e.cnt.Reset()
		e.s.prefetchLastSeen(context.Background(), groups, map[string]bool{}, map[string]bool{})
		prefetch = e.cnt.Queries()
		e.cnt.Reset()
		for _, c := range groups {
			e.s.lastSeen(context.Background(), c.files[0])
		}
		return prefetch, e.cnt.Queries()
	}
	sp, sr := count(6)
	lp, lr := count(60)
	t.Logf("last-seen statements: 6 files prefetch %d reads %d, 60 files prefetch %d reads %d", sp, sr, lp, lr)
	if sp != lp || lp > 2 || sr != 0 || lr != 0 {
		t.Fatalf("statements grow with the data: prefetch %d/%d, reads %d/%d", sp, lp, sr, lr)
	}
}
