package daemon

import (
	"sync"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

// sharedState holds the daemon's current view of the world. The tick
// loop writes; the gRPC handlers read. RWMutex bounded — Tree pointers
// are immutable once published, so handlers can read freely.
type sharedState struct {
	mu               sync.RWMutex
	tree             *aggregate.Tree
	caffeinateOn     bool
	caffeinateActive bool // distinct from "on": on=user wants caffeinate, active=process running
	caffeinateCause  string
	runtimePath      string // for persistence on toggle
	nudger           *nudger.Nudger
	watermarks       *WatermarkStore
}

func newSharedState() *sharedState {
	return &sharedState{}
}

func (s *sharedState) setTree(t *aggregate.Tree) {
	s.mu.Lock()
	s.tree = t
	s.mu.Unlock()
}

func (s *sharedState) snapshot() *aggregate.Tree {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tree
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
