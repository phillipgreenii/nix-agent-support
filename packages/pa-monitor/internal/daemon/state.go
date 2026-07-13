package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// pendingNudgeQuerier is the interface sharedState uses to look up pending
// nudge sources per session at snapshot time. *nudger.Nudger satisfies it.
type pendingNudgeQuerier interface {
	PendingSourcesForSession(sid string) []nudger.Source
}

// sharedState holds the daemon's current view of the world. The tick
// loop writes; the gRPC handlers read. All read access to tree state goes
// through snapshot() which materialises from the DB via ReadService.
type sharedState struct {
	mu               sync.RWMutex
	readService      *service.ReadService
	caffeinateOn     bool
	caffeinateActive bool // legacy collapsed flag; on=user wants caffeinate, active=process running OR armed
	caffeinateCause  string
	// Two unambiguous caffeinate indicators (D6). caffeinateProcess mirrors
	// the caffeinate.Manager's State (off / on(holding) / grace / error);
	// caffeinateGraceRemaining carries the countdown seconds while in grace.
	// caffeinateOn above is the auto-caffeinate MODE (the user toggle).
	caffeinateProcess        caffeinate.State
	caffeinateGraceRemaining time.Duration
	runtimePath              string // for persistence on toggle
	nudger                   *nudger.Nudger
	nudgerForPending         pendingNudgeQuerier // used by snapshot() to annotate pending nudge state
	watermarks               *WatermarkStore
	autoResumeDelay          time.Duration // static config: how long to wait before auto-nudging

	// cachedSnapshotTree is the most recent snapshot() result, refreshed by the
	// daemon's tick loop (refreshSnapshot) on the TICK goroutine. gRPC handlers
	// (buildState) serve this cached copy instead of doing a synchronous SQLite
	// read on their own goroutine — so a slow DB cannot stall the BridgeChannel
	// writer past its snapshot watchdog. The DB is only re-read once per tick,
	// off the request path.
	cachedSnapshotTree *aggregate.Tree
}

func newSharedState() *sharedState {
	return &sharedState{}
}

func (s *sharedState) setReadService(rs *service.ReadService) {
	s.mu.Lock()
	s.readService = rs
	s.mu.Unlock()
}

// setPendingNudgeQueue wires in the nudger used to annotate the DB-materialised
// tree with live pending-nudge state at snapshot time.
func (s *sharedState) setPendingNudgeQueue(q pendingNudgeQuerier) {
	s.mu.Lock()
	s.nudgerForPending = q
	s.mu.Unlock()
}

// snapshot materialises the current aggregate.Tree from the DB via ReadService.
// Returns nil when ReadService is not yet wired (daemon not fully started).
func (s *sharedState) snapshot() *aggregate.Tree {
	s.mu.RLock()
	rs := s.readService
	pq := s.nudgerForPending
	s.mu.RUnlock()

	if rs == nil {
		return nil
	}
	st, err := rs.GetState(context.Background(), store.FilterAll)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pa-monitor: snapshot: DB read error: %v\n", err)
		return nil
	}
	if st == nil {
		return nil
	}
	tree := convertStateToAggregateTree(st)
	// Annotate sessions with live pending-nudge state from the in-memory
	// nudger. The DB does not persist pending intents — they live only in
	// the nudger's PendingStore.
	if pq != nil && tree != nil {
		for _, sv := range tree.Sessions() {
			sources := pq.PendingSourcesForSession(sv.SessionID)
			if len(sources) == 0 {
				continue
			}
			strs := make([]string, 0, len(sources))
			for _, src := range sources {
				strs = append(strs, string(src))
			}
			sv.PendingNudge = &aggregate.PendingNudge{Sources: strs}
		}
	}
	return tree
}

// refreshSnapshot materialises the tree once (the SQLite read) and stores it as
// the cached snapshot. The daemon's tick loop calls this on the TICK goroutine,
// so the expensive DB read happens off the gRPC request path. Because the DB
// only changes per tick, the cache is as fresh as a per-request read for the
// tree itself; live pending-nudge annotations may lag by up to one tick.
func (s *sharedState) refreshSnapshot() {
	s.setCachedSnapshot(s.snapshot())
}

// setCachedSnapshot publishes tree as the cached snapshot (pointer swap under
// the lock; readers never see a torn tree).
func (s *sharedState) setCachedSnapshot(tree *aggregate.Tree) {
	s.mu.Lock()
	s.cachedSnapshotTree = tree
	s.mu.Unlock()
}

// cachedSnapshot returns the most recently refreshed snapshot, or nil before
// the first refresh (cold start).
func (s *sharedState) cachedSnapshot() *aggregate.Tree {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cachedSnapshotTree
}

func (s *sharedState) setCaffeinateOn(on bool) {
	s.mu.Lock()
	s.caffeinateOn = on
	s.mu.Unlock()
}

func (s *sharedState) isCaffeinateOn() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.caffeinateOn
}

func (s *sharedState) setCaffeinateActive(active bool, cause string) {
	s.mu.Lock()
	s.caffeinateActive = active
	s.caffeinateCause = cause
	s.mu.Unlock()
}

// setCaffeinateState records both indicators in one shot: the legacy collapsed
// `active` flag plus the richer PROCESS state (from caffeinate.Manager.State())
// and its grace-remaining countdown. The MODE (user toggle) lives separately on
// caffeinateOn. Called by the tick loop after Caffeinate.Tick.
func (s *sharedState) setCaffeinateState(active bool, cause string, process caffeinate.State, graceRemaining time.Duration) {
	s.mu.Lock()
	s.caffeinateActive = active
	s.caffeinateCause = cause
	s.caffeinateProcess = process
	s.caffeinateGraceRemaining = graceRemaining
	s.mu.Unlock()
}

func (s *sharedState) caffeinateView() (active bool, cause string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.caffeinateActive, s.caffeinateCause
}

// caffeinateIndicators returns the full two-indicator view for surfacing:
// mode (the user toggle), the legacy collapsed active flag, the PROCESS state,
// its grace-remaining countdown, and the cause.
func (s *sharedState) caffeinateIndicators() (mode, active bool, process caffeinate.State, graceRemaining time.Duration, cause string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.caffeinateOn, s.caffeinateActive, s.caffeinateProcess, s.caffeinateGraceRemaining, s.caffeinateCause
}

// Nudger returns the daemon's Nudger instance. May be nil when nudger
// signaling is not configured (NudgerSignalers empty / RuntimePath absent).
// Used by gRPC handlers (Phase 6) to enqueue manual nudges.
func (s *sharedState) Nudger() *nudger.Nudger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nudger
}

// Watermarks returns the daemon's WatermarkStore instance. May be nil when
// the nudger is not configured. Used by gRPC handlers to persist
// auto-resume settings.
func (s *sharedState) Watermarks() *WatermarkStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.watermarks
}
