package signal

import "context"

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
