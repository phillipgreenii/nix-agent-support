package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
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
// loop writes; the gRPC handlers read. RWMutex bounded — Tree pointers
// are immutable once published, so handlers can read freely.
type sharedState struct {
	mu               sync.RWMutex
	tree             *aggregate.Tree
	readService      *service.ReadService
	caffeinateOn     bool
	caffeinateActive bool // distinct from "on": on=user wants caffeinate, active=process running
	caffeinateCause  string
	runtimePath      string // for persistence on toggle
	nudger           *nudger.Nudger
	nudgerForPending pendingNudgeQuerier // used by snapshot() to annotate pending nudge state
	watermarks       *WatermarkStore
	autoResumeDelay  time.Duration // static config: how long to wait before auto-nudging
}

func newSharedState() *sharedState {
	return &sharedState{}
}

func (s *sharedState) setTree(t *aggregate.Tree) {
	s.mu.Lock()
	s.tree = t
	s.mu.Unlock()
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

// snapshot returns the current aggregate.Tree. When a ReadService is wired
// (production path), it materialises the tree from the DB on each call.
// When no ReadService is set (tests that inject via setTree), it falls back
// to the in-memory tree pointer so existing tests continue to work.
func (s *sharedState) snapshot() *aggregate.Tree {
	s.mu.RLock()
	rs := s.readService
	fallback := s.tree
	s.mu.RUnlock()

	if rs != nil {
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
		s.mu.RLock()
		pnq := s.nudgerForPending
		s.mu.RUnlock()
		if pnq != nil && tree != nil {
			for _, sv := range tree.Sessions() {
				sources := pnq.PendingSourcesForSession(sv.SessionID)
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
	return fallback
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

func (s *sharedState) caffeinateView() (active bool, cause string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.caffeinateActive, s.caffeinateCause
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
