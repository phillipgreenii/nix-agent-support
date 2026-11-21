package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/goleak"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/blocks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/quota"
	"github.com/phillipgreenii/x/jsonllogger/jsonlloggertest"
	"github.com/phillipgreenii/x/tmux"
)

// The supervisor's tests run Run inside a testing/synctest bubble against
// hand-written recording fakes of tmux, pg-claude-control and folder trust.
// The fakes share one world: the sessions on the fake tmux server and an
// ordered log of every call, each stamped with its offset from the start of
// the bubble and the budget its context allowed.

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

const (
	exampleA = "example-a"
	exampleB = "example-b"
	firstPID = 1000
)

var contextPoints = config.Points{Nudge: 0.8, Interrupt: 0.95, Kill: 1.0}

// call is one recorded call to a fake.
type call struct {
	at       time.Duration // since the bubble started
	op       string        // new-session, state, pane-pid, dead, list, kill, usage, submit, interrupt, stop, signal, is-trusted, ensure
	name     string        // the session (control calls are resolved from the pid)
	pid      int
	text     string
	cwd      string
	env      []string
	argv     []string
	escalate time.Duration
	tail     int
	budget   time.Duration // the context's deadline minus the call time; 0 without one
}

type fakeSession struct {
	pid  int
	dead bool
	info tmux.DeadInfo
}

// world is the fakes' shared state. The hooks are set before start and never
// changed after; nil means the default behavior. A hook runs without the
// world's lock held, so it may block or call the world's helpers.
type world struct {
	t0 time.Time

	mu       sync.Mutex
	sessions map[string]*fakeSession
	pidName  map[int]string
	nextPID  int
	nextID   int
	calls    []call
	untrust  map[string]bool

	// newSession replaces the default (an alive session).
	newSession func(name string) error
	// state runs before State answers; a test may panic in it.
	state func(name string)
	// usage answers Usage; n counts this session's usage calls from 1. The
	// default reports a small context and no blocks.
	usage     func(ctx context.Context, name string, n int, now time.Time) (control.Usage, error)
	submit    func(name, text string) error
	interrupt func(name string) error
	// stop replaces the default (the pane dies with status 0 and Stop returns nil).
	stop func(ctx context.Context, name string) error
	// signal replaces the default (nothing happens: the Claude ignores it).
	signal func(name string) error
	// kill replaces the default (the session is removed).
	kill func(name string) error
}

func newWorld(t0 time.Time) *world {
	return &world{
		t0:       t0,
		sessions: map[string]*fakeSession{},
		pidName:  map[int]string{},
		nextPID:  firstPID,
		untrust:  map[string]bool{},
	}
}

func budget(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return 0
}

// recordCtx is record for a call made with ctx, whose budget it notes.
func (w *world) recordCtx(ctx context.Context, c call) int {
	c.budget = budget(ctx)
	return w.record(c)
}

// record appends c and returns how many calls of c.op for c.name there are now.
func (w *world) record(c call) int {
	c.at = time.Since(w.t0)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, c)
	n := 0
	for _, x := range w.calls {
		if x.op == c.op && x.name == c.name {
			n++
		}
	}
	return n
}

// addSession puts an alive session on the server and returns its pid.
func (w *world) addSession(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	pid := w.nextPID
	w.nextPID++
	w.sessions[name] = &fakeSession{pid: pid}
	w.pidName[pid] = name
	return pid
}

// die turns the session's pane into a dead pane.
func (w *world) die(name string, info tmux.DeadInfo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.sessions[name]; s != nil {
		s.dead, s.info = true, info
	}
}

func (w *world) remove(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.sessions, name)
}

func (w *world) session(name string) (fakeSession, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[name]
	if s == nil {
		return fakeSession{}, false
	}
	return *s, true
}

func (w *world) nameOf(pid int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pidName[pid]
}

func (w *world) newID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextID++
	return fmt.Sprintf("uuid-%d", w.nextID)
}

// callsOf returns the recorded calls of op for name ("" for every op or name).
func (w *world) callsOf(op, name string) []call {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []call
	for _, c := range w.calls {
		if (op == "" || c.op == op) && (name == "" || c.name == name) {
			out = append(out, c)
		}
	}
	return out
}

// times returns the offsets of the calls of op for name.
func (w *world) times(op, name string) []time.Duration {
	var out []time.Duration
	for _, c := range w.callsOf(op, name) {
		out = append(out, c.at)
	}
	return out
}

// actions returns "op@offset" for name's calls that act on a session.
func (w *world) actions(name string) []string {
	acting := map[string]bool{"new-session": true, "submit": true, "interrupt": true, "stop": true, "signal": true, "kill": true}
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, c := range w.calls {
		if acting[c.op] && c.name == name {
			out = append(out, fmt.Sprintf("%s@%s", c.op, c.at))
		}
	}
	return out
}

var errNoSession = errors.New("can't find session")

type fakeTmux struct{ w *world }

func (f fakeTmux) NewSession(ctx context.Context, name, cwd string, env, argv []string) error {
	f.w.recordCtx(ctx, call{op: "new-session", name: name, cwd: cwd, env: slices.Clone(env), argv: slices.Clone(argv)})
	if f.w.newSession != nil {
		return f.w.newSession(name)
	}
	f.w.addSession(name)
	return nil
}

func (f fakeTmux) State(ctx context.Context, name string) (tmux.SessionState, error) {
	f.w.recordCtx(ctx, call{op: "state", name: name})
	if f.w.state != nil {
		f.w.state(name)
	}
	s, ok := f.w.session(name)
	if !ok {
		return tmux.SessionState{}, nil
	}
	return tmux.SessionState{Exists: true, Dead: s.dead}, nil
}

func (f fakeTmux) PanePID(ctx context.Context, name string) (int, error) {
	f.w.recordCtx(ctx, call{op: "pane-pid", name: name})
	s, ok := f.w.session(name)
	if !ok {
		return 0, errNoSession
	}
	return s.pid, nil
}

func (f fakeTmux) Dead(ctx context.Context, name string, tailLines int) (tmux.DeadInfo, error) {
	f.w.recordCtx(ctx, call{op: "dead", name: name, tail: tailLines})
	s, ok := f.w.session(name)
	if !ok || !s.dead {
		return tmux.DeadInfo{}, fmt.Errorf("pane of %q is not dead", name)
	}
	return s.info, nil
}

func (f fakeTmux) List(ctx context.Context) ([]string, error) {
	f.w.recordCtx(ctx, call{op: "list"})
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	var names []string
	for n := range f.w.sessions {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (f fakeTmux) Kill(ctx context.Context, name string) error {
	f.w.recordCtx(ctx, call{op: "kill", name: name})
	if f.w.kill != nil {
		return f.w.kill(name)
	}
	f.w.remove(name)
	return nil
}

type fakeControl struct{ w *world }

func (f fakeControl) Usage(ctx context.Context, pid int, now time.Time) (control.Usage, error) {
	name := f.w.nameOf(pid)
	n := f.w.recordCtx(ctx, call{op: "usage", name: name, pid: pid})
	if f.w.usage != nil {
		return f.w.usage(ctx, name, n, now)
	}
	return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: 1000}, nil
}

func (f fakeControl) Submit(ctx context.Context, pid int, text string) error {
	name := f.w.nameOf(pid)
	f.w.recordCtx(ctx, call{op: "submit", name: name, pid: pid, text: text})
	if f.w.submit != nil {
		return f.w.submit(name, text)
	}
	return nil
}

func (f fakeControl) Interrupt(ctx context.Context, pid int) error {
	name := f.w.nameOf(pid)
	f.w.recordCtx(ctx, call{op: "interrupt", name: name, pid: pid})
	if f.w.interrupt != nil {
		return f.w.interrupt(name)
	}
	return nil
}

func (f fakeControl) Stop(ctx context.Context, pid int, escalate time.Duration) error {
	name := f.w.nameOf(pid)
	f.w.recordCtx(ctx, call{op: "stop", name: name, pid: pid, escalate: escalate})
	if f.w.stop != nil {
		return f.w.stop(ctx, name)
	}
	f.w.die(name, tmux.DeadInfo{})
	return nil
}

func (f fakeControl) Signal(ctx context.Context, pid int) error {
	name := f.w.nameOf(pid)
	f.w.recordCtx(ctx, call{op: "signal", name: name, pid: pid})
	if f.w.signal != nil {
		return f.w.signal(name)
	}
	return nil
}

type fakeTrust struct{ w *world }

func (f fakeTrust) IsTrusted(cwd string) (bool, error) {
	f.w.record(call{op: "is-trusted", name: filepath.Base(cwd), cwd: cwd})
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	return !f.w.untrust[cwd], nil
}

func (f fakeTrust) Ensure(cwd string) error {
	f.w.record(call{op: "ensure", name: filepath.Base(cwd), cwd: cwd})
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	delete(f.w.untrust, cwd)
	return nil
}

// group stands in for lifecycle.Group: it records each loop's name, runs it
// on the group's context, and records how it returned.
type group struct {
	ctx context.Context
	wg  sync.WaitGroup

	mu      sync.Mutex
	started []string
	ended   map[string]int
	errs    []error
}

func (g *group) Go(name string, fn func(ctx context.Context) error) {
	g.mu.Lock()
	g.started = append(g.started, name)
	g.mu.Unlock()
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		err := fn(g.ctx)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.ended[name]++
		if err != nil {
			g.errs = append(g.errs, fmt.Errorf("%s: %w", name, err))
		}
	}()
}

func (g *group) names() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.started)
}

func (g *group) endedCount(name string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ended[name]
}

// harness runs Run in a synctest bubble. Build it with newHarness inside the
// bubble, adjust cfg, w and blocks, then start; every test stops it.
type harness struct {
	t      *testing.T
	t0     time.Time
	root   string
	cfg    config.Config
	w      *world
	blocks *blocks.Tracker
	quota  quota.Source
	// transcript answers the idle check; nil means no idle detection.
	transcript Transcript
	stat       func(string) (os.FileInfo, error)

	group  *group
	status *StatusBoard
	events *jsonlloggertest.Buffer
	cancel context.CancelFunc
	runErr chan error
	once   sync.Once
}

func newHarness(t *testing.T, ids ...string) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{t: t, t0: time.Now(), root: root, stat: os.Stat}
	h.w = newWorld(h.t0)
	stateDir := filepath.Join(root, "state")
	h.cfg = config.Defaults()
	h.cfg.Paths = config.Paths{
		Tmux: "/example/bin/tmux", Claude: "/example/bin/claude", PgClaudeControl: "/example/bin/pg-claude-control",
		Shell: "/bin/zsh", StateDir: stateDir, ClaudeJSON: filepath.Join(root, ".claude.json"),
	}
	for _, id := range ids {
		cwd := filepath.Join(root, "work", id)
		if err := os.MkdirAll(cwd, 0o700); err != nil {
			t.Fatal(err)
		}
		h.cfg.Assistants = append(h.cfg.Assistants, config.Assistant{
			ID: id, Name: "Example " + id, Model: "claude-sonnet-5-5", Effort: "medium", Cwd: cwd,
			Prompt: "Resume your duties.", Enabled: true, Tick: time.Minute,
			Context: config.Context{Limit: 200000, Window: 1000000, Points: contextPoints},
		})
	}
	return h
}

func (h *harness) a(id string) *config.Assistant {
	for i := range h.cfg.Assistants {
		if h.cfg.Assistants[i].ID == id {
			return &h.cfg.Assistants[i]
		}
	}
	h.t.Fatalf("no assistant %q", id)
	return nil
}

func (h *harness) statusPath() string { return filepath.Join(h.cfg.Paths.StateDir, "status.json") }

func (h *harness) start() { h.startWith(nil) }

// startWith is start with a hook that adjusts the status board first.
func (h *harness) startWith(board func(*StatusBoard)) {
	h.t.Helper()
	logger, buf := jsonlloggertest.New(h.t, EventLogOptions()...)
	h.events = buf
	h.status = NewStatusBoard(h.statusPath(), DaemonStatus{PID: 1, Version: "test"})
	if board != nil {
		board(h.status)
	}
	if h.blocks == nil {
		h.blocks = blocks.NewTracker(h.cfg.Blocks)
	}
	ctx, cancel := context.WithCancel(h.t.Context())
	h.cancel = cancel
	h.group = &group{ctx: ctx, ended: map[string]int{}}
	h.runErr = make(chan error, 1)
	d := Deps{
		Tmux: fakeTmux{h.w}, Control: fakeControl{h.w}, Trust: fakeTrust{h.w},
		Blocks: h.blocks, Quota: h.quota, Transcript: h.transcript, Status: h.status, Events: logger,
		Go: h.group.Go, NewID: h.w.newID, Stat: h.stat,
	}
	go func() { h.runErr <- Run(ctx, h.cfg, d) }()
	synctest.Wait()
}

// stop cancels Run's context and waits for Run and every loop, which must all
// return nil. It is safe to call more than once.
func (h *harness) stop() {
	h.t.Helper()
	h.once.Do(func() {
		h.cancel()
		if err := <-h.runErr; err != nil {
			h.t.Errorf("Run = %v, want nil", err)
		}
		h.group.wg.Wait()
		for _, err := range h.group.errs {
			h.t.Errorf("loop returned an error: %v", err)
		}
	})
}

// until advances the bubble's clock to offset d from its start and waits for
// every goroutine to settle.
func (h *harness) until(d time.Duration) {
	h.t.Helper()
	if wait := time.Until(h.t0.Add(d)); wait > 0 {
		time.Sleep(wait)
	}
	synctest.Wait()
}

func (h *harness) now() time.Duration { return time.Since(h.t0) }

// snapshot is the board's in-memory state.
func (h *harness) snapshot() Status {
	h.status.mu.Lock()
	defer h.status.mu.Unlock()
	return h.status.snapshot()
}

func (h *harness) statusOf(id string) AssistantStatus {
	h.t.Helper()
	for _, a := range h.snapshot().Assistants {
		if a.ID == id {
			return a
		}
	}
	h.t.Fatalf("no status entry for %q", id)
	return AssistantStatus{}
}

// eventsNamed returns the events log records whose event is name.
func (h *harness) eventsNamed(name string) []map[string]any {
	var out []map[string]any
	for _, r := range h.events.Records(h.t) {
		if r["event"] == name {
			out = append(out, r)
		}
	}
	return out
}

// eventsFor returns the records of event name for assistant id.
func (h *harness) eventsFor(name, id string) []map[string]any {
	var out []map[string]any
	for _, r := range h.eventsNamed(name) {
		if r["assistant"] == id {
			out = append(out, r)
		}
	}
	return out
}

func (h *harness) exitRequested(id string) bool {
	h.t.Helper()
	ok, err := HasExitRequest(h.cfg.Paths.StateDir, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return ok
}

func (h *harness) requestExit(id string) {
	h.t.Helper()
	if err := WriteExitRequest(h.cfg.Paths.StateDir, id, time.Now()); err != nil {
		h.t.Fatal(err)
	}
}

// usageTokens answers every usage call with the given context tokens.
func usageTokens(tokens int64) func(context.Context, string, int, time.Time) (control.Usage, error) {
	return func(_ context.Context, name string, _ int, _ time.Time) (control.Usage, error) {
		return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: tokens}, nil
	}
}

func minutes(ms ...float64) []time.Duration {
	out := make([]time.Duration, len(ms))
	for i, m := range ms {
		out[i] = time.Duration(m * float64(time.Minute))
	}
	return out
}

func wantTimes(t *testing.T, label string, got, want []time.Duration) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s at %v, want %v", label, got, want)
	}
}

func wantActions(t *testing.T, h *harness, name string, want ...string) {
	t.Helper()
	if got := h.w.actions(name); !slices.Equal(got, want) {
		t.Errorf("%s actions:\n got  %q\n want %q", name, got, want)
	}
}

// The budgets every call gets (spec: tmux 10 s, a control call 15 s, a
// graceful stop grace.kill + 15 s).
func wantBudgets(t *testing.T, h *harness) {
	t.Helper()
	want := map[string]time.Duration{
		"new-session": 10 * time.Second, "state": 10 * time.Second, "pane-pid": 10 * time.Second,
		"dead": 10 * time.Second, "list": 10 * time.Second, "kill": 10 * time.Second,
		"usage": control.CallTimeout, "submit": control.CallTimeout, "interrupt": control.CallTimeout,
		"signal": control.CallTimeout, "stop": h.cfg.Grace.Kill + control.StopMargin,
	}
	for _, c := range h.w.callsOf("", "") {
		if w, ok := want[c.op]; ok && c.budget != w {
			t.Errorf("%s %s at %s: budget %s, want %s", c.op, c.name, c.at, c.budget, w)
		}
	}
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// fakeTranscript answers every session's transcript with one state, which a
// test may replace between ticks.
type fakeTranscript struct {
	mu sync.Mutex
	st TranscriptState
}

func (f *fakeTranscript) set(st TranscriptState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st = st
}

func (f *fakeTranscript) State(string, string) (TranscriptState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, nil
}
