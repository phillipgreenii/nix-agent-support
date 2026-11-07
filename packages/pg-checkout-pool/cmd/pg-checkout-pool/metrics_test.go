package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/metrics"
	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
	"github.com/phillipgreenii/pg-checkout-pool/internal/provision"
)

// metricsOut runs the metrics command against the environment and returns the
// exposition. The daemon probe is faked: the real one reads the process table.
func (e *env) metricsOut(daemons int, extra ...string) string {
	e.t.Helper()
	var out, errb bytes.Buffer
	a := newApp(&out, &errb, e.clk)
	a.freeBytes = func(string) (uint64, error) { return e.free, nil }
	a.daemons = func(string) (int, error) { return daemons, nil }
	if code := a.run(append([]string{"metrics", "--root", e.root}, extra...)); code != 0 {
		e.t.Fatalf("metrics exit %d: %s", code, errb.String())
	}
	return out.String()
}

func hasLine(t *testing.T, text, line string) {
	t.Helper()
	if !strings.Contains(text, line+"\n") {
		t.Errorf("missing %q in:\n%s", line, text)
	}
}

// slowReset makes every reset take d on the environment's clock.
func (e *env) slowReset(d time.Duration) {
	e.wrap = func(p provision.Provisioner) provision.Provisioner {
		return wrapped{Provisioner: p, reset: nil, resetHook: func() { e.clk.Advance(d) }}
	}
}

func TestMetricsCountResetsAndGauges(t *testing.T) {
	e := newEnv(t)
	e.slowReset(9 * time.Second)
	l := e.mustLease("s1", "")
	code, _ := e.release(l.LeaseID)
	if code != 0 {
		t.Fatalf("release %d", code)
	}
	l2 := e.mustLease("s2", "")
	_ = l2
	text := e.metricsOut(2)
	// Three resets of 9 s each: s1 lease, s1 release, s2 lease.
	hasLine(t, text, "pg_checkout_pool_reset_seconds_count 3")
	hasLine(t, text, `pg_checkout_pool_reset_seconds_bucket{le="10"} 3`)
	hasLine(t, text, `pg_checkout_pool_reset_seconds_bucket{le="5"} 0`)
	hasLine(t, text, "pg_checkout_pool_reset_seconds_sum 27")
	hasLine(t, text, `pg_checkout_pool_slots{state="leased"} 1`)
	hasLine(t, text, `pg_checkout_pool_slots{state="warm-free"} 0`)
	hasLine(t, text, "pg_checkout_pool_acquire_wait_seconds_count 2")
	hasLine(t, text, "pg_checkout_pool_fsmonitor_daemons 2")
	hasLine(t, text, "pg_checkout_pool_free_bytes 1099511627776")
	hasLine(t, text, "pg_checkout_pool_waiters 0")
	hasLine(t, text, "pg_checkout_pool_reset_failures_total 0")
	hasLine(t, text, "pg_checkout_pool_lease_age_seconds 9")
}

func TestMetricsLeakQuarantineAndLeaseAge(t *testing.T) {
	e := newEnv(t)
	e.mustLease("old", "", "--ttl", "10m")
	e.clk.Advance(time.Hour) // expired, nobody holds the lock: a leak
	text := e.metricsOut(1)
	hasLine(t, text, "pg_checkout_pool_leaked_slots 1")
	hasLine(t, text, "pg_checkout_pool_lease_age_seconds 3600")
	hasLine(t, text, "pg_checkout_pool_quarantined_slots 0")

	k := e.mustLease("keeper", "", "--max-slots", "2")
	if code, _ := e.release(k.LeaseID, "--disposition", "quarantine"); code != 0 {
		t.Fatal("quarantine release failed")
	}
	text = e.metricsOut(2)
	hasLine(t, text, "pg_checkout_pool_quarantined_slots 1")
	hasLine(t, text, `pg_checkout_pool_slots{state="quarantined"} 1`)
}

func TestMetricsCountResetFailureAndRescue(t *testing.T) {
	e := newEnv(t)
	first := e.mustLease("s1", "")
	writeFile(t, filepath.Join(first.Path, "a.txt"), "uncommitted\n")
	if code, m := e.release(first.LeaseID); code != 0 || m["rescueRef"] == nil {
		t.Fatalf("release %d %v", code, m)
	}
	e.wrap = func(p provision.Provisioner) provision.Provisioner {
		return wrapped{Provisioner: p, reset: func() (string, error) { return "", errors.New("transient") }}
	}
	if _, code, _ := e.lease("s2", "", "--max-slots", "1"); code == 0 {
		t.Fatal("lease must fail when every reset fails")
	}
	text := e.metricsOut(0)
	hasLine(t, text, "pg_checkout_pool_rescued_total 1")
	// One slot, one failed attempt (two tries inside it) counts once.
	hasLine(t, text, "pg_checkout_pool_reset_failures_total 1")
}

func TestMetricsWaitersAndAcquireWait(t *testing.T) {
	e := newEnv(t)
	holder := e.mustLease("holder", "", "--max-slots", "1")
	var during string
	e.onSlp = func() {
		if during == "" {
			during = e.metricsOut(1)
		}
		if e.clk.Now().Sub(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)) > 20*time.Second {
			e.release(holder.LeaseID)
		}
	}
	// Wait up to 2m for the single slot; the holder releases while we wait.
	l, code, errs := e.lease("waiter", "", "--max-slots", "1", "--wait", "2m")
	if code != 0 {
		t.Fatalf("waiter exit %d: %s", code, errs)
	}
	_ = l
	// The waiter marker is in place while it sleeps (this process' pid) and gone after.
	hasLine(t, during, "pg_checkout_pool_waiters 1")
	hasLine(t, e.metricsOut(0), "pg_checkout_pool_waiters 0")
	text := e.metricsOut(0)
	if strings.Contains(text, "pg_checkout_pool_acquire_wait_seconds_count 1\n") {
		t.Errorf("both leases must be observed:\n%s", text)
	}
	hasLine(t, text, `pg_checkout_pool_acquire_wait_seconds_bucket{le="0.1"} 1`)
	hasLine(t, text, `pg_checkout_pool_acquire_wait_seconds_bucket{le="60"} 2`)
}

func TestMetricsExhaustedWaitIsObserved(t *testing.T) {
	e := newEnv(t)
	e.mustLease("holder", "", "--max-slots", "1")
	if _, code, _ := e.lease("late", "", "--max-slots", "1", "--wait", "3s"); code != exitExhausted {
		t.Fatalf("exit %d", code)
	}
	hasLine(t, e.metricsOut(0), "pg_checkout_pool_acquire_wait_seconds_count 2")
}

func TestMetricsOutWritesAtomically(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "pg_checkout_pool.prom")
	if err := os.WriteFile(target, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := e.metricsOut(0, "--out", target); got != "" {
		t.Errorf("--out must not print: %q", got)
	}
	b, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(b), "pg_checkout_pool_slots{state=\"leased\"} 0\n") {
		t.Fatalf("file: %v %q", err, b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("temp file left behind: %v", ents)
	}
}

func TestMetricsOnEmptyPoolAndProbeFailure(t *testing.T) {
	var out, errb bytes.Buffer
	a := newApp(&out, &errb, fixedClock{time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)})
	a.freeBytes = func(string) (uint64, error) { return 0, errors.New("no statfs") }
	a.daemons = func(string) (int, error) { return 0, errors.New("no lsof") }
	root := filepath.Join(t.TempDir(), "not-yet", "pool")
	if code := a.run([]string{"metrics", "--root", root}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	text := out.String()
	hasLine(t, text, `pg_checkout_pool_slots{state="warm-free"} 0`)
	if strings.Contains(text, "free_bytes") || strings.Contains(text, "fsmonitor_daemons") {
		t.Errorf("failed probes must be omitted:\n%s", text)
	}
	if !strings.Contains(errb.String(), "no lsof") {
		t.Errorf("probe failure not reported: %q", errb.String())
	}
}

var (
	_ = pool.WarmFree
	_ = metrics.Prefix
)
