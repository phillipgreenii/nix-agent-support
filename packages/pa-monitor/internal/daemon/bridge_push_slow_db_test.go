package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// stuckBlocks is a BlockStore whose read never returns until released,
// modelling a DB read wedged behind slow recorder/tick work.
type stuckBlocks struct {
	store.BlockStore
	release chan struct{}
}

func (b *stuckBlocks) GetActive(ctx context.Context, _ time.Time, _ store.FreshnessWindow) (*store.Block, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil, nil
}

// TestBridgePushesNotStarvedBySlowRead pins the push cadence: with a read that
// never completes and no cached snapshot (cold start), the BridgeChannel must
// still push snapshots at its interval, because the push path never reads the
// DB. The bridge's push watchdog would otherwise trip and disconnect.
func TestBridgePushesNotStarvedBySlowRead(t *testing.T) {
	srv := newBridgeTestServer(t)
	stuck := &stuckBlocks{release: make(chan struct{})}
	defer close(stuck.release)
	srv.state.setReadService(service.NewReadService(service.ReadDeps{Blocks: stuck}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeBridgeStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.BridgeChannel(fake) }()

	fake.recvCh <- &pb.BridgeMsg{Kind: &pb.BridgeMsg_Register{Register: &pb.Register{
		BridgePid: 1, ServerPid: 2, WorkspaceId: "ws-slow",
	}}}

	// Interval is 20ms; demand several pushes well inside a 1s window.
	if !waitFor(t, time.Second, func() bool { return fake.sentSnapshotCount() >= 5 }) {
		t.Fatalf("pushes starved by slow read: got %d snapshots", fake.sentSnapshotCount())
	}
	cancel()
	<-errCh
}

// TestRefreshSnapshotKeepsLastGoodOnFailure: a failed refresh must not wipe the
// cache (which would send readers back to a synchronous DB read).
func TestRefreshSnapshotKeepsLastGoodOnFailure(t *testing.T) {
	st := newSharedState()
	st.setCachedSnapshot(&aggregate.Tree{Dirs: []*aggregate.Directory{{Path: "/p"}}})
	st.refreshSnapshot(context.Background()) // no ReadService -> snapshot() == nil
	if st.cachedSnapshot() == nil {
		t.Fatal("failed refresh discarded the last good cached snapshot")
	}
}
