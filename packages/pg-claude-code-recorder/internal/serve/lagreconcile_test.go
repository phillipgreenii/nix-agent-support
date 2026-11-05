package serve

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

func openCounting(t *testing.T, name string) (*Daemon, *env, *countdriver.Counter) {
	t.Helper()
	c := countdriver.Register(name)
	e := newEnv(t, false)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, StoreDriver: name})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	t.Cleanup(func() { _ = d.Close() })
	return d, e, c
}

func gaugeVal(d *Daemon, name string) float64 {
	v, _ := d.obs.Reg.Gauge(name, nil)
	return v
}

// A refresh_gauges pass reads only maintained values: it issues no AllFiles,
// AllGenerations or CopyCommits statement, and the four gauges equal what a
// whole-table rebuild computes on the same state.
func TestRefreshGaugesIssuesNoWholeTableReads(t *testing.T) {
	d, e, c := openCounting(t, "lag-gauges-counting")
	seedFiles(t, d, filepath.Join(e.claude, "projects", "example-proj"), 20)
	reseedMaintained(t, d)
	d.startLagRebuild() // the one start-up rebuild, outside the measured call
	c.Reset()
	d.refreshGauges()
	for _, q := range []string{"FROM files ORDER BY id", "FROM generations ORDER BY file_id, gen", "FROM commits WHERE file_id"} {
		if n := c.Matching(q); n != 0 {
			t.Fatalf("refreshGauges issued %d statements matching %q", n, q)
		}
	}
	r, ok := d.prs.RebuildLag()
	if !ok {
		t.Fatal("rebuild failed")
	}
	if gaugeVal(d, "parse_lag_bytes") != float64(r.LagBytes) ||
		gaugeVal(d, "parse_progress") != r.Progress || gaugeVal(d, "unknown_kinds") != float64(r.Kinds) {
		t.Fatalf("gauges differ from rebuildLag %+v", r)
	}
}

func lagRebuildCount(d *Daemon, result string) float64 {
	v, _ := d.obs.Reg.Counter(obs.MLagRebuild, map[string]string{"result": result})
	return v
}

// The reconcile counts a match when the maintained state agrees, and a drift
// (with a lag_drift WARN) when an offset moved behind the hook's back.
func TestReconcileLagMatchThenDrift(t *testing.T) {
	d, e, _ := openCounting(t, "lag-reconcile-counting")
	seedFiles(t, d, filepath.Join(e.claude, "projects", "example-proj"), 3)
	reseedMaintained(t, d)
	d.reconcileLag()
	if lagRebuildCount(d, "match") != 1 || lagRebuildCount(d, "drift") != 0 {
		t.Fatalf("match %v drift %v", lagRebuildCount(d, "match"), lagRebuildCount(d, "drift"))
	}
	err := d.st.WithWrite("", func(tx *sql.Tx) error { // no hook fires for a raw UPDATE
		_, err := tx.Exec(`UPDATE files SET state='deleted'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	d.reconcileLag()
	if lagRebuildCount(d, "drift") != 1 {
		t.Fatalf("drift %v", lagRebuildCount(d, "drift"))
	}
	if !strings.Contains(e.log.String(), "lag_drift") {
		t.Fatal("no lag_drift WARN logged")
	}
}

// reseedMaintained reloads the maintained state after seedFiles wrote rows
// with raw SQL (which fires no hook).
func reseedMaintained(t *testing.T, d *Daemon) {
	t.Helper()
	var err error
	if d.lag, err = d.prs.StartLag(); err != nil {
		t.Fatal(err)
	}
	if d.unknown, err = d.prs.StartUnknown(); err != nil {
		t.Fatal(err)
	}
}
