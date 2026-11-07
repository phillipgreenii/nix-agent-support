package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/metrics"
	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
)

// collectGauges computes the point-in-time metrics from the slot store.
func (a *app) collectGauges(root string, sts []pool.SlotStatus) metrics.Gauges {
	g := metrics.Gauges{SlotsByState: map[string]int{}, Waiters: metrics.CountWaiters(root)}
	now := a.clock.Now()
	for _, s := range sts {
		r := s.Record
		g.SlotsByState[string(r.State)]++
		if r.State == pool.Quarantined {
			g.Quarantined++
		}
		if s.Leak {
			g.Leaked++
		}
		if (r.State == pool.Leased || r.State == pool.Resetting) && !r.Acquired.IsZero() {
			g.LeaseAgeMax = max(g.LeaseAgeMax, now.Sub(r.Acquired).Seconds())
		}
	}
	// The root may not exist yet on a brand-new pool: probe its nearest parent.
	probe := root
	for {
		if _, err := os.Stat(probe); err == nil || filepath.Dir(probe) == probe {
			break
		}
		probe = filepath.Dir(probe)
	}
	if free, err := a.freeBytes(probe); err == nil {
		g.FreeBytes = &free
	} else {
		fmt.Fprintf(a.stderr, "pg-checkout-pool: metrics: free bytes: %v\n", err)
	}
	if n, err := a.daemons(filepath.Join(root, "slots")); err == nil {
		g.Daemons = &n
	} else {
		fmt.Fprintf(a.stderr, "pg-checkout-pool: metrics: fsmonitor daemons: %v\n", err)
	}
	return g
}

// metrics prints the Prometheus text exposition, or with --out writes it
// atomically to FILE (a node_exporter textfile collector reads it).
func (a *app) metrics(args []string) int {
	fs, root := a.newFlags("metrics")
	out := fs.String("out", "", "write the exposition to this file atomically instead of stdout")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	sts, err := pool.NewStore(*root, a.clock).Status()
	if err != nil {
		return a.fail(err)
	}
	j, err := metrics.Load(*root)
	if err != nil {
		fmt.Fprintf(a.stderr, "pg-checkout-pool: metrics: %v\n", err)
	}
	text := metrics.Render(a.collectGauges(*root, sts), j)
	if *out == "" {
		fmt.Fprint(a.stdout, text)
		return 0
	}
	tmp, err := os.CreateTemp(filepath.Dir(*out), filepath.Base(*out)+".tmp-*")
	if err != nil {
		return a.fail(err)
	}
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return a.fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return a.fail(err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return a.fail(err)
	}
	if err := os.Rename(tmp.Name(), *out); err != nil {
		_ = os.Remove(tmp.Name())
		return a.fail(err)
	}
	return 0
}

var _ = time.Second
