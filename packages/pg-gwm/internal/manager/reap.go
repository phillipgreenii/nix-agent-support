package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// Reap outcomes.
const (
	ReapCompleted   = "completed"
	ReapSkipped     = "skipped"
	ReapDeadline    = "deadline"
	ReapUnreachable = "unreachable"
	ReapError       = "error"
)

// ReapOptions says how a reap runs. A zero Deadline means none.
type ReapOptions struct {
	DryRun   bool
	Deadline time.Time
}

// ReapResult is one repository's reap. Repo is the main checkout's path; the
// lists hold worktree ids, except Preserved, which holds preserved branch
// names (with a dry run, the names the run would write).
type ReapResult struct {
	Repo      string   `json:"repo"`
	Removed   []string `json:"removed"`
	Kept      []string `json:"kept"`
	Preserved []string `json:"preserved"`
	Broken    []string `json:"broken"`
	Created   []string `json:"created"`
	Warnings  []string `json:"warnings"`
	Outcome   string   `json:"outcome"`
	// Started is when the reap began; StepsMS holds the milliseconds each
	// step took (drop, retry, reclaim, check, create), so a slow reap shows
	// where the time went.
	Started time.Time        `json:"started"`
	StepsMS map[string]int64 `json:"steps_ms"`
}

func (r *ReapResult) normalize() {
	for _, p := range []*[]string{&r.Removed, &r.Kept, &r.Preserved, &r.Broken, &r.Created, &r.Warnings} {
		if *p == nil {
			*p = []string{}
		}
	}
	if r.StepsMS == nil {
		r.StepsMS = map[string]int64{}
	}
}

// severity orders outcomes; error is the worst.
func severity(o string) int {
	switch o {
	case ReapSkipped:
		return 1
	case ReapUnreachable:
		return 2
	case ReapDeadline:
		return 3
	case ReapError:
		return 4
	}
	return 0
}

type reaper struct {
	m       *Manager
	ctx     context.Context
	o       ReapOptions
	res     ReapResult
	errored bool
	late    bool // the deadline passed
	// standbyHold releases the operation hold of the standby being created.
	standbyHold func()
}

func (r *reaper) warn(format string, args ...any) {
	r.res.Warnings = append(r.res.Warnings, fmt.Sprintf(format, args...))
}

func (r *reaper) fail(format string, args ...any) {
	r.errored = true
	r.warn(format, args...)
}

// timed runs f and adds its duration to the step's total.
func (r *reaper) timed(step string, f func()) {
	t0 := r.m.nowWall()
	f()
	if r.res.StepsMS == nil {
		r.res.StepsMS = map[string]int64{}
	}
	r.res.StepsMS[step] += r.m.nowWall().Sub(t0).Milliseconds()
}

// poolFull reports whether min is set and that many worktrees are ready.
func (r *reaper) poolFull() bool {
	min := r.m.Cfg.Min
	if min == nil {
		return false
	}
	st, err := r.m.Store.ReadUnlocked()
	if err != nil {
		return false
	}
	ready := 0
	for _, w := range st.Worktrees {
		if w.Status == state.Available || w.Status == state.Creating {
			ready++
		}
	}
	return ready >= *min
}

// stop reports whether the run must end: the deadline passed or the command
// was interrupted. Callers ask it only while work remains, so a true answer
// for the deadline means that work was left undone.
func (r *reaper) stop() bool {
	if r.ctx.Err() != nil {
		return true
	}
	if !r.o.Deadline.IsZero() && !r.m.nowWall().Before(r.o.Deadline) {
		r.late = true
		return true
	}
	return false
}

func (m *Manager) nowWall() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}

func isBroken(err error) bool {
	var ce *Error
	return errors.As(err, &ce) && ce.Code == CodeBroken
}

// Reap reclaims, repairs and tops up the repository's worktrees (INV-GWM-23,
// INV-GWM-30), in this order: single-flight on reap.lock (held: skipped); a
// missing worktree-root (unreachable); records whose directory is gone;
// operations that died (broken); one retry of every broken worktree; every
// released and expired worktree; the unsaved-work check of the available ones
// and the removal of those above max; standbys up to min. It stops at the
// deadline. A repository lock not taken within lock-wait returns the busy
// error with Outcome skipped. With DryRun nothing is written and no worktree
// is touched; the result says what a run would do.
func (m *Manager) Reap(ctx context.Context, o ReapOptions) (ReapResult, error) {
	r := &reaper{m: m, ctx: ctx, o: o, res: ReapResult{Repo: m.Repo.Main, Outcome: ReapCompleted, Started: m.nowWall().UTC()}}
	res, err := r.run()
	res.normalize()
	return res, err
}

func (r *reaper) run() (ReapResult, error) {
	m := r.m
	rel, err := m.Store.HoldReap()
	if errors.Is(err, state.ErrBusy) {
		r.res.Outcome = ReapSkipped
		return r.res, nil
	}
	if err != nil {
		r.res.Outcome = ReapError
		return r.res, ErrInternal(err)
	}
	defer rel()

	st, err := m.Store.ReadUnlocked()
	if err != nil {
		r.res.Outcome = ReapError
		return r.res, ErrInternal(err)
	}
	// A missing worktree-root while worktrees are recorded means their volume
	// is gone: change nothing. A repository that never made one has nothing
	// to lose, and standbys create the root.
	if _, serr := os.Stat(m.Cfg.WorktreeRoot); errors.Is(serr, os.ErrNotExist) && len(st.Worktrees) > 0 {
		r.res.Outcome = ReapUnreachable
		r.warn("worktree-root %s is missing; nothing was changed", m.Cfg.WorktreeRoot)
		if !r.o.DryRun {
			r.record()
		}
		return r.res, nil
	}
	before := map[string]bool{}
	for _, p := range st.Preserved {
		before[p.Branch] = true
	}

	if r.o.DryRun {
		r.dry(st)
		return r.finish(), nil
	}

	if r.stop() {
		return r.finish(), r.interrupted()
	}
	if err := r.markBroken(); err != nil {
		if isBusy(err) {
			r.res.Outcome = ReapSkipped
			return r.res, err
		}
		r.fail("%v", err)
		return r.finish(), nil
	}
	r.timed("drop", r.dropMissing)
	r.timed("retry", r.retryBroken)
	r.timed("reclaim", r.reclaimAll)
	r.timed("check", r.checkAvailable)
	r.timed("create", r.standbys)

	if after, err := m.Store.ReadUnlocked(); err == nil {
		for _, p := range after.Preserved {
			if !before[p.Branch] {
				r.res.Preserved = append(r.res.Preserved, p.Branch)
			}
		}
	}
	res := r.finish()
	if ctx := r.ctx.Err(); ctx != nil {
		return res, r.interrupted()
	}
	r.res = res
	r.record()
	return r.res, nil
}

func (r *reaper) interrupted() error {
	if r.ctx.Err() == nil {
		return nil
	}
	return ErrInterrupted("interrupted during reap")
}

func isBusy(err error) bool {
	var ce *Error
	return errors.As(err, &ce) && ce.Name == "busy"
}

// finish settles the outcome.
func (r *reaper) finish() ReapResult {
	switch {
	case r.errored:
		r.res.Outcome = ReapError
	case r.late:
		r.res.Outcome = ReapDeadline
	}
	return r.res
}

// record writes the reap event, the counters and the repository's entry in
// reaper.json (never for a dry run or a skipped reap).
func (r *reaper) record() {
	m, res := r.m, r.res
	m.event(state.Event{
		Kind: "reap", Outcome: res.Outcome,
		Details: map[string]any{
			"removed": len(res.Removed), "kept": len(res.Kept), "preserved": len(res.Preserved),
			"broken": len(res.Broken), "created": len(res.Created), "warnings": res.Warnings,
			"started": res.Started, "steps_ms": res.StepsMS,
		},
	})
	if m.Counters.Path != "" {
		m.Counters.Reaped(m.Repo.Label, len(res.Removed), len(res.Kept))
	}
	_ = state.UpdateRepoReap(state.ReaperPath(), m.reapEntry(res))
}

func (m *Manager) reapEntry(res ReapResult) state.RepoReap {
	return state.RepoReap{
		Label: m.Repo.Label, At: m.now(), Outcome: res.Outcome,
		Removed: len(res.Removed), Kept: len(res.Kept), Preserved: len(res.Preserved),
		Broken: len(res.Broken), Created: len(res.Created),
	}
}

// held reports whether some command holds the operation hold of worktree id.
func (r *reaper) held(id string) bool {
	h, err := r.m.Store.OpHeld(id)
	return err != nil || h
}

// markBroken is step 4 (and finds step 3's records): under the lock it marks
// every Preparing, Creating or Reclaiming record no command holds as broken.
func (r *reaper) markBroken() error {
	m := r.m
	lk, err := m.lockRepo(r.ctx)
	if err != nil {
		return err
	}
	defer lk.Unlock()
	changed := false
	for i := range lk.State().Worktrees {
		w := &lk.State().Worktrees[i]
		switch w.Status {
		case state.Preparing, state.Creating, state.Reclaiming:
		default:
			continue
		}
		if r.held(w.ID) {
			continue
		}
		if _, err := os.Lstat(w.Path); errors.Is(err, os.ErrNotExist) {
			continue // dropMissing handles it
		}
		reason := "the operation on it died before it finished"
		ev := state.Event{Kind: "broken", WorktreeID: w.ID, Outcome: "error", Details: map[string]any{"reason": reason}}
		if w.Lease != nil {
			ev.LeaseID, ev.Name, ev.Owner = w.Lease.ID, w.Lease.Name, w.Lease.Owner
		}
		w.Status, w.Reason, w.Lease, w.StatusSince = state.Broken, reason, nil, m.now()
		changed = true
		m.event(ev)
	}
	if changed {
		return lk.Commit()
	}
	return nil
}

// dropMissing is step 3: a record no command holds whose directory is gone is
// unlocked, its registration pruned, and the record dropped; its branch stays.
func (r *reaper) dropMissing() {
	m := r.m
	st, err := m.Store.ReadUnlocked()
	if err != nil {
		r.fail("%v", err)
		return
	}
	var ids []string
	paths := map[string]string{}
	for _, w := range st.Worktrees {
		if _, err := os.Lstat(w.Path); errors.Is(err, os.ErrNotExist) {
			ids = append(ids, w.ID)
			paths[w.ID] = w.Path
		}
	}
	var gone []string
	var releases []func()
	defer func() {
		for _, f := range releases {
			f()
		}
	}()
	for _, id := range ids {
		if r.stop() {
			return
		}
		rel, err := m.Store.HoldOp(id)
		if err != nil {
			continue
		}
		releases = append(releases, rel)
		_ = m.Prov.Unlock(r.ctx, paths[id]) // git may no longer register it
		gone = append(gone, id)
	}
	if len(gone) == 0 {
		return
	}
	if err := m.Prov.Prune(r.ctx); err != nil {
		r.fail("pruning registrations: %v", err)
		return
	}
	lk, cancel, err := m.lockDetached(r.ctx)
	if err != nil {
		r.fail("%v", err)
		return
	}
	defer cancel()
	defer lk.Unlock()
	keep := lk.State().Worktrees[:0]
	for _, w := range lk.State().Worktrees {
		drop := false
		for _, id := range gone {
			drop = drop || id == w.ID
		}
		if !drop {
			keep = append(keep, w)
			continue
		}
		ev := state.Event{Kind: "remove", WorktreeID: w.ID, Outcome: "ok", Details: map[string]any{"path": w.Path, "branch": w.Branch}}
		if w.Lease != nil {
			ev.LeaseID, ev.Name, ev.Owner = w.Lease.ID, w.Lease.Name, w.Lease.Owner
		}
		m.event(ev)
		r.res.Removed = append(r.res.Removed, w.ID)
	}
	lk.State().Worktrees = keep
	if err := lk.Commit(); err != nil {
		r.fail("%v", err)
	}
}

// retryBroken is step 5: each broken worktree gets one try, which preserves
// its work and removes it. A failure keeps the directory and is recorded in
// an event; the next reap tries again.
func (r *reaper) retryBroken() {
	m := r.m
	st, err := m.Store.ReadUnlocked()
	if err != nil {
		r.fail("%v", err)
		return
	}
	for _, w := range st.Worktrees {
		if w.Status != state.Broken {
			continue
		}
		if r.stop() {
			return
		}
		r.res.Broken = append(r.res.Broken, w.ID)
		rel, err := m.Store.HoldOp(w.ID)
		if err != nil {
			continue
		}
		gitErr := r.preserveAndRemove(w)
		r.settleRetry(w, gitErr)
		rel()
	}
}

// neverLeased reports whether no lease ever took w, so nothing was written
// into it on anyone's behalf. The record says so (NeverLeased); a record an
// older build wrote lacks the flag, and counts as never leased when the event
// log, which reaches back to the worktree's creation, holds no lease event
// for it.
func (m *Manager) neverLeased(w state.Worktree) bool {
	return w.NeverLeased || m.Store.NoLeaseEvent(w.ID, w.CreatedAt)
}

// transient reports whether err only shows that git was slow, which says
// nothing about the worktree's condition.
func transient(err error) bool { return errors.Is(err, gitrun.ErrTimedOut) }

func (r *reaper) preserveAndRemove(w state.Worktree) error {
	m := r.m
	w.Lease = nil
	if !m.registered(r.ctx, w.Path) {
		// git no longer knows the directory, so there is nothing for it to
		// preserve and every git command inside it fails: delete what is
		// left and forget the registration.
		if err := os.RemoveAll(w.Path); err != nil {
			return err
		}
		_ = m.Prov.Unlock(r.ctx, w.Path) // a locked leftover registration blocks the prune
		return m.Prov.Prune(r.ctx)
	}
	if !m.neverLeased(w) {
		if _, err := m.preserveIfNeeded(r.ctx, w); err != nil {
			return err
		}
	}
	if _, err := m.Prov.RemoveStaleIndexLock(w.Path, m.Cfg.IndexLockStaleAfter.D); err != nil {
		return err
	}
	return m.removeWorktree(r.ctx, w.Path)
}

func (r *reaper) settleRetry(w state.Worktree, gitErr error) {
	m := r.m
	lk, cancel, err := m.lockDetached(r.ctx)
	if err != nil {
		r.fail("%v", err)
		return
	}
	defer cancel()
	defer lk.Unlock()
	st := lk.State()
	for i := range st.Worktrees {
		if st.Worktrees[i].ID != w.ID {
			continue
		}
		if gitErr != nil {
			if transient(gitErr) {
				// Slow git is not damage: keep the record as it is (broken, its
				// directory intact) for the next reap to retry, and say so once.
				r.warn("retrying %s later: %v", w.ID, gitErr)
				m.event(state.Event{
					Kind: "retry", WorktreeID: w.ID, Outcome: "timeout",
					Details: map[string]any{"reason": gitErr.Error()},
				})
				return
			}
			st.Worktrees[i].Reason, st.Worktrees[i].StatusSince = gitErr.Error(), m.now()
			_ = lk.Commit()
			m.event(state.Event{
				Kind: "broken", WorktreeID: w.ID, Outcome: "error",
				Details: map[string]any{"reason": gitErr.Error(), "retry": true},
			})
			return
		}
		st.Worktrees = append(st.Worktrees[:i], st.Worktrees[i+1:]...)
		if err := lk.Commit(); err != nil {
			r.fail("%v", err)
			return
		}
		m.event(state.Event{Kind: "remove", WorktreeID: w.ID, Outcome: "ok"})
		r.res.Removed = append(r.res.Removed, w.ID)
		return
	}
}

// keepBudget is how many reclaimed worktrees may stay as available: with
// reuse on, min less the worktrees already ready or being made ready.
func (m *Manager) keepBudget(st *state.State) int {
	if !m.Cfg.Reuse || m.Cfg.Min == nil {
		return 0
	}
	n := *m.Cfg.Min
	for _, w := range st.Worktrees {
		if w.Status == state.Available || w.Status == state.Creating {
			n--
		}
	}
	if n < 0 {
		return 0
	}
	return n
}

// reclaimAll is step 6.
func (r *reaper) reclaimAll() {
	m := r.m
	lk, err := m.lockRepo(r.ctx)
	if err != nil {
		r.fail("%v", err)
		return
	}
	budget := m.keepBudget(lk.State())
	var ids []string
	for _, c := range m.reclaimCandidates(lk.State()) {
		ids = append(ids, c.ID)
	}
	lk.Unlock()

	for _, id := range ids {
		if r.stop() {
			return
		}
		lk, err := m.lockRepo(r.ctx)
		if err != nil {
			r.fail("%v", err)
			return
		}
		if !m.reclaimable(lk.State(), id) {
			lk.Unlock()
			continue
		}
		w, err := m.reclaimWorktree(r.ctx, lk, id, budget > 0)
		lk.Unlock()
		switch {
		case errors.Is(err, errHeld):
		case isBroken(err):
			r.res.Broken = append(r.res.Broken, id)
		case err != nil:
			r.fail("reclaiming %s: %v", id, err)
		case w.Status == state.Available:
			r.res.Kept = append(r.res.Kept, id)
			budget--
		default:
			r.res.Removed = append(r.res.Removed, id)
		}
	}
}

func (m *Manager) reclaimable(st *state.State, id string) bool {
	for _, w := range st.Worktrees {
		if w.ID == id {
			s := w.Effective(m.now())
			return s == state.Released || s == state.LeaseExpired
		}
	}
	return false
}

// checkAvailable is step 7: available worktrees above max are removed (the
// highest slots first), and the others are checked for unsaved work, which is
// preserved before any later hand-out.
func (r *reaper) checkAvailable() {
	m := r.m
	st, err := m.Store.ReadUnlocked()
	if err != nil {
		r.fail("%v", err)
		return
	}
	var avail []state.Worktree
	for _, w := range st.Worktrees {
		if w.Status == state.Available && w.Lease == nil {
			avail = append(avail, w)
		}
	}
	sort.Slice(avail, func(i, j int) bool { return avail[i].Slot > avail[j].Slot })
	excess := 0
	if m.Cfg.Max != nil && len(st.Worktrees) > *m.Cfg.Max {
		excess = len(st.Worktrees) - *m.Cfg.Max
	}
	for i, w := range avail {
		if r.stop() {
			return
		}
		if i < excess {
			r.removeAvailable(w.ID)
		} else {
			r.checkOne(w.ID)
		}
	}
}

func (r *reaper) removeAvailable(id string) {
	m := r.m
	lk, err := m.lockRepo(r.ctx)
	if err != nil {
		r.fail("%v", err)
		return
	}
	var ok bool
	for _, w := range lk.State().Worktrees {
		ok = ok || (w.ID == id && w.Status == state.Available && w.Lease == nil)
	}
	if !ok {
		lk.Unlock()
		return
	}
	_, err = m.reclaimWorktree(r.ctx, lk, id, false)
	lk.Unlock()
	switch {
	case errors.Is(err, errHeld):
	case isBroken(err):
		r.res.Broken = append(r.res.Broken, id)
	case err != nil:
		r.fail("removing %s: %v", id, err)
	default:
		r.res.Removed = append(r.res.Removed, id)
	}
}

// checkOne preserves the unsaved work of the available worktree id and puts it
// back to a detached head.
func (r *reaper) checkOne(id string) {
	m := r.m
	rel, err := m.Store.HoldOp(id)
	if err != nil {
		return
	}
	defer rel()
	st, err := m.Store.ReadUnlocked()
	if err != nil {
		r.fail("%v", err)
		return
	}
	var w *state.Worktree
	for i := range st.Worktrees {
		if st.Worktrees[i].ID == id && st.Worktrees[i].Status == state.Available && st.Worktrees[i].Lease == nil {
			w = &st.Worktrees[i]
		}
	}
	if w == nil {
		return
	}
	if m.neverLeased(*w) {
		return // nothing was written into it on anyone's behalf
	}
	pb, gitErr := m.preserveIfNeeded(r.ctx, *w)
	if gitErr == nil && pb != "" {
		gitErr = m.Prov.Switch(r.ctx, w.Path, provision.BranchSpec{Detach: true}, m.Cfg.ReuseHookPolicy != "skip")
	}
	if gitErr == nil {
		return
	}
	if transient(gitErr) {
		// Slow git (host load) is not damage: the worktree stays available and
		// the next reap checks it again.
		r.warn("checking %s later: %v", id, gitErr)
		return
	}
	if lk, cancel, err := m.lockDetached(r.ctx); err == nil {
		for i := range lk.State().Worktrees {
			if c := &lk.State().Worktrees[i]; c.ID == id {
				c.Status, c.Reason, c.StatusSince = state.Broken, gitErr.Error(), m.now()
			}
		}
		_ = lk.Commit()
		lk.Unlock()
		cancel()
	}
	m.event(state.Event{Kind: "broken", WorktreeID: id, Outcome: "error", Details: map[string]any{"reason": gitErr.Error()}})
	r.res.Broken = append(r.res.Broken, id)
}

// standbys is step 8: fresh detached worktrees, one at a time, until min are
// ready or the free-space floor or max stops it.
func (r *reaper) standbys() {
	m := r.m
	if m.Cfg.Min == nil {
		return
	}
	for {
		if r.poolFull() {
			return // done: a deadline that passed during the last create is not late
		}
		if r.stop() {
			return
		}
		id, path, stopped := r.reserveStandby()
		if stopped || id == "" {
			return
		}
		if !r.createStandby(id, path) {
			return
		}
	}
}

// reserveStandby adds a Creating record when fewer than min worktrees are
// ready and room remains. stopped is true when creation must end.
func (r *reaper) reserveStandby() (id, path string, stopped bool) {
	m := r.m
	lk, err := m.lockRepo(r.ctx)
	if err != nil {
		r.fail("%v", err)
		return "", "", true
	}
	defer lk.Unlock()
	st := lk.State()
	ready := 0
	for _, w := range st.Worktrees {
		if w.Status == state.Available || w.Status == state.Creating {
			ready++
		}
	}
	if ready >= *m.Cfg.Min {
		return "", "", true
	}
	now := m.now()
	w, err := m.newWorktree(r.ctx, st, &state.Lease{Metadata: map[string]string{}}, LeaseRequest{}, now)
	if err != nil {
		var ce *Error
		switch {
		case errors.As(err, &ce) && ce.Code == CodeLowDisk:
			r.warn("%s; no more standby worktrees were created", ce.Message)
			m.event(state.Event{Kind: "low-disk", Outcome: "error", Details: ce.Details})
		case errors.As(err, &ce) && ce.Code == CodeMaxReached:
		default:
			r.fail("%v", err)
		}
		return "", "", true
	}
	rel, err := m.Store.HoldOp(w.ID)
	if err != nil {
		r.fail("%v", err)
		return "", "", true
	}
	// The hold stays with this process until createStandby ends.
	r.standbyHold = rel
	w.Lease, w.Status, w.NeverLeased = nil, state.Creating, true
	st.Worktrees = append(st.Worktrees, w)
	if err := lk.Commit(); err != nil {
		rel()
		r.fail("%v", err)
		return "", "", true
	}
	return w.ID, w.Path, false
}

// createStandby creates the reserved worktree detached and records it
// available, or broken. It reports whether the loop may go on.
func (r *reaper) createStandby(id, path string) bool {
	m := r.m
	defer r.standbyHold()
	err := os.MkdirAll(m.Cfg.WorktreeRoot, 0o755)
	if err == nil {
		var rel func()
		rel, err = m.Store.HoldCreate(r.ctx, span(m.Cfg.CreateTimeout, 24*time.Hour))
		if err == nil {
			err = m.Prov.Create(r.ctx, path, provision.BranchSpec{Detach: true}, "pg-gwm "+id)
			rel()
		}
	}
	lk, cancel, lerr := m.lockDetached(r.ctx)
	if lerr != nil {
		r.fail("%v", lerr)
		return false
	}
	defer cancel()
	defer lk.Unlock()
	for i := range lk.State().Worktrees {
		w := &lk.State().Worktrees[i]
		if w.ID != id {
			continue
		}
		w.StatusSince = m.now()
		if err != nil {
			w.Status, w.Reason = state.Broken, err.Error()
		} else {
			w.Status, w.Reason = state.Available, ""
		}
	}
	_ = lk.Commit()
	if err != nil {
		m.event(state.Event{Kind: "broken", WorktreeID: id, Outcome: "error", Details: map[string]any{"reason": err.Error()}})
		r.res.Broken = append(r.res.Broken, id)
		return false
	}
	m.event(state.Event{Kind: "create", WorktreeID: id, Outcome: "ok", Details: map[string]any{"path": path}})
	r.res.Created = append(r.res.Created, id)
	return true
}

// dry computes what a run would do from a copy of the state, touching
// nothing. It assumes every git step succeeds.
func (r *reaper) dry(st state.State) {
	m := r.m
	now := m.now()
	budget := m.keepBudget(&st)
	count := len(st.Worktrees)
	ready := 0
	for _, w := range st.Worktrees {
		if w.Status == state.Available {
			ready++
		}
	}
	preview := func(w state.Worktree, label string) {
		un, err := m.Prov.Unsaved(r.ctx, w.Path)
		if err != nil || !un.Any() || un.Blocker != "" {
			return
		}
		r.res.Preserved = append(r.res.Preserved, preservedBranchName(now, w.Branch, label))
	}
	var avail []state.Worktree
	for _, w := range st.Worktrees {
		if _, err := os.Lstat(w.Path); errors.Is(err, os.ErrNotExist) && !r.held(w.ID) {
			r.res.Removed = append(r.res.Removed, w.ID)
			count--
			if w.Status == state.Available {
				ready--
			}
			continue
		}
		switch {
		case w.Status == state.Broken, (w.Status == state.Preparing || w.Status == state.Creating || w.Status == state.Reclaiming) && !r.held(w.ID):
			r.res.Broken = append(r.res.Broken, w.ID)
			preview(w, w.ID)
			r.res.Removed = append(r.res.Removed, w.ID)
			count--
		}
	}
	for _, c := range m.reclaimCandidates(&st) {
		label := c.ID
		if c.Lease != nil {
			label = c.Lease.Name
			if label == "" {
				label = c.Lease.ID
			}
		}
		preview(*c, label)
		if budget > 0 {
			budget--
			ready++
			r.res.Kept = append(r.res.Kept, c.ID)
		} else {
			r.res.Removed = append(r.res.Removed, c.ID)
			count--
		}
	}
	for _, w := range st.Worktrees {
		if w.Status == state.Available && w.Lease == nil {
			avail = append(avail, w)
		}
	}
	sort.Slice(avail, func(i, j int) bool { return avail[i].Slot > avail[j].Slot })
	excess := 0
	if m.Cfg.Max != nil && count > *m.Cfg.Max {
		excess = count - *m.Cfg.Max
	}
	for i, w := range avail {
		if i < excess {
			r.res.Removed = append(r.res.Removed, w.ID)
			ready--
			count--
		} else {
			preview(w, w.ID)
		}
	}
	if m.Cfg.Min == nil {
		return
	}
	for ready < *m.Cfg.Min {
		if m.Cfg.Max != nil && count >= *m.Cfg.Max {
			return
		}
		if fl := m.Cfg.FreeSpaceFloor; fl != nil && *fl > 0 {
			free, err := m.freeBytes(existingAncestor(m.Cfg.WorktreeRoot))
			if err == nil && free < *fl {
				r.warn("%d bytes free, below the free-space-floor of %d; no more standby worktrees would be created", free, *fl)
				return
			}
		}
		r.res.Created = append(r.res.Created, "(new)")
		ready++
		count++
	}
}

// ---------------------------------------------------------------------------

// reapAllNow is the clock of ReapAll's registry bookkeeping; tests replace it.
var reapAllNow = time.Now

// ReapAll reaps the union of the configured and the registered repositories.
// A registered path that no longer exists is unreachable, marked missing, and
// pruned from the registry after tombstone-retention. A path ReapAll cannot
// open is reported without a Manager: unreachable when it no longer resolves
// to a repository, error when newManager fails. It then writes reaper.json:
// the run's start, end and worst outcome and every non-skipped repository's
// entry. A run with nothing to reap is completed and a success.
func ReapAll(ctx context.Context, f config.File, reg state.Registry, newManager func(repo.Repo) (*Manager, error), o ReapOptions) []ReapResult {
	started := reapAllNow().UTC()
	type target struct {
		path       string
		registered bool
	}
	seen := map[string]*target{}
	var order []string
	add := func(p string, registered bool) {
		p = realPath(p)
		if t, ok := seen[p]; ok {
			t.registered = t.registered || registered
			return
		}
		seen[p] = &target{path: p, registered: registered}
		order = append(order, p)
	}
	for k := range f.Repos {
		add(k, false)
	}
	var results []ReapResult
	if reg.Path != "" {
		entries, err := reg.List()
		if err != nil {
			results = append(results, ReapResult{Repo: reg.Path, Outcome: ReapError, Warnings: []string{err.Error()}})
		}
		for _, e := range entries {
			add(e.Main, true)
		}
	}
	sort.Strings(order)

	entries := map[string]state.RepoReap{}
	anyVanished := false
	for _, p := range order {
		t := seen[p]
		res := ReapResult{Repo: p}
		rp, err := repo.FromMain(p)
		if err != nil {
			res.Outcome = ReapUnreachable
			res.Warnings = []string{err.Error()}
			if _, serr := os.Stat(p); serr != nil && t.registered && !o.DryRun {
				_ = reg.MarkMissing(p, reapAllNow())
				anyVanished = true
			}
			res.normalize()
			results = append(results, res)
			entries[repo.PathLabel(p)] = pathEntry(res, reapAllNow())
			continue
		}
		if t.registered && !o.DryRun {
			_ = reg.Add(rp.Main) // clears a stale missing mark
		}
		m, err := newManager(rp)
		if err != nil {
			res.Outcome = ReapError
			res.Warnings = []string{err.Error()}
			res.normalize()
			results = append(results, res)
			entries[repo.PathLabel(p)] = pathEntry(res, reapAllNow())
			continue
		}
		res, err = m.Reap(ctx, o)
		if err != nil && !isBusy(err) {
			res.Outcome = ReapError
			res.Warnings = append(res.Warnings, err.Error())
		}
		res.normalize()
		results = append(results, res)
		if res.Outcome != ReapSkipped {
			entries[m.Repo.Label] = m.reapEntry(res)
		}
	}
	_ = anyVanished
	if reg.Path != "" && !o.DryRun {
		retention := f.For(repo.Repo{}).TombstoneRetention
		if !retention.None && retention.D > 0 {
			_ = reg.Prune(retention.D, reapAllNow())
		}
	}

	worst, errored, ran := ReapCompleted, false, len(results) == 0
	for _, r := range results {
		if severity(r.Outcome) > severity(worst) {
			worst = r.Outcome
		}
		errored = errored || r.Outcome == ReapError
		ran = ran || r.Outcome != ReapSkipped
	}
	ended := reapAllNow().UTC()
	if !o.DryRun {
		rec := state.ReaperRecord{Started: started, Ended: ended, Outcome: worst, Repos: entries}
		if !errored && ran {
			rec.LastSuccess = &ended
		}
		_ = state.WriteReaperRecord(state.ReaperPath(), rec)
	}
	if results == nil {
		results = []ReapResult{}
	}
	return results
}

func pathEntry(res ReapResult, at time.Time) state.RepoReap {
	return state.RepoReap{Label: repo.PathLabel(res.Repo), At: at.UTC(), Outcome: res.Outcome}
}
