package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/usagelimit"
)

// fakeChecker is a usagelimit.Checker returning a canned reading.
type fakeChecker struct {
	limit *usagelimit.Limit
	err   error
	calls int
}

func (f *fakeChecker) Check(context.Context) (*usagelimit.Limit, error) {
	f.calls++
	return f.limit, f.err
}

// useUsageChecker installs c as the usage checker for the test's duration and
// restores the hermetic TestMain default (never-blocks) afterwards.
func useUsageChecker(t *testing.T, c usagelimit.Checker) {
	t.Helper()
	orig := newUsageChecker
	newUsageChecker = func(config.UsageGate) usagelimit.Checker { return c }
	t.Cleanup(func() { newUsageChecker = orig })
}

func fiveHourLimit() *usagelimit.Limit {
	return &usagelimit.Limit{Window: usagelimit.FiveHour, UsedPct: 100, ResetsAt: time.Date(2026, 10, 6, 17, 0, 0, 0, time.UTC)}
}

// captureStdout mirrors captureStderr for a command that prints its result.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	code := fn()
	_ = w.Close()
	os.Stdout = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out), code
}

func TestUsageGate_exitCodeIsDistinctFromReplysOwn(t *testing.T) {
	// 1 generic, 2 usage, 3 errored, 4 timed out, 5 busy, 6 cancel unconfirmed,
	// 7 prompt not ingested: the usage-limit refusal must not collide with any.
	if exitUsageLimited != 8 {
		t.Fatalf("exitUsageLimited = %d, want 8", exitUsageLimited)
	}
}

func TestRefuseOnUsageLimit_blockedNamesWindowAndReset(t *testing.T) {
	useUsageChecker(t, &fakeChecker{limit: fiveHourLimit()})
	out, code := captureStderr(t, func() int {
		c, refused := refuseOnUsageLimit("new", config.Config{})
		if !refused {
			t.Error("refused = false, want true")
		}
		return c
	})
	if code != exitUsageLimited {
		t.Fatalf("code = %d, want %d", code, exitUsageLimited)
	}
	for _, want := range []string{"new:", "not accepting work", "five_hour", "100.0%", "resets at"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr %q missing %q", out, want)
		}
	}
}

func TestRefuseOnUsageLimit_notBlockedProceeds(t *testing.T) {
	useUsageChecker(t, &fakeChecker{})
	if code, refused := refuseOnUsageLimit("new", config.Config{}); refused || code != 0 {
		t.Fatalf("got (%d, %v), want (0, false)", code, refused)
	}
}

// TestCurrentUsageLimit_failsOpen: an unavailable monitor must never read as a
// refusal — ccpool keeps accepting work when it cannot find out.
func TestCurrentUsageLimit_failsOpen(t *testing.T) {
	useUsageChecker(t, &fakeChecker{err: errors.New("pa-monitor: not found")})
	if l := currentUsageLimit(context.Background(), config.Config{}); l != nil {
		t.Fatalf("got %+v, want nil (fail open)", l)
	}
	if _, refused := refuseOnUsageLimit("reply", config.Config{}); refused {
		t.Fatal("an unavailable monitor must not refuse work")
	}
}

// TestProductionUsageChecker maps config onto the right Checker: disabled is
// the never-blocks Off (the monitor is never run), enabled is a Gate carrying the
// configured command, threshold and timeout.
func TestProductionUsageChecker(t *testing.T) {
	if got := productionUsageChecker(config.UsageGate{Enabled: false, Command: "x"}); got != (usagelimit.Off{}) {
		t.Errorf("disabled gate = %#v, want Off", got)
	}
	g := config.UsageGate{Enabled: true, Command: "/opt/pa-monitor", ThresholdPct: 90, Timeout: config.Duration(2 * time.Second)}
	got, ok := productionUsageChecker(g).(usagelimit.Gate)
	if !ok {
		t.Fatalf("enabled gate = %T, want usagelimit.Gate", productionUsageChecker(g))
	}
	if got.Command != "/opt/pa-monitor" || got.ThresholdPct != 90 || got.Timeout != 2*time.Second {
		t.Errorf("gate = %+v, want the configured command/threshold/timeout", got)
	}
}

// TestRunNew_refusesWhileUsageLimitHit: `ccpool new` exits 8 and launches
// NOTHING — no store row, no trust write, no tmux server — whoever called it.
func TestRunNew_refusesWhileUsageLimitHit(t *testing.T) {
	a := armedLaunchEnv(t)
	useUsageChecker(t, &fakeChecker{limit: fiveHourLimit()})
	out, code := captureStderr(t, func() int { return runNew([]string{"alpha", "--cwd", a.cwd}) })
	if code != exitUsageLimited {
		t.Fatalf("runNew = %d, want %d (stderr: %s)", code, exitUsageLimited, out)
	}
	if !strings.Contains(out, "five_hour") {
		t.Errorf("stderr %q must name the window", out)
	}
	a.assertNoLaunch(t)
}

// TestRunNew_usageLimitNotHitIsNotRefused: with no limit hit the same invocation
// gets past the gate (it then stops at the armed config's unlaunchable claude,
// which is NOT exit 8).
func TestRunNew_usageLimitNotHitIsNotRefused(t *testing.T) {
	a := armedLaunchEnv(t)
	c := &fakeChecker{}
	useUsageChecker(t, c)
	_, code := captureStderr(t, func() int { return runNew([]string{"alpha", "--cwd", a.cwd}) })
	if code == exitUsageLimited {
		t.Fatalf("runNew exited %d with no limit hit", code)
	}
	if c.calls != 1 {
		t.Errorf("checker consulted %d times, want 1", c.calls)
	}
}

// TestRunReply_refusesWhileUsageLimitHit: a prompt is work; reply refuses before
// resuming the session or delivering anything.
func TestRunReply_refusesWhileUsageLimitHit(t *testing.T) {
	a := armedLaunchEnv(t)
	useUsageChecker(t, &fakeChecker{limit: fiveHourLimit()})
	out, code := captureStderr(t, func() int { return runReply([]string{"alpha", "do the thing", "--no-wait"}) })
	if code != exitUsageLimited {
		t.Fatalf("runReply = %d, want %d (stderr: %s)", code, exitUsageLimited, out)
	}
	a.assertNoLaunch(t)
}

// TestRunCapacity_reportsUsageLimitAndZeroFree: an empty pool with plenty of
// slots reports free=0 plus the limit while a window is hit — the one field every
// admission gate already reads — and the unchanged shape when none is.
func TestRunCapacity_reportsUsageLimitAndZeroFree(t *testing.T) {
	armedLaunchEnv(t)
	useUsageChecker(t, &fakeChecker{limit: fiveHourLimit()})
	out, code := captureStdout(t, func() int { return runCapacity([]string{"--json"}) })
	if code != 0 {
		t.Fatalf("runCapacity = %d (%s)", code, out)
	}
	var got session.Capacity
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if got.Free != 0 || got.MaxSessions == 0 || got.Counted != 0 {
		t.Errorf("got %+v, want free=0 with max>0 and an empty pool", got)
	}
	if got.UsageLimit == nil || got.UsageLimit.Window != usagelimit.FiveHour || !got.UsageLimit.ResetsAt.Equal(fiveHourLimit().ResetsAt) {
		t.Errorf("usage_limit = %+v, want the five_hour limit", got.UsageLimit)
	}
}

func TestRunCapacity_noLimitKeepsShapeUnchanged(t *testing.T) {
	armedLaunchEnv(t)
	useUsageChecker(t, &fakeChecker{})
	out, code := captureStdout(t, func() int { return runCapacity([]string{"--json"}) })
	if code != 0 {
		t.Fatalf("runCapacity = %d (%s)", code, out)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if _, ok := raw["usage_limit"]; ok {
		t.Errorf("usage_limit must be omitted when no limit is hit: %s", out)
	}
	if free, _ := raw["free"].(float64); free != raw["max_sessions"] {
		t.Errorf("empty pool free = %v, want max_sessions %v", raw["free"], raw["max_sessions"])
	}
}

// TestRunCapacity_unavailableMonitorFailsOpen: a failing checker leaves the
// occupancy answer exactly as before.
func TestRunCapacity_unavailableMonitorFailsOpen(t *testing.T) {
	armedLaunchEnv(t)
	useUsageChecker(t, &fakeChecker{err: errors.New("daemon down")})
	out, code := captureStdout(t, func() int { return runCapacity([]string{"--json"}) })
	if code != 0 {
		t.Fatalf("runCapacity = %d (%s)", code, out)
	}
	var got session.Capacity
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.UsageLimit != nil || got.Free != got.MaxSessions {
		t.Errorf("got %+v, want an unblocked empty pool", got)
	}
}

func TestRenderCapacityText_withUsageLimit(t *testing.T) {
	c := session.Capacity{MaxSessions: 6, Counted: 1, Live: 1}.WithUsageLimit(fiveHourLimit())
	want := "free=0 counted=1 preserved=0 live=1 max=6 usage_limit=five_hour resets_at=2026-10-06T17:00:00Z\n"
	if got := renderCapacityText(c); got != want {
		t.Fatalf("renderCapacityText = %q, want %q", got, want)
	}
}
