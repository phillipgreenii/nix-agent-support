package signal

import (
	"context"
	"time"
)

// EnrichCmdErrForTest exposes enrichCmdErr for whitebox testing.
func EnrichCmdErrForTest(err error) error { return enrichCmdErr(err) }

// EnumeratePanesForTest exposes enumeratePanes for whitebox testing.
func EnumeratePanesForTest(t *TmuxSignaler, ctx context.Context) (map[int]PaneLocForTest, error) {
	locs, err := t.enumeratePanes(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]PaneLocForTest, len(locs))
	for pid, loc := range locs {
		out[pid] = PaneLocForTest{SocketName: loc.socketName, PaneID: loc.paneID}
	}
	return out, nil
}

// PaneLocForTest is the test-visible view of paneLoc.
type PaneLocForTest struct {
	SocketName string
	PaneID     string
}

// CachedPanesForTest exposes cachedPanes for whitebox testing.
func CachedPanesForTest(t *TmuxSignaler) (map[int]PaneLocForTest, error) {
	locs, err := t.cachedPanes()
	if err != nil {
		return nil, err
	}
	out := make(map[int]PaneLocForTest, len(locs))
	for pid, loc := range locs {
		out[pid] = PaneLocForTest{SocketName: loc.socketName, PaneID: loc.paneID}
	}
	return out, nil
}

// Test seams for the TTL caches (injectable clock, TTL constants, and the
// server-PID cache accessor).

// SetClockForTest injects the clock the signaler's TTL checks read.
func SetClockForTest(s Signaler, now func() time.Time) {
	switch v := s.(type) {
	case *CmuxSignaler:
		v.now = now
	case *TmuxSignaler:
		v.now = now
	}
}

// CachedCmuxServerPIDsForTest exposes cachedCmuxServerPIDs for whitebox testing.
func CachedCmuxServerPIDsForTest(c *CmuxSignaler) ([]int, error) { return c.cachedCmuxServerPIDs() }

// TTLs exposes the cache TTL constants so tests can pin their relationship.
func TTLs() (surface, serverPID, tmux time.Duration) {
	return surfaceCacheTTL, serverPIDCacheTTL, tmuxCacheTTL
}
