//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Bead pg2-dlnm0: a hermetic, real-signal proof of the transient-handler retry
// path, end to end:
//
//	real handler subprocess that dies from a real SIGKILL
//	  -> wireclient.OSRunner reports exit code -1
//	  -> wireclient.Client.Dispatch builds *wireclient.ExitError{Code: -1}
//	  -> orchestrator classifies it "killed" and schedules a re-run
//	  -> eventqueue re-offers it after the retry backoff
//	  -> metrics.Emitter counts pg_router_dispatch_retries_total
//	  -> a real HTTP scrape of /metrics shows the series.
//
// SAFETY MODEL (read before editing; a past malfunctioning test of this shape
// took down a whole machine). The ONLY signal sent anywhere in this file is
// the single syscall in TestHelperSelfTerminatingHandler, whose target is the
// helper process's own pid (os.Getpid()), never a value read from an env
// var, a file, argv, a port or a process listing. That line runs only when ALL
// hold: the sentinel env var carries the exact expected value, argv carries
// the exact anchored -test.run flag for this helper, and the pid is > 1.
//
// The sentinel reaches the child ONLY through the argv wrapper built by
// helperArgv (env(1) sets the variable for the one child it execs). The test
// binary's own environment is never given the sentinel: nothing here calls
// t.Setenv or os.Setenv, and the parent test fails at once if it finds the
// sentinel already present in its own environment (recursion guard). The only
// other process-ending mechanism in play is exec.CommandContext's built-in
// cancellation of the direct child that wireclient.OSRunner itself started;
// this test holds no exec.Cmd and installs no custom cancel hook.
//
// Hermetic: no bootCore, no runRun*, no config.Load, no reads of the router's own
// environment-variable configuration, in-memory queue store, metrics listener on
// 127.0.0.1:0 only, scratch files under t.TempDir(). The live daemon's
// metrics port is never contacted. The test does not skip under -short.
const (
	selfTerminateHelperName    = "TestHelperSelfTerminatingHandler"
	selfTerminateSentinelEnv   = "HELPER_SELF_TERMINATE_SENTINEL"
	selfTerminateSentinelValue = "pg2-dlnm0-self-terminate-v1-3c9f1e7a"
	selfTerminateAttemptsEnv   = "HELPER_SELF_TERMINATE_ATTEMPTS_FILE"

	retryRole      = "desk-pr"
	retryEventType = "pr.changed"
	// maxRetries is the role's max_dispatch_retries: 1 original attempt plus
	// maxRetries re-runs.
	maxRetries = 2
)

// TestHelperSelfTerminatingHandler is not a test: it is the child process of
// TestKilledHandlerDispatch_RetriedAsKilledAndExportedOnMetrics. Without the
// sentinel it is a skipped no-op (so a plain `go test ./...` that reaches it
// does nothing). With the sentinel it appends one line to the attempts file
// and terminates ITSELF with a real SIGKILL, which is what makes the parent's
// OSRunner see a signalled child.
func TestHelperSelfTerminatingHandler(t *testing.T) {
	if os.Getenv(selfTerminateSentinelEnv) != selfTerminateSentinelValue {
		t.Skip("helper process only")
	}
	// Second guard: argv is EXACTLY the binary plus the anchored -test.run flag
	// for THIS helper, so this can never be reached by an unanchored, broader
	// or otherwise decorated run.
	if len(os.Args) != 2 || os.Args[1] != "-test.run=^"+selfTerminateHelperName+"$" {
		return
	}
	// Third guard: never act on pid 0/1 or anything nonsensical. This one
	// variable is the only pid in the helper and the only signal target.
	pid := os.Getpid()
	if pid <= 1 {
		return
	}
	// The attempts file is only ever appended to; its path is not a pid and
	// plays no part in choosing what is terminated. It must be absolute and
	// under the scratch root.
	path := os.Getenv(selfTerminateAttemptsEnv)
	if !filepath.IsAbs(path) || !isUnderTempDir(path) {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, werr := f.WriteString("attempt\n")
	if cerr := f.Close(); werr != nil || cerr != nil {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	// A signal a process sends to itself is delivered asynchronously: the call
	// above can return before the pending SIGKILL has taken effect, and falling
	// straight through to an exit here would race it and sometimes win, ending
	// the process normally instead of by signal. So wait for delivery; the
	// process never survives this sleep. Only if the signal somehow never
	// arrives, exit with a distinctive non-retryable code so the parent test
	// fails loudly instead of hanging.
	time.Sleep(10 * time.Second)
	os.Exit(97)
}

// isUnderTempDir reports whether path's directory lies inside os.TempDir(),
// comparing symlink-resolved forms of both sides (on macOS the temp root under
// /var/folders is a symlink into /private/var, and t.TempDir() may be spelled
// either way). The file itself need not exist yet; its directory must.
func isUnderTempDir(path string) bool {
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// recordingHandler wraps the REAL wireclient.Client (so the real OSRunner exit
// code mapping is what is under test) and records every Dispatch error.
type recordingHandler struct {
	inner wireclient.HandlerClient
	mu    sync.Mutex
	errs  []error
}

func (h *recordingHandler) Dispatch(ctx context.Context, role roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	reply, err := h.inner.Dispatch(ctx, role, evt)
	h.mu.Lock()
	h.errs = append(h.errs, err)
	h.mu.Unlock()
	return reply, err
}

func (h *recordingHandler) PostStartup(ctx context.Context, role roles.Role) (wireclient.Reply, error) {
	return h.inner.PostStartup(ctx, role)
}

func (h *recordingHandler) PreShutdown(ctx context.Context, role roles.Role) (wireclient.Reply, error) {
	return h.inner.PreShutdown(ctx, role)
}

func (h *recordingHandler) snapshot() []error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.errs)
}

// helperArgv builds the child's argv: env(1) sets the sentinel (and the
// attempts-file path) for the one process it execs, which is this same test
// binary restricted to the anchored helper test. OSRunner sets no cmd.Env, so
// this argv wrapper is the only way the sentinel reaches the child.
func helperArgv(t *testing.T, attemptsFile string) []string {
	t.Helper()
	envBin, err := exec.LookPath("env")
	if err != nil || !filepath.IsAbs(envBin) || filepath.Base(envBin) != "env" {
		t.Fatalf("need an absolute path to env(1): %q, %v", envBin, err)
	}
	if !filepath.IsAbs(attemptsFile) || !isUnderTempDir(attemptsFile) {
		t.Fatalf("attempts file %q is not an absolute path under the temp root %q; the helper would refuse it", attemptsFile, os.TempDir())
	}
	self := os.Args[0]
	if !filepath.IsAbs(self) {
		t.Fatalf("test binary path %q is not absolute", self)
	}
	return []string{
		envBin,
		selfTerminateSentinelEnv + "=" + selfTerminateSentinelValue,
		selfTerminateAttemptsEnv + "=" + attemptsFile,
		self,
		"-test.run=^" + selfTerminateHelperName + "$",
	}
}

var metricLabelRE = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)="([^"]*)"`)

// seriesValue returns the value of every exposition line for metric name whose
// labels include all of want, matching per label (label order and extra
// otel_scope_* labels vary, so no fixed-string match).
func seriesValue(body, name string, want map[string]string) []float64 {
	var out []float64
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		open, closeIdx := strings.Index(line, "{"), strings.LastIndex(line, "}")
		if open < 0 || closeIdx < open {
			continue
		}
		got := map[string]string{}
		for _, m := range metricLabelRE.FindAllStringSubmatch(line[open+1:closeIdx], -1) {
			got[m[1]] = m[2]
		}
		ok := true
		for k, v := range want {
			if got[k] != v {
				ok = false
			}
		}
		if !ok {
			continue
		}
		fields := strings.Fields(line[closeIdx+1:])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// TestKilledHandlerDispatch_RetriedAsKilledAndExportedOnMetrics is the
// end-to-end proof described at the top of the file. It never self-skips under
// -short.
func TestKilledHandlerDispatch_RetriedAsKilledAndExportedOnMetrics(t *testing.T) {
	// Recursion guard (S5): the sentinel must NOT be in this process's own
	// environment. If it is, this process is itself a helper child and running
	// the parent would spawn yet another child.
	if v, set := os.LookupEnv(selfTerminateSentinelEnv); set {
		t.Fatalf("recursion guard: %s is already set (%q) in the test process's own environment", selfTerminateSentinelEnv, v)
	}

	attemptsFile := filepath.Join(t.TempDir(), "attempts.log")

	// Real exporter on an ephemeral loopback port.
	var ln net.Listener
	mp, shutdown, err := startMetricsServer("127.0.0.1:0", func(l net.Listener) { ln = l })
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})
	if ln == nil {
		t.Fatal("onListen was never called")
	}
	if host, _, err := net.SplitHostPort(ln.Addr().String()); err != nil || host != "127.0.0.1" {
		t.Fatalf("metrics listener bound to %q, want loopback 127.0.0.1 only (%v)", ln.Addr(), err)
	}

	// The catalog exactly as run.go wires it for dispatch retries.
	var q *eventqueue.Queue
	emitter, err := metrics.New(mp, func() map[string]int { return q.DepthByType() })
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}

	// Virtual queue clock: backoff is crossed by advancing it, never by sleeping.
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); defer clockMu.Unlock(); now = now.Add(d) }

	policy := backoff.Policy{Initial: 5 * time.Second, Factor: 2, Max: time.Minute}
	q, err = eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(clock), eventqueue.WithRetryBackoff(policy))
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}

	dir := t.TempDir()
	cfg := config.Config{RetryBackoff: policy, WorktreeDir: dir, LogDir: dir}
	argv := helperArgv(t, attemptsFile)
	handler := &recordingHandler{inner: wireclient.New(func(roles.Role, string) ([]string, error) {
		return slices.Clone(argv), nil
	})}

	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: handler, DispatchRetryObserver: emitter}
	// The queue's own listener context. Cancelling it is the only cleanup: it
	// makes exec.CommandContext cancel just the direct child OSRunner started.
	runCtx, cancelRun := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(func() {
		cancelRun()
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		q.WaitForInFlightDrain(drainCtx, 10*time.Millisecond)
	})
	role := roles.Role{Name: retryRole, Enabled: true, Binds: []string{retryEventType}, MaxDispatchRetries: maxRetries}
	q.Register(o.NewListener(runCtx, role))

	// A FUTURE expiresAt is what opens the retry window; without it the event
	// is born expired and gets exactly one attempt.
	at := clock()
	if _, err := q.Enqueue(eventqueue.Event{
		ID: "pr-1", Type: retryEventType, At: at, ExpiresAt: at.Add(24 * time.Hour),
		Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Drive passes. Each pass is bounded: at most one offer per pass, the whole
	// loop is capped, so a regression cannot spawn an unbounded number of
	// children.
	q.Dispatch()
	for range 8 {
		advance(time.Minute)
		q.Dispatch()
		q.Expire()
	}

	// Attempts, counted from the helper's own file: 1 original + maxRetries.
	raw, err := os.ReadFile(attemptsFile)
	if err != nil {
		t.Fatalf("read attempts file (did the helper child ever run?): %v", err)
	}
	if got := strings.Count(string(raw), "attempt\n"); got != 1+maxRetries {
		t.Fatalf("handler attempts = %d, want %d (1 original + max_dispatch_retries %d)", got, 1+maxRetries, maxRetries)
	}

	// Every attempt surfaced as the typed exit -1 error (the real signalled-child path).
	errs := handler.snapshot()
	if len(errs) != 1+maxRetries {
		t.Fatalf("recorded dispatches = %d, want %d", len(errs), 1+maxRetries)
	}
	for i, e := range errs {
		var ee *wireclient.ExitError
		if !errors.As(e, &ee) || ee.Code != -1 {
			t.Fatalf("dispatch %d error = %v (%T), want *wireclient.ExitError with Code -1", i+1, e, e)
		}
	}

	// The event settled: the queue holds no deliverable (event, listener) pair
	// any more. (DepthByType is NOT the check: it counts retained events, and a
	// settled event stays retained until its expiry.)
	if !q.Idle() {
		t.Fatalf("queue is not idle after the retries ran out: the event never settled (depth %v)", q.DepthByType())
	}

	// A real scrape shows the counter, class killed, this role, value maxRetries.
	if err := forceFlush(context.Background(), mp); err != nil {
		t.Fatalf("force flush: %v", err)
	}
	body := scrape(t, fmt.Sprintf("http://%s/metrics", ln.Addr().String()))
	vals := seriesValue(body, metrics.MetricDispatchRetries+"_total", map[string]string{"class": orchestrator.RetryClassKilled, "role": retryRole})
	if len(vals) != 1 || vals[0] != float64(maxRetries) {
		t.Fatalf("pg_router_dispatch_retries_total{class=%q,role=%q} = %v, want exactly one series with value %d; scrape:\n%s",
			orchestrator.RetryClassKilled, retryRole, vals, maxRetries, body)
	}
}
