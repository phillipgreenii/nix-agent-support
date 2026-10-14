package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The client is tested as the real binary: built with go build, run as a child
// with an empty environment and working directory "/", against an in-process
// daemon on a short temp socket (the real process shell, the real socket
// server, the real router), the way the umbrella execs it.

const (
	exitUnavailable     = 4 // scriptout.ExitCodeForCode("unavailable")
	exitUnknownOp       = 5
	exitVersionMismatch = 6
	exitInvalidArgument = 7
)

var (
	buildMu   sync.Mutex
	buildRoot string
	buildDirs = map[string]string{}
)

// removeBuilds deletes the built binaries; TestMain calls it after the run.
func removeBuilds() {
	buildMu.Lock()
	defer buildMu.Unlock()
	if buildRoot != "" {
		_ = os.RemoveAll(buildRoot)
	}
}

// buildClient builds cmd/pg-connector-github with the given -ldflags and
// returns the binary. One build per distinct flag set per test process.
func buildClient(t *testing.T, ldflags string) string {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	if bin, ok := buildDirs[ldflags]; ok {
		return bin
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go toolchain on PATH to build the real binary: %v", err)
	}
	if buildRoot == "" {
		root, err := os.MkdirTemp("", "pcgbin")
		if err != nil {
			t.Fatal(err)
		}
		buildRoot = root
	}
	bin := filepath.Join(buildRoot, strconv.Itoa(len(buildDirs)), "pg-connector-github")
	args := []string{"build", "-o", bin}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, ".")
	cmd := exec.Command(goBin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	buildDirs[ldflags] = bin
	return bin
}

// fastBudget is the call budget the "fast" binary gives every op: the retry
// window is half of it. The real default (30 s) would make every daemon-down
// case wait 15 s.
const fastBudget = "3s"

func fastClient(t *testing.T) string {
	return buildClient(t, "-X main.clientBudgetOverride="+fastBudget)
}

// budget is fastBudget as a duration; the client retries a refused connection
// for half of it and never longer than all of it.
func budget() time.Duration {
	d, err := time.ParseDuration(fastBudget)
	if err != nil {
		panic(err)
	}
	return d
}

func retryWindow() time.Duration { return budget() / 2 }

// retryWindowSlack is what the client's own measured retry time may fall
// short of the window: it reads the clock at the top of its loop, a hair
// before the last wait is cut to the window's end, and logs whole milliseconds.
const retryWindowSlack = 100 * time.Millisecond

// retryAnswerSlack is how far past the retry window the client's last connect
// may land: the final attempt is made at the window's end.
const retryAnswerSlack = 250 * time.Millisecond

// clientLogDuration returns the duration_ms of the want-th client-log row (the
// kind=request, daemon_unreachable row the client appends when it answers
// unavailable), after checking there are exactly want rows. It is measured
// inside the client from the start of its call, so unlike the test's own wall
// clock it excludes exec and scheduling delay before the client's main runs.
func clientLogDuration(t *testing.T, stateDir string, want int) time.Duration {
	t.Helper()
	raw, err := os.ReadFile(logs.ClientLogPath(stateDir))
	if err != nil {
		t.Fatalf("the client log is the observable of how long the client retried: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != want {
		t.Fatalf("client log has %d rows, want %d: %s", len(lines), want, raw)
	}
	var row struct {
		Kind       string `json:"kind"`
		Outcome    string `json:"outcome"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(lines[want-1]), &row); err != nil {
		t.Fatalf("client log row %d: %v: %s", want, err, lines[want-1])
	}
	if row.Kind != logs.KindRequest || row.Outcome != logs.OutcomeDaemonUnreachable {
		t.Fatalf("client log row %d is not a daemon_unreachable request: %s", want, lines[want-1])
	}
	return time.Duration(row.DurationMS) * time.Millisecond
}

type clientResult struct {
	code   int
	stdout string
	stderr string
}

// runClient runs bin in mode ("pr" or "ci") with stdin and an empty environment.
func runClientBinary(t *testing.T, bin, cfgPath, mode, stdin string) clientResult {
	t.Helper()
	cmd := exec.Command(bin, "--config", cfgPath, mode)
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := clientResult{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	default:
		t.Fatal(err)
	}
	return res
}

func (r clientResult) errorBody(t *testing.T) scriptout.Error {
	t.Helper()
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(r.stdout), &resp); err != nil || resp.Error == nil {
		t.Fatalf("want an error response, got stdout %q stderr %q (%v)", r.stdout, r.stderr, err)
	}
	return *resp.Error
}

// daemon is an in-process daemon: the real shell, socket server and router.
type daemon struct {
	router *router.Router
	cancel context.CancelFunc
	done   chan struct{}
}

type daemonOpts struct {
	protocol int
	peerUID  func(net.Conn) (uint32, error)
	ownUID   func() uint32
	// onCall, when set, sees every call the daemon dispatches, as the client
	// framed it (its stated deadline included), before the router answers.
	onCall func(router.Call)
}

// recordingDispatcher passes each call to seen, then to the router.
type recordingDispatcher struct {
	next socket.Dispatcher
	seen func(router.Call)
}

func (d recordingDispatcher) Dispatch(ctx context.Context, c router.Call) scriptout.Response {
	d.seen(c)
	return d.next.Dispatch(ctx, c)
}

// startDaemonAt serves a router with a "list" op (and nothing else) on
// socketPath, which the caller's config names.
func startDaemonAt(t *testing.T, stateDir, socketPath string, o daemonOpts) *daemon {
	t.Helper()
	r := router.New(router.Options{Instances: []string{"pr", "ci"}, CheckConfig: func(block json.RawMessage) error {
		return config.CheckBlock(&config.Config{Path: "/the/daemon/config.json", Hash: "daemon-hash"}, block)
	}})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
		return map[string]any{"items": []string{}, "args": args}, nil
	}})
	srv, err := proc.Start(proc.Config{StateDir: stateDir, SocketPath: socketPath, DrainTimeout: time.Second, PeerUID: o.peerUID, OwnUID: o.ownUID})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{router: r, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		var dispatcher socket.Dispatcher = r
		if o.onCall != nil {
			dispatcher = recordingDispatcher{next: r, seen: o.onCall}
		}
		_ = srv.Serve(ctx, socket.Handler(dispatcher, socket.ServerConfig{Build: "test-daemon", Protocol: o.protocol}), nil)
	}()
	t.Cleanup(func() { cancel(); <-d.done })
	return d
}

func clientSetup(t *testing.T) (cfgPath, stateDir, socketPath string) {
	t.Helper()
	stateDir = shortTempDir(t)
	socketPath = filepath.Join(stateDir, "s")
	return writeServeConfig(t, stateDir, socketPath), stateDir, socketPath
}

const listRequest = `{"op":"list","args":{"query":"mine"}}`

func TestClientBinaryDaemonAbsent(t *testing.T) {
	bin := fastClient(t)
	cfg, stateDir, sock := clientSetup(t)
	hint := proc.RestartHint(config.DefaultSupervisorLabel, cfg)

	for i, req := range []string{listRequest, `{"op":"show","args":{"id":"x"}}`, `{"op":"changes"}`, `{"op":"auth_status"}`} {
		res := runClientBinary(t, bin, cfg, "pr", req)
		e := res.errorBody(t)
		if e.Code != "unavailable" || res.code != exitUnavailable {
			t.Errorf("%s: code %q exit %d (stdout %q)", req, e.Code, res.code, res.stdout)
		}
		for _, want := range []string{sock, hint} {
			if !strings.Contains(e.Message, want) {
				t.Errorf("%s: message %q must name %q", req, e.Message, want)
			}
		}
		if strings.Contains(res.stdout, "served_from") || strings.Contains(res.stdout, "cache") {
			t.Errorf("%s: a down daemon never answers from a cache: %s", req, res.stdout)
		}
		// It retried for the retry window (half the budget), and no longer than
		// the budget. The time is the client's own, from its client-log row, so
		// process start-up and host load before main do not count.
		if took := clientLogDuration(t, stateDir, i+1); took < retryWindow()-retryWindowSlack || took >= budget() {
			t.Errorf("%s: retried for %v, want the %v retry window (half the %v budget) and under the budget", req, took, retryWindow(), budget())
		}
	}
}

func TestClientBinaryCapabilitiesWithoutTheDaemon(t *testing.T) {
	bin := buildClient(t, "") // the unmodified build: nothing is waited on
	cfg, _, _ := clientSetup(t)
	for mode, inst := range map[string]client.Instance{"pr": client.PR(), "ci": client.CI()} {
		start := time.Now()
		res := runClientBinary(t, bin, cfg, mode, `{"op":"capabilities"}`)
		// A forwarded call to the absent daemon would retry for half the op's
		// production budget and then answer unavailable; so answering at all
		// (exit 0) is the proof, and waiting out that retry window first
		// would be the regression. The ceiling is that window, not a guess at
		// host speed: nothing short of waiting reaches it.
		if res.code != 0 || time.Since(start) > client.BudgetFor(scriptout.OpCapabilities)/2 {
			t.Fatalf("%s: exit %d after %v: %s %s", mode, res.code, time.Since(start), res.stdout, res.stderr)
		}
		var caps scriptout.CapabilitiesResponse
		if err := json.Unmarshal([]byte(res.stdout), &caps); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(caps.Ops, inst.OpNames()) {
			t.Errorf("%s: ops %v, want the compiled-in table %v", mode, caps.Ops, inst.OpNames())
		}
		if caps.Vocabulary["cache_opt_out"] != true || caps.Vocabulary["owns_changes"] != true || caps.ProtocolVersion != scriptout.ProtocolVersion {
			t.Errorf("%s: %+v", mode, caps)
		}
		if caps.Version != "dev" || len(caps.SchemaVersions) == 0 {
			t.Errorf("%s: %+v", mode, caps)
		}
	}
}

func TestClientBinaryForwardsEverythingToTheDaemon(t *testing.T) {
	bin := fastClient(t)
	cfg, state, sock := clientSetup(t)
	startDaemonAt(t, state, sock, daemonOpts{})

	res := runClientBinary(t, bin, cfg, "pr", listRequest)
	if res.code != 0 {
		t.Fatalf("exit %d: %s %s", res.code, res.stdout, res.stderr)
	}
	var resp struct {
		Result struct {
			Args map[string]string `json:"args"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &resp); err != nil || resp.Result.Args["query"] != "mine" {
		t.Errorf("the daemon's answer is relayed: %s (%v)", res.stdout, err)
	}

	// pr show is not an op this phase's client lists, and no handler is
	// registered for it: the client forwards it and the daemon says unknown_op.
	res = runClientBinary(t, bin, cfg, "pr", `{"op":"show","args":{}}`)
	if e := res.errorBody(t); e.Code != "unknown_op" || res.code != exitUnknownOp {
		t.Errorf("show: %+v exit %d", e, res.code)
	}
	// The ci instance has nothing registered yet: an op not delivered is unknown_op.
	res = runClientBinary(t, bin, cfg, "ci", `{"op":"list_runs"}`)
	if e := res.errorBody(t); e.Code != "unknown_op" || res.code != exitUnknownOp {
		t.Errorf("ci with no handlers: %+v exit %d", e, res.code)
	}

	// A stale registry fails loudly: a block stamped with another hash.
	res = runClientBinary(t, bin, cfg, "pr", `{"op":"list","config":{"config_hash":"other","config_path":"/the/umbrella/block.json"}}`)
	if e := res.errorBody(t); e.Code != "invalid_argument" || !strings.Contains(e.Message, "/the/umbrella/block.json") || !strings.Contains(e.Message, "/the/daemon/config.json") {
		t.Errorf("config hash mismatch: %+v", e)
	}
	// No hash, or no block, is accepted.
	for _, req := range []string{`{"op":"list","config":{"queries":{}}}`, `{"op":"list"}`} {
		if res := runClientBinary(t, bin, cfg, "pr", req); res.code != 0 {
			t.Errorf("%s: %s", req, res.stdout)
		}
	}
}

func TestClientBinaryRelaysTheMigratingAnswer(t *testing.T) {
	bin := fastClient(t)
	cfg, state, sock := clientSetup(t)
	d := startDaemonAt(t, state, sock, daemonOpts{})
	d.router.SetMigrating(true, "store schema 2 to 3")
	res := runClientBinary(t, bin, cfg, "pr", listRequest)
	if e := res.errorBody(t); e.Code != "unavailable" || !strings.HasPrefix(e.Message, "migrating:") || res.code != exitUnavailable {
		t.Errorf("migrating: %+v exit %d", e, res.code)
	}
}

func TestClientBinaryProtocolWindow(t *testing.T) {
	bin := fastClient(t)
	for _, tc := range []struct {
		name     string
		daemon   int // the daemon's protocol N; the binary speaks socket.ProtocolVersion (1)
		wantCode string
		wantExit int
	}{
		{"same version", 1, "", 0},
		{"client is N-1", 2, "", 0},
		{"client is N-2", 3, "version_mismatch", exitVersionMismatch},
		{"daemon far ahead", 9, "version_mismatch", exitVersionMismatch},
	} {
		cfg, state, sock := clientSetup(t)
		startDaemonAt(t, state, sock, daemonOpts{protocol: tc.daemon})
		res := runClientBinary(t, bin, cfg, "pr", listRequest)
		if res.code != tc.wantExit {
			t.Errorf("%s: exit %d, want %d (%s)", tc.name, res.code, tc.wantExit, res.stdout)
			continue
		}
		if tc.wantCode != "" {
			if e := res.errorBody(t); e.Code != tc.wantCode {
				t.Errorf("%s: %+v", tc.name, e)
			}
		}
	}
}

func TestClientBinaryRestartWindowIsRetriedInsideTheDeadline(t *testing.T) {
	bin := fastClient(t)
	cfg, state, sock := clientSetup(t)

	// The daemon reports the deadline the client stated: the client's
	// deadline (budget less ForwardMargin) taken down by the time it spent
	// connecting, measured on the client's own clock from the start of its
	// call. So it excludes process start-up and host load before main. (A
	// client that only starts after the daemon is up connects at once and
	// proves nothing about the retry; that errs towards passing, never
	// towards a host-load failure.)
	stated := make(chan time.Duration, 1)
	got := make(chan clientResult, 1)
	go func() { got <- runClientBinary(t, bin, cfg, "pr", listRequest) }()
	time.Sleep(500 * time.Millisecond) // the supervisor brings the daemon back
	startDaemonAt(t, state, sock, daemonOpts{onCall: func(c router.Call) { stated <- c.Deadline }})

	res := <-got
	if res.code != 0 || !strings.Contains(res.stdout, `"result"`) {
		t.Fatalf("the retry must reach a daemon that starts 500ms after the call: exit %d %s", res.code, res.stdout)
	}
	connecting := budget() - client.ForwardMargin - <-stated
	if connecting > retryWindow()+retryAnswerSlack {
		t.Errorf("the client spent %v connecting, want it inside the %v retry window (half the %v budget)", connecting, retryWindow(), budget())
	}
}

func TestClientBinarySocketPathOverThePlatformLimitIsRefused(t *testing.T) {
	bin := fastClient(t)
	stateDir := shortTempDir(t)
	long := filepath.Join(stateDir, strings.Repeat("d", proc.MaxSocketPathBytes()), "sock")
	cfg := writeServeConfig(t, stateDir, long)

	res := runClientBinary(t, bin, cfg, "pr", listRequest)
	e := res.errorBody(t)
	if e.Code != "unavailable" || res.code != exitUnavailable || !strings.Contains(e.Message, long) || !strings.Contains(e.Message, "sun_path") {
		t.Errorf("%+v exit %d: the over-long path is refused and named", e, res.code)
	}
	if took := clientLogDuration(t, stateDir, 1); took >= retryWindow()-retryWindowSlack {
		t.Errorf("an over-long path is final, not retried (the client spent %v, a retry waits the %v window)", took, retryWindow())
	}
}

func TestClientBinarySocketPermissionIsFinal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores socket permissions")
	}
	bin := fastClient(t)
	cfg, state, sock := clientSetup(t)
	startDaemonAt(t, state, sock, daemonOpts{})
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	res := runClientBinary(t, bin, cfg, "pr", listRequest)
	e := res.errorBody(t)
	if e.Code != "unavailable" || res.code != exitUnavailable || !strings.Contains(e.Message, sock) {
		t.Errorf("%+v exit %d", e, res.code)
	}
	if took := clientLogDuration(t, state, 1); took >= retryWindow()-retryWindowSlack {
		t.Errorf("a permission refusal is not retried (the client spent %v, a retry waits the %v window)", took, retryWindow())
	}
}

func TestClientBinaryPeerUIDRejectionIsUnavailableNamingTheSocket(t *testing.T) {
	bin := fastClient(t)
	cfg, state, sock := clientSetup(t)
	// The daemon believes it runs as another user, so it drops this client.
	startDaemonAt(t, state, sock, daemonOpts{ownUID: func() uint32 { return uint32(os.Getuid()) + 1 }})
	res := runClientBinary(t, bin, cfg, "pr", listRequest)
	e := res.errorBody(t)
	if e.Code != "unavailable" || res.code != exitUnavailable || !strings.Contains(e.Message, sock) {
		t.Errorf("%+v exit %d", e, res.code)
	}
}

func TestClientBinaryOversizeRequestIsRejectedByTheDaemon(t *testing.T) {
	bin := fastClient(t)
	cfg, state, sock := clientSetup(t)
	startDaemonAt(t, state, sock, daemonOpts{})
	big := `{"op":"list","args":"` + strings.Repeat("a", socket.MaxFrameBytes) + `"}`
	res := runClientBinary(t, bin, cfg, "pr", big)
	if e := res.errorBody(t); e.Code != "invalid_argument" || res.code != exitInvalidArgument {
		t.Errorf("%+v exit %d", e, res.code)
	}
}

func TestClientBinaryMalformedStdin(t *testing.T) {
	bin := fastClient(t)
	cfg, _, _ := clientSetup(t)
	for name, in := range map[string]string{"empty": "", "garbage": "][", "no op": `{"args":{}}`} {
		res := runClientBinary(t, bin, cfg, "ci", in)
		if e := res.errorBody(t); res.code != scriptout.ExitCodeForCode(e.Code) || res.code == 0 {
			t.Errorf("%s: %+v exit %d", name, e, res.code)
		}
	}
}
