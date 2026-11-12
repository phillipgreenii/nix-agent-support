package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/facts"
	"github.com/phillipgreenii/ccpool/internal/session"
)

// TestNewSessionDeps_wiresExister is the regression for CRITICAL #1: every
// production session.Deps must carry a non-nil Exister, or resumable
// is always false, resume never happens and `ccpool reap` prunes resumable rows
// (ADR 0015). The wired Exister is the recorder's "originals present" fact, and
// with the recorder down it fails (daemon offline) instead of answering false, so
// an outage never reads as "the session is gone".
func TestNewSessionDeps_wiresExister(t *testing.T) {
	e := useRecorder(t)
	deps := newSessionDeps(config.Config{}, nil, nil)
	if deps.Exister == nil {
		t.Fatal("production session.Deps must wire Exister (nil → reap prunes resumable rows)")
	}
	ce, ok := deps.Exister.(session.CheckedExister)
	if !ok {
		t.Fatalf("production Exister %T must be a session.CheckedExister", deps.Exister)
	}
	if deps.Gate == nil || deps.Control == nil {
		t.Fatalf("production deps must wire Gate and Control, got %v and %v", deps.Gate, deps.Control)
	}

	transcript := e.Path("projects", e.Project, testSess+".jsonl")
	if got, err := ce.ExistsChecked(transcript); err != nil || got {
		t.Fatalf("before the transcript exists: ExistsChecked = %v, %v; want false, nil", got, err)
	}
	e.Write("projects/"+e.Project+"/"+testSess+".jsonl", tuserLine("2026-01-01T00:00:01Z", "hi"))
	start := time.Now().Add(-time.Hour)
	e.Procs.Add(4242, start, nil)
	e.Write("sessions/4242.json", fmt.Sprintf(`{"pid":4242,"sessionId":%q,"cwd":"/example/work","startedAt":%d,"procStart":%q,"kind":"interactive","status":"idle","updatedAt":%d}`,
		testSess, start.UnixMilli(), start.UTC().Format(procStartLayout), start.UnixMilli()))
	e.Settle()
	if got, err := ce.ExistsChecked(transcript); err != nil || !got {
		t.Errorf("wired Exister must find the recorded transcript: %v, %v", got, err)
	}

	e.Stop()
	if _, err := ce.ExistsChecked(transcript); !errors.Is(err, facts.ErrDaemonOffline) {
		t.Errorf("recorder down: err = %v; want daemon offline", err)
	}
}

// TestNewSessionDeps_wiresFailFastLaunchWait: the production Waiter is the
// launch-aware one (session.LaunchWaiter) with the tmux client as its pane
// prober and the pool's tmux prefix, so ccpool new and reply's resume fail fast
// on a dead launch. A plain Waiter would silently wait out the full timeout.
func TestNewSessionDeps_wiresFailFastLaunchWait(t *testing.T) {
	var cfg config.Config
	cfg.Tmux.Prefix = "pfx-"
	deps := newSessionDeps(cfg, nil, nil)
	if _, ok := deps.Wait.(session.LaunchWaiter); !ok {
		t.Fatalf("production Waiter %T is not a session.LaunchWaiter", deps.Wait)
	}
	w, ok := deps.Wait.(storeWaiter)
	if !ok {
		t.Fatalf("production Waiter is %T, want storeWaiter", deps.Wait)
	}
	if w.panes == nil || w.prefix != "pfx-" {
		t.Errorf("storeWaiter panes=%v prefix=%q, want a pane prober and the pool's prefix", w.panes, w.prefix)
	}
}

// TestNewSessionDeps_wiresPoolAndMetricLabelAllowlist: every production
// session.Deps carries the pool root (whose basename is every metric record's
// pool attribute) and the metric-label allowlist of THE POOL'S OWN config. It
// loads configs through config.LoadForPool, the exact seam reap-all uses to
// govern many pools in one process, so each pool's Service is built with its
// own PoolPath (no shared or environment-derived state).
func TestNewSessionDeps_wiresPoolAndMetricLabelAllowlist(t *testing.T) {
	base := isolateLabelerEnv(t)
	poolA := newRegisteredPool(t, base, "poolA")
	poolB := newRegisteredPool(t, base, "poolB")
	if err := os.WriteFile(filepath.Join(poolB, "config.toml"),
		[]byte("[telemetry]\nmetric_label_allowlist = [\"pgrouter.role\", \"pgrouter.kind\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_POOL", "/pools/ENV-MUST-BE-IGNORED")

	cases := []struct {
		name, root, wantPath string
		wantAllow            []string
	}{
		{"default pool", "", "", []string{"pgrouter.role"}},
		{"pool A", poolA, poolA, []string{"pgrouter.role"}},
		{"pool B (configured)", poolB, poolB, []string{"pgrouter.role", "pgrouter.kind"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.LoadForPool(tc.root)
			if err != nil {
				t.Fatal(err)
			}
			deps := newSessionDeps(cfg, nil, nil)
			if deps.PoolPath != tc.wantPath {
				t.Errorf("Deps.PoolPath = %q, want %q", deps.PoolPath, tc.wantPath)
			}
			if !reflect.DeepEqual(deps.MetricLabelAllowlist, tc.wantAllow) {
				t.Errorf("Deps.MetricLabelAllowlist = %v, want %v", deps.MetricLabelAllowlist, tc.wantAllow)
			}
		})
	}
}
