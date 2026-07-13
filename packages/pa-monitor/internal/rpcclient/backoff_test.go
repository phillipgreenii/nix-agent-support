package rpcclient

import (
	"testing"
	"time"
)

// TestScheduleBackoffCapsLowForFastRecovery: the daemon is a local unix socket,
// so a long reconnect backoff just makes the TUI/bridge sit on stale-or-empty
// state long after the daemon is reachable again. The cap MUST stay small so
// recovery is prompt, and repeated failures MUST NOT grow the backoff past it.
func TestScheduleBackoffCapsLowForFastRecovery(t *testing.T) {
	rp := &RemotePoller{backoff: 1 * time.Second}
	for i := 0; i < 25; i++ {
		rp.scheduleBackoff()
	}
	if rp.backoff > maxBackoff {
		t.Fatalf("backoff %s exceeded cap %s", rp.backoff, maxBackoff)
	}
	if maxBackoff > 5*time.Second {
		t.Fatalf("backoff cap %s is too large for a local socket; recovery would lag", maxBackoff)
	}
}
