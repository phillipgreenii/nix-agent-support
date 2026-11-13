//go:build soak

package parser_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// The soak tier measures first-run parse throughput, the memory ceiling, WAL
// growth and the trigram index size over a generated tree. It is not a unit
// test (V-4): it runs only under the soak tag, through the
// pg-claude-code-recorder-soak check.
//
// Budgets live in testdata/soak-budgets.json. They are the baseline of the
// first run, and a run fails at 1.5 times that baseline. Set
// PG_CCREC_SOAK_BASELINE=1 to write the file from this run (first run only).
//
// Time budget: the parse budget is process CPU time (user + system, from
// getrusage RUSAGE_SELF, all threads, so parser goroutines, SQLite and GC
// workers are all counted), not wall clock. Decision: the parse is CPU-bound
// work, and wall clock also counts time spent waiting for a CPU. Under a host
// load of 60 to 80 the wall time measured 31 s against a 12.6 s baseline
// although the parse worked. CPU time ignores that scheduling delay yet still
// rises when the parser does more work per record (a real slowdown still
// fails). A quiet-host-only rule was rejected: it would make the check
// unreliable on the shared machine and need a load probe of its own. Wall
// seconds are still recorded and logged for information, but not enforced.
// The "cpu_seconds" baseline is measured under the same conditions as the run.

const (
	soakFiles    = 40
	soakLines    = 400
	budgetFactor = 1.5
)

type soakResult struct {
	Seconds      float64 `json:"seconds"`
	CPUSeconds   float64 `json:"cpu_seconds"`
	PeakHeap     int64   `json:"peak_heap_bytes"`
	WALBytes     int64   `json:"wal_bytes"`
	TrigramBytes int64   `json:"trigram_bytes"`
	InputBytes   int64   `json:"input_bytes"`
	Records      int64   `json:"records"`
}

func soakLine(f, i int) string {
	pad := strings.Repeat(fmt.Sprintf("example output line %d of file %d. ", i, f), 6)
	return fmt.Sprintf(`{"type":"assistant","uuid":"u-%d-%d","parentUuid":"u-%d-%d","sessionId":"example-session-%d","timestamp":"2026-10-29T20:%02d:%02dZ","message":{"id":"m-%d-%d","model":"example-model-a","role":"assistant","usage":{"input_tokens":%d,"output_tokens":%d},"content":[{"type":"text","text":"reply %d number %d with words to index"},{"type":"tool_use","id":"tu-%d-%d","name":"Bash","input":{"command":"echo %s"}}]}}`+"\n",
		f, i, f, i-1, f, (i/60)%60, i%60, f, i, i%97, i%53, f, i, f, i, pad)
}

// cpuSeconds returns the user plus system CPU time consumed so far by every
// thread of this process.
func cpuSeconds(t *testing.T) float64 {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	tv := func(v syscall.Timeval) float64 { return float64(v.Sec) + float64(v.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime)
}

func TestSoakFirstRunParse(t *testing.T) {
	k := testkit.New(t)
	var in int64
	for f := 0; f < soakFiles; f++ {
		var b strings.Builder
		for i := 0; i < soakLines; i++ {
			b.WriteString(soakLine(f, i))
		}
		in += int64(b.Len())
		k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), b.String())
	}
	var peak atomic.Int64
	var walMax atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if h := int64(ms.HeapInuse); h > peak.Load() {
				peak.Store(h)
			}
			if fi, err := os.Stat(filepath.Join(k.Dir, "index.db-wal")); err == nil && fi.Size() > walMax.Load() {
				walMax.Store(fi.Size())
			}
			select {
			case <-stop:
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	cpu0 := cpuSeconds(t)
	k.Parse()
	cpu := cpuSeconds(t) - cpu0
	secs := time.Since(start).Seconds()
	close(stop)
	<-done
	res := soakResult{Seconds: secs, CPUSeconds: cpu, PeakHeap: peak.Load(), WALBytes: walMax.Load(), InputBytes: in}
	res.Records = int64(k.Count(`SELECT COUNT(*) FROM records`))
	if res.Records != int64(soakFiles*soakLines*3) {
		t.Fatalf("records %d, want %d", res.Records, soakFiles*soakLines*3)
	}
	res.TrigramBytes = trigramBytes(t, k)
	t.Logf("soak: parsed %d bytes into %d records in %.2fs wall, %.2fs CPU (%.1f MB/s wall); peak heap %d; WAL max %d; trigram index %d bytes",
		in, res.Records, secs, cpu, float64(in)/1e6/secs, res.PeakHeap, res.WALBytes, res.TrigramBytes)

	path := filepath.Join("testdata", "soak-budgets.json")
	if os.Getenv("PG_CCREC_SOAK_BASELINE") == "1" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("baseline written to %s", path)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no committed budgets (%v): run once with PG_CCREC_SOAK_BASELINE=1 and commit %s", err, path)
	}
	var base soakResult
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	check := func(name string, got, want float64) {
		if want > 0 && got > want*budgetFactor {
			t.Errorf("%s %.4g exceeds 1.5 x baseline %.4g", name, got, want)
		}
	}
	if base.CPUSeconds <= 0 {
		t.Fatalf("baseline has no cpu_seconds: re-record with PG_CCREC_SOAK_BASELINE=1")
	}
	check("cpu seconds", res.CPUSeconds, base.CPUSeconds)
	check("peak heap bytes", float64(res.PeakHeap), float64(base.PeakHeap))
	check("WAL bytes", float64(res.WALBytes), float64(base.WALBytes))
	check("trigram index bytes", float64(res.TrigramBytes), float64(base.TrigramBytes))
}

func trigramBytes(t *testing.T, k *testkit.Kit) int64 {
	t.Helper()
	var n int64
	if err := k.Store.Read().QueryRowContext(context.Background(), `SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name LIKE 'fts_tri%'`).Scan(&n); err == nil && n > 0 {
		return n
	}
	// dbstat is not compiled in: fall back to the whole database size.
	fi, err := os.Stat(filepath.Join(k.Dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
