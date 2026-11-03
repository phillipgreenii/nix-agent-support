package serve

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/exporter"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

var sink [][]byte

func TestCollectRuntimeMetricsIssuesNoStoreQuery(t *testing.T) {
	c := countdriver.Register("runtimemetrics-counting")
	dir := t.TempDir()
	s, err := store.Open(dir, store.Options{DriverName: "runtimemetrics-counting"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	c.Reset()
	collectRuntimeMetrics(obs.NewRegistry(), filepath.Join(dir, store.DBName+"-wal"))
	if c.Queries() != 0 || c.RowsRead() != 0 {
		t.Fatalf("collector queried the store: %d queries, %d rows", c.Queries(), c.RowsRead())
	}
}

func TestCollectRuntimeMetricsNamesTypesAndMonotony(t *testing.T) {
	reg := obs.NewRegistry()
	wal := filepath.Join(t.TempDir(), "index.db-wal")
	collectRuntimeMetrics(reg, wal)
	collectRuntimeMetrics(reg, wal)
	var b bytes.Buffer
	exporter.Render(&b, reg)
	out := b.String()
	for name, typ := range map[string]string{
		obs.MProcessCPUSeconds: "counter", obs.MGoGoroutines: "gauge", obs.MGoHeapAlloc: "gauge",
		obs.MGoAllocTotal: "counter", obs.MGoGCPauseSeconds: "counter", obs.MWALBytes: "gauge",
	} {
		if !strings.Contains(out, "# TYPE "+exporter.Prefix+name+" "+typ+"\n") {
			t.Errorf("missing TYPE %s %s in:\n%s", name, typ, out)
		}
	}
	if g, _ := reg.Gauge(obs.MGoGoroutines, nil); g < 1 {
		t.Errorf("goroutines %v", g)
	}
	if g, _ := reg.Gauge(obs.MGoHeapAlloc, nil); g <= 0 {
		t.Errorf("heap alloc %v", g)
	}
	if g, ok := reg.Gauge(obs.MWALBytes, nil); !ok || g != 0 {
		t.Errorf("absent wal: %v %v", g, ok)
	}

	before := map[string]float64{}
	for _, n := range []string{obs.MProcessCPUSeconds, obs.MGoAllocTotal, obs.MGoGCPauseSeconds} {
		before[n], _ = reg.Counter(n, nil)
	}
	for i := 0; i < 200; i++ {
		sink = append(sink, make([]byte, 1<<20))
	}
	sink = nil
	burn := 0
	for i := 0; i < 50_000_000; i++ {
		burn += i
	}
	_ = burn
	collectRuntimeMetrics(reg, wal)
	for n, was := range before {
		now, _ := reg.Counter(n, nil)
		if now < was {
			t.Errorf("%s went backwards: %v -> %v", n, was, now)
		}
	}
	if now, _ := reg.Counter(obs.MGoAllocTotal, nil); now <= before[obs.MGoAllocTotal] {
		t.Errorf("alloc total did not grow after allocating: %v", now)
	}
}

func TestCollectRuntimeMetricsWALBytes(t *testing.T) {
	reg := obs.NewRegistry()
	wal := filepath.Join(t.TempDir(), "index.db-wal")
	if err := os.WriteFile(wal, make([]byte, 4321), 0o600); err != nil {
		t.Fatal(err)
	}
	collectRuntimeMetrics(reg, wal)
	if g, _ := reg.Gauge(obs.MWALBytes, nil); g != 4321 {
		t.Fatalf("wal_bytes %v", g)
	}
	_ = os.Remove(wal)
	collectRuntimeMetrics(reg, wal)
	if g, _ := reg.Gauge(obs.MWALBytes, nil); g != 0 {
		t.Fatalf("wal_bytes after removal %v", g)
	}
}

func TestRefreshGaugesRunsRuntimeCollector(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	d.refreshGauges()
	if _, ok := d.obs.Reg.Gauge(obs.MGoGoroutines, nil); !ok {
		t.Fatal("refreshGauges did not run the collector")
	}
}

// A scrape must answer without the store or the state directory walk: the
// Daemon here has no parser and no store, so any attempt to reach them panics,
// and the scrape still answers well inside a second.
func TestScrapeRefreshNeverReachesTheStoreOrTheDirectoryWalk(t *testing.T) {
	d := &Daemon{obs: obs.New(nil), cfg: config.Config{StateDir: t.TempDir()}}
	h := exporter.Handler(d.obs.Reg, d.scrapeRefresh)
	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "process_cpu_seconds") {
		t.Fatalf("scrape: %d %q", rec.Code, rec.Body.String())
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("scrape took %v", el)
	}
}
