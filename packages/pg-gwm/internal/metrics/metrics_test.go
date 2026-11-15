package metrics

import (
	"os"
	"strings"
	"sync"
	"testing"
)

func TestHistObserveBucketsAndOverflow(t *testing.T) {
	var h Hist
	for _, v := range []float64{0.5, 1, 1.5, 400} {
		h.Observe(ResetBuckets, v)
	}
	if h.Counts[0] != 2 || h.Counts[1] != 1 || h.Overflow != 1 || h.Count != 4 || h.Sum != 403 {
		t.Fatalf("hist = %+v", h)
	}
}

func TestRenderExposition(t *testing.T) {
	var j Journal
	j.Reset.Observe(ResetBuckets, 9)
	j.Reset.Observe(ResetBuckets, 400)
	j.ResetFailures, j.Rescued = 2, 3
	free := uint64(1 << 30)
	out := Render(Gauges{
		SlotsByState: map[string]int{"leased": 2, "warm-free": 1},
		LeaseAgeMax:  125.5, Leaked: 1, Quarantined: 2, Waiters: 3, FreeBytes: &free,
	}, j)
	for _, want := range []string{
		`pg_gwm_slots{state="leased"} 2`,
		`pg_gwm_slots{state="quarantined"} 0`, // a state with no slots still exports 0
		`pg_gwm_slots{state="retired"} 0`,
		"pg_gwm_lease_age_seconds 125.5",
		"pg_gwm_leaked_slots 1",
		"pg_gwm_quarantined_slots 2",
		"pg_gwm_waiters 3",
		"pg_gwm_free_bytes 1073741824",
		`pg_gwm_reset_seconds_bucket{le="5"} 0`,
		`pg_gwm_reset_seconds_bucket{le="10"} 1`,
		`pg_gwm_reset_seconds_bucket{le="300"} 1`, // cumulative, the 400 s one is only in +Inf
		`pg_gwm_reset_seconds_bucket{le="+Inf"} 2`,
		"pg_gwm_reset_seconds_sum 409",
		"pg_gwm_reset_seconds_count 2",
		"pg_gwm_acquire_wait_seconds_count 0",
		"# TYPE pg_gwm_reset_failures_total counter",
		"pg_gwm_reset_failures_total 2",
		"pg_gwm_rescued_total 3",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing line %q in:\n%s", want, out)
		}
	}
}

func TestRenderOmitsFailedProbes(t *testing.T) {
	out := Render(Gauges{}, Journal{})
	for _, name := range []string{"free_bytes"} {
		if strings.Contains(out, Prefix+name) {
			t.Errorf("%s exported although its probe failed", name)
		}
	}
}

func TestUpdateAccumulatesAcrossConcurrentWriters(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Update(root, func(j *Journal) { j.Rescued++ }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	j, err := Load(root)
	if err != nil || j.Rescued != 20 {
		t.Fatalf("load: %v %+v", err, j)
	}
}

func TestLoadMissingAndCorruptJournal(t *testing.T) {
	root := t.TempDir()
	if j, err := Load(root); err != nil || j.Schema != 1 || j.Rescued != 0 {
		t.Fatalf("missing: %v %+v", err, j)
	}
	if err := os.WriteFile(root+"/metrics.json", []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("corrupt journal must be reported")
	}
	// An update replaces a corrupt journal instead of wedging the pool.
	if err := Update(root, func(j *Journal) { j.Rescued = 1 }); err != nil {
		t.Fatal(err)
	}
	if j, err := Load(root); err != nil || j.Rescued != 1 {
		t.Fatalf("after update: %v %+v", err, j)
	}
}

func TestWaitersIgnoreDeadProcesses(t *testing.T) {
	root := t.TempDir()
	done, err := Waiting(root, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	// A pid far above any real one is dead.
	dead, err := Waiting(root, 2147483000)
	if err != nil {
		t.Fatal(err)
	}
	defer dead()
	if n := CountWaiters(root); n != 1 {
		t.Fatalf("waiters = %d, want 1 (the dead marker must not count)", n)
	}
	if _, err := os.Stat(root + "/waiters/2147483000"); !os.IsNotExist(err) {
		t.Errorf("dead marker not removed: %v", err)
	}
	done()
	if n := CountWaiters(root); n != 0 {
		t.Fatalf("waiters after done = %d", n)
	}
}
