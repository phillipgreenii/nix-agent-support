package collect

import (
	"context"
	"testing"

	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

// toleranceCollector builds a Collector over one database whose up gauge needs
// downAfter consecutive failed passes before it reads 0.
func toleranceCollector(t *testing.T, downAfter int) (*Collector, *fakeAdapter) {
	t.Helper()
	clock := newClock()
	ad := baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	mp := NewMainPass(nil, 5)
	c := New(clock, dbs, []Pass{mp, ThroughputPass{}}, quietLogger(), WithDownAfter(downAfter))
	mp.Init(context.Background(), dbs)
	return c, ad
}

func upOf(t *testing.T, c *Collector) float64 {
	t.Helper()
	v, ok := valueOf(t, samplesByFamily(c)[metrics.FamExporterUp], "db", "alpha")
	if !ok {
		t.Fatal("beads_exporter_up missing for alpha")
	}
	return v
}

func timeoutCount(t *testing.T, c *Collector, pass string) float64 {
	t.Helper()
	v, _ := valueOf(t, samplesByFamily(c)[metrics.FamCollectErrors], "db", "alpha", "pass", pass, "reason", "timeout")
	return v
}

func TestUpToleratesOneFailedPassAndGoesToZeroOnTheSecondConsecutive(t *testing.T) {
	c, ad := toleranceCollector(t, 2)
	ctx := context.Background()

	c.RunPass(ctx, PassMain)
	if got := upOf(t, c); got != 1 {
		t.Fatalf("up after a good pass = %v, want 1", got)
	}

	ad.fail = map[string]error{"blocked": failure.New(failure.Timeout, "blocked", errf("slow"))}
	c.RunPass(ctx, PassMain)
	if got := upOf(t, c); got != 1 {
		t.Fatalf("up after one failed pass = %v, want 1 (tolerated)", got)
	}
	if got := timeoutCount(t, c, "main"); got != 1 {
		t.Fatalf("timeout counter after one failed pass = %v, want 1 (a single timeout stays visible)", got)
	}
	if n := len(samplesByFamily(c)[metrics.FamIssues]); n != 0 {
		t.Fatal("a tolerated failure must still drop the pass's domain series")
	}

	c.RunPass(ctx, PassMain)
	if got := upOf(t, c); got != 0 {
		t.Fatalf("up after two consecutive failed passes = %v, want 0", got)
	}
	if got := timeoutCount(t, c, "main"); got != 2 {
		t.Fatalf("timeout counter after two failed passes = %v, want 2", got)
	}

	c.RunPass(ctx, PassMain)
	if got := upOf(t, c); got != 0 {
		t.Fatalf("up after three consecutive failed passes = %v, want it to stay 0", got)
	}
}

func TestUpSuccessAfterOneFailureResetsTheConsecutiveCount(t *testing.T) {
	c, ad := toleranceCollector(t, 2)
	ctx := context.Background()
	fail := map[string]error{"blocked": failure.New(failure.Timeout, "blocked", errf("slow"))}

	c.RunPass(ctx, PassMain) // ok
	ad.fail = fail
	c.RunPass(ctx, PassMain) // failure 1: tolerated
	ad.fail = nil
	c.RunPass(ctx, PassMain) // success: resets the count
	if got := upOf(t, c); got != 1 {
		t.Fatalf("up after recovery = %v, want 1", got)
	}

	ad.fail = fail
	c.RunPass(ctx, PassMain) // failure 1 again, not failure 2
	if got := upOf(t, c); got != 1 {
		t.Fatalf("up after a failure that follows a success = %v, want 1: the success must reset the count", got)
	}
	if got := timeoutCount(t, c, "main"); got != 2 {
		t.Fatalf("timeout counter = %v, want 2 (the counter is monotonic across resets)", got)
	}
	c.RunPass(ctx, PassMain)
	if got := upOf(t, c); got != 0 {
		t.Fatalf("up after two consecutive failures following a reset = %v, want 0", got)
	}
}

func TestUpFailureCountIsPerDatabaseAndPerPass(t *testing.T) {
	clock := newClock()
	bad, good := baseAdapter(clock), baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: bad}, {Name: "beta", Adapter: good}}
	mp := NewMainPass(nil, 5)
	c := New(clock, dbs, []Pass{mp, ThroughputPass{}}, quietLogger(), WithDownAfter(2))
	mp.Init(context.Background(), dbs)
	ctx := context.Background()

	bad.fail = map[string]error{"blocked": failure.New(failure.Timeout, "blocked", errf("slow"))}
	c.RunPass(ctx, PassMain)
	// A failed throughput pass must not count toward the main pass's streak.
	bad.fail = map[string]error{"list-all": failure.New(failure.ParseError, "list", errf("junk"))}
	c.RunPass(ctx, PassThroughput)
	c.RunPass(ctx, PassThroughput)
	bad.fail = nil
	c.RunPass(ctx, PassMain)

	fams := samplesByFamily(c)
	for _, db := range []string{"alpha", "beta"} {
		if v, ok := valueOf(t, fams[metrics.FamExporterUp], "db", db); !ok || v != 1 {
			t.Fatalf("up{db=%s} = %v %v, want 1", db, v, ok)
		}
	}
}

func TestDefaultDownAfterIsOneSoAnySingleFailureIsDown(t *testing.T) {
	c, ad := toleranceCollector(t, 1)
	ad.fail = map[string]error{"blocked": failure.New(failure.Timeout, "blocked", errf("slow"))}
	c.RunPass(context.Background(), PassMain)
	if got := upOf(t, c); got != 0 {
		t.Fatalf("up with downAfter=1 after one failure = %v, want 0", got)
	}
}
