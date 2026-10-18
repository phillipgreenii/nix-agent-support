package proc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recLogger records every line the shell logs.
type recLogger struct {
	mu    sync.Mutex
	lines []string
}

func (r *recLogger) Log(change string, kv ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, change)
}

func (r *recLogger) has(change string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if l == change {
			return true
		}
	}
	return false
}

// shortDir returns a fresh directory with a short absolute path: a unix socket
// path is capped at about 104 bytes, and t.TempDir can exceed that.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcp")
	if err != nil { // a sandbox without a writable /tmp: fall back to TMPDIR
		d, err = os.MkdirTemp("", "pcp")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func testConfig(t *testing.T) (Config, *recLogger) {
	t.Helper()
	dir := filepath.Join(shortDir(t), "state")
	log := &recLogger{}
	return Config{StateDir: dir, SocketPath: filepath.Join(dir, "sock"), Logger: log}, log
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestStartCreatesStateDirAndSocketWithTheirModes(t *testing.T) {
	cfg, _ := testConfig(t)
	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := mode(t, cfg.StateDir); got != 0o700 {
		t.Errorf("state dir mode = %04o, want 0700", got)
	}
	if got := mode(t, cfg.SocketPath); got != 0o600 {
		t.Errorf("socket mode = %04o, want 0600", got)
	}
}

func TestStartTightensAnExistingLooseStateDirectory(t *testing.T) {
	cfg, _ := testConfig(t)
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := mode(t, cfg.StateDir); got != 0o700 {
		t.Errorf("state dir mode = %04o, want 0700", got)
	}
}

func TestSecondStartOnTheSameStateDirRefusesNamingTheLock(t *testing.T) {
	cfg, log := testConfig(t)
	first, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()

	second, err := Start(cfg)
	if err == nil {
		_ = second.Close()
		t.Fatal("second Start succeeded on a held lock")
	}
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "daemon.lock") {
		t.Errorf("error %q must be ErrLocked and name the lock file", err)
	}
	if !log.has("lock_held") {
		t.Error("the refusal must be logged as lock_held")
	}
	// The refused attempt must not have disturbed the first instance's socket.
	if c, err := net.Dial("unix", cfg.SocketPath); err != nil {
		t.Errorf("first instance's socket is gone: %v", err)
	} else {
		_ = c.Close()
	}
}

func TestStartRemovesAStaleSocketLeftByADeadInstance(t *testing.T) {
	cfg, log := testConfig(t)
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A killed process leaves its socket file behind: listen, and close
	// without unlinking.
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.SocketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ul.SetUnlinkOnClose(false)
	_ = ul.Close()
	if fi, err := os.Lstat(cfg.SocketPath); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("fixture: stale socket missing: %v", err)
	}

	s, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start must replace a stale socket: %v", err)
	}
	defer func() { _ = s.Close() }()
	if !log.has("stale_socket_removed") {
		t.Error("removing a stale socket must be logged")
	}
}

func TestStartNeverDeletesANonSocketAtTheSocketPath(t *testing.T) {
	cfg, _ := testConfig(t)
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.SocketPath, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Start(cfg)
	if err == nil {
		_ = s.Close()
		t.Fatal("Start must refuse a regular file at the socket path")
	}
	if b, rerr := os.ReadFile(cfg.SocketPath); rerr != nil || string(b) != "mine" {
		t.Errorf("the file must be left alone, got %q (%v)", b, rerr)
	}
	// The failed Start must not leave the lock held.
	if err := os.Remove(cfg.SocketPath); err != nil {
		t.Fatal(err)
	}
	s, err = Start(cfg)
	if err != nil {
		t.Fatalf("a failed Start left the lock held: %v", err)
	}
	_ = s.Close()
}

func TestCheckSocketPathBoundary(t *testing.T) {
	const limit = 104
	fits := "/" + strings.Repeat("a", limit-2) // limit-1 bytes: path plus NUL fit exactly
	if err := CheckSocketPath(fits, limit); err != nil {
		t.Errorf("a path of %d bytes must fit a %d-byte sun_path: %v", len(fits), limit, err)
	}
	over := fits + "a" // limit bytes: no room for the NUL
	err := CheckSocketPath(over, limit)
	if !errors.Is(err, ErrSocketPathTooLong) {
		t.Fatalf("a path of %d bytes must be refused, got %v", len(over), err)
	}
	if !strings.Contains(err.Error(), "104") {
		t.Errorf("the error must name the limit: %v", err)
	}
}

func TestStartRefusesAPathOverThePlatformLimitAndSaysSo(t *testing.T) {
	cfg, log := testConfig(t)
	limit := MaxSocketPathBytes()
	pad := limit + 1 - len(cfg.StateDir) - 1
	cfg.SocketPath = filepath.Join(cfg.StateDir, strings.Repeat("s", pad))
	if len(cfg.SocketPath) != limit+1 {
		t.Fatalf("fixture: path is %d bytes, want %d", len(cfg.SocketPath), limit+1)
	}
	s, err := Start(cfg)
	if err == nil {
		_ = s.Close()
		t.Fatal("Start accepted an over-limit socket path")
	}
	if !errors.Is(err, ErrSocketPathTooLong) || !strings.Contains(err.Error(), "limit") {
		t.Errorf("error %q must say the path exceeds the limit", err)
	}
	if !log.has("socket_path_too_long") {
		t.Error("the refusal must be logged as socket_path_too_long")
	}
	if _, err := os.Stat(cfg.StateDir); err == nil {
		t.Error("a refused path must not have created the state directory")
	}
}

func TestStartRejectsRelativePaths(t *testing.T) {
	cfg, _ := testConfig(t)
	rel := cfg
	rel.StateDir = "state"
	if s, err := Start(rel); err == nil {
		_ = s.Close()
		t.Error("a relative state directory must be refused")
	}
	rel = cfg
	rel.SocketPath = "sock"
	if s, err := Start(rel); err == nil {
		_ = s.Close()
		t.Error("a relative socket path must be refused")
	}
}

// serveAsync starts Serve and returns a stop function that cancels and waits.
func serveAsync(t *testing.T, s *Server, h Handler, hooks *Hooks) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, h, hooks) }()
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(20 * time.Second):
			t.Fatal("Serve did not return after cancel")
			return nil
		}
	}
}

func TestServeAcceptsAPeerWithTheDaemonsUID(t *testing.T) {
	cfg, _ := testConfig(t)
	s, err := Start(cfg) // the real PeerUID: same process, same uid
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan Peer, 1)
	stop := serveAsync(t, s, func(_ context.Context, c net.Conn, p Peer) {
		got <- p
		_, _ = c.Write([]byte("hi"))
	}, nil)
	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 2)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(buf); err != nil || string(buf) != "hi" {
		t.Fatalf("handler did not answer: %q %v", buf, err)
	}
	if p := <-got; p.UID != uint32(os.Getuid()) {
		t.Errorf("peer uid = %d, want %d", p.UID, os.Getuid())
	}
	if err := stop(); err != nil {
		t.Errorf("Serve: %v", err)
	}
}

func TestServeRejectsAPeerWhoseUIDDiffersAndLogsIt(t *testing.T) {
	cfg, log := testConfig(t)
	cfg.PeerUID = func(net.Conn) (uint32, error) { return uint32(os.Getuid()) + 1, nil }
	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	stop := serveAsync(t, s, func(context.Context, net.Conn, Peer) { called <- struct{}{} }, nil)

	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// The daemon closes a rejected connection: the read ends with EOF.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := conn.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Errorf("a rejected peer must see its connection closed, read n=%d err=%v", n, err)
	}
	if err := stop(); err != nil {
		t.Errorf("Serve: %v", err)
	}
	select {
	case <-called:
		t.Error("the handler must never see a rejected peer")
	default:
	}
	if !log.has("peer_uid_rejected") {
		t.Error("the rejection must be logged as peer_uid_rejected")
	}
}

func TestServeRejectsAPeerWhoseCredentialsCannotBeRead(t *testing.T) {
	cfg, log := testConfig(t)
	cfg.PeerUID = func(net.Conn) (uint32, error) { return 0, errors.New("no credentials") }
	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop := serveAsync(t, s, func(context.Context, net.Conn, Peer) { t.Error("handler ran") }, nil)
	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Read(make([]byte, 1))
	_ = conn.Close()
	_ = stop()
	if !log.has("peer_cred_failed") {
		t.Error("an unreadable credential must be logged as peer_cred_failed")
	}
}

func TestDrainLetsInFlightHandlersFinishThenRunsHooksInOrderThenReleasesTheLock(t *testing.T) {
	cfg, _ := testConfig(t)
	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		order []string
	)
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	entered := make(chan struct{})
	release := make(chan struct{})
	hooks := &Hooks{}
	hooks.Register("first", func(context.Context) error { note("hook-first"); return nil })
	hooks.Register("last", func(context.Context) error { note("hook-last"); return nil })

	stop := serveAsync(t, s, func(_ context.Context, c net.Conn, _ Peer) {
		close(entered)
		<-release
		note("handler-done")
	}, hooks)

	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	<-entered

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()

	// While the handler is in flight the drain must not finish, and a new
	// connection must not be accepted.
	select {
	case <-stopped:
		t.Fatal("drain finished while a handler was in flight")
	case <-time.After(300 * time.Millisecond):
	}
	if c, err := net.DialTimeout("unix", cfg.SocketPath, time.Second); err == nil {
		// The kernel may complete the dial against a closed listener's
		// backlog on some systems; it must at least not be served.
		_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, rerr := c.Read(make([]byte, 1))
		_ = c.Close()
		if rerr == nil {
			t.Error("a connection accepted during drain")
		}
	}

	close(release)
	if err := <-stopped; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	mu.Lock()
	got := strings.Join(order, ",")
	mu.Unlock()
	if want := "handler-done,hook-first,hook-last"; got != want {
		t.Errorf("order = %s, want %s", got, want)
	}

	// The lock is released: a new instance starts at once, and the socket the
	// drained one served is gone.
	s2, err := Start(cfg)
	if err != nil {
		t.Fatalf("a second Start after the drain: %v", err)
	}
	_ = s2.Close()
}

func TestDrainTimeoutCancelsAStuckHandlerAndStillRunsHooks(t *testing.T) {
	cfg, log := testConfig(t)
	cfg.DrainTimeout = 200 * time.Millisecond
	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	hookRan := make(chan struct{})
	hooks := &Hooks{}
	hooks.Register("h", func(context.Context) error { close(hookRan); return nil })
	stop := serveAsync(t, s, func(ctx context.Context, c net.Conn, _ Peer) {
		close(entered)
		<-ctx.Done() // cancelled only when the drain gives up
	}, hooks)
	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	<-entered
	if err := stop(); err != nil {
		t.Errorf("Serve: %v", err)
	}
	select {
	case <-hookRan:
	default:
		t.Error("the hook must run after a drain timeout")
	}
	if !log.has("drain_timeout") {
		t.Error("a drain timeout must be logged")
	}
}

func TestFailedHookIsReportedAndDoesNotSkipLaterHooksOrTheLockRelease(t *testing.T) {
	cfg, _ := testConfig(t)
	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	later := false
	hooks := &Hooks{}
	hooks.Register("bad", func(context.Context) error { return errors.New("boom") })
	hooks.Register("later", func(context.Context) error { later = true; return nil })
	stop := serveAsync(t, s, func(context.Context, net.Conn, Peer) {}, hooks)
	err = stop()
	if err == nil || !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Serve error %v must name the failed hook", err)
	}
	if !later {
		t.Error("a failed hook must not skip later hooks")
	}
	s2, err := Start(cfg)
	if err != nil {
		t.Fatalf("lock not released after a failed hook: %v", err)
	}
	_ = s2.Close()
}

func TestHooksRegistryIsOrderedAndNilSafe(t *testing.T) {
	var nilHooks *Hooks
	if got := nilHooks.snapshot(); got != nil {
		t.Errorf("nil registry snapshot = %v", got)
	}
	h := &Hooks{}
	for _, n := range []string{"a", "b", "c"} {
		h.Register(n, nil)
	}
	var names []string
	for _, hk := range h.snapshot() {
		names = append(names, hk.Name)
	}
	if strings.Join(names, "") != "abc" {
		t.Errorf("registration order lost: %v", names)
	}
}

func TestRunIsStartThenServe(t *testing.T) {
	cfg, _ := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, func(context.Context, net.Conn, Peer) {}, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", cfg.SocketPath); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never started listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	if err := Run(context.Background(), Config{}, nil, nil); err == nil {
		t.Error("Run with no paths must fail")
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/plain/path.json":  "/plain/path.json",
		"/has space/c.json": "'/has space/c.json'",
		"it's":              `'it'\''s'`,
		"":                  "''",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSupervisorStateStrings(t *testing.T) {
	for s, want := range map[SupervisorState]string{
		SupervisorNone: "none", SupervisorNotRegistered: "not_registered",
		SupervisorRegistered: "registered", SupervisorRunning: "running", SupervisorUnknown: "unknown",
	} {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", s, s.String(), want)
		}
	}
}
