package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// flushChildEnv makes this test binary act as the ccpool binary: when set, the
// package init runs the real run() (exactly what main() does) and exits, so the
// test can drive a genuine `ccpool close` process without `go build` (which is
// unavailable in the hermetic nix check sandbox).
const flushChildEnv = "CCPOOL_FLUSH_TEST_CHILD"

func init() {
	if os.Getenv(flushChildEnv) == "1" {
		os.Exit(run())
	}
}

// fakeCollector is a local OTLP/gRPC metrics sink recording the metric names it
// receives.
type fakeCollector struct {
	colmetrics.UnimplementedMetricsServiceServer
	mu    sync.Mutex
	names map[string]bool
}

func (c *fakeCollector) Export(_ context.Context, req *colmetrics.ExportMetricsServiceRequest) (*colmetrics.ExportMetricsServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				c.names[m.GetName()] = true
			}
		}
	}
	return &colmetrics.ExportMetricsServiceResponse{}, nil
}

func (c *fakeCollector) saw(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.names[name]
}

// TestClose_flushesRunLifecycleMetricsThroughRun proves the close path exits
// through run() (whose deferred telemetry shutdown flushes the periodic metric
// reader): a child process running run() closes a seeded session with an OTLP endpoint set,
// and the collector receives the run-lifecycle instruments. os.Exit appears
// only in main(), after run() has returned, so no bypass exists on this path.
// It also shows a ccpool close subprocess exports to the OTLP endpoint in its
// environment (the environment is inherited like any exec'd child's).
func TestClose_flushesRunLifecycleMetricsThroughRun(t *testing.T) {
	col := &fakeCollector{names: map[string]bool{}}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	srv := grpc.NewServer()
	colmetrics.RegisterMetricsServiceServer(srv, col)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	base, err := shortTempDir("ccpool-flush")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	env := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"CCPOOL_POOL=",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://"+lis.Addr().String(),
		"OTEL_EXPORTER_OTLP_PROTOCOL=grpc",
		flushChildEnv+"=1",
	)
	// Resolve the DB path the binary will use, under the same environment.
	for _, kv := range env[len(os.Environ()):] {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath, &clock.Fake{T: time.Unix(1000, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(context.Background(), store.Session{ExternalID: "flush", ClaudeSessionID: "csid-f", State: store.Idle}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenRun(context.Background(), "flush"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "close", "flush")
	cmd.Env = env
	cmd.Dir = base
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("close: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (!col.saw("ccpool_sessions_closed_total") || !col.saw("ccpool_session_duration_seconds")) {
		time.Sleep(50 * time.Millisecond)
	}
	for _, name := range []string{"ccpool_sessions_closed_total", "ccpool_session_duration_seconds"} {
		if !col.saw(name) {
			t.Errorf("collector never received %s (close did not flush through run())", name)
		}
	}
}

// The SessionEnd hook process has no OTLP environment, so it only records the
// end; the run is left pending for the next reaper sweep to emit (that sweep
// is covered in internal/session: TestRunMetrics_sweepEmitsHookEndedRunOnceKeptRow).
func TestHookEnd_leavesRunPendingEmissionForTheSweep(t *testing.T) {
	st := endFixture(t)
	if err := handleHook("end", strings.NewReader(endOther), st, ""); err != nil {
		t.Fatal(err)
	}
	pend, err := st.RunsPendingEmission(context.Background())
	if err != nil || len(pend) != 1 || pend[0].ExternalID != "ext" ||
		pend[0].Run.EndSource != store.RunEndHook || pend[0].Run.EndedAt != 2000 {
		t.Fatalf("pending = %+v err=%v, want the hook-ended run (ended_at 2000)", pend, err)
	}
}
