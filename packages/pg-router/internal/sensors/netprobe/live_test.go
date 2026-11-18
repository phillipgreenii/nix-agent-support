//go:build live

package netprobe

import (
	"context"
	"testing"
	"time"
)

// TestLiveNetwork probes the generic public example targets for real; it needs
// outbound HTTPS and never runs in the unit gate.
func TestLiveNetwork(t *testing.T) {
	results := (&Prober{Timeout: 5 * time.Second}).Probe(context.Background(), DefaultTargets)
	ok := 0
	for _, r := range results {
		if r.Reachable {
			ok++
		}
	}
	if ok == 0 {
		t.Fatalf("no public target reachable: %+v", results)
	}
}
