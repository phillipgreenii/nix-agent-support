package serve

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func counterOf(d *Daemon, name string) float64 {
	v, _ := d.obs.Reg.Counter(name, nil)
	return v
}

// sweepDoneLines returns the sweep_done log lines written so far.
func sweepDoneLines(log string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "sweep_done") {
			out = append(out, l)
		}
	}
	return out
}

// Over a real tree and store: skipped and ingested add up to walked, the ratio
// gauge follows the last sweep, a vanished file costs one lstat fallback, a
// forced sweep counts as a verification, and every sweep logs one sweep_done
// line carrying all six fields.
func TestSweepMetricsAndSweepDoneLine(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	var paths []string
	for _, n := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		paths = append(paths, e.transcript(n, "{\"type\":\"user\"}\n", old))
	}
	d := e.open(t)
	defer func() { _ = d.Close() }()
	ratio := func() float64 { v, _ := d.obs.Reg.Gauge(obs.MSweepSkippedRatio, nil); return v }

	rescan(t, d) // first sweep: the seen table was empty, so a full verification
	rescan(t, d) // catches every file up
	rescan(t, d) // skips all three
	if got, ing, walked := sweepCount(d, obs.SweepSkipped), sweepCount(d, obs.SweepIngested), sweepCount(d, obs.SweepWalked); got < 3 || got+ing != walked {
		t.Fatalf("skipped=%v ingested=%v walked=%v; want skipped at least 3 and skipped+ingested=walked", got, ing, walked)
	}
	if ratio() != 1 {
		t.Errorf("sweep_skipped_ratio = %v after an all-skipped sweep, want 1", ratio())
	}
	if got := counterOf(d, obs.MSweepVerifyForced); got != 1 {
		t.Errorf("sweep_verify_forced_total = %v, want 1 (the first sweep)", got)
	}
	if got := counterOf(d, obs.MSweepLstatFallbacks); got != 0 {
		t.Errorf("sweep_lstat_fallbacks_total = %v with nothing missing, want 0", got)
	}

	if err := os.Remove(paths[2]); err != nil {
		t.Fatal(err)
	}
	rescan(t, d)
	if got := counterOf(d, obs.MSweepLstatFallbacks); got != 1 {
		t.Errorf("sweep_lstat_fallbacks_total = %v after one file vanished, want 1", got)
	}
	if ratio() != 1 {
		t.Errorf("sweep_skipped_ratio = %v, want 1 (both walked files skipped)", ratio())
	}

	d.forcePending.Store(true)
	rescan(t, d)
	if got := counterOf(d, obs.MSweepVerifyForced); got != 2 {
		t.Errorf("sweep_verify_forced_total = %v after a forced sweep, want 2", got)
	}
	if ratio() != 0 {
		t.Errorf("sweep_skipped_ratio = %v after a forced sweep, want 0", ratio())
	}

	lines := sweepDoneLines(e.log.String())
	if len(lines) != 5 {
		t.Fatalf("%d sweep_done lines for 5 sweeps: %v", len(lines), lines)
	}
	for _, f := range []string{"walked", "skipped", "ingested", "missing", "duration_ms", "verify"} {
		for _, l := range lines {
			if !strings.Contains(l, f) {
				t.Errorf("sweep_done line lacks %q: %s", f, l)
			}
		}
	}
	if !strings.Contains(lines[0], `"verify":true`) || !strings.Contains(lines[2], `"verify":false`) || !strings.Contains(lines[4], `"verify":true`) {
		t.Errorf("verify flags wrong: first %s third %s last %s", lines[0], lines[2], lines[4])
	}
}
