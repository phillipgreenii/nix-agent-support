package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// INV-CCH-41, design D2: progress counters. The worktree sweep runs against real
// git in a throwaway repository (the sweepHarness); the rest uses the passes'
// own fakes.

// sweepCounted runs the sweep with counters on its context, as the janitor does.
func (h *sweepHarness) sweepCounted() (int, *passCounts) {
	c := &passCounts{Pass: passWorktree, Role: h.role.Name}
	n := sweepLeakedWorktrees(withCounts(context.Background(), c), h.role, h.deps, h.env)
	c.finish(n)
	return n, c
}

func (h *sweepHarness) seenFiles() []string {
	h.t.Helper()
	m, err := filepath.Glob(filepath.Join(h.lockDir, seenFilePrefix+"*"+seenFileSuffix))
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

func TestProgress_worktreeCountersAddUpWhenTheCapCutsThePass(t *testing.T) {
	h := newSweepHarness(t)
	total := worktreeSweepMaxExamined + 2
	for i := 0; i < total; i++ {
		h.makeDirty(h.leak(fmt.Sprintf("zr-%02d", i)))
	}
	h.env.cwds = func(context.Context) ([]string, error) { return nil, nil }

	_, c := h.sweepCounted()
	if c.Candidates != total || c.Examined != worktreeSweepMaxExamined || c.Cached != 0 || c.Unexamined != 2 {
		t.Fatalf("pass 1 counters = %+v, want candidates %d examined %d cached 0 unexamined 2", c, total, worktreeSweepMaxExamined)
	}
	if c.Candidates != c.Examined+c.Cached+c.Unexamined {
		t.Errorf("candidates %d != examined %d + cached %d + unexamined %d", c.Candidates, c.Examined, c.Cached, c.Unexamined)
	}
	if c.Kept != worktreeSweepMaxExamined || c.Removed != 0 || c.Kept+c.Removed > c.Examined {
		t.Errorf("kept %d removed %d examined %d", c.Kept, c.Removed, c.Examined)
	}
	if c.ListMS == nil || c.ProbeMS == nil || c.ScanMS == nil {
		t.Errorf("phase timings missing: %+v", c)
	}
	if got := len(h.seenFiles()); got != 2 {
		t.Errorf("%d first-seen records, want 2 (the unreached candidates)", got)
	}

	// The next run continues: the kept ones are cached, the two left over are examined.
	_, c = h.sweepCounted()
	if c.Candidates != total || c.Cached != worktreeSweepMaxExamined || c.Examined != 2 || c.Unexamined != 0 {
		t.Fatalf("pass 2 counters = %+v, want cached %d examined 2 unexamined 0", c, worktreeSweepMaxExamined)
	}
	if c.OldestUnexaminedAgeSeconds != 0 || len(h.seenFiles()) != 0 {
		t.Errorf("age %v and %d first-seen records after every candidate was reached, want 0 and 0", c.OldestUnexaminedAgeSeconds, len(h.seenFiles()))
	}
}

func TestProgress_oldestUnexaminedAgeGrowsWhileUnreachedAndResetsWhenExamined(t *testing.T) {
	h := newSweepHarness(t)
	h.leak("zr-a")
	probeErr := errors.New("lsof unavailable")
	h.env.cwds = func(context.Context) ([]string, error) { return nil, probeErr }

	_, c := h.sweepCounted()
	if c.Candidates != 1 || c.Unexamined != 1 || c.OldestUnexaminedAgeSeconds != 0 {
		t.Fatalf("first sighting = %+v, want one unexamined candidate of age 0", c)
	}
	h.nowFixed = h.nowFixed.Add(3 * time.Hour)
	_, c = h.sweepCounted()
	if c.Unexamined != 1 || c.OldestUnexaminedAgeSeconds != (3*time.Hour).Seconds() {
		t.Fatalf("three hours later = %+v, want age %v", c, (3 * time.Hour).Seconds())
	}
	h.nowFixed = h.nowFixed.Add(time.Hour)
	_, c = h.sweepCounted()
	if c.OldestUnexaminedAgeSeconds != (4 * time.Hour).Seconds() {
		t.Fatalf("age = %v, want it to keep growing to %v", c.OldestUnexaminedAgeSeconds, (4 * time.Hour).Seconds())
	}

	h.env.cwds = func(context.Context) ([]string, error) { return nil, nil }
	n, c := h.sweepCounted()
	if n != 1 || c.Examined != 1 || c.Removed != 1 || c.Kept != 0 || c.Unexamined != 0 || c.OldestUnexaminedAgeSeconds != 0 {
		t.Fatalf("once examined = removed %d, %+v, want it examined, removed and the age reset", n, c)
	}
	if len(h.seenFiles()) != 0 {
		t.Errorf("first-seen record survived the candidate: %v", h.seenFiles())
	}
}

func TestProgress_candidateHeldByAProcessIsNotCountedAndForgotten(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-a")
	h.env.cwds = func(context.Context) ([]string, error) { return nil, errors.New("down") }
	h.sweepCounted() // records a first sighting
	if len(h.seenFiles()) != 1 {
		t.Fatalf("want one first-seen record, got %v", h.seenFiles())
	}
	h.env.cwds = func(context.Context) ([]string, error) { return []string{path}, nil }
	_, c := h.sweepCounted()
	if c.Candidates != 0 || len(h.seenFiles()) != 0 {
		t.Errorf("a worktree a live process holds is in use, not waiting: %+v, records %v", c, h.seenFiles())
	}
}

func TestProgress_firstSeenRecordsAreCollectedWhenTheWorktreeIsGone(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live")
	if err := os.Mkdir(live, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	noteFirstSeen(dir, leakedWorktree{bead: "live", path: live}, now)
	noteFirstSeen(dir, leakedWorktree{bead: "gone", path: filepath.Join(dir, "gone")}, now)
	if err := os.WriteFile(seenFile(dir, "torn"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sweepKeepCache(dir, now); got != 2 {
		t.Errorf("collected %d records, want 2 (gone and torn)", got)
	}
	if _, ok := readSeen(seenFile(dir, "live")); !ok {
		t.Error("the record of an existing worktree was collected")
	}
}

func TestProgress_closedBeadCounters(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-w-zr-done", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-done"}},
		{ExternalID: "pg-router-w-zr-open", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-open"}},
		{ExternalID: "pg-router-w-zr-busy", State: ccpool.StateWorking, Meta: map[string]string{ccpool.MetaKeyBead: "zr-busy"}},
		{ExternalID: "someone-else", State: ccpool.StateIdle},
	}}}
	br := fakeBR{out: map[string]string{
		"show zr-done --json": `{"status":"closed"}`,
		"show zr-open --json": `{"status":"open"}`,
	}}
	c := &passCounts{Pass: passClosedBead}
	ctx := withCounts(context.Background(), c)
	n := reconcileClosedBeadSessions(ctx, cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", "", nil)
	c.finish(n)
	if c.Candidates != 2 || c.Examined != 2 || c.Removed != 1 || c.Kept != 1 || c.Unexamined != 0 {
		t.Errorf("closed-bead counters = %+v, want 2 candidates, 2 examined, 1 removed, 1 kept", c)
	}
}

// The janitor_run record carries every role's counters and the progress gauges
// are published; the oldest age is reported only for a pass that ran.
func TestProgress_janitorRecordsAndPublishesCounters(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""})
	var pts []gaugePoint
	rec := &recorder{}
	p := rec.passes()
	p.emit = spyEmit(&pts)
	p.worktree = func(ctx context.Context, _ roles.Role, _ config.Config, _ executor.Deps) int {
		c := countsFrom(ctx)
		c.seen(3)
		c.examined()
		c.examined()
		c.setPhase(phaseScan, 1500*time.Millisecond)
		c.OldestUnexaminedAgeSeconds = 7200
		return 1
	}
	if code, _ := f.run(p); code != 0 {
		t.Fatalf("exit %d", code)
	}
	ev := f.events()
	if len(ev) != 1 {
		t.Fatalf("events = %v", ev)
	}
	rows, _ := ev[0]["counters"].([]any)
	var wt map[string]any
	for _, r := range rows {
		if m, _ := r.(map[string]any); m["pass"] == passWorktree {
			wt = m
		}
	}
	if wt == nil || wt["role"] != "a" || wt["candidates"] != 3.0 || wt["examined"] != 2.0 || wt["removed"] != 1.0 || wt["kept"] != 1.0 || wt["unexamined"] != 1.0 || wt["scan_ms"] != 1500.0 {
		t.Fatalf("worktree_sweep counters = %v in %v", wt, rows)
	}
	for _, g := range []struct {
		name string
		want float64
	}{{gaugeJanitorCandidates, 3}, {gaugeJanitorExamined, 2}, {gaugeJanitorCached, 0}, {gaugeJanitorKept, 1}, {gaugeJanitorUnexamined, 1}, {gaugeJanitorOldestAge, 7200}} {
		if p, ok := find(pts, g.name, passWorktree); !ok || p.value != g.want {
			t.Errorf("%s = %+v ok=%v, want %v", g.name, p, ok, g.want)
		}
	}
	if p, ok := find(pts, gaugeJanitorPhase, passWorktree); !ok || p.value != 1.5 || p.labels["phase"] != phaseScan || p.labels["role"] != "a" {
		t.Errorf("phase gauge = %+v ok=%v", p, ok)
	}
	if _, ok := find(pts, gaugeJanitorOldestAge, passDefaultPool); ok {
		t.Error("the oldest age of a pass that did not run must not be reported as zero")
	}
}
