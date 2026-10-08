package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
)

// Orphan reconcile (bead pg2-g2u9m, INV-CCH-18): reclaim or budget-stop the
// sessions of one role whose supervision lease expired.

var orphanNowT = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const (
	orphanActor = "pgii-pool__worker"
	orphanID    = "pg-router-worker-zr-w-20261006T115000.000000000"
	orphanCWD   = "/wt/zr-w"
)

// orphanCC is a ccpool.Runner whose rows really change: Close(non-purge) turns
// a row into a handler-closed one and SetMeta upserts meta, like ccpool does.
type orphanCC struct {
	mu       sync.Mutex
	sessions []ccpool.Session
	listErr  error
	closes   []string
	purges   []bool
	metaSets [][3]string
	onClose  func()
}

func (c *orphanCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return nil
}
func (c *orphanCC) Send(context.Context, string, string, ccpool.SendMode) error { return nil }
func (c *orphanCC) Cancel(context.Context, string) error                        { return nil }
func (c *orphanCC) Capacity(context.Context) (ccpool.Capacity, error) {
	return ccpool.Capacity{Free: 1}, nil
}

func (c *orphanCC) List(context.Context) ([]ccpool.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listErr != nil {
		return nil, c.listErr
	}
	out := make([]ccpool.Session, len(c.sessions))
	for i, s := range c.sessions {
		m := make(map[string]string, len(s.Meta))
		for k, v := range s.Meta {
			m[k] = v
		}
		s.Meta = m
		out[i] = s
	}
	return out, nil
}

func (c *orphanCC) Close(_ context.Context, id string, purge bool) error {
	if c.onClose != nil {
		c.onClose()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes = append(c.closes, id)
	c.purges = append(c.purges, purge)
	for i := range c.sessions {
		if c.sessions[i].ExternalID == id {
			c.sessions[i].CloseReason, c.sessions[i].Live = "handler", false
		}
	}
	return nil
}

func (c *orphanCC) SetMeta(_ context.Context, id, key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metaSets = append(c.metaSets, [3]string{id, key, value})
	for i := range c.sessions {
		if c.sessions[i].ExternalID == id {
			if c.sessions[i].Meta == nil {
				c.sessions[i].Meta = map[string]string{}
			}
			c.sessions[i].Meta[key] = value
		}
	}
	return nil
}

func (c *orphanCC) closed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.closes...)
}

// orphanBD serves `show` from a status/assignee table and records every other
// call. showErr makes every show fail.
type orphanBD struct {
	mu      sync.Mutex
	issues  map[string]string // bead id -> raw show JSON
	showErr error
	calls   []string
}

func (b *orphanBD) Run(_ context.Context, args ...string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(args) > 0 && args[0] == "show" {
		b.calls = append(b.calls, strings.Join(args, " "))
		if b.showErr != nil {
			return "", b.showErr
		}
		return b.issues[args[1]], nil
	}
	b.calls = append(b.calls, strings.Join(args, " "))
	return "", nil
}

func (b *orphanBD) writes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var w []string
	for _, c := range b.calls {
		if !strings.HasPrefix(c, "show ") {
			w = append(w, c)
		}
	}
	return w
}

func (b *orphanBD) shows() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		if strings.HasPrefix(c, "show ") {
			n++
		}
	}
	return n
}

func inProgressBy(actor string) string {
	return `{"id":"zr-w","status":"in_progress","assignee":"` + actor + `"}`
}

func orphanRole(c roles.Completion, timeBudget time.Duration) roles.Role {
	return roles.Role{Name: "worker", Type: "ccpool", CCPool: &roles.CCPoolConfig{
		Actor: orphanActor, Completion: c,
		Budget: budget.Budget{Time: timeBudget, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00}},
	}}
}

// orphanRow is a worker session whose lease expired a minute ago.
func orphanRow(state ccpool.SessionState) ccpool.Session {
	return ccpool.Session{
		ExternalID: orphanID, Name: "pg-router-worker-zr-w", State: state, Live: true, CWD: orphanCWD,
		Meta: map[string]string{
			ccpool.MetaKeyBead: "zr-w", ccpool.MetaKeyRole: "worker", ccpool.MetaKeyPool: ccpool.PoolName,
			ccpool.MetaKeyLeaseUntil: orphanNowT.Add(-time.Minute).Format(time.RFC3339),
			ccpool.MetaKeyLaunchedAt: orphanNowT.Add(-10 * time.Minute).Format(time.RFC3339),
		},
	}
}

type orphanHarness struct {
	cc      *orphanCC
	bd      *orphanBD
	opener  *fakeWorktreeOpener
	deps    executor.Deps
	env     orphanEnv
	logPath string
}

func newOrphanHarness(t *testing.T, sessions ...ccpool.Session) *orphanHarness {
	t.Helper()
	cc := &orphanCC{sessions: sessions}
	bd := &orphanBD{issues: map[string]string{"zr-w": inProgressBy(orphanActor)}}
	opener := &fakeWorktreeOpener{}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lw.Close() })
	cfg := config.Default()
	cfg.RepoRoot = "/repo"
	cfg.SessionPrefix = "pg-router-"
	return &orphanHarness{
		cc: cc, bd: bd, opener: opener, logPath: logPath,
		deps: executor.Deps{CC: cc, BD: bd, Cfg: cfg, Log: lw, Now: func() time.Time { return orphanNowT }},
		env:  orphanEnv{open: opener.Open, lockDir: t.TempDir()},
	}
}

func (h *orphanHarness) run(role roles.Role) (int, int) {
	return reconcileOrphanSessions(context.Background(), role, h.deps, h.env)
}

func (h *orphanHarness) events(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(h.logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (h *orphanHarness) eventOfKind(t *testing.T, kind string) map[string]any {
	t.Helper()
	for _, e := range h.events(t) {
		if e["kind"] == kind {
			return e
		}
	}
	return nil
}

// --- orphan definition ---

func TestIsOrphanOf(t *testing.T) {
	role := orphanRole(roles.CloseOnly, 0)
	base := orphanRow(ccpool.StateIdle)
	with := func(mut func(*ccpool.Session)) ccpool.Session {
		s := base
		m := map[string]string{}
		for k, v := range base.Meta {
			m[k] = v
		}
		s.Meta = m
		mut(&s)
		return s
	}
	cases := []struct {
		name string
		s    ccpool.Session
		want bool
	}{
		{"expired lease, this role, this pool", base, true},
		{"lease still in the future (still in Ensure / supervised)", with(func(s *ccpool.Session) {
			s.Meta[ccpool.MetaKeyLeaseUntil] = orphanNowT.Add(time.Minute).Format(time.RFC3339)
		}), false},
		{"lease exactly now is not yet expired", with(func(s *ccpool.Session) {
			s.Meta[ccpool.MetaKeyLeaseUntil] = orphanNowT.Format(time.RFC3339)
		}), false},
		{"no lease at all (older build) is never an orphan", with(func(s *ccpool.Session) { delete(s.Meta, ccpool.MetaKeyLeaseUntil) }), false},
		{"unparseable lease is never an orphan", with(func(s *ccpool.Session) { s.Meta[ccpool.MetaKeyLeaseUntil] = "garbage" }), false},
		{"another role", with(func(s *ccpool.Session) { s.Meta[ccpool.MetaKeyRole] = "review" }), false},
		{"another pool tag", with(func(s *ccpool.Session) { s.Meta[ccpool.MetaKeyPool] = "someone-else" }), false},
		{"non-matching prefix", with(func(s *ccpool.Session) { s.ExternalID = "other-worker-zr-w" }), false},
		{"already closed (a handler-closed settled row outlives its lease)", with(func(s *ccpool.Session) { s.CloseReason = "handler" }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOrphanOf(tc.s, role, "pg-router-", orphanNowT); got != tc.want {
				t.Errorf("isOrphanOf = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconcileOrphans_listErrorMeansNoAction(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	h.cc.listErr = errors.New("ccpool list: boom")
	if r, s := h.run(orphanRole(roles.CloseOnly, 0)); r != 0 || s != 0 {
		t.Fatalf("list error must not act: reclaimed=%d stopped=%d", r, s)
	}
	if len(h.cc.closed()) != 0 || len(h.bd.writes()) != 0 {
		t.Errorf("no close / bead write expected; closes=%v writes=%v", h.cc.closed(), h.bd.writes())
	}
}

func TestReconcileOrphans_noLockDirDisablesReconcile(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	h.env.lockDir = ""
	if r, _ := h.run(orphanRole(roles.CloseOnly, 0)); r != 0 || len(h.cc.closed()) != 0 {
		t.Fatalf("acting without mutual exclusion is forbidden; reclaimed=%d closes=%v", r, h.cc.closed())
	}
}

func TestReconcileOrphans_untouchedSessions(t *testing.T) {
	inEnsure := orphanRow(ccpool.StateStarting)
	inEnsure.Meta[ccpool.MetaKeyLeaseUntil] = orphanNowT.Add(13 * time.Minute).Format(time.RFC3339)
	noLease := orphanRow(ccpool.StateIdle)
	delete(noLease.Meta, ccpool.MetaKeyLeaseUntil)
	otherRole := orphanRow(ccpool.StateIdle)
	otherRole.Meta[ccpool.MetaKeyRole] = "review"
	otherRole.ExternalID = "pg-router-review-zr-w-1"
	otherPool := orphanRow(ccpool.StateIdle)
	otherPool.Meta[ccpool.MetaKeyPool] = "someone-else"
	otherPool.ExternalID = "pg-router-worker-zr-x-1"
	otherPrefix := orphanRow(ccpool.StateIdle)
	otherPrefix.ExternalID = "unrelated-worker-zr-w"
	needsInput := orphanRow(ccpool.StateNeedsInput)
	needsInput.ExternalID = "pg-router-worker-zr-n-1"
	h := newOrphanHarness(t, inEnsure, noLease, otherRole, otherPool, otherPrefix, needsInput)
	if r, s := h.run(orphanRole(roles.CloseOnly, 25*time.Minute)); r != 0 || s != 0 {
		t.Fatalf("none of these is an orphan to act on: reclaimed=%d stopped=%d", r, s)
	}
	if len(h.cc.closed()) != 0 || len(h.bd.writes()) != 0 || len(h.cc.metaSets) != 0 {
		t.Errorf("no session or bead may be touched; closes=%v writes=%v meta=%v", h.cc.closed(), h.bd.writes(), h.cc.metaSets)
	}
}

// The lease was refreshed between the first list and the lock: abort.
func TestReconcileOrphans_leaseRefreshedUnderLockAborts(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	refreshed := false
	h.cc.onClose = func() {} // never reached
	// First List (candidate scan) sees the expired lease; by the re-List under
	// the lock a live handler has refreshed it.
	wrapped := &refreshingCC{orphanCC: h.cc, afterFirstList: func() {
		h.cc.mu.Lock()
		h.cc.sessions[0].Meta[ccpool.MetaKeyLeaseUntil] = orphanNowT.Add(2 * time.Minute).Format(time.RFC3339)
		h.cc.mu.Unlock()
		refreshed = true
	}}
	h.deps.CC = wrapped
	if r, _ := h.run(orphanRole(roles.CloseOnly, 0)); r != 0 {
		t.Fatalf("a refreshed lease must abort the reclaim, reclaimed=%d", r)
	}
	if !refreshed || len(h.cc.closed()) != 0 || len(h.bd.writes()) != 0 {
		t.Errorf("refreshed=%v closes=%v writes=%v", refreshed, h.cc.closed(), h.bd.writes())
	}
}

type refreshingCC struct {
	*orphanCC
	afterFirstList func()
	once           sync.Once
}

func (r *refreshingCC) List(ctx context.Context) ([]ccpool.Session, error) {
	out, err := r.orphanCC.List(ctx)
	r.once.Do(r.afterFirstList)
	return out, err
}

// --- idle / errored orphans ---

func TestReclaimIdleOrphan_closeOnlyUnclaimsClosesAndRemovesWorktree(t *testing.T) {
	for _, state := range []ccpool.SessionState{ccpool.StateIdle, ccpool.StateErrored} {
		t.Run(string(state), func(t *testing.T) {
			h := newOrphanHarness(t, orphanRow(state))
			r, s := h.run(orphanRole(roles.CloseOnly, 25*time.Minute))
			if r != 1 || s != 0 {
				t.Fatalf("reclaimed=%d stopped=%d, want 1/0", r, s)
			}
			w := h.bd.writes()
			if len(w) != 2 || !strings.HasPrefix(w[0], "comment zr-w orphaned session "+orphanID+" reclaimed after handler loss (lease expired ") ||
				w[1] != "update zr-w --status=open --assignee=" {
				t.Errorf("want one reclaim comment then exactly one unclaim; writes=%v", w)
			}
			if got := h.cc.closed(); len(got) != 1 || got[0] != orphanID || h.cc.purges[0] {
				t.Errorf("must close the orphan once, non-purge; closes=%v purges=%v", got, h.cc.purges)
			}
			if len(h.opener.Removed) != 1 || h.opener.Removed[0] != orphanCWD {
				t.Errorf("worktree must be removed; removed=%v", h.opener.Removed)
			}
			if len(h.opener.BranchDeletes) != 1 || h.opener.BranchDeletes[0] != (branchDelete{Branch: "pg-router/zr-w", Force: true}) {
				t.Errorf("anchor branch must be deleted; got %v", h.opener.BranchDeletes)
			}
			marked := false
			for _, m := range h.cc.metaSets {
				if m[0] == orphanID && m[1] == ccpool.MetaKeyOrphanReclaimed && m[2] != "" {
					marked = true
				}
			}
			if !marked {
				t.Errorf("the reclaimed row must be marked so a redelivery launches afresh; meta=%v", h.cc.metaSets)
			}
			ev := h.eventOfKind(t, "orphan_reclaimed")
			if ev == nil {
				t.Fatalf("missing orphan_reclaimed event; events=%v", h.events(t))
			}
			for k, want := range map[string]any{
				"session": orphanID, "bead": "zr-w", "role": "worker", "pool": "default", "state": string(state),
				"lease_until": orphanNowT.Add(-time.Minute).Format(time.RFC3339),
				"launched_at": orphanNowT.Add(-10 * time.Minute).Format(time.RFC3339),
			} {
				if ev[k] != want {
					t.Errorf("event field %s = %v, want %v", k, ev[k], want)
				}
			}
			// Second pass: the row is closed now, so nothing acts again.
			if r2, _ := h.run(orphanRole(roles.CloseOnly, 25*time.Minute)); r2 != 0 || len(h.cc.closed()) != 1 {
				t.Errorf("a reclaimed orphan must not be reclaimed twice; r2=%d closes=%v", r2, h.cc.closed())
			}
		})
	}
}

func TestReclaimIdleOrphan_beadRules(t *testing.T) {
	cases := []struct {
		name       string
		completion roles.Completion
		issue      string
		wantWrites int
	}{
		{"close-or-handback in_progress by this actor", roles.CloseOrHandback, inProgressBy(orphanActor), 2},
		{"close-or-release in_progress by this actor", roles.CloseOrRelease, inProgressBy(orphanActor), 2},
		{"close-or-release in_progress by a PEER", roles.CloseOrRelease, inProgressBy("someone-else"), 0},
		{"in_progress by ANOTHER actor", roles.CloseOnly, inProgressBy("someone-else"), 0},
		{"already open", roles.CloseOnly, `{"id":"zr-w","status":"open"}`, 0},
		{"already closed", roles.CloseOnly, `{"id":"zr-w","status":"closed","assignee":"` + orphanActor + `"}`, 0},
		{"close-or-triage never claims: no bead write", roles.CloseOrTriage, inProgressBy(orphanActor), 0},
		{"close-or-split-triage never claims: no bead write", roles.CloseOrSplitTriage, inProgressBy(orphanActor), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
			h.bd.issues["zr-w"] = tc.issue
			if r, _ := h.run(orphanRole(tc.completion, 0)); r != 1 {
				t.Fatalf("the session must still be reclaimed; reclaimed=%d", r)
			}
			if got := len(h.bd.writes()); got != tc.wantWrites {
				t.Errorf("bead writes = %v, want %d", h.bd.writes(), tc.wantWrites)
			}
			if tc.completion == roles.CloseOrTriage || tc.completion == roles.CloseOrSplitTriage {
				if h.bd.shows() != 0 {
					t.Errorf("a triage role must not even read the bead")
				}
			}
			if len(h.cc.closed()) != 1 {
				t.Errorf("session must be closed once; closes=%v", h.cc.closed())
			}
		})
	}
}

// If bd cannot be read the claim holder is unknown: closing the session would
// strand an invisible claim, so nothing is done this pass.
func TestReclaimIdleOrphan_beadLookupFailureLeavesOrphan(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	h.bd.showErr = errors.New("bd: unreachable")
	if r, _ := h.run(orphanRole(roles.CloseOnly, 0)); r != 0 || len(h.cc.closed()) != 0 || len(h.bd.writes()) != 0 {
		t.Fatalf("reclaimed=%d closes=%v writes=%v", r, h.cc.closed(), h.bd.writes())
	}
}

func TestReclaimIdleOrphan_transcriptStillActiveDefers(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	h.env.quiet = func(ccpool.Session) bool { return false }
	if r, _ := h.run(orphanRole(roles.CloseOnly, 0)); r != 0 || len(h.cc.closed()) != 0 || len(h.bd.writes()) != 0 {
		t.Fatalf("a not-quiet orphan (subagents running) must wait; reclaimed=%d closes=%v writes=%v", r, h.cc.closed(), h.bd.writes())
	}
}

// A dirty worktree is refused by git (force=false): the session is still
// reclaimed, the worktree and its branch are kept, and the keep is logged.
func TestReclaimIdleOrphan_dirtyWorktreeKeptAndLogged(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	h.opener.RemoveErrAt = map[string]bool{orphanCWD: true}
	var r int
	msgs := captureSlog(t, func() { r, _ = h.run(orphanRole(roles.CloseOnly, 0)) })
	if r != 1 {
		t.Fatalf("reclaimed=%d, want 1", r)
	}
	if len(h.opener.BranchDeletes) != 0 {
		t.Errorf("a kept worktree must keep its anchor branch; deletes=%v", h.opener.BranchDeletes)
	}
	if !containsSubstring(msgs, "worktree kept -- removal refused") {
		t.Errorf("the kept worktree must be logged; msgs=%v", msgs)
	}
	if len(h.opener.Removed) != 1 || h.opener.Removed[0] != orphanCWD {
		t.Errorf("removal must have been attempted non-forced once; %v", h.opener.Removed)
	}
}

func TestReclaimIdleOrphan_livePeerKeepsWorktree(t *testing.T) {
	for _, st := range []ccpool.SessionState{ccpool.StateStarting, ccpool.StateReady, ccpool.StateWorking, ccpool.StateNeedsInput} {
		t.Run(string(st), func(t *testing.T) {
			peer := ccpool.Session{
				ExternalID: "pg-router-feedback-zr-w-1", State: st, Live: true, CWD: orphanCWD,
				Meta: map[string]string{ccpool.MetaKeyRole: "feedback", ccpool.MetaKeyPool: ccpool.PoolName},
			}
			h := newOrphanHarness(t, orphanRow(ccpool.StateIdle), peer)
			if r, _ := h.run(orphanRole(roles.CloseOnly, 0)); r != 1 {
				t.Fatalf("the orphan itself is still reclaimed; reclaimed=%d", r)
			}
			if len(h.opener.Removed) != 0 || len(h.opener.BranchDeletes) != 0 {
				t.Errorf("a live %s peer on the same cwd keeps the worktree; removed=%v", st, h.opener.Removed)
			}
		})
	}
	// An idle peer does not protect it.
	idlePeer := ccpool.Session{ExternalID: "pg-router-feedback-zr-w-1", State: ccpool.StateIdle, Live: true, CWD: orphanCWD}
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle), idlePeer)
	h.run(orphanRole(roles.CloseOnly, 0))
	if len(h.opener.Removed) != 1 {
		t.Errorf("an idle peer must not keep the worktree; removed=%v", h.opener.Removed)
	}
}

func TestReclaimIdleOrphan_nonWorktreeIsolationAndRepoRootNeverRemoved(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	role := orphanRole(roles.CloseOnly, 0)
	role.CCPool.Isolation = roles.IsolationConfig{Type: "none"}
	h.run(role)
	if len(h.opener.Removed) != 0 || len(h.cc.closed()) != 1 {
		t.Errorf("none isolation: close yes, remove no; removed=%v closes=%v", h.opener.Removed, h.cc.closed())
	}
	row := orphanRow(ccpool.StateIdle)
	row.CWD = "/repo"
	h = newOrphanHarness(t, row)
	h.run(orphanRole(roles.CloseOnly, 0))
	if len(h.opener.Removed) != 0 {
		t.Errorf("the repo root is never removed; removed=%v", h.opener.Removed)
	}
}

// --- starting / ready / working orphans ---

func TestStopActiveOrphan_overBudgetHardStopsOnceThenRemovesWorktree(t *testing.T) {
	for _, st := range []ccpool.SessionState{ccpool.StateStarting, ccpool.StateReady, ccpool.StateWorking} {
		t.Run(string(st), func(t *testing.T) {
			row := orphanRow(st)
			row.Meta[ccpool.MetaKeyLaunchedAt] = orphanNowT.Add(-30 * time.Minute).Format(time.RFC3339)
			h := newOrphanHarness(t, row)
			role := orphanRole(roles.CloseOnly, 25*time.Minute)
			role.CCPool.BudgetStopEscalateAfter = 3
			r, s := h.run(role)
			if r != 0 || s != 1 {
				t.Fatalf("reclaimed=%d stopped=%d, want 0/1", r, s)
			}
			if got := h.cc.closed(); len(got) != 1 || got[0] != orphanID || h.cc.purges[0] {
				t.Errorf("the hard stop closes the session once, non-purge; closes=%v", got)
			}
			var comment, unclaim, stop bool
			for _, w := range h.bd.writes() {
				comment = comment || w == "comment zr-w interrupted — budget"
				unclaim = unclaim || w == "update zr-w --status=open --assignee="
				stop = stop || strings.Contains(w, "--add-label budget-stop:"+orphanID)
			}
			if !comment || !unclaim || !stop {
				t.Errorf("hard stop sequence incomplete (comment=%v unclaim=%v budget-stop-record=%v); writes=%v", comment, unclaim, stop, h.bd.writes())
			}
			if len(h.opener.Removed) != 1 || h.opener.Removed[0] != orphanCWD || len(h.opener.BranchDeletes) != 1 {
				t.Errorf("the worktree and branch go afterwards; removed=%v branches=%v", h.opener.Removed, h.opener.BranchDeletes)
			}
			ev := h.eventOfKind(t, "orphan_hard_stop")
			if ev == nil {
				t.Fatalf("missing orphan_hard_stop event; events=%v", h.events(t))
			}
			if ev["session"] != orphanID || ev["bead"] != "zr-w" || ev["role"] != "worker" || ev["pool"] != "default" ||
				ev["state"] != string(st) || ev["lease_until"] == "" || ev["launched_at"] == "" {
				t.Errorf("orphan_hard_stop fields incomplete: %v", ev)
			}
			if h.eventOfKind(t, "hard_stop") == nil {
				t.Errorf("the shared terminal sequence also records its own hard_stop event")
			}
			// Once: the row is closed now.
			if _, s2 := h.run(role); s2 != 0 || len(h.cc.closed()) != 1 {
				t.Errorf("hard stop must fire once; s2=%d closes=%v", s2, h.cc.closed())
			}
		})
	}
}

func TestStopActiveOrphan_leftAlone(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*ccpool.Session)
		budget time.Duration
		issue  string
	}{
		{"under budget", func(s *ccpool.Session) {}, 25 * time.Minute, ""},
		{"no time budget (Budget.Time == 0)", func(s *ccpool.Session) {
			s.Meta[ccpool.MetaKeyLaunchedAt] = orphanNowT.Add(-5 * time.Hour).Format(time.RFC3339)
		}, 0, ""},
		{"no launched_at to measure from", func(s *ccpool.Session) { delete(s.Meta, ccpool.MetaKeyLaunchedAt) }, time.Minute, ""},
		{"over budget but its bead already closed (the hard stop would reopen it)", func(s *ccpool.Session) {
			s.Meta[ccpool.MetaKeyLaunchedAt] = orphanNowT.Add(-30 * time.Minute).Format(time.RFC3339)
		}, 25 * time.Minute, `{"id":"zr-w","status":"closed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := orphanRow(ccpool.StateWorking)
			tc.mut(&row)
			h := newOrphanHarness(t, row)
			if tc.issue != "" {
				h.bd.issues["zr-w"] = tc.issue
			}
			if r, s := h.run(orphanRole(roles.CloseOnly, tc.budget)); r != 0 || s != 0 {
				t.Fatalf("reclaimed=%d stopped=%d, want untouched", r, s)
			}
			if len(h.cc.closed()) != 0 || len(h.bd.writes()) != 0 || len(h.opener.Removed) != 0 {
				t.Errorf("untouched means untouched; closes=%v writes=%v removed=%v", h.cc.closed(), h.bd.writes(), h.opener.Removed)
			}
		})
	}
}

func TestStopActiveOrphan_beadLookupFailureDoesNotStop(t *testing.T) {
	row := orphanRow(ccpool.StateWorking)
	row.Meta[ccpool.MetaKeyLaunchedAt] = orphanNowT.Add(-30 * time.Minute).Format(time.RFC3339)
	h := newOrphanHarness(t, row)
	h.bd.showErr = errors.New("bd: unreachable")
	if _, s := h.run(orphanRole(roles.CloseOnly, 25*time.Minute)); s != 0 || len(h.cc.closed()) != 0 {
		t.Fatalf("stopped=%d closes=%v", s, h.cc.closed())
	}
}

// --- mutual exclusion ---

// Two real flock holders over the same orphan act once: while the first is
// mid-close the second finds the lock held and skips; afterwards the row is
// closed so a later pass acts on nothing.
func TestReconcileOrphans_concurrentHoldersActOnce(t *testing.T) {
	h := newOrphanHarness(t, orphanRow(ccpool.StateIdle))
	inClose := make(chan struct{})
	release := make(chan struct{})
	h.cc.onClose = func() {
		close(inClose)
		<-release
	}
	first := make(chan [2]int, 1)
	go func() {
		r, s := h.run(orphanRole(roles.CloseOnly, 0))
		first <- [2]int{r, s}
	}()
	<-inClose // the first holder owns the flock and is inside Close
	var secondR int
	msgs := captureSlog(t, func() { secondR, _ = h.run(orphanRole(roles.CloseOnly, 0)) })
	if secondR != 0 {
		t.Errorf("the second holder must skip a locked session, reclaimed=%d", secondR)
	}
	if !containsSubstring(msgs, "locked by another holder") {
		t.Errorf("skip must be logged; msgs=%v", msgs)
	}
	close(release)
	if got := <-first; got[0] != 1 {
		t.Errorf("the first holder reclaims; got %v", got)
	}
	if got := h.cc.closed(); len(got) != 1 {
		t.Errorf("closed once, got %v", got)
	}
	if n := len(h.bd.writes()); n != 2 {
		t.Errorf("one comment + one unclaim in total, got %v", h.bd.writes())
	}
	// And the lock is free again.
	l, err := sessionlock.TryLock(h.env.lockDir, orphanID)
	if err != nil {
		t.Fatalf("lock must be released: %v", err)
	}
	l.Unlock()
}

// claimsBead lists every completion mode whose session claims its bead.
func TestClaimsBead(t *testing.T) {
	for c, want := range map[roles.Completion]bool{
		roles.CloseOnly:          true,
		roles.CloseOrHandback:    true,
		roles.CloseOrRelease:     true,
		roles.CloseOrTriage:      false,
		roles.CloseOrSplitTriage: false,
	} {
		if got := claimsBead(c); got != want {
			t.Errorf("claimsBead(%s) = %v, want %v", c, got, want)
		}
	}
}
