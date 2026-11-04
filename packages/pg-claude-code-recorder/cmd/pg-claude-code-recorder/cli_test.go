package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func shortDir(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// fakeDaemon serves status and coverage answers built by the test.
func fakeDaemon(t *testing.T, status func() wire.Status, cov func() wire.Coverage) string {
	t.Helper()
	sock := filepath.Join(shortDir(t), "r.sock")
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	s.Register("status", server.KindQuery, func(context.Context, []byte) (any, *wire.Error) { return status(), nil })
	s.Register("coverage", server.KindQuery, func(context.Context, []byte) (any, *wire.Error) { return cov(), nil })
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return sock
}

func TestOfflineStatusStillAnswersWithExit3AndAnActionableLine(t *testing.T) {
	dir := shortDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "blob"), make([]byte, 1234), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfgPath, []byte("stateDir = \""+filepath.Join(dir, "state")+"\"\nlogFile = \"/example/recorder.log\"\n"), 0o600)
	sock := filepath.Join(dir, "none.sock")

	a, out, errw := testApp(nil)
	if code := a.run([]string{"status", "--config", cfgPath, "--socket", sock}); code != 3 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{sock, "loaded, not running", "/example/recorder.log", "1234 bytes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.HasPrefix(errw.String(), "error: daemon_offline\n") ||
		!strings.Contains(errw.String(), "daemon offline: no answer on "+sock+"; start it with the launch agent or run \"pg-claude-code-recorder serve\"") {
		t.Fatalf("stderr %q", errw.String())
	}

	a, out, errw = testApp(nil)
	if code := a.run([]string{"status", "--json", "--config", cfgPath, "--socket", sock}); code != 3 {
		t.Fatal(code)
	}
	var st wire.Status
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || st.Short != "down" || st.StateBytes != 1234 || st.Socket != sock {
		t.Fatalf("stdout %q %v", out.String(), err)
	}
	var e struct {
		Error   string `json:"error"`
		Exit    int    `json:"exit"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(errw.Bytes(), &e); err != nil || e.Error != "daemon_offline" || e.Exit != 3 || e.Message == "" {
		t.Fatalf("stderr %q %v", errw.String(), err)
	}

	a, out, _ = testApp(nil)
	if code := a.run([]string{"status", "--short", "--socket", sock, "--config", cfgPath}); code != 3 || strings.TrimSpace(out.String()) != "down" {
		t.Fatalf("short: %d %q", code, out.String())
	}
}

func TestExit5PrintsThePartialAnswerAndAllowPartialMakesItZero(t *testing.T) {
	sock := fakeDaemon(t,
		func() wire.Status {
			return wire.Status{Verdict: "behind: first backfill is copying, 1 of 9 files copied (newest first)", Short: "behind", Degraded: true}
		},
		func() wire.Coverage {
			return wire.Coverage{Degraded: true, CoverageSummary: wire.CoverageSummary{Files: 1}}
		})

	for _, sub := range []string{"status", "coverage"} {
		a, out, errw := testApp(nil)
		if code := a.run([]string{sub, "--socket", sock}); code != 5 {
			t.Fatalf("%s exit %d", sub, code)
		}
		if out.Len() == 0 || !strings.HasPrefix(errw.String(), "error: behind\n") {
			t.Fatalf("%s: stdout %q stderr %q", sub, out, errw)
		}
		a, out, errw = testApp(nil)
		if code := a.run([]string{sub, "--socket", sock, "--allow-partial"}); code != 0 || errw.Len() != 0 || out.Len() == 0 {
			t.Fatalf("%s --allow-partial: %d %q %q", sub, code, out, errw)
		}
	}
	a, _, errw := testApp(nil)
	if code := a.run([]string{"status", "--json", "--socket", sock}); code != 5 || !strings.Contains(errw.String(), `"error":"behind","exit":5`) {
		t.Fatalf("json exit5: %d %q", code, errw)
	}
}

func TestWaitCaughtUpFollowsTheFirstRunAndTimesOutWithExit6(t *testing.T) {
	var polls atomic.Int32
	sock := fakeDaemon(t,
		func() wire.Status { return wire.Status{Short: "ok", CaughtUp: polls.Add(1) >= 3, Verdict: "v"} },
		func() wire.Coverage {
			n := polls.Add(1)
			return wire.Coverage{CaughtUp: n >= 3, Degraded: n < 3}
		})
	a, out, _ := testApp(nil)
	a.pollEvery = time.Millisecond
	if code := a.run([]string{"coverage", "--socket", sock, "--wait-caught-up", "--json"}); code != 0 || polls.Load() < 3 {
		t.Fatalf("exit %d after %d polls\n%s", code, polls.Load(), out)
	}
	polls.Store(-1000000)
	a, _, errw := testApp(nil)
	a.pollEvery = time.Millisecond
	if code := a.run([]string{"coverage", "--socket", sock, "--wait-caught-up", "--timeout", "30ms", "--allow-partial"}); code != 6 || !strings.HasPrefix(errw.String(), "error: wait_timeout\n") {
		t.Fatalf("exit %d %q", code, errw)
	}
	polls.Store(0)
	a, out, _ = testApp(nil)
	if code := a.run([]string{"status", "--watch", "--interval", "1ms", "--socket", sock}); code != 0 || strings.Count(out.String(), "v\n") < 2 {
		t.Fatalf("watch: %d\n%s", code, out)
	}
}

func TestBadFlagIsAUsageErrorAndBadConfigToo(t *testing.T) {
	a, _, errw := testApp(nil)
	if code := a.run([]string{"status", "--nope"}); code != 2 || !strings.HasPrefix(errw.String(), "error: usage") {
		t.Fatalf("%d %q", code, errw)
	}
	a, _, _ = testApp(nil)
	if code := a.run([]string{"coverage", "--config", "/nonexistent/example.toml"}); code != 2 {
		t.Fatalf("missing explicit config: %d", code)
	}
}

func TestPprofIsOffByDefaultAndFlagOpensTheSocketUnderStateHome(t *testing.T) {
	dir := shortDir(t)
	env := map[string]string{"XDG_STATE_HOME": dir}
	getenv := func(k string) string { return env[k] }
	var w strings.Builder
	stop := startPprof(false, getenv, &w)
	stop()
	if _, err := os.Stat(filepath.Join(dir, "pg-claude-code-recorder")); err == nil || w.Len() != 0 {
		t.Fatalf("off by default but %q / %v", w.String(), err)
	}
	stop = startPprof(true, getenv, &w)
	defer stop()
	want := filepath.Join(dir, "pg-claude-code-recorder", "pprof.sock")
	fi, err := os.Stat(want)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pprof socket %v %v (%q)", fi, err, w.String())
	}
	if pprofSocket(getenv) != want {
		t.Fatal("pprofSocket mismatch")
	}
}

// TestStatusAnswersFastWhenTheCrashQueriesAreSlow pins the cause of the 30 s
// status: the cached status op answers at once, but the two conversation.list
// reads behind crashed_recent and suspected_recent each take as long as the
// request deadline on a large index (here, while the daemon is busy, they block
// until the test ends). status must give up on them after its budget and answer
// without the counts.
func TestStatusAnswersFastWhenTheCrashQueriesAreSlow(t *testing.T) {
	release := make(chan struct{})
	sock := filepath.Join(shortDir(t), "r.sock")
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	s.Register("status", server.KindQuery, func(context.Context, []byte) (any, *wire.Error) {
		return wire.Status{Healthy: true, Short: "ok", Verdict: "healthy", CaughtUp: true, StatusAgeS: 2}, nil
	})
	s.Register("conversation.list", server.KindQuery, func(ctx context.Context, _ []byte) (any, *wire.Error) {
		select {
		case <-release:
		case <-ctx.Done():
		case <-time.After(20 * time.Second):
		}
		return map[string]any{}, nil
	})
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(release) })

	a, out, _ := testApp(nil)
	start := time.Now()
	code := a.run([]string{"status", "--json", "--socket", sock})
	if el := time.Since(start); el > time.Second {
		t.Fatalf("status took %s with slow crash queries, want under 1s", el)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var st wire.Status
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatal(err, out.String())
	}
	if st.CrashedRecent != nil || st.SuspectedRecent != nil {
		t.Fatalf("the counts must be absent when the queries miss the budget: %v %v", st.CrashedRecent, st.SuspectedRecent)
	}
	if st.StatusAgeS != 2 {
		t.Fatalf("the cached figures were lost: %+v", st)
	}
}
