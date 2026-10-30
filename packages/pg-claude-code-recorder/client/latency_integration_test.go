//go:build integration

package client_test

import (
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// latencyBound is what the test enforces on a quiet machine. The design's
// target is about one second from a write in the Claude directory to the record
// appearing in the feed; it is a measured report, not a guarantee, so the bound
// leaves headroom and the measurements are logged.
const latencyBound = 3 * time.Second

func TestWriteToFeedLatencyIsMeasuredAgainstABound(t *testing.T) {
	e := clienttest.Start(t, clienttest.Options{Events: true})
	e.Write(rel, cL1)
	e.Settle()
	c := e.Dial(ctx())
	var got []time.Duration
	for i := 0; i < 5; i++ {
		head := mustFeedHead(t, c)
		answered := make(chan time.Time, 1)
		go func() {
			a, err := c.FeedPage(ctx(), &client.Filter{Field: "type", Eq: []byte(`"assistant"`)}, head, nil, 0, 20000)
			if err != nil || len(a.Entries) != 1 {
				t.Errorf("feed %+v %v", a, err)
			}
			answered <- time.Now()
		}()
		waitUntil(t, func() bool { return gauge(e, "feed_waiters") == 1 })
		wrote := time.Now()
		e.Append(rel, cL2)
		select {
		case at := <-answered:
			got = append(got, at.Sub(wrote))
		case <-time.After(25 * time.Second):
			t.Fatal("no feed answer")
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	t.Logf("write-to-feed latency over %d writes: min %v median %v max %v (bound %v)", len(got), got[0], got[len(got)/2], got[len(got)-1], latencyBound)
	if got[len(got)-1] > latencyBound {
		t.Fatalf("slowest write-to-feed latency %v exceeds the bound %v", got[len(got)-1], latencyBound)
	}
}
