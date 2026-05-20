package daemon

import (
	"sync"

	"github.com/phillipgreenii/claude-agents-tui/internal/core/aggregate"
)

// sharedState holds the daemon's current view of the world. The tick
// loop writes; the gRPC handlers read. RWMutex bounded — Tree pointers
// are immutable once published, so handlers can read freely.
type sharedState struct {
	mu           sync.RWMutex
	tree         *aggregate.Tree
	caffeinateOn bool
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
