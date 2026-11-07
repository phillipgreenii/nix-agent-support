package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
	"github.com/phillipgreenii/pg-checkout-pool/internal/provision"
)

// gateProv wraps a provisioner and measures how many Create calls overlap.
type gateProv struct {
	provision.Provisioner
	cur, max *atomic.Int32
}

func (g gateProv) Create(ctx context.Context, dir, commit string) error {
	n := g.cur.Add(1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
	defer g.cur.Add(-1)
	return g.Provisioner.Create(ctx, dir, commit)
}

func TestSlotCreationIsSerialized(t *testing.T) {
	e := newEnv(t)
	var cur, max atomic.Int32
	prov, err := provision.NewWorktree(provision.WorktreeConfig{Repo: e.repo, Env: hermeticEnv, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	g := gateProv{prov, &cur, &max}
	st := pool.NewStore(e.root, e.clk)
	a := newApp(os.Stderr, os.Stderr, e.clk)
	a.freeBytes = func(string) (uint64, error) { return e.free, nil }
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			created := map[string]bool{}
			mu.Unlock()
			if _, err := a.grow(context.Background(), g, st, e.root, e.c1, 4, 0, created); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if max.Load() != 1 {
		t.Errorf("concurrent creations: %d, want 1", max.Load())
	}
	ids, _ := st.Slots()
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if len(ids) != 4 || len(seen) != 4 {
		t.Errorf("slots %v, want 4 distinct", ids)
	}
}

type prewarmResult struct {
	Target, Active int
	Created        []string
	Stopped        string
}

func (e *env) prewarm(extra ...string) (int, prewarmResult) {
	e.t.Helper()
	code, out, _ := e.do(append([]string{"prewarm", "--repo", e.repo, "--ref", "main"}, extra...)...)
	var r prewarmResult
	if out != "" {
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			e.t.Fatalf("prewarm json: %v\n%s", err, out)
		}
	}
	return code, r
}

func TestPrewarmIsThrottled(t *testing.T) {
	e := newEnv(t)
	code, r := e.prewarm("--count", "3", "--delay", "45s")
	if code != 0 || len(r.Created) != 3 || r.Active != 3 {
		t.Fatalf("prewarm: %d %+v", code, r)
	}
	// One pause between creations, none before the first or after the last.
	if len(e.sleeps) != 2 || e.sleeps[0] != 45*time.Second || e.sleeps[1] != 45*time.Second {
		t.Errorf("sleeps %v, want two of 45s", e.sleeps)
	}
	// A warm pool needs nothing more.
	e.sleeps = nil
	code, r = e.prewarm("--count", "3")
	if code != 0 || len(r.Created) != 0 || len(e.sleeps) != 0 {
		t.Errorf("second prewarm: %d %+v sleeps %v", code, r, e.sleeps)
	}
	// The cap wins over the count.
	code, r = e.prewarm("--count", "9", "--max-slots", "4", "--delay", "0s")
	if code != 0 || r.Active != 4 || len(r.Created) != 1 {
		t.Errorf("capped prewarm: %d %+v", code, r)
	}
}

func TestPrewarmHonoursMinFreeBytes(t *testing.T) {
	e := newEnv(t)
	e.free = 1 << 20
	code, r := e.prewarm("--count", "2", "--delay", "0s")
	if code != exitLowDisk || len(r.Created) != 0 || r.Stopped == "" {
		t.Fatalf("prewarm below floor: %d %+v", code, r)
	}
	if ids, _ := pool.NewStore(e.root, e.clk).Slots(); len(ids) != 0 {
		t.Errorf("slots created below the floor: %v", ids)
	}
}

func TestRevalidateQuarantinesBadSlots(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.prewarm("--count", "3", "--delay", "0s"); code != 0 {
		t.Fatal("prewarm failed")
	}
	// slot-03 is leased and must be left alone; slot-02 loses its checkout.
	l := e.mustLease("s1", "")
	if l.Slot != "slot-01" {
		t.Fatalf("leased %s", l.Slot)
	}
	if err := os.RemoveAll(filepath.Join(e.root, "slots", "slot-02", "tree")); err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.do("revalidate", "--repo", e.repo)
	if code != exitInternal {
		t.Fatalf("exit %d, want %d (a slot was quarantined)", code, exitInternal)
	}
	var entries []revalidateEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, en := range entries {
		got[en.Slot] = en.Action
	}
	want := map[string]string{"slot-01": "skipped-leased", "slot-02": "quarantined", "slot-03": "ok"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	rec, err := pool.NewStore(e.root, e.clk).ReadRecord("slot-02")
	if err != nil || rec.State != pool.Quarantined || rec.Reason == "" {
		t.Errorf("slot-02 record %+v err %v", rec, err)
	}
	// A second pass finds nothing new to quarantine.
	if code, _, _ := e.do("revalidate", "--repo", e.repo); code != 0 {
		t.Errorf("second revalidate exit %d", code)
	}
}
