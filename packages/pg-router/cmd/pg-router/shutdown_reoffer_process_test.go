//go:build unix

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/emit"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
)

// Bead pg2-bn5io: a real-process proof that a command-role dispatch which is in
// flight when the router is stopped is NOT recorded as a handler error, and is
// re-offered by the next router.
//
// Choice made for the bead's design question (longer drain for command roles, or
// re-offer a dispatch cancelled by shutdown): re-offer. The drain budget cannot
// grow: shutdownDrainTimeout (20s) + the 30s session-sweep allowance +
// inFlightDrainTimeout (5s) already fill launchd's 60s ExitTimeOut, and desk-pr
// runs are 20 to 160 s. roleListener.Offer therefore hands a dispatch that was
// cut short by the cancelled run context back as a pre-accept decline
// (orchestrator.DeclineDetailShutdown): nothing durable is written, the event
// stays un-accepted, and the next router's replay re-offers it.
//
// The router under test is a real OS process: this test binary re-executed with a
// sentinel, whose helper runs the same run() entry point main() runs, so
// signal.NotifyContext, the durable queue under a scratch log dir, the unix-socket
// core, the real wireclient exec of a real handler script and the Prometheus
// /metrics endpoint are all the production ones. The test never starts a Go-level
// router in-process and never touches the live daemon: every path, the socket and
// the metrics port (127.0.0.1, ephemeral) are scratch.
//
// SAFETY MODEL. The only process this file signals is the router child it started
// itself (cmd.Process, SIGTERM for the stop, SIGKILL in cleanup if it is still
// alive); no pid is read from a file, an env var or a process listing. The helper
// runs only when ALL hold: the sentinel env var carries the exact value, and argv
// is exactly the binary plus the anchored -test.run flag for the helper. The
// sentinel is given to the child only through cmd.Env; the parent fails at once if
// it finds the sentinel in its own environment (recursion guard). The handler
// script sleeps at most handlerSleepSeconds, so a leaked one ends by itself.
const (
	routerHelperName     = "TestHelperRouterProcess"
	routerSentinelEnv    = "HELPER_ROUTER_PROCESS_SENTINEL"
	routerSentinelValue  = "pg2-bn5io-router-process-v1-7d2e9b41"
	handlerSleepSeconds  = 90
	reofferRole          = "desk-pr"
	reofferEventType     = "pr.changed"
	reofferEventJSON     = `{"schemaVersion":"1","id":"pr-1@1","type":"pr.changed","expiresAt":"2099-01-01T00:00:00Z","payload":{"id":"pr-1"}}`
	reofferShutdownLine  = "dispatch cut short by shutdown"
	reofferStartedMarker = "dispatch-started"
	reofferDoneMarker    = "dispatch-finished"
	reofferPreShutMarker = "pre-shutdown"
)

// TestHelperRouterProcess is not a test: it is the router process of
// TestShutdownReoffer_RealRouterProcess. Without the sentinel it is a skipped
// no-op. With it, it runs `pg-router run` and exits with its code.
func TestHelperRouterProcess(t *testing.T) {
	if os.Getenv(routerSentinelEnv) != routerSentinelValue {
		t.Skip("helper process only")
	}
	if len(os.Args) != 2 || os.Args[1] != "-test.run=^"+routerHelperName+"$" {
		return
	}
	os.Args = []string{"pg-router", "run"}
	os.Exit(run())
}

// lockedBuffer is an io.Writer a child process's stderr can fill while the test
// reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// reofferHarness holds the scratch state shared by both router processes.
type reofferHarness struct {
	t          *testing.T
	base       string // short, so the unix socket path stays within its length limit
	logDir     string
	configPath string
	markers    string // handler-written marker files live here
	script     string
	metrics    string // host:port of the router's /metrics endpoint
}

func newReofferHarness(t *testing.T) *reofferHarness {
	t.Helper()
	h := &reofferHarness{t: t, base: shortDir(t)}
	h.logDir = filepath.Join(h.base, "state")
	h.markers = filepath.Join(h.base, "markers")
	h.configPath = filepath.Join(h.base, "config.toml")
	h.script = filepath.Join(h.base, "handler.sh")
	for _, d := range []string{h.logDir, h.markers, filepath.Join(h.base, "home"), filepath.Join(h.base, "repo")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// A command-type role: its dispatch is a real subprocess, the shape desk-pr has.
	// `exec sleep` replaces the shell, so the process exec.CommandContext kills at
	// cancellation is the one holding the pipes (Wait returns at once).
	script := "#!/bin/sh\n" +
		"M='" + h.markers + "'\n" +
		"sub=\"$1\"\n" +
		"cat > /dev/null\n" +
		"case \"$sub\" in\n" +
		"dispatch)\n" +
		"  echo started >> \"$M/" + reofferStartedMarker + "\"\n" +
		"  if [ -f \"$M/finish-fast\" ]; then\n" +
		"    echo done >> \"$M/" + reofferDoneMarker + "\"\n" +
		"    echo '{\"schemaVersion\":\"1\",\"id\":\"x\",\"outcome\":\"ok\"}'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"  exec sleep " + fmt.Sprint(handlerSleepSeconds) + "\n" +
		"  ;;\n" +
		"preShutdown)\n" +
		"  echo ran >> \"$M/" + reofferPreShutMarker + "\"\n" +
		// Keeps the router (and its /metrics endpoint) alive for a moment after the
		// dispatch was settled, so the scrape below reads the post-decision state.
		"  sleep 2\n" +
		"  ;;\n" +
		"esac\n" +
		"echo '{\"schemaVersion\":\"1\",\"id\":\"x\",\"outcome\":\"ok\"}'\n"
	if err := os.WriteFile(h.script, []byte(script), 0o755); err != nil {
		t.Fatalf("write handler script: %v", err)
	}
	cfg := `[[query]]
name = "idle-source"
emits = ["` + reofferEventType + `"]
type = "command"
[query.command]
argv = ["true"]
format = "jsonl"

[[role]]
name = "` + reofferRole + `"
enabled = true
binds = ["` + reofferEventType + `"]
`
	if err := os.WriteFile(h.configPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	// An ephemeral loopback port for the /metrics endpoint, reserved then released.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	h.metrics = ln.Addr().String()
	_ = ln.Close()
	return h
}

func (h *reofferHarness) markerLines(name string) int {
	data, err := os.ReadFile(filepath.Join(h.markers, name))
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

// waitFor polls cond up to d, failing the test with msg if it never holds.
func (h *reofferHarness) waitFor(d time.Duration, msg string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out after %v waiting for %s", d, msg)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// router is one real router process.
type router struct {
	cmd    *exec.Cmd
	stderr *lockedBuffer
	done   chan error
}

func (h *reofferHarness) startRouter() *router {
	h.t.Helper()
	self := os.Args[0]
	if !filepath.IsAbs(self) {
		h.t.Fatalf("test binary path %q is not absolute", self)
	}
	cmd := exec.Command(self, "-test.run=^"+routerHelperName+"$")
	// A complete, explicit environment: nothing of the operator's PG_ROUTER_* or
	// XDG state reaches the child, so it cannot read or write the live daemon's.
	cmd.Env = []string{
		routerSentinelEnv + "=" + routerSentinelValue,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(h.base, "home"),
		"TMPDIR=" + os.TempDir(),
		"PG_ROUTER_REPO_ROOT=" + filepath.Join(h.base, "repo"),
		"PG_ROUTER_CONFIG=" + h.configPath,
		"PG_ROUTER_GLOBAL_CONFIG=" + filepath.Join(h.base, "no-such-global-config.toml"),
		"PG_ROUTER_LOG_DIR=" + h.logDir,
		"PG_ROUTER_WORKTREE_DIR=" + filepath.Join(h.base, "worktrees"),
		"PG_ROUTER_HANDLER_COMMAND=" + h.script,
		"PG_ROUTER_POLL_INTERVAL=1",
		"PG_ROUTER_METRICS_ADDR=" + h.metrics,
	}
	r := &router{cmd: cmd, stderr: &lockedBuffer{}, done: make(chan error, 1)}
	cmd.Stderr = r.stderr
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("start router process: %v", err)
	}
	go func() { r.done <- cmd.Wait() }()
	h.t.Cleanup(func() {
		select {
		case <-r.done:
			r.done <- nil // keep the channel readable for any later waiter
		default:
			_ = cmd.Process.Kill()
			<-r.done
		}
	})
	return r
}

// waitCore waits until the router's core socket is discoverable under the log dir.
func (h *reofferHarness) waitCore(r *router) {
	h.t.Helper()
	h.waitFor(30*time.Second, "the router's core to become discoverable\nstderr:\n"+r.stderr.String(), func() bool {
		_, err := core.Discover(h.logDir)
		return err == nil
	})
}

func (h *reofferHarness) inject() {
	h.t.Helper()
	var out, errOut bytes.Buffer
	loc := emit.Locator{Discover: emit.Discoverer(h.logDir)}
	if code := pushInject(&out, &errOut, true, loc, emit.SocketEnqueuer{}, reofferEventJSON); code != exitOK {
		h.t.Fatalf("push-inject exit = %d\nstdout=%s\nstderr=%s", code, out.String(), errOut.String())
	}
}

func (h *reofferHarness) scrape() (string, error) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + h.metrics + "/metrics")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// stop sends SIGTERM to the router child and waits for it to exit, returning its
// exit code and how long the stop took.
func (h *reofferHarness) stop(r *router, within time.Duration) (code int, took time.Duration) {
	h.t.Helper()
	begun := time.Now()
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		h.t.Fatalf("SIGTERM the router child: %v", err)
	}
	select {
	case err := <-r.done:
		r.done <- err
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), time.Since(begun)
			}
			h.t.Fatalf("router wait: %v", err)
		}
		return 0, time.Since(begun)
	case <-time.After(within):
		h.t.Fatalf("router did not exit within %v of SIGTERM\nstderr:\n%s", within, r.stderr.String())
	}
	return 0, 0
}

// TestShutdownReoffer_RealRouterProcess stops a real router while a real command
// role's handler is running, and checks the three things the bead requires: the
// cancelled dispatch is not counted as a handler error, it is not logged as a
// failed dispatch result, and the next router re-offers it.
func TestShutdownReoffer_RealRouterProcess(t *testing.T) {
	// Recursion guard: this process must not itself be a helper child.
	if _, set := os.LookupEnv(routerSentinelEnv); set {
		t.Fatalf("recursion guard: %s is already set in the test process's own environment", routerSentinelEnv)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	h := newReofferHarness(t)

	// ---- First router: dispatch starts, then the router is stopped mid-run. ----
	r1 := h.startRouter()
	h.waitCore(r1)
	h.inject()
	h.waitFor(30*time.Second, "the handler to start its dispatch\nstderr:\n"+r1.stderr.String(), func() bool {
		return h.markerLines(reofferStartedMarker) >= 1
	})

	// Scrape in the background from the moment the stop is requested: the endpoint
	// is only up while the process is, and the router stays up through the drain.
	// Only the first scrape AFTER the router reported the shutdown hand-back counts.
	type scrapeResult struct {
		body string
		err  error
	}
	scraped := make(chan scrapeResult, 1)
	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(r1.stderr.String(), reofferShutdownLine) && h.markerLines(reofferPreShutMarker) >= 1 {
				body, err := h.scrape()
				scraped <- scrapeResult{body, err}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		scraped <- scrapeResult{err: fmt.Errorf("the router never reported %q", reofferShutdownLine)}
	}()

	code, took := h.stop(r1, 90*time.Second)
	if code != exitOK {
		t.Fatalf("first router exit = %d, want %d\nstderr:\n%s", code, exitOK, r1.stderr.String())
	}
	// The handler outlives the drain budget, so the stop only completes after
	// shutdownDrainTimeout and then cancels it: this is the C6 situation.
	if took < shutdownDrainTimeout-2*time.Second {
		t.Fatalf("router stopped after %v, want about the %v drain budget: the dispatch was not actually in flight at shutdown", took, shutdownDrainTimeout)
	}

	stderr1 := r1.stderr.String()
	if !strings.Contains(stderr1, reofferShutdownLine) {
		t.Errorf("router stderr lacks %q: the cancelled dispatch was not handed back\nstderr:\n%s", reofferShutdownLine, stderr1)
	}
	// The old path logged the cancelled run as a failed "dispatch result" and
	// counted it a handler error.
	if strings.Contains(stderr1, "dispatch result") {
		t.Errorf("router stderr has a dispatch result row for the cancelled dispatch:\n%s", stderr1)
	}

	// The failure series, read from the real /metrics endpoint after the hand-back.
	sr := <-scraped
	if sr.err != nil {
		t.Fatalf("scraping /metrics after the shutdown hand-back: %v\nstderr:\n%s", sr.err, stderr1)
	}
	if !strings.Contains(sr.body, "pg_router_") {
		t.Fatalf("scrape returned no pg_router_ series, so it proves nothing:\n%s", sr.body)
	}
	for _, class := range []string{metrics.FailureClassHandlerError, metrics.FailureClassDeclined, metrics.FailureClassDispatchFail} {
		if v := seriesValue(sr.body, metrics.MetricFailures+"_total", map[string]string{"role": reofferRole, "class": class}); len(v) != 0 {
			t.Errorf("pg_router_failures_total{role=%q,class=%q} = %v after a shutdown cut, want no series\nscrape:\n%s", reofferRole, class, v, sr.body)
		}
	}
	if n := h.markerLines(reofferStartedMarker); n != 1 {
		t.Fatalf("handler dispatches before the restart = %d, want 1", n)
	}
	if n := h.markerLines(reofferDoneMarker); n != 0 {
		t.Fatalf("handler finished %d time(s) before the restart, want 0 (it must have been cut short)", n)
	}

	// ---- Second router, same state: the event is re-offered and now completes. ----
	if err := os.WriteFile(filepath.Join(h.markers, "finish-fast"), []byte("1\n"), 0o644); err != nil {
		t.Fatalf("write finish-fast: %v", err)
	}
	r2 := h.startRouter()
	h.waitFor(30*time.Second, "the restarted router to re-offer the cancelled dispatch and finish it\nstderr:\n"+r2.stderr.String(), func() bool {
		return h.markerLines(reofferDoneMarker) >= 1
	})
	if n := h.markerLines(reofferStartedMarker); n != 2 {
		t.Errorf("handler dispatches after the restart = %d, want 2 (the original plus exactly one re-offer)", n)
	}
	if code, _ := h.stop(r2, 60*time.Second); code != exitOK {
		t.Errorf("second router exit = %d, want %d\nstderr:\n%s", code, exitOK, r2.stderr.String())
	}
	if n := h.markerLines(reofferStartedMarker); n != 2 {
		t.Errorf("handler dispatches after the second stop = %d, want 2: the re-offered event was accepted once and must not run again", n)
	}
}

// The orchestrator and metrics packages share the shutdown hand-back reason as two
// constants (metrics does not import orchestrator); this pins them equal.
func TestShutdownHandBackReasonIsSharedByOrchestratorAndMetrics(t *testing.T) {
	if orchestrator.DeclineDetailShutdown != metrics.ReasonShutdown {
		t.Fatalf("orchestrator.DeclineDetailShutdown = %q, metrics.ReasonShutdown = %q: they must be equal",
			orchestrator.DeclineDetailShutdown, metrics.ReasonShutdown)
	}
}
