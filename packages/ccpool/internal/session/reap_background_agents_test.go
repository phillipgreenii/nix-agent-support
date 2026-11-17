package session

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// TestReap_backgroundAgentsGaugeCountsOnlyWaitingLiveSessions: a live idle row
// whose transcript has a pending background agent (the drain orchestrator
// waiting on its subagent) is counted by ccpool_session_background_agents, and
// an idle row without one is not. Without a probe the count is 0.
func TestReap_backgroundAgentsGaugeCountsOnlyWaitingLiveSessions(t *testing.T) {
	orig := recordSessionBackgroundAgents
	t.Cleanup(func() { recordSessionBackgroundAgents = orig })
	var got []int64
	recordSessionBackgroundAgents = func(n int64, _ []attribute.KeyValue) { got = append(got, n) }

	now := time.Unix(10_000, 0)
	s, _, _ := reapService(
		t, now, "/pools/alpha",
		liveRow{id: "waiting", state: store.Idle, ageS: 60, path: "/t/waiting.jsonl"},
		liveRow{id: "empty", state: store.Idle, ageS: 60, path: "/t/empty.jsonl"},
	)
	ctx := context.Background()

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("no probe: gauge = %v, want one 0", got)
	}

	got = nil
	s.d.BackgroundAgents = func(p string) bool { return p == "/t/waiting.jsonl" }
	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("with probe: gauge = %v, want one 1 (only the waiting session)", got)
	}
}
