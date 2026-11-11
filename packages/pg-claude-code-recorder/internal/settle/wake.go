package settle

// The event-driven settle loop. Wake evaluates the Conversations whose inputs
// changed (the dirty set, a process entering or leaving, a flip of low-space
// mode) and the ones whose deadline passed (the due-time heap), with the same
// evaluate the full scan uses. Tick stays as the reconciliation: it runs at
// start and every Config.Reconcile, rebuilds the heap, and is the oracle the
// equivalence test compares Wake against.
//
// Wake does not call refresh or groups(): it reads the kept group map in place,
// so its cost follows the Conversations it processes, not the store.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The settle loop modes.
const (
	ModeScan  = "scan"
	ModeEvent = "event"
)

// EventMode reports whether the settler runs the event-driven loop.
func (s *Settler) EventMode() bool { return s.cfg.Mode == ModeEvent }

// ReconcileEvery is the interval of the full reconciliation Tick.
func (s *Settler) ReconcileEvery() time.Duration { return s.cfg.Reconcile }

// MinWake is the shortest time between two wakes.
func (s *Settler) MinWake() time.Duration { return s.cfg.MinWake }

// NextDue returns the earliest deadline in the due-time heap as of the last
// pass, or false when it is empty.
func (s *Settler) NextDue() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextAt, s.nextOK
}

func (s *Settler) publishNext() {
	at, ok := time.Time{}, false
	if s.due != nil {
		at, ok = s.due.Next()
	}
	s.mu.Lock()
	s.nextAt, s.nextOK = at, ok
	s.mu.Unlock()
	s.cfg.Obs.Reg.Set(obs.MSettleDueQueueDepth, nil, float64(s.dueLen()))
}

func (s *Settler) dueLen() int {
	if s.due == nil {
		return 0
	}
	return s.due.Len()
}

// priorStage is the stage a Conversation was last published in; a Conversation
// seen for the first time is taken to have been quiet (live when live now).
func (s *Settler) priorStage(session, now string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.last[session]; ok {
		return prev
	}
	if now == StageLive {
		return StageLive
	}
	return StageQuiet
}

// transition publishes a Conversation's stage: it records it, hands the reason
// the stage machine left to the stage_changed event, and emits the event when
// the stage differs from from.
func (s *Settler) transition(session, from, to string) {
	s.mu.Lock()
	s.last[session] = to
	s.gauges.setStage(session, to)
	reason := s.reasons[session]
	delete(s.reasons, session)
	s.mu.Unlock()
	if from != to {
		s.cfg.Obs.Event(obs.EvStageChanged, "session", session, "from", from, "to", to, "reason", reason)
	}
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		if v {
			out[k] = true
		}
	}
	return out
}

// seedLocked rebuilds the due-time state from the Conversations as the
// reconciliation left them. The run lock is held.
func (s *Settler) seedLocked(ctx context.Context, groups map[string]*conv, live map[string]bool, now time.Time) {
	s.due = NewDueHeap()
	s.liveSeen = copySet(live)
	s.lowSeen = s.LowSpace()
	rows, _ := s.st.Stages()
	stage := map[string]store.StageRow{}
	for _, r := range rows {
		stage[r.Session] = r
	}
	for session, c := range groups {
		if live[session] {
			continue
		}
		s.schedule(ctx, c, stage[session].Stage, false, now, nil)
	}
	s.seeded = true
	s.publishNext()
}

// schedule derives a Conversation's next deadline from its stage after an
// evaluation and pushes it. Removal deadlines that are already due go into
// removal when it is non-nil (the caller runs the removal pass), else into the
// heap. stage is the persisted stage ("" for none).
func (s *Settler) schedule(ctx context.Context, c *conv, stage string, errored bool, now time.Time, removal map[string]bool) {
	session := c.session
	act, at := scheduleDecision(schedInput{
		errored: errored, stage: stage, now: now,
		removeEnable: s.cfg.RemoveEnable, removeAfter: s.cfg.RemoveAfter,
		recheck: s.cfg.Recheck, after: s.cfg.After,
		removalPass: removal != nil,
		newest:      func() time.Time { return s.newest(ctx, c) },
		checkedAt: func() time.Time {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.checkedAt[session]
		},
	})
	switch act {
	case schedPush:
		s.due.Push(session, at)
	case schedPushRemoval:
		s.due.PushKind(session, DueRemoval, at)
	case schedRemovalNow:
		removal[session] = true
	}
}

// heldRewritten returns the held Conversations whose newest original mtime is
// later than the moment they were held. Only an mtime change reaches this: the
// copier's commits feed the dirty set, but a file given a new mtime without a
// copier commit does not, and the scan moves such a Conversation from held back
// to quiet within a minute. The check stats the files of the held Conversations
// only (a handful), not the store, so no per-minute full scan returns. The run
// lock is held.
func (s *Settler) heldRewritten(ctx context.Context) map[string]bool {
	s.mu.Lock()
	var held []string
	for session, stage := range s.last {
		if stage == StageHeld {
			held = append(held, session)
		}
	}
	s.mu.Unlock()
	out := map[string]bool{}
	for _, session := range held {
		c := s.gcache[session]
		if c == nil {
			continue
		}
		row, ok, err := s.st.Stage(session)
		if err != nil || !ok || row.Stage != StageHeld {
			continue
		}
		if s.newest(ctx, c).After(time.Unix(0, row.SinceNS)) {
			out[session] = true
		}
	}
	return out
}

// Wake runs one event-driven pass at now. Before the first reconciliation it
// is the reconciliation.
func (s *Settler) Wake(now time.Time) (TickStats, error) {
	ctx := s.detached()
	var stats TickStats
	t0 := s.cfg.Clock.Now()
	lap := func(into *time.Duration) {
		n := s.cfg.Clock.Now()
		*into = n.Sub(t0)
		t0 = n
	}
	var ph TickPhases
	// TakeDirty may rebuild the group map and so takes the run lock itself.
	dirty, rebuildWait := s.takeDirty()
	sigLive := s.TakeLiveChanged()
	lap(&ph.Dirty)
	unlock, wait := s.lockRunWait("wake")
	defer unlock()
	ph.LockWait = wait + rebuildWait
	t0 = s.cfg.Clock.Now() // the lock wait is reported by lockRunWait; the phases below start here
	if !s.seeded {
		return s.tickLockedWaited(ctx, ph.LockWait)
	}
	defer func() {
		s.mu.Lock()
		s.phases = ph
		s.mu.Unlock()
	}()
	reg := s.cfg.Obs.Reg
	s.checkLow()
	low := s.LowSpace()
	flip := low != s.lowSeen
	s.lowSeen = low
	live, err := s.cfg.Live()
	if err != nil {
		return stats, err
	}
	if err := s.syncGroups(ctx); err != nil {
		return stats, err
	}
	work := map[string]bool{}
	for session := range dirty {
		work[session] = true
	}
	liveDiff := false
	for session := range live {
		if live[session] && !s.liveSeen[session] {
			work[session], liveDiff = true, true
		}
	}
	for session := range s.liveSeen {
		if !live[session] {
			work[session], liveDiff = true, true
		}
	}
	lap(&ph.Reads)
	s.liveSeen = copySet(live)
	for session := range s.heldRewritten(ctx) {
		if !live[session] {
			work[session] = true
		}
	}
	if flip {
		rows, _ := s.st.Stages()
		settled := map[string]bool{}
		for _, r := range rows {
			if r.Stage == StageSettled {
				settled[r.Session] = true
			}
		}
		for session := range s.gcache {
			if !settled[session] {
				work[session] = true
			}
		}
	}
	items := s.due.PopDue(now)
	removal := map[string]bool{}
	for _, it := range items {
		work[it.Session] = true
		if it.Kind == DueRemoval {
			removal[it.Session] = true
		}
	}
	dirtyReason := len(dirty) > 0 || sigLive
	for reason, on := range map[string]bool{
		"dirty": dirtyReason,
		"due":   len(items) > 0,
		"poll":  (liveDiff && !sigLive) || flip || (!dirtyReason && len(items) == 0),
	} {
		if on {
			reg.Add(obs.MSettleWakes, map[string]string{"reason": reason}, 1)
		}
	}
	reg.Set(obs.MSettleDirtySessions, nil, float64(len(dirty)))

	sessions := make([]string, 0, len(work))
	for session := range work {
		sessions = append(sessions, session)
	}
	sort.Strings(sessions)
	var first error
	touched := map[string]bool{}
	for _, session := range sessions {
		if s.stopping() {
			break // between Conversations; the next start reconciles the rest
		}
		c := s.gcache[session]
		if c == nil {
			continue
		}
		stats.Examined++
		out, err := s.evaluate(ctx, c, live[session], now, false)
		if err != nil && first == nil {
			first = fmt.Errorf("settle %s: %w", session, err)
		}
		touched[session] = true
		if live[session] {
			continue
		}
		stage := out.Stage
		if row, ok, e := s.st.Stage(session); e == nil {
			stage = row.Stage
			if !ok {
				stage = ""
			}
		}
		s.schedule(ctx, c, stage, err != nil, now, removal)
	}
	lap(&ph.Evaluate)
	if s.cfg.RemoveEnable && len(removal) > 0 && !s.stopping() {
		names := make([]string, 0, len(removal))
		for session := range removal {
			names = append(names, session)
		}
		sort.Strings(names)
		var rep RemoveReport
		run := func() error {
			var e error
			rep, e = s.removeLocked(ctx, RemoveOptions{Sessions: names}, now)
			return e
		}
		if s.cfg.Timer != nil {
			err = s.cfg.Timer(run)
		} else {
			err = run()
		}
		if err != nil && first == nil {
			first = fmt.Errorf("removal pass: %w", err)
		}
		for _, session := range names {
			touched[session] = true
		}
		for _, r := range rep.Removed {
			touched[r] = true
		}
		for _, sk := range rep.Skipped {
			c := s.gcache[sk.Session]
			if c == nil {
				continue
			}
			for _, r := range sk.Reasons {
				switch r {
				case BlockRunCap, BlockHourCap:
					s.due.PushKind(sk.Session, DueRemoval, now)
				case BlockQuietAge:
					s.due.PushKind(sk.Session, DueRemoval, s.newest(ctx, c).Add(s.cfg.RemoveAfter))
				}
			}
		}
		if err := s.syncGroups(ctx); err == nil {
			if l, err := s.cfg.Live(); err == nil {
				live = l
			}
		}
	}
	lap(&ph.Removal)
	for _, session := range sortedKeys(touched) {
		var row *store.StageRow
		if r, ok, err := s.st.Stage(session); err == nil && ok {
			row = &r
		}
		c := s.gcache[session]
		if c == nil {
			// No files any more: only a removal stage is counted, as refresh does.
			s.mu.Lock()
			if row != nil && (row.Stage == store.StageRemoved || row.Stage == store.StageRemovalPending) {
				s.gauges.setStage(session, row.Stage)
			} else {
				s.gauges.setStage(session, "")
			}
			s.mu.Unlock()
			continue
		}
		to := StageQuiet
		if live[session] {
			to = StageLive
		} else if row != nil {
			to = row.Stage
		}
		s.transition(session, s.priorStage(session, to), to)
		s.noteSession(c, to, row)
	}
	s.refreshRemovalGaugesIfDue(ctx, s.gcache, live, now)
	s.publishGauges(now)
	s.publishNext()
	lap(&ph.Refresh)
	return stats, first
}
