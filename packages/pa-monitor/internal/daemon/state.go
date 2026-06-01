package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

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
		if err != nil || st == nil {
			return nil
		}
		return convertStateToAggregateTree(st)
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
