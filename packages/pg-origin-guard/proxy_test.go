package originguard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeTimers fires channels when the shared fake clock passes their time.
type fakeTimers struct {
	mu      sync.Mutex
	clock   *fakeClock
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func (f *fakeTimers) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	f.waiters = append(f.waiters, fakeWaiter{at: f.clock.Now().Add(d), ch: ch})
	return ch
}

func (f *fakeTimers) pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// advance moves the clock and fires every due timer in time order.
func (f *fakeTimers) advance(d time.Duration) {
	f.clock.Advance(d)
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.clock.Now()
	keep := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.at.After(now) {
			w.ch <- now
		} else {
			keep = append(keep, w)
		}
	}
	f.waiters = keep
}

type fakeChild struct {
	spec    ChildSpec
	pid     int
	done    chan struct{}
	once    sync.Once
	code    int
	mu      sync.Mutex
	signals []syscall.Signal
	dieOn   map[syscall.Signal]int
}

func (c *fakeChild) Pid() int              { return c.pid }
func (c *fakeChild) Done() <-chan struct{} { return c.done }
func (c *fakeChild) ExitCode() int         { return c.code }
func (c *fakeChild) Signal(s syscall.Signal) error {
	c.mu.Lock()
	c.signals = append(c.signals, s)
	c.mu.Unlock()
	if code, ok := c.dieOn[s]; ok {
		c.exit(code)
	}
	return nil
}
func (c *fakeChild) exit(code int) { c.once.Do(func() { c.code = code; close(c.done) }) }
func (c *fakeChild) sent() []syscall.Signal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]syscall.Signal(nil), c.signals...)
}

type fakeSpawner struct {
	mu       sync.Mutex
	children []*fakeChild
	// script prepares each child; it may end it at once.
	script func(c *fakeChild)
}

func (s *fakeSpawner) Start(spec ChildSpec) (Child, error) {
	c := &fakeChild{
		spec: spec, pid: 4000 + len(s.children), done: make(chan struct{}),
		dieOn: map[syscall.Signal]int{syscall.SIGKILL: 137, syscall.SIGTERM: 143, syscall.SIGINT: 130},
	}
	s.mu.Lock()
	s.children = append(s.children, c)
	s.mu.Unlock()
	if s.script != nil {
		s.script(c)
	}
	return c, nil
}

func (s *fakeSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.children)
}

func (s *fakeSpawner) child(i int) *fakeChild {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.children[i]
}

type fakePresence struct{ state PresenceState }

func (f fakePresence) Read() Presence { return Presence{State: f.state} }

type fakeProcs struct {
	mu     sync.Mutex
	tokens map[int]string
	kills  []syscall.Signal
}

func (f *fakeProcs) Token(pid int) string { f.mu.Lock(); defer f.mu.Unlock(); return f.tokens[pid] }
func (f *fakeProcs) KillGroup(pid int, s syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, s)
	return nil
}

type proxyRig struct {
	p       *Proxy
	clock   *fakeClock
	timers  *fakeTimers
	spawner *fakeSpawner
	sink    *captureSink
	agent   fakeAgent
	procs   *fakeProcs
	stderr  *syncBuf
	term    bool
	errTerm bool
	pres    *fakePresence
	sigs    chan os.Signal
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }

func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newProxyRig(t *testing.T) *proxyRig {
	t.Helper()
	env := testEnv(t)
	cfg := mustParse(t, `
[realm.zr]
match = { host = "github.com", user = "org-*" }
[realm.zr.login]
attempt_timeout = "3m"
backoff = ["5m", "10m"]
`, env)
	clock := newClock()
	sink := &captureSink{}
	store := NewStore(filepath.Join(t.TempDir(), "state"), clock, cfg.MaxBackoff())
	validCert := Cert{ValidAfter: clock.Now().Add(-time.Hour), ValidBefore: clock.Now().Add(time.Hour)}
	r := &proxyRig{
		clock: clock, timers: &fakeTimers{clock: clock}, spawner: &fakeSpawner{}, sink: sink,
		agent: fakeAgent{"/env.sock": {validCert}}, stderr: &syncBuf{},
		procs: &fakeProcs{tokens: map[int]string{}}, pres: &fakePresence{state: PresencePresent},
		sigs: make(chan os.Signal, 4),
	}
	r.p = &Proxy{
		Config: cfg, Clock: clock, Store: store,
		Certs: &CertChecker{Clock: clock, Dialer: r.agent, Getenv: func(k string) string {
			if k == "SSH_AUTH_SOCK" {
				return "/env.sock"
			}
			return ""
		}},
		Rec:      &Recorder{Store: store, Sink: sink, Clock: clock, HeartbeatInterval: 5 * time.Minute},
		Presence: r.pres, Spawner: r.spawner, Procs: r.procs, Timers: r.timers,
		HasTerminal:      func() bool { return r.term },
		StderrIsTerminal: func() bool { return r.errTerm },
		Stderr:           r.stderr, Signals: r.sigs,
	}
	return r
}

func (r *proxyRig) expire() { r.agent["/env.sock"] = nil }
func (r *proxyRig) renew() {
	r.agent["/env.sock"] = []Cert{{ValidAfter: r.clock.Now().Add(-time.Hour), ValidBefore: r.clock.Now().Add(time.Hour)}}
}

// run starts Run in a goroutine and returns the result channel.
func (r *proxyRig) run(argv ...string) <-chan int {
	if len(argv) == 0 {
		argv = []string{"step", "ssh", "proxycommand"}
	}
	ch := make(chan int, 1)
	go func() { ch <- r.p.Run(context.Background(), "zr", argv) }()
	return ch
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func result(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
		return -1
	}
}

// exitWith makes every child end at once with code, writing stderr text.
func (r *proxyRig) exitWith(code int, stderr string) {
	r.spawner.script = func(c *fakeChild) {
		if c.spec.Stderr != nil && stderr != "" {
			_, _ = c.spec.Stderr.Write([]byte(stderr))
		}
		c.exit(code)
	}
}

func TestProxyIgnoredRunsUnchanged(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	if err := r.p.Store.SetIgnored("zr", true); err != nil {
		t.Fatal(err)
	}
	r.exitWith(7, "")
	if got := result(t, r.run()); got != 7 {
		t.Fatalf("exit = %d, want 7 (passthrough)", got)
	}
	if c := r.spawner.child(0); c.spec.OwnGroup || c.spec.Stderr != nil {
		t.Fatalf("plain run must have no group and inherited stderr: %+v", c.spec)
	}
}

func TestProxyUnknownRealmIsFaultAndRunsUnchanged(t *testing.T) {
	r := newProxyRig(t)
	r.exitWith(0, "")
	code := r.p.Run(context.Background(), "nope", []string{"x"})
	if code != 0 || r.spawner.count() != 1 {
		t.Fatalf("code=%d spawned=%d", code, r.spawner.count())
	}
	ev := r.sink.list()
	if len(ev) != 1 || ev[0].Event != EventGuardFault || ev[0].Fault != "unknown-realm" {
		t.Fatalf("events = %+v", ev)
	}
}

func TestProxyValidCertPassesExitStatusThrough(t *testing.T) {
	r := newProxyRig(t)
	r.errTerm = true
	r.exitWith(255, "")
	if got := result(t, r.run()); got != 255 {
		t.Fatalf("exit = %d", got)
	}
	if r.spawner.child(0).spec.Stderr != nil {
		t.Fatal("stderr is a terminal and MUST be inherited unchanged")
	}
}

func TestProxyEstablishedAfterRecordsOKAndResetsBreaker(t *testing.T) {
	r := newProxyRig(t)
	_ = r.p.Store.Update("zr", func(s *RealmState) error {
		s.ConsecutiveFailures = 1
		return nil
	})
	ch := r.run()
	waitUntil(t, "established timer", func() bool { return r.timers.pending() == 1 })
	r.timers.advance(time.Second)
	waitUntil(t, "state reset", func() bool {
		st, _ := r.p.Store.Load("zr")
		return st.ConsecutiveFailures == 0 && !st.LastSuccessAt.IsZero()
	})
	r.spawner.child(0).exit(1) // exit after established_after is still ok
	result(t, ch)
	st, _ := r.p.Store.Load("zr")
	if st.ConsecutiveFailures != 0 {
		t.Fatalf("a late non-zero exit must not count: %+v", st)
	}
}

func TestProxyStartupFailuresOpenBreakerAndTrial(t *testing.T) {
	r := newProxyRig(t)
	r.errTerm = false
	r.exitWith(255, "ssh: Could not resolve hostname github.com: nodename nor servname provided\n")
	for range 2 {
		if got := result(t, r.run()); got != 255 {
			t.Fatalf("exit = %d", got)
		}
	}
	st, _ := r.p.Store.Load("zr")
	if !st.BreakerOpen(r.clock.Now()) || st.Class != string(ReasonNetwork) {
		t.Fatalf("breaker should be open on network: %+v", st)
	}
	if got := r.stderr.String(); got == "" {
		t.Fatal("stderr must be written through while teed")
	}

	// first terminal-less caller in the window is the trial, the next is refused
	if got := result(t, r.run()); got != 255 {
		t.Fatalf("trial caller must run, exit = %d stderr=%q st=%+v", got, r.stderr.String(), st)
	}
	n := r.spawner.count()
	if got := result(t, r.run()); got != 3 {
		t.Fatalf("second caller must be refused, exit = %d", got)
	}
	if r.spawner.count() != n {
		t.Fatal("a refused caller must not spawn")
	}
	if want := "pg-origin-guard: refused (network)"; !bytes.Contains([]byte(r.stderr.String()), []byte(want)) {
		t.Fatalf("stderr %q lacks %q", r.stderr.String(), want)
	}

	// a terminal caller is never refused for an open breaker
	r.term = true
	if got := result(t, r.run()); got != 255 {
		t.Fatalf("terminal caller exit = %d", got)
	}

	// the first ok closes the breaker
	r.exitWith(0, "")
	r.term = false
	r.clock.Advance(2 * time.Minute)
	if got := result(t, r.run()); got != 0 {
		t.Fatalf("exit = %d", got)
	}
	st, _ = r.p.Store.Load("zr")
	if st.ConsecutiveFailures != 0 || st.BreakerOpen(r.clock.Now()) {
		t.Fatalf("ok must close the breaker: %+v", st)
	}
}

func TestProxyAuthRejectedNeverOpensBreaker(t *testing.T) {
	r := newProxyRig(t)
	r.exitWith(128, "git@github.com: Permission denied (publickey).\n")
	for range 3 {
		result(t, r.run())
	}
	st, _ := r.p.Store.Load("zr")
	if st.BreakerOpen(r.clock.Now()) || st.ConsecutiveFailures != 0 {
		t.Fatalf("auth-rejected must not count: %+v", st)
	}
}

func TestProxyNonZeroAfterStartupWindowIsOK(t *testing.T) {
	r := newProxyRig(t)
	ch := r.run()
	waitUntil(t, "timer", func() bool { return r.timers.pending() == 1 })
	r.clock.Advance(30 * time.Second) // window passes without the established timer firing
	r.spawner.child(0).exit(255)
	result(t, ch)
	st, _ := r.p.Store.Load("zr")
	if st.ConsecutiveFailures != 0 {
		t.Fatalf("relayed then failed is ok: %+v", st)
	}
}

func TestProxyTerminalLessAsleepRefusedWithoutSpawning(t *testing.T) {
	for _, state := range []PresenceState{PresenceAsleep, PresenceAway} {
		r := newProxyRig(t)
		r.expire()
		r.pres.state = state
		if got := result(t, r.run()); got != 3 {
			t.Fatalf("%s: exit = %d", state, got)
		}
		if r.spawner.count() != 0 {
			t.Fatalf("%s: must not spawn", state)
		}
		if want := "pg-origin-guard: refused (no-human)\n"; r.stderr.String() != want {
			t.Fatalf("stderr = %q", r.stderr.String())
		}
	}
}

func TestProxyTerminalLessPresentRunsInOwnGroupUnderTimeout(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	ch := r.run()
	waitUntil(t, "child", func() bool { return r.spawner.count() == 1 && r.timers.pending() >= 2 })
	c := r.spawner.child(0)
	if !c.spec.OwnGroup {
		t.Fatal("terminal-less login child must have its own process group")
	}
	st, _ := r.p.Store.Load("zr")
	if st.AttemptStartedAt.IsZero() || st.HolderPID != c.pid {
		t.Fatalf("attempt must be persisted: %+v", st)
	}

	// a second caller sees the login in flight
	if got := result(t, r.run()); got != 3 {
		t.Fatalf("second caller exit = %d", got)
	}
	if want := "refused (attempting)"; !bytes.Contains([]byte(r.stderr.String()), []byte(want)) {
		t.Fatalf("stderr %q", r.stderr.String())
	}

	r.timers.advance(3 * time.Minute) // attempt timeout: SIGTERM to the group
	waitUntil(t, "SIGTERM", func() bool { return len(c.sent()) >= 1 })
	if c.sent()[0] != syscall.SIGTERM {
		t.Fatalf("signals = %v", c.sent())
	}
	if got := result(t, ch); got != 143 {
		t.Fatalf("exit = %d", got)
	}
	st, _ = r.p.Store.Load("zr")
	if st.Attempts != 1 || !st.NextAttemptAt.After(r.clock.Now()) || !st.AttemptStartedAt.IsZero() {
		t.Fatalf("failed attempt must set backoff and clear the attempt: %+v", st)
	}
	var sawTimeout bool
	for _, e := range r.sink.list() {
		sawTimeout = sawTimeout || e.LoginAttempt == "timeout"
	}
	if !sawTimeout {
		t.Fatalf("events = %+v", r.sink.list())
	}

	// within backoff a terminal-less caller is refused, a terminal one is not
	if got := result(t, r.run()); got != 3 {
		t.Fatalf("backoff exit = %d", got)
	}
	if want := "refused (backoff)"; !bytes.Contains([]byte(r.stderr.String()), []byte(want)) {
		t.Fatalf("stderr %q", r.stderr.String())
	}
	r.term = true
	r.exitWith(0, "")
	if got := result(t, r.run()); got != 0 {
		t.Fatalf("terminal caller bypasses backoff, exit = %d", got)
	}
}

func TestProxyAttemptTimeoutEscalatesToSIGKILL(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	r.spawner.script = func(c *fakeChild) { delete(c.dieOn, syscall.SIGTERM) }
	ch := r.run()
	waitUntil(t, "timers", func() bool { return r.spawner.count() == 1 && r.timers.pending() >= 2 })
	r.timers.advance(3 * time.Minute)
	waitUntil(t, "kill timer", func() bool { return len(r.spawner.child(0).sent()) == 1 && r.timers.pending() >= 1 })
	r.timers.advance(2 * time.Second)
	if got := result(t, ch); got != 137 {
		t.Fatalf("exit = %d", got)
	}
	if s := r.spawner.child(0).sent(); len(s) != 2 || s[1] != syscall.SIGKILL {
		t.Fatalf("signals = %v", s)
	}
}

func TestProxyLoginSuccessMidRunReleasesLockAndRecordsOK(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	ch := r.run()
	waitUntil(t, "child", func() bool { return r.spawner.count() == 1 && r.timers.pending() >= 2 })
	r.renew()
	r.timers.advance(time.Second) // cert poll
	waitUntil(t, "lock released", func() bool {
		lk, err := r.p.Store.TryLogin("zr")
		if err == nil {
			lk.Unlock()
			return true
		}
		return false
	})
	st, _ := r.p.Store.Load("zr")
	if !st.Available() || st.Attempts != 0 {
		t.Fatalf("state = %+v", st)
	}
	r.spawner.child(0).exit(0)
	if got := result(t, ch); got != 0 {
		t.Fatalf("exit = %d", got)
	}
}

func TestProxyTerminalCallerHasNoGroupAndNoTimeout(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	r.term = true
	ch := r.run()
	waitUntil(t, "child", func() bool { return r.spawner.count() == 1 })
	c := r.spawner.child(0)
	if c.spec.OwnGroup {
		t.Fatal("terminal caller's child MUST NOT get a separate process group")
	}
	waitUntil(t, "cert poll only", func() bool { return r.timers.pending() == 1 })
	r.timers.advance(time.Hour)
	if len(c.sent()) != 0 {
		t.Fatalf("no timeout for a terminal caller, got %v", c.sent())
	}
	c.exit(0)
	result(t, ch)
}

func TestProxyTerminalWaiterPreemptsVerifiedHolder(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	r.term = true
	if err := r.p.Store.Update("zr", func(s *RealmState) error {
		s.AttemptStartedAt = r.clock.Now()
		s.HolderPID, s.HolderToken = 999, "tok"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.procs.tokens[999] = "tok"
	held, err := r.p.Store.TryLogin("zr")
	if err != nil {
		t.Fatal(err)
	}
	ch := r.run()
	waitUntil(t, "status line", func() bool {
		return bytes.Contains([]byte(r.stderr.String()), []byte("waiting for a login already in progress, started 0s ago"))
	})
	waitUntil(t, "wait timers", func() bool { return r.timers.pending() >= 1 })
	r.timers.advance(30 * time.Second)
	waitUntil(t, "SIGTERM to holder", func() bool {
		r.procs.mu.Lock()
		defer r.procs.mu.Unlock()
		return len(r.procs.kills) == 1
	})
	held.Unlock() // the holder dies and its lock goes
	r.timers.advance(2 * time.Second)
	waitUntil(t, "child", func() bool { return r.spawner.count() == 1 })
	r.spawner.child(0).exit(0)
	result(t, ch)
	if r.procs.kills[0] != syscall.SIGTERM {
		t.Fatalf("kills = %v", r.procs.kills)
	}
}

func TestProxyPreemptNeverSignalsReusedPid(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	r.term = true
	_ = r.p.Store.Update("zr", func(s *RealmState) error {
		s.AttemptStartedAt = r.clock.Now()
		s.HolderPID, s.HolderToken = 999, "old-start"
		return nil
	})
	r.procs.tokens[999] = "different-process"
	held, _ := r.p.Store.TryLogin("zr")
	defer held.Unlock()
	ch := r.run()
	waitUntil(t, "wait timers", func() bool { return r.timers.pending() >= 1 })
	r.timers.advance(30 * time.Second)
	waitUntil(t, "final wait", func() bool { return r.timers.pending() >= 1 })
	r.timers.advance(5 * time.Second)
	waitUntil(t, "proceed without lock", func() bool { return r.spawner.count() == 1 })
	r.spawner.child(0).exit(0)
	result(t, ch)
	if len(r.procs.kills) != 0 {
		t.Fatalf("a reused pid must never be signalled: %v", r.procs.kills)
	}
}

func TestProxyForwardsSignalsToChild(t *testing.T) {
	r := newProxyRig(t)
	ch := r.run()
	waitUntil(t, "child", func() bool { return r.spawner.count() == 1 })
	r.sigs <- syscall.SIGHUP
	c := r.spawner.child(0)
	waitUntil(t, "forwarded", func() bool { return len(c.sent()) == 1 })
	if c.sent()[0] != syscall.SIGHUP {
		t.Fatalf("signals = %v", c.sent())
	}
	c.exit(0)
	result(t, ch)
}

func TestProxyStaleAttemptingWithFreeLockIsIgnored(t *testing.T) {
	r := newProxyRig(t)
	r.expire()
	_ = r.p.Store.Update("zr", func(s *RealmState) error {
		s.AttemptStartedAt = r.clock.Now().Add(-time.Hour)
		return nil
	})
	r.exitWith(1, "")
	if got := result(t, r.run()); got != 1 {
		t.Fatalf("a crash must not leave a stale attempting; exit = %d", got)
	}
}

func TestProxyValidCertResetsUnavailableRealm(t *testing.T) {
	r := newProxyRig(t)
	_, _ = r.p.Rec.Record("zr", Observation{Reason: ReasonCertExpired})
	r.exitWith(0, "")
	result(t, r.run())
	st, _ := r.p.Store.Load("zr")
	if !st.Available() {
		t.Fatalf("a renewed cert must be seen at once: %+v", st)
	}
	evs := r.sink.list()
	if last := evs[len(evs)-1]; last.Available == nil || *last.Available != 1 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestProxyEventsCarryNoStderrText(t *testing.T) {
	r := newProxyRig(t)
	r.exitWith(255, "ssh: Could not resolve hostname secret-host.example\n")
	for range 2 {
		result(t, r.run())
	}
	for _, e := range r.sink.list() {
		if bytes.Contains([]byte(e.Reason+e.Fault+e.LoginAttempt), []byte("secret-host")) {
			t.Fatalf("event leaks stderr text: %+v", e)
		}
	}
}

func TestClassifyStartup(t *testing.T) {
	cases := map[string]Reason{
		"git@github.com: Permission denied (publickey).": ReasonAuthRejected,
		"Could not resolve hostname x":                   ReasonNetwork,
		"operation timeout":                              ReasonTimeout,
		"something odd":                                  ReasonUnknown,
		"":                                               ReasonUnknown,
	}
	for in, want := range cases {
		if got := classifyStartup(in); got != want {
			t.Errorf("classifyStartup(%q) = %s, want %s", in, got, want)
		}
	}
}
