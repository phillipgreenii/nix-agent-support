//go:build integration && unix

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/metrics"
)

// End-to-end check of source acknowledgement after handler acceptance (the
// opt-in path of a cursor-feed query that declares `settle`), against real
// processes:
//
//   - a real router (this test binary re-executed as `pg-router run`, the same
//     helper the shutdown re-offer test uses),
//   - the real pg-router-source-pg-connector adapter binary (built by
//     buildAdapterBinary), run with --deferred-ack as the query command and as
//     its settle command,
//   - a fake pg-connector (a /bin/sh script found first on the adapter's PATH)
//     that models the daemon feed: a cursor that moves only on changes-ack, and
//     a poll that returns the one change while the cursor is behind it,
//   - a fake dispatcher (a /bin/sh handler) that declines (exit 9) until told to
//     accept.
//
// Nothing here touches a live daemon, a real pg-connector or the network: every
// path, the socket and the metrics port (127.0.0.1, ephemeral) are scratch.
// The only processes signalled are the router children this test started
// itself, through cmd.Process. The router is an integration-tier process, so the
// file carries the integration tag and runs only in
// integration-checks.pg-router-integration-tests (never the unit gate).

const (
	ackQueryName   = "pr-changes"
	ackEventType   = "pr.changed"
	ackRole        = "ack-handler"
	ackRetryWindow = 3 * time.Second
)

// ackHarness is the scratch state shared by every router process of one test.
type ackHarness struct {
	t          *testing.T
	base       string // short, so the router's unix socket path stays within its length limit
	logDir     string
	configPath string
	feed       string // the fake pg-connector's state dir; log holds one ordered line per observed step
	metrics    string // host:port of the router's /metrics endpoint
}

// buildAdapterBinary compiles the REAL cmd/pg-router-source-pg-connector main
// package, the artifact operators run, next to this package.
func buildAdapterBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pg-router-source-pg-connector")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "../pg-router-source-pg-connector")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/pg-router-source-pg-connector: %v\n%s", err, out)
	}
	return bin
}

func newAckHarness(t *testing.T, adapterBin string) *ackHarness {
	t.Helper()
	h := &ackHarness{t: t, base: shortDir(t)}
	h.logDir = filepath.Join(h.base, "state")
	h.feed = filepath.Join(h.base, "feed")
	h.configPath = filepath.Join(h.base, "config.toml")
	bin := filepath.Join(h.base, "bin")
	for _, d := range []string{h.logDir, h.feed, bin, filepath.Join(h.base, "home"), filepath.Join(h.base, "repo")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// The fake pg-connector. A poll (always with --no-ack on an opted-in query)
	// returns the one change while the cursor is behind its seq, otherwise an
	// empty answer positioned at the cursor. changes-ack moves the cursor; it is
	// refused while the block-ack marker exists. A poll WITHOUT --no-ack would
	// acknowledge at emit, which is the loss path under test, so it is logged
	// as implicit-ack and moves the cursor.
	fake := "#!/bin/sh\n" +
		"D='" + h.feed + "'\n" +
		"verb=\"$2\"; shift 2\n" +
		"next=''; noack=0\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --next-seq) next=\"$2\"; shift;;\n" +
		"    --no-ack) noack=1;;\n" +
		"  esac\n" +
		"  shift\n" +
		"done\n" +
		"cursor=0\n" +
		"[ -f \"$D/cursor\" ] && cursor=$(cat \"$D/cursor\")\n" +
		"case \"$verb\" in\n" +
		"changes)\n" +
		"  echo \"poll cursor=$cursor\" >> \"$D/log\"\n" +
		"  if [ \"$cursor\" -lt 1 ]; then\n" +
		"    if [ \"$noack\" = 0 ]; then echo 1 > \"$D/cursor\"; echo implicit-ack >> \"$D/log\"; fi\n" +
		"    printf '%s\\n' '{\"sources\":[{\"backend\":\"gh\",\"status\":\"succeeded\",\"version\":1,\"truncated\":false,\"next_seq\":1}],\"changes\":[{\"change\":\"changed\",\"source\":\"gh\",\"seq\":1,\"kinds\":[],\"fields\":[],\"entity\":{\"id\":\"o/r#1\",\"title\":\"T\",\"version\":\"v1\"}}]}'\n" +
		"  else\n" +
		"    printf '%s\\n' \"{\\\"sources\\\":[{\\\"backend\\\":\\\"gh\\\",\\\"status\\\":\\\"succeeded\\\",\\\"version\\\":1,\\\"truncated\\\":false,\\\"next_seq\\\":$cursor}],\\\"changes\\\":[]}\"\n" +
		"  fi\n" +
		"  ;;\n" +
		"changes-ack)\n" +
		"  if [ -f \"$D/block-ack\" ]; then\n" +
		"    echo \"ack-refused $next\" >> \"$D/log\"\n" +
		"    echo 'pg-connector: changes_ack refused' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"  echo \"$next\" > \"$D/cursor\"\n" +
		"  echo \"ack $next\" >> \"$D/log\"\n" +
		"  echo '{}'\n" +
		"  ;;\n" +
		"*) echo \"unexpected verb $verb\" >&2; exit 98;;\n" +
		"esac\n"
	writeExec(t, filepath.Join(bin, "pg-connector"), fake)

	// The query command and the settle command both go through this wrapper:
	// the router gives a command query a minimal PATH, so the fake pg-connector
	// is put first on the adapter's PATH here.
	wrapper := filepath.Join(h.base, "adapter.sh")
	writeExec(t, wrapper, "#!/bin/sh\nPATH='"+bin+":"+os.Getenv("PATH")+"'\nexport PATH\nexec '"+adapterBin+"' \"$@\"\n")

	// The fake dispatcher: declines (exit 9, busy) until the accept marker
	// exists, then accepts. Every attempt is logged in the shared ordered log.
	handler := filepath.Join(h.base, "handler.sh")
	writeExec(t, handler, "#!/bin/sh\n"+
		"D='"+h.feed+"'\n"+
		"cat > /dev/null\n"+
		"case \"$1\" in\n"+
		"dispatch)\n"+
		"  if [ -f \"$D/accept\" ]; then\n"+
		"    echo accepted >> \"$D/log\"\n"+
		"    echo '{\"schemaVersion\":\"1\",\"id\":\"x\",\"outcome\":\"ok\"}'\n"+
		"    exit 0\n"+
		"  fi\n"+
		"  echo declined >> \"$D/log\"\n"+
		"  echo '{\"schemaVersion\":\"1\",\"reason\":\"busy\"}'\n"+
		"  exit 9\n"+
		"  ;;\n"+
		"esac\n"+
		"echo '{\"schemaVersion\":\"1\",\"id\":\"x\",\"outcome\":\"ok\"}'\n")

	cfg := fmt.Sprintf(`[[query]]
name = "%[1]s"
emits = ["%[2]s"]
type = "command"
settle = ["%[3]s", "settle", "pr", "q", "--consumer", "c1"]
[query.trigger]
kind = "period"
every = "1s"
[query.command]
argv = ["%[3]s", "changes", "pr", "q", "--consumer", "c1", "--retry-window", "%[4]s", "--deferred-ack"]
format = "json"

[[role]]
name = "%[5]s"
enabled = true
binds = ["%[2]s"]
handler = "%[6]s"
`, ackQueryName, ackEventType, wrapper, ackRetryWindow, ackRole, handler)
	if err := os.WriteFile(h.configPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	h.metrics = ln.Addr().String()
	_ = ln.Close()
	return h
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// lines returns the ordered log of the fake feed and the fake dispatcher.
func (h *ackHarness) lines() []string {
	data, err := os.ReadFile(filepath.Join(h.feed, "log"))
	if err != nil {
		return nil
	}
	return strings.Fields(strings.ReplaceAll(string(data), " ", "_"))
}

func (h *ackHarness) count(prefix string) int {
	n := 0
	for _, l := range h.lines() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// first returns the index of the first log line with the prefix, or -1.
func (h *ackHarness) first(prefix string) int {
	for i, l := range h.lines() {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func (h *ackHarness) mark(name string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.feed, name), []byte("1\n"), 0o644); err != nil {
		h.t.Fatalf("write marker %s: %v", name, err)
	}
}

func (h *ackHarness) unmark(name string) {
	h.t.Helper()
	if err := os.Remove(filepath.Join(h.feed, name)); err != nil {
		h.t.Fatalf("remove marker %s: %v", name, err)
	}
}

func (h *ackHarness) waitFor(d time.Duration, msg string, r *router, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			stderr := ""
			if r != nil {
				stderr = r.stderr.String()
			}
			ledger, _ := os.ReadFile(filepath.Join(h.logDir, "ack-ledger.json"))
			h.t.Fatalf("timed out after %v waiting for %s\nlog: %v\nledger: %s\nrouter stderr:\n%s", d, msg, h.lines(), ledger, stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *ackHarness) startAckRouter() *router {
	h.t.Helper()
	self := os.Args[0]
	if !filepath.IsAbs(self) {
		h.t.Fatalf("test binary path %q is not absolute", self)
	}
	cmd := exec.CommandContext(context.Background(), self, "-test.run=^"+routerHelperName+"$")
	// A complete, explicit environment: nothing of the operator's PG_ROUTER_* or
	// XDG state reaches the child.
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
		"PG_ROUTER_POLL_INTERVAL=1",
		// A loaded host must not skip the producer ticks this test depends on.
		"PG_ROUTER_LOAD_BACKOFF_THRESHOLD=0",
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
			r.done <- nil
		default:
			_ = cmd.Process.Kill()
			<-r.done
		}
	})
	return r
}

// crash SIGKILLs the router child (no shutdown path runs) and waits for it.
func (h *ackHarness) crash(r *router) {
	h.t.Helper()
	if err := r.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		h.t.Fatalf("SIGKILL the router child: %v", err)
	}
	select {
	case err := <-r.done:
		r.done <- err
	case <-time.After(30 * time.Second):
		h.t.Fatalf("router did not die after SIGKILL")
	}
}

func (h *ackHarness) stopAckRouter(r *router) {
	h.t.Helper()
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		h.t.Fatalf("SIGTERM the router child: %v", err)
	}
	select {
	case err := <-r.done:
		r.done <- err
	case <-time.After(90 * time.Second):
		h.t.Fatalf("router did not exit within 90s of SIGTERM\nstderr:\n%s", r.stderr.String())
	}
}

func (h *ackHarness) scrape() (string, error) {
	client := http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+h.metrics+"/metrics", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// metricSum is the sum of every series of name (0 when the counter never
// incremented and so has no series).
func (h *ackHarness) metricSum(body, name string) float64 {
	var sum float64
	for _, v := range seriesValue(body, name, map[string]string{}) {
		sum += v
	}
	return sum
}

func ackIntegrationPreconditions(t *testing.T) {
	t.Helper()
	if _, set := os.LookupEnv(routerSentinelEnv); set {
		t.Fatalf("recursion guard: %s is already set in the test process's own environment", routerSentinelEnv)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
}

// TestAckAfterAccept_DeclinePastRetryWindowThenAccept is the success criterion
// of the design: a dispatcher declines for longer than the retry window and then
// accepts. The event must be delivered exactly once after the outage, the source
// cursor must move only after acceptance (never at emit), the event must survive
// past its retry window (re-opened by the next poll, not lost), and nothing may be abandoned.
func TestAckAfterAccept_DeclinePastRetryWindowThenAccept(t *testing.T) {
	ackIntegrationPreconditions(t)
	h := newAckHarness(t, buildAdapterBinary(t))
	r := h.startAckRouter()

	// The outage: the dispatcher declines until a decline is seen longer than the
	// retry window after the first one, i.e. the outage outlasted the window and
	// the held event was still being offered (its window re-opened by the next
	// poll) instead of being dropped at expiresAt. While a lane is held the core
	// never retires the event, so listener_expired_pending stays 0 here: a
	// retirement only happens through the hold limit, which this test must not
	// reach.
	var firstDecline time.Time
	h.waitFor(120*time.Second, "a decline after the retry window has passed", r, func() bool {
		if firstDecline.IsZero() {
			if h.count("declined") >= 1 {
				firstDecline = time.Now()
			}
			return false
		}
		return time.Since(firstDecline) > ackRetryWindow+time.Second && h.count("declined") >= 2
	})
	if n := h.count("declined"); n < 1 {
		t.Fatalf("the dispatcher declined %d times, want at least 1; log: %v", n, h.lines())
	}
	if n := h.count("accepted"); n != 0 {
		t.Fatalf("accepted %d times during the outage, want 0", n)
	}
	// The cursor did not move at emit nor during the outage: no acknowledgement
	// of any kind, and no poll that acknowledged by itself.
	if i := h.first("ack"); i >= 0 {
		t.Fatalf("an acknowledgement reached the feed during the outage (index %d): %v", i, h.lines())
	}
	if n := h.count("implicit-ack"); n != 0 {
		t.Fatalf("a poll acknowledged at emit (%d times); the adapter must pass --no-ack: %v", n, h.lines())
	}
	// A replay is a fresh offer after expiry: the feed was polled again while
	// the cursor stayed behind the change.
	if n := h.count("poll_cursor=0"); n < 2 {
		t.Fatalf("polls at cursor 0 = %d, want at least 2 (the expired event is re-offered): %v", n, h.lines())
	}

	// The outage ends.
	h.mark("accept")
	h.waitFor(120*time.Second, "the settle report to acknowledge the feed after acceptance", r, func() bool {
		return h.first("ack_1") >= 0
	})
	acceptedAt, ackAt := h.first("accepted"), h.first("ack_1")
	if acceptedAt < 0 || ackAt < acceptedAt {
		t.Fatalf("the cursor was acknowledged (index %d) before the handler accepted (index %d): %v", ackAt, acceptedAt, h.lines())
	}

	// Delivered exactly once: let several more polls and a full retry window
	// pass, then count accepts.
	pollsBefore := h.count("poll_cursor=1")
	h.waitFor(60*time.Second, "later polls to find the feed caught up", r, func() bool {
		return h.count("poll_cursor=1") >= pollsBefore+2
	})
	time.Sleep(ackRetryWindow)
	if n := h.count("accepted"); n != 1 {
		t.Fatalf("handler accepted %d times, want exactly 1 (delivered once after the outage): %v", n, h.lines())
	}
	if n := h.count("implicit-ack"); n != 0 {
		t.Fatalf("a poll acknowledged at emit: %v", h.lines())
	}

	body, err := h.scrape()
	if err != nil {
		t.Fatalf("scraping /metrics: %v", err)
	}
	if !strings.Contains(body, "pg_router_") {
		t.Fatalf("scrape returned no pg_router_ series, so it proves nothing:\n%s", body)
	}
	if v := h.metricSum(body, metrics.MetricSourceAckAbandoned+"_total"); v != 0 {
		t.Errorf("pg_router_source_ack_abandoned_total = %v, want 0 (nothing lost to the hold limit)\nscrape:\n%s", v, body)
	}
	h.stopAckRouter(r)
}

// TestAckAfterAccept_CrashBetweenAcceptAndSettle is the crash point of design
// section 6 where the handler accepted, the router recorded the acceptance and
// the report to the source had not succeeded: the router is killed (SIGKILL, no
// shutdown path) while the feed refuses every acknowledgement. The restarted
// router must re-report the frontier, the cursor must then move, and the event
// must be neither lost nor delivered to the handler a second time.
func TestAckAfterAccept_CrashBetweenAcceptAndSettle(t *testing.T) {
	ackIntegrationPreconditions(t)
	h := newAckHarness(t, buildAdapterBinary(t))
	h.mark("block-ack")
	h.mark("accept")
	r1 := h.startAckRouter()
	h.waitFor(120*time.Second, "the handler to accept and a settle report to be refused", r1, func() bool {
		return h.count("accepted") >= 1 && h.count("ack-refused") >= 1
	})
	if h.first("ack_") >= 0 {
		t.Fatalf("the feed acknowledged before the crash although every ack was refused: %v", h.lines())
	}
	h.crash(r1)
	acceptedBefore := h.count("accepted")
	if acceptedBefore != 1 {
		t.Fatalf("handler accepted %d times before the crash, want 1: %v", acceptedBefore, h.lines())
	}

	// Restart on the same durable state with the feed healthy again.
	h.unmark("block-ack")
	r2 := h.startAckRouter()
	h.waitFor(120*time.Second, "the restarted router to re-report the frontier", r2, func() bool {
		return h.first("ack_1") >= 0
	})
	if n := h.count("implicit-ack"); n != 0 {
		t.Fatalf("a poll acknowledged at emit: %v", h.lines())
	}
	// Let a few polls pass: the event, now behind the cursor, must not be
	// delivered again.
	pollsBefore := h.count("poll_cursor=1")
	h.waitFor(60*time.Second, "polls to find the feed caught up after the restart", r2, func() bool {
		return h.count("poll_cursor=1") >= pollsBefore+2
	})
	if n := h.count("accepted"); n != acceptedBefore {
		t.Errorf("handler accepted %d times in total, want %d: the accepted event was delivered again after the restart: %v", n, acceptedBefore, h.lines())
	}
	h.stopAckRouter(r2)
}
