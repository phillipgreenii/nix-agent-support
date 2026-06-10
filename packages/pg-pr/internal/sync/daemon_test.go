package sync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
)

// makeDaemonEngine builds an Engine whose Sync is cheap: empty config means
// the loop iterates without touching bd or VCS.
func makeDaemonEngine(t *testing.T) *Engine {
	t.Helper()
	cfg := &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: t.TempDir(),
		// Empty Repos slice -> Sync iterates zero repos. The "list open
		// merge-request beads" call still goes to Beads though, so we use a
		// noopBeads that returns nothing.
	}
	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": &fakeVCS{}},
		Beads:    noopBeads{},
		StateDir: t.TempDir(),
		Now:      func() time.Time { return time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDaemon_ExitsOnContextCancel(t *testing.T) {
	e := makeDaemonEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval: 50 * time.Millisecond,
			LockDir:  t.TempDir(),
			Logger:   discardLogger(),
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Daemon: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Daemon did not exit within 2s after ctx cancel")
	}
}

func TestDaemon_LockHeldReturnsError(t *testing.T) {
	lockDir := t.TempDir()
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Acquire the lock from outside the daemon.
	lockPath := filepath.Join(lockDir, "daemon.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("pre-acquire lock: %v", err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	e := makeDaemonEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err = e.Daemon(ctx, DaemonOpts{
		Interval: 50 * time.Millisecond,
		LockDir:  lockDir,
		Logger:   discardLogger(),
	})
	if err == nil {
		t.Fatal("expected Daemon to fail when lock is held; got nil")
	}
}

func TestDaemon_SighupReloadsConfig(t *testing.T) {
	e := makeDaemonEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Snapshot pre-reload SelfLogin.
	if e.cfg().SelfLogin != "phillipg" {
		t.Fatalf("precondition: SelfLogin=%q", e.cfg().SelfLogin)
	}

	sighup := make(chan os.Signal, 1)
	reloaded := make(chan struct{}, 1)
	newCfg := &config.Config{
		Path:         "/tmp/reloaded.yaml",
		SelfLogin:    "reloaded-user",
		WorktreeRoot: t.TempDir(),
	}
	var reloadCount atomic.Int32
	reload := func(context.Context) (*config.Config, error) {
		reloadCount.Add(1)
		select {
		case reloaded <- struct{}{}:
		default:
		}
		return newCfg, nil
	}

	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval:     1 * time.Hour, // never fire the time tick
			LockDir:      t.TempDir(),
			Sighup:       sighup,
			Logger:       discardLogger(),
			ReloadConfig: reload,
		})
	}()

	// Give the daemon a chance to enter its select loop after the first
	// iteration (which is synchronous before the select).
	time.Sleep(50 * time.Millisecond)
	sighup <- syscall.SIGHUP

	select {
	case <-reloaded:
	case <-time.After(2 * time.Second):
		t.Fatal("reload was not invoked after SIGHUP")
	}

	// Give the daemon time to assign the new config.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Daemon: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Daemon did not exit after cancel")
	}

	if got := e.cfg().SelfLogin; got != "reloaded-user" {
		t.Fatalf("post-SIGHUP SelfLogin: got %q want %q", got, "reloaded-user")
	}
	if got := reloadCount.Load(); got != 1 {
		t.Fatalf("reload invocations: got %d want 1", got)
	}
}

func TestDaemon_SighupReloadFailureKeepsPreviousConfig(t *testing.T) {
	e := makeDaemonEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sighup := make(chan os.Signal, 1)
	reloadAttempted := make(chan struct{}, 1)
	reload := func(context.Context) (*config.Config, error) {
		select {
		case reloadAttempted <- struct{}{}:
		default:
		}
		return nil, errors.New("yaml parse failed")
	}

	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval:     1 * time.Hour,
			LockDir:      t.TempDir(),
			Sighup:       sighup,
			Logger:       discardLogger(),
			ReloadConfig: reload,
		})
	}()

	time.Sleep(50 * time.Millisecond)
	sighup <- syscall.SIGHUP

	select {
	case <-reloadAttempted:
	case <-time.After(2 * time.Second):
		t.Fatal("reload was not attempted after SIGHUP")
	}

	// Config must NOT have been replaced.
	time.Sleep(50 * time.Millisecond)
	if got := e.cfg().SelfLogin; got != "phillipg" {
		t.Fatalf("SelfLogin changed despite reload failure: %q", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Daemon did not exit after cancel")
	}
}

func TestDaemon_DefaultInterval(t *testing.T) {
	// Smoke test: verify nil Interval is treated as DefaultDaemonInterval
	// without crashing. We cancel the ctx immediately so the loop runs once.
	e := makeDaemonEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before starting; the first Sync call still runs.

	err := e.Daemon(ctx, DaemonOpts{
		LockDir: t.TempDir(),
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("Daemon: %v", err)
	}
}

func TestReplaceCfg_NilIsNoop(t *testing.T) {
	e := makeDaemonEngine(t)
	pre := e.cfg()
	e.ReplaceCfg(nil)
	if e.cfg() != pre {
		t.Fatal("ReplaceCfg(nil) replaced the config")
	}
}

func TestDaemon_ServesPrometheusMetrics(t *testing.T) {
	e := makeDaemonEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Prometheus only emits a series after at least one observation has
	// been recorded for that label set. Touch each metric so the daemon
	// scrape produces a complete sample.
	telemetry.SyncPRDuration.WithLabelValues("test/repo").Observe(0.01)
	telemetry.SyncErrorsTotal.WithLabelValues("test/repo").Inc()

	listener := make(chan net.Listener, 1)
	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval:    1 * time.Hour, // never tick
			LockDir:     t.TempDir(),
			Logger:      discardLogger(),
			MetricsAddr: "127.0.0.1:0",
			MetricsListener: func(ln net.Listener) {
				listener <- ln
			},
		})
	}()

	var ln net.Listener
	select {
	case ln = <-listener:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not publish metrics listener within 2s")
	}

	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("/metrics status: got %d want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	for _, want := range []string{
		"pg_pr_sync_pr_duration_seconds",
		"pg_pr_sync_errors_total",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics missing %q\nbody:\n%s", want, string(body))
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after cancel")
	}
}

func TestDaemon_EmptyMetricsAddrSkipsServer(t *testing.T) {
	e := makeDaemonEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // exit after first iteration

	err := e.Daemon(ctx, DaemonOpts{
		Interval:    1 * time.Hour,
		LockDir:     t.TempDir(),
		Logger:      discardLogger(),
		MetricsAddr: "", // disabled
	})
	if err != nil {
		t.Fatalf("Daemon: %v", err)
	}
}

func TestXdgRuntimeDir_FallsBackToTempDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := xdgRuntimeDir(); got != os.TempDir() {
		t.Fatalf("xdgRuntimeDir without env: got %q want %q", got, os.TempDir())
	}
	t.Setenv("XDG_RUNTIME_DIR", "/custom/runtime")
	if got := xdgRuntimeDir(); got != "/custom/runtime" {
		t.Fatalf("xdgRuntimeDir with env: got %q", got)
	}
}

func TestDaemonMountsDashboard(t *testing.T) {
	t.Parallel()
	e := makeDaemonEngine(t)
	store := snapshot.NewStore()
	store.Set(&snapshot.Snapshot{
		GeneratedAt: time.Now().UTC(),
		Mine:        []snapshot.MineRow{},
		Team:        []snapshot.TeamRow{},
	})

	boundCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- e.Daemon(ctx, DaemonOpts{
			Interval:        time.Hour, // never tick
			LockDir:         t.TempDir(),
			Logger:          discardLogger(),
			Sighup:          make(chan os.Signal),
			MetricsAddr:     "127.0.0.1:0",
			MetricsListener: func(ln net.Listener) { boundCh <- ln.Addr().String() },
			Dashboard:       store,
		})
	}()

	var bound string
	select {
	case bound = <-boundCh:
	case <-time.After(2 * time.Second):
		t.Fatal("listener never bound")
	}

	resp, err := http.Get("http://" + bound + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET /api/v1/dashboard: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("dashboard status: got %d want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Confirm /metrics still serves.
	mResp, err := http.Get("http://" + bound + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	if mResp.StatusCode != http.StatusOK {
		t.Errorf("metrics status: got %d want 200", mResp.StatusCode)
	}
	_ = mResp.Body.Close()

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after cancel")
	}
}

func TestDaemonNoDashboardWhenNil(t *testing.T) {
	t.Parallel()
	e := makeDaemonEngine(t)
	boundCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- e.Daemon(ctx, DaemonOpts{
			Interval:        time.Hour,
			LockDir:         t.TempDir(),
			Logger:          discardLogger(),
			Sighup:          make(chan os.Signal),
			MetricsAddr:     "127.0.0.1:0",
			MetricsListener: func(ln net.Listener) { boundCh <- ln.Addr().String() },
			Dashboard:       nil,
		})
	}()

	var bound string
	select {
	case bound = <-boundCh:
	case <-time.After(2 * time.Second):
		t.Fatal("listener never bound")
	}

	resp, err := http.Get("http://" + bound + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET /api/v1/dashboard: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("dashboard status: got %d want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after cancel")
	}
}

func TestNewStderrHandler_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newStderrHandler(&buf, true)).Error("boom", "err", "bad")
	out := buf.String()
	if !strings.Contains(out, `"msg":"boom"`) || !strings.Contains(out, `"err":"bad"`) {
		t.Fatalf("unexpected json log: %q", out)
	}
	if !strings.Contains(out, `"level":"ERROR"`) {
		t.Fatalf("missing level: %q", out)
	}
}

func TestNewStderrHandler_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newStderrHandler(&buf, false)).Warn("watch")
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "watch") {
		t.Fatalf("unexpected text log: %q", out)
	}
}

// TestLogSyncOutcome_LogsErrorDetails pins the observability fix: when a sync
// finishes with per-repo errors recorded in the Summary (the normal path —
// errors land in Summary.Errors, not as a returned Go error), the daemon must
// log the actual error MESSAGES, not just a count. Previously only
// "errors": <count> was logged at INFO, so the "why" (e.g. "invalid issue
// type: feedback") was invisible in the logs and only reached the per-repo
// state file (overwritten each sync).
func TestLogSyncOutcome_LogsErrorDetails(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	sum := &Summary{
		TotalPRs: 3,
		Errors: []SummaryError{
			{Repo: "ZR-Private/ziprecruiter", Message: "PR #92955 feedback: invalid issue type: feedback"},
		},
	}

	logSyncOutcome(log, sum, nil, 1500*time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, "invalid issue type: feedback") {
		t.Fatalf("sync error messages must appear in the daemon log so failures are diagnosable; got: %s", out)
	}
}
