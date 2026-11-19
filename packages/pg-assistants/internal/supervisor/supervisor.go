package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/blocks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/ladder"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/quota"
	"github.com/phillipgreenii/x/jsonllogger"
	"github.com/phillipgreenii/x/tmux"
)

// Tmux is the part of x/tmux's Client the supervisor uses.
type Tmux interface {
	NewSession(ctx context.Context, name, cwd string, env, argv []string) error
	State(ctx context.Context, name string) (tmux.SessionState, error)
	PanePID(ctx context.Context, name string) (int, error)
	Dead(ctx context.Context, name string, tailLines int) (tmux.DeadInfo, error) // the daemon reads 40 lines
	List(ctx context.Context) ([]string, error)
	Kill(ctx context.Context, name string) error
}

// Control is the part of control's Client the supervisor uses.
type Control interface {
	Usage(ctx context.Context, pid int, now time.Time) (control.Usage, error)
	Submit(ctx context.Context, pid int, text string) error
	Interrupt(ctx context.Context, pid int) error
	Stop(ctx context.Context, pid int, escalate time.Duration) error
	Signal(ctx context.Context, pid int) error
}

// Trust reads and writes Claude's folder trust for an assistant's directory.
type Trust interface {
	IsTrusted(cwd string) (bool, error)
	Ensure(cwd string) error
}

// Deps are the supervisor's collaborators.
type Deps struct {
	Tmux    Tmux
	Control Control
	Trust   Trust
	Blocks  *blocks.Tracker // seeded by the caller from the previous status.json
	// Quota reads an assistant's consumption for its [assistant.quota] meter;
	// nil (or an assistant without a quota) means no quota meter.
	Quota  quota.Source
	Status *StatusBoard
	Events *slog.Logger // a jsonllogger logger built with EventLogOptions
	// Go starts a named loop: lifecycle.Group.Go in production. A loop
	// returns nil when its context ends; an error would begin shutdown.
	Go    func(name string, fn func(ctx context.Context) error)
	NewID func() string                          // a fresh UUID v4, the new session's id
	Stat  func(path string) (os.FileInfo, error) // os.Stat in production
}

// Call budgets (ADR 0128): every tmux call tmuxTimeout; usage, submit,
// interrupt and stop --signal control.CallTimeout; a graceful stop
// grace.kill plus control.StopMargin. Each call gets its own context with
// that deadline, and the clients put the same value on their cmdrun.Call.
const (
	tmuxTimeout = 10 * time.Second
	// quotaTimeout bounds one recorder read for a quota meter.
	quotaTimeout = 20 * time.Second
	// tickMargin is added to a loop's tick and grace.kill to bound one whole
	// tick, which may hold a graceful stop.
	tickMargin = 30 * time.Second
	// heartbeatEvery is how often the status file is written whatever the
	// loops do, so a loop blocked in a graceful stop does not make it stale.
	heartbeatEvery = 15 * time.Second
	// deadTailLines is how much of a dead pane's output is kept.
	deadTailLines = 40
	// noControlTicks consecutive usage failures saying the mod is not there
	// (no control mod, no such process, mod not accepting) set the hint.
	noControlTicks = 3
	// noContextTicks consecutive answered usage calls without a context token
	// count mean the session never completed a model turn: claude accepts a
	// model id it cannot use, shows its prompt and answers the first message
	// with an error, so it stays idle and no process exit reports it.
	noContextTicks = 2
)

// The status hints.
const permissionPromptHint = "did not exit after the nudge (a permission prompt?)"

func noContextHint(id string) string {
	return "no context reading yet (model id not accepted? first message unanswered?): pg-assistants attach " + id
}

func controlModHint(id string) string {
	return "control mod not answering (shell startup waiting? trust dialog? plugin disabled?): pg-assistants attach " + id
}

// The status wait reasons: why a not-running, enabled assistant was not
// started on its last tick.
const (
	waitBackoff    = "backoff"
	waitBlockHold  = "block_hold"
	waitMissingCwd = "missing_cwd"
)

// The daemon's own events. The ladder actions log under their
// ladder.Action names (nudge, interrupt, kill, force_kill, graceful_stop);
// x/lifecycle logs the loop and shutdown events.
const (
	evStart             = "start"
	evStartFailed       = "start_failed"
	evStartStalled      = "start_stalled"
	evAdopt             = "adopt"
	evExited            = "exited"
	evUsageFailed       = "usage_failed"
	evQuotaFailed       = "quota_failed"
	evWindowMismatch    = "window_mismatch"
	evActionFailed      = "action_failed"
	evConfigProblem     = "config_problem"
	evOrphan            = "orphan"
	evLoopPanic         = "loop_panic"
	evStatusWriteFailed = "status_write_failed"
)

// EventLogOptions are the jsonllogger options for the events log: each line
// is {ts, level, event, service, assistant, session_id, pid, ...}, the event
// name being slog's message.
func EventLogOptions() []jsonllogger.Option {
	return []jsonllogger.Option{
		jsonllogger.WithService("pg-assistants"),
		jsonllogger.WithKeyRenames(map[string]string{slog.TimeKey: "ts", slog.MessageKey: "event"}),
	}
}

// supervisor is the state Run's loops share. Everything a loop writes here is
// mutex-guarded memory; no lock is held across I/O.
type supervisor struct {
	cfg   config.Config
	d     Deps
	grace ladder.Grace
	flush chan struct{} // end-of-tick requests to the heartbeat; a send never blocks

	// valid holds the ids of the assistants with a loop, problem those of the
	// assistants with config problems. Both are written before any loop
	// starts and only read after.
	valid, problem map[string]bool

	mu      sync.Mutex
	orphans map[string]bool // sessions with an orphan loop running

	listErr string // the session list error last logged; owned by the sweep loop
}

// Run supervises cfg's assistants. It starts, through d.Go, one loop per
// assistant without Problems ("assistant:<id>"; its first tick at once, then
// every a.Tick), the orphan sweep ("orphans", every cfg.Tick; each session no
// valid assistant supervises gets "orphan:<name>") and the heartbeat
// ("heartbeat", which writes the status file every 15 s and at the end of
// every tick). An assistant with Problems gets no loop: its status shows the
// config error, and a session of it is handled as an orphan. Run returns nil
// once ctx is done; the caller's run group waits for the loops. Nothing here
// touches a tmux session on shutdown: every in-flight call is cancelled and
// the sessions keep running.
//
// The loops are isolated from each other (ADR 0128): each makes its own
// assistant's tmux and pg-claude-control calls, each call bounded by its own
// timeout, and no loop waits on another.
func Run(ctx context.Context, cfg config.Config, d Deps) error {
	s := &supervisor{
		cfg:     cfg,
		d:       d,
		grace:   ladder.Grace{Interrupt: cfg.Grace.Interrupt, Kill: cfg.Grace.Kill},
		flush:   make(chan struct{}, 1),
		valid:   map[string]bool{},
		problem: map[string]bool{},
		orphans: map[string]bool{},
	}
	var loops []*assistantLoop
	for _, a := range cfg.Assistants {
		if len(a.Problems) > 0 {
			s.problem[a.ID] = true
			s.configError(ctx, a)
			continue
		}
		s.valid[a.ID] = true
		loops = append(loops, &assistantLoop{s: s, a: a})
	}
	for _, l := range loops {
		name := "assistant:" + l.a.ID
		d.Go(name, s.guard(name, l.a.ID, l.panicked, l.run))
	}
	d.Go("orphans", s.guard("orphans", "", nil, s.sweep))
	d.Go("heartbeat", s.guard("heartbeat", "", nil, s.heartbeat))
	<-ctx.Done()
	return nil
}

// configError records an assistant that will not run: each problem as a
// config_problem event, and all of them as its status's config error.
func (s *supervisor) configError(ctx context.Context, a config.Assistant) {
	msgs := make([]string, len(a.Problems))
	for i, p := range a.Problems {
		msgs[i] = p.Error()
		s.event(ctx, slog.LevelWarn, evConfigProblem, "assistant", a.ID, "field", p.Field, "error", p.Msg)
	}
	s.d.Status.Set(AssistantStatus{ID: a.ID, Enabled: a.Enabled, ConfigError: strings.Join(msgs, "; ")})
}

func (s *supervisor) event(ctx context.Context, level slog.Level, name string, args ...any) {
	s.d.Events.Log(ctx, level, name, args...)
}

// guard turns body into a named loop for Deps.Go. A panic in body is
// recovered, logged as loop_panic with its stack, reported to onPanic, and
// body runs again after the restart backoff, which doubles on each panic from
// restart.backoff_initial up to restart.backoff_max and starts over once body
// ran restart.min_uptime without one. The loop returns nil when body returns
// or ctx ends.
func (s *supervisor) guard(name, assistant string, onPanic func(msg string), body func(context.Context)) func(context.Context) error {
	return func(ctx context.Context) error {
		var wait time.Duration
		for {
			began := time.Now()
			p, stack := recovered(ctx, body)
			if p == nil {
				return nil
			}
			if time.Since(began) >= s.cfg.Restart.MinUptime {
				wait = 0
			}
			wait = s.backoff(wait)
			msg := fmt.Sprintf("panic: %v", p)
			args := []any{"loop", name, "error", msg, "stack", stack, "restart_in_s", wait.Seconds()}
			if assistant != "" {
				args = append(args, "assistant", assistant)
			}
			s.event(ctx, slog.LevelError, evLoopPanic, args...)
			if onPanic != nil {
				onPanic(fmt.Sprintf("%s (loop restarts in %s)", msg, wait))
			}
			if !sleep(ctx, wait) {
				return nil
			}
		}
	}
}

// recovered runs body and returns what it panicked with, and the stack.
func recovered(ctx context.Context, body func(context.Context)) (p any, stack string) {
	defer func() {
		if r := recover(); r != nil {
			p, stack = r, string(debug.Stack())
		}
	}()
	body(ctx)
	return nil, ""
}

// sleep waits d, or until ctx ends, which it reports as false.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// backoff is the restart backoff after one more crash, given the previous
// one (0 for none): restart.backoff_initial, then double, never above
// restart.backoff_max.
func (s *supervisor) backoff(prev time.Duration) time.Duration {
	r := s.cfg.Restart
	return min(max(2*prev, r.BackoffInitial), r.BackoffMax)
}

// heartbeat writes the status file every heartbeatEvery and whenever a loop
// asks at the end of its tick. It is the only caller of Status.Flush, so no
// loop ever waits on the file. A write failure is logged when it starts and
// whenever its message changes.
func (s *supervisor) heartbeat(ctx context.Context) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	var failing string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.flush:
		}
		err := s.d.Status.Flush(time.Now())
		switch {
		case err == nil:
			failing = ""
		case err.Error() != failing:
			failing = err.Error()
			s.event(ctx, slog.LevelError, evStatusWriteFailed, "error", failing)
		}
	}
}

// requestFlush asks the heartbeat to write the status file, without waiting.
func (s *supervisor) requestFlush() {
	select {
	case s.flush <- struct{}{}:
	default: // a write is already asked for
	}
}

// tickTime is the time a loop uses for a tick it runs at now: the scheduled
// tick (start plus a whole number of ticks) when now is within a small slack
// of it, else now. A tick runs a little after its scheduled time, by an
// amount that varies, so raw readings that should be whole ticks apart (a
// 5 minute grace, a 1 minute backoff) would compare a tick late about half
// the time. The result is now shifted by that small difference, so it keeps
// now's wall clock, which block reset times are compared with.
func tickTime(start, now time.Time, tick time.Duration) time.Time {
	slack := min(time.Second, tick/4)
	n := int64((now.Sub(start) + tick/2) / tick)
	off := start.Add(time.Duration(n) * tick).Sub(now)
	if off > -slack && off < slack {
		return now.Add(off)
	}
	return now
}

// ticker runs tick at once and then every d until ctx ends or tick reports
// it is done, passing each tick's time (tickTime).
func ticker(ctx context.Context, d time.Duration, tick func(now time.Time) (done bool)) {
	if d <= 0 {
		d = config.DefaultTick // config never allows it; time.NewTicker would panic
	}
	start := time.Now()
	t := time.NewTicker(d)
	defer t.Stop()
	now := start
	for {
		if tick(now) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now = tickTime(start, time.Now(), d)
		}
	}
}

// bounded runs f with a context bounded by d, or not at all once ctx has ended.
func bounded(ctx context.Context, d time.Duration, f func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return f(cctx)
}

// ending reports whether a tick's ctx was cancelled, which happens only when
// the daemon shuts down: then nothing more is done, logged or counted. (A
// tick or call that runs out of time ends with DeadlineExceeded instead.)
func ending(ctx context.Context) bool { return errors.Is(ctx.Err(), context.Canceled) }

// target is the session an action is for.
type target struct {
	name      string // the tmux session name
	pid       int    // the pane pid; 0 while it is not known
	sessionID string
}

// attrs are the event keys naming t.
func (t target) attrs() []any {
	args := []any{"assistant", t.name}
	if t.sessionID != "" {
		args = append(args, "session_id", t.sessionID)
	}
	if t.pid != 0 {
		args = append(args, "pid", t.pid)
	}
	return args
}

var errNoPID = errors.New("the pane pid is not known")

// act performs a ladder action on t and logs it: under the action's own name
// when it worked, as action_failed naming it when it did not. Either way the
// ladder has moved on. A stop or signal for a process that no longer exists
// worked (the session is already going). During shutdown nothing is logged.
func (s *supervisor) act(ctx context.Context, t target, act ladder.Action, reason string) error {
	if act == ladder.ActNone {
		return nil
	}
	gone, err := s.perform(ctx, t, act, reason)
	if ending(ctx) {
		return err
	}
	args := append(t.attrs(), "reason", reason)
	if err != nil {
		s.event(ctx, slog.LevelWarn, evActionFailed, append(args, "action", act.String(), "error", err.Error())...)
		return err
	}
	if gone {
		args = append(args, "already_gone", true)
	}
	s.event(ctx, slog.LevelInfo, act.String(), args...)
	return nil
}

// perform makes the calls of one ladder action. gone reports a stop or
// signal that found the process already gone.
func (s *supervisor) perform(ctx context.Context, t target, act ladder.Action, reason string) (gone bool, err error) {
	if act == ladder.ActForceKill {
		return false, s.kill(ctx, t.name)
	}
	if t.pid == 0 {
		return false, errNoPID
	}
	c := s.d.Control
	switch act {
	case ladder.ActNudge:
		return false, bounded(ctx, control.CallTimeout, func(ctx context.Context) error { return c.Submit(ctx, t.pid, NudgeText(reason)) })
	case ladder.ActInterrupt:
		// interrupt --reply returns once the turn is aborted, so the submit
		// cannot reach the mod while the abort is still in progress; after a
		// failed interrupt it is not sent.
		if err := bounded(ctx, control.CallTimeout, func(ctx context.Context) error { return c.Interrupt(ctx, t.pid) }); err != nil {
			return false, fmt.Errorf("interrupt: %w", err)
		}
		if err := bounded(ctx, control.CallTimeout, func(ctx context.Context) error { return c.Submit(ctx, t.pid, StopText(reason)) }); err != nil {
			return false, fmt.Errorf("submit after the interrupt: %w", err)
		}
		return false, nil
	case ladder.ActKill:
		err = bounded(ctx, control.CallTimeout, func(ctx context.Context) error { return c.Signal(ctx, t.pid) })
	case ladder.ActGracefulStop:
		escalate := s.cfg.Grace.Kill
		err = bounded(ctx, escalate+control.StopMargin, func(ctx context.Context) error { return c.Stop(ctx, t.pid, escalate) })
	default:
		return false, fmt.Errorf("unknown action %v", act)
	}
	if errors.Is(err, control.ErrNoProcess) {
		return true, nil
	}
	return false, err
}

func (s *supervisor) kill(ctx context.Context, name string) error {
	return bounded(ctx, tmuxTimeout, func(ctx context.Context) error { return s.d.Tmux.Kill(ctx, name) })
}

func (s *supervisor) state(ctx context.Context, name string) (st tmux.SessionState, err error) {
	err = bounded(ctx, tmuxTimeout, func(ctx context.Context) error {
		st, err = s.d.Tmux.State(ctx, name)
		return err
	})
	return st, err
}

func (s *supervisor) panePID(ctx context.Context, name string) (pid int, err error) {
	err = bounded(ctx, tmuxTimeout, func(ctx context.Context) error {
		pid, err = s.d.Tmux.PanePID(ctx, name)
		return err
	})
	return pid, err
}

func (s *supervisor) dead(ctx context.Context, name string) (d tmux.DeadInfo, err error) {
	err = bounded(ctx, tmuxTimeout, func(ctx context.Context) error {
		d, err = s.d.Tmux.Dead(ctx, name, deadTailLines)
		return err
	})
	return d, err
}

// assistantLoop supervises one assistant. Only its own goroutine touches it.
type assistantLoop struct {
	s *supervisor
	a config.Assistant

	sess      *session      // nil while no session is known
	backoff   time.Duration // the last crash backoff; 0 once reset
	notBefore time.Time     // no start before this: the crash backoff
	lastExit  *LastExit
	lastError string // the latest failure; cleared by a usage answer
	reason    string // the latest non-empty ladder reason; cleared by a new session
	cwdErr    string // the cwd error last logged, so it is logged once until it changes
	lastTick  time.Time

	// Per tick.
	wait      string
	waitUntil time.Time
	failed    bool // a failure was recorded this tick
}

// session is one known session of the assistant.
type session struct {
	target
	startedAt time.Time // the start, or the adoption
	aliveAt   time.Time // when it was last seen alive; zero: never
	ladder    ladder.State

	answered     bool   // a usage call has succeeded
	usageErr     string // the latest usage error
	loggedErr    string // the usage error last logged
	noControl    int    // consecutive usage failures saying the mod is not there
	noContext    int    // consecutive answered usage calls without a context token count
	windowWarned bool
	stalled      bool // the start stalled: its end counts as a crash
	exitSeen     bool // an exit request was seen while it ran

	contextTokens   int64
	contextFraction float64
	contextAt       time.Time

	quotaErr      string // the quota error last logged
	quotaFraction float64
	quotaDetail   string
}

func (l *assistantLoop) run(ctx context.Context) {
	ticker(ctx, l.a.Tick, func(now time.Time) bool {
		l.tick(ctx, now)
		return false
	})
}

// panicked records a panic of the loop in the assistant's status.
func (l *assistantLoop) panicked(msg string) {
	l.lastError = msg
	l.s.d.Status.Set(l.status(l.lastTick))
	l.s.requestFlush()
}

func (l *assistantLoop) fail(what string, err error) {
	l.lastError = what + ": " + err.Error()
	l.failed = true
}

// tick is one reconcile pass (ADR 0128), under a deadline that leaves room
// for a graceful stop. The status is updated at its end.
func (l *assistantLoop) tick(loopCtx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(loopCtx, l.a.Tick+l.s.cfg.Grace.Kill+tickMargin)
	defer cancel()
	l.wait, l.waitUntil, l.failed = "", time.Time{}, false
	l.reconcile(ctx, now)
	l.publish(now)
}

func (l *assistantLoop) reconcile(ctx context.Context, now time.Time) {
	st, err := l.s.state(ctx, l.a.ID)
	if err != nil {
		if !ending(ctx) {
			l.fail("tmux state", err)
		}
		return
	}
	switch {
	case st.Exists && !st.Dead:
		l.running(ctx, now)
		return
	case st.Exists:
		if !l.reap(ctx, now) {
			return
		}
	case l.sess != nil:
		l.ended(ctx, now, nil) // gone without a dead pane: killed
	}
	if !ending(ctx) {
		l.notRunning(ctx, now)
	}
}

// running supervises a live session: adopt it if it is not known, read the
// exit request and the usage report, and take the ladder's next step.
func (l *assistantLoop) running(ctx context.Context, now time.Time) {
	adopted := l.sess == nil
	if adopted {
		l.sess = &session{target: target{name: l.a.ID}, startedAt: now}
		l.reason = ""
		l.readPID(ctx)
		l.s.event(ctx, slog.LevelInfo, evAdopt, l.sess.attrs()...)
	}
	s := l.sess
	s.aliveAt = now
	if s.pid == 0 && !adopted { // an adoption just asked: a failed ask is not repeated in the same tick
		l.readPID(ctx)
	}
	req := l.exitRequested()
	var meters []ladder.Meter
	// The exit ladder ignores the meters, and a session at the kill rung is
	// only killed again, so neither waits on a usage call.
	if !req && s.ladder.Kind != ladder.KindExit && s.ladder.Rung < ladder.RungKill {
		meters = l.usage(ctx, now)
		if ending(ctx) || l.stalled(ctx, now) {
			return
		}
	}
	if !req && s.ladder.Kind != ladder.KindExit && s.ladder.Rung < ladder.RungKill {
		meters = append(meters, l.quota(ctx, now)...)
	}
	meters = append(meters, l.s.d.Blocks.Meters(now)...)
	in := ladder.Input{Now: now, Enabled: l.a.Enabled, ExitRequested: req, Meters: meters}
	next, act, reason := ladder.Step(s.ladder, in, l.s.grace)
	s.ladder = next
	if reason != "" {
		l.reason = reason
	}
	if act == ladder.ActGracefulStop {
		l.publish(now) // the stop may block for grace.kill: show its rung meanwhile
	}
	if err := l.s.act(ctx, s.target, act, reason); err != nil && !ending(ctx) {
		l.fail(act.String(), err)
	}
}

func (l *assistantLoop) readPID(ctx context.Context) {
	pid, err := l.s.panePID(ctx, l.a.ID)
	if err != nil {
		if !ending(ctx) {
			l.fail("tmux pane pid", err)
		}
		return
	}
	l.sess.pid = pid
}

// exitRequested reads the assistant's exit request, which stays on file
// until the session is seen gone.
func (l *assistantLoop) exitRequested() bool {
	ok, err := HasExitRequest(l.s.cfg.Paths.StateDir, l.a.ID)
	if err != nil {
		l.fail("exit request", err)
		return false
	}
	if ok {
		l.sess.exitSeen = true
	}
	return ok
}

// usage reads the session's usage report, feeds the account blocks to the
// tracker and returns the context meter (none when the call failed: the
// ladder then runs on the block meters and the time steps alone).
func (l *assistantLoop) usage(ctx context.Context, now time.Time) []ladder.Meter {
	s := l.sess
	if s.pid == 0 {
		return nil
	}
	var u control.Usage
	err := bounded(ctx, control.CallTimeout, func(ctx context.Context) (err error) {
		u, err = l.s.d.Control.Usage(ctx, s.pid, now)
		return err
	})
	if err != nil {
		if ending(ctx) {
			return nil
		}
		s.usageErr = err.Error()
		if notAnswering(err) {
			s.noControl++
		} else {
			s.noControl = 0
		}
		if s.usageErr != s.loggedErr {
			s.loggedErr = s.usageErr
			l.s.event(ctx, slog.LevelWarn, evUsageFailed, append(s.attrs(), "error", s.usageErr)...)
		}
		l.fail("usage", err)
		return nil
	}
	s.answered, s.noControl, s.loggedErr = true, 0, ""
	if !l.failed {
		l.lastError = ""
	}
	if u.SessionID != "" {
		s.sessionID = u.SessionID // current even after /clear changed it
	}
	l.s.d.Blocks.Observe(u.Limits)
	if u.Window != 0 && u.Window != l.a.Context.Window && !s.windowWarned {
		s.windowWarned = true
		l.s.event(ctx, slog.LevelWarn, evWindowMismatch,
			append(s.attrs(), "window", u.Window, "configured_window", l.a.Context.Window)...)
	}
	if !u.HaveContext {
		s.noContext++
		return nil
	}
	s.noContext = 0
	if l.a.Context.Limit <= 0 {
		return nil
	}
	s.contextTokens, s.contextAt = u.ContextTokens, now
	s.contextFraction = float64(u.ContextTokens) / float64(l.a.Context.Limit)
	return []ladder.Meter{{
		Name: "context", Fraction: s.contextFraction, Points: l.a.Context.Points,
		Detail: fmt.Sprintf("%d of %d tokens", u.ContextTokens, l.a.Context.Limit),
	}}
}

// quota reads the assistant's quota consumption from the recorder and returns
// its meter. A failed read skips the meter for this tick (the ladder still runs
// on the other meters and the time steps) and is logged once until it changes.
func (l *assistantLoop) quota(ctx context.Context, now time.Time) []ladder.Meter {
	q, s := l.a.Quota, l.sess
	if q == nil || l.s.d.Quota == nil {
		return nil
	}
	var r quota.Reading
	err := bounded(ctx, quotaTimeout, func(ctx context.Context) (err error) {
		r, err = quota.Read(ctx, l.s.d.Quota, l.a.Cwd, *q, now)
		return err
	})
	if err != nil {
		if !ending(ctx) && err.Error() != s.quotaErr {
			s.quotaErr = err.Error()
			l.s.event(ctx, slog.LevelWarn, evQuotaFailed, append(s.attrs(), "error", s.quotaErr)...)
		}
		return nil
	}
	s.quotaErr = ""
	s.quotaFraction, s.quotaDetail = r.Meter.Fraction, r.Meter.Detail
	return []ladder.Meter{r.Meter}
}

// notAnswering reports a usage failure that says the control mod is not
// there to answer, which the control-mod hint counts.
func notAnswering(err error) bool {
	return errors.Is(err, control.ErrNoControl) || errors.Is(err, control.ErrNoProcess) || errors.Is(err, control.ErrBusy)
}

// stalled handles a start that never came up: an enabled session that has
// not answered a usage call within restart.start_timeout of its start (or
// adoption) gets start_stalled and the kill path (a signal now, tmux
// kill-session on the next tick, as the kill rung does), and its end counts
// as a crash. It reports whether it fired.
func (l *assistantLoop) stalled(ctx context.Context, now time.Time) bool {
	s, timeout := l.sess, l.s.cfg.Restart.StartTimeout
	if !l.a.Enabled || s.answered || s.stalled || now.Sub(s.startedAt) < timeout {
		return false
	}
	s.stalled = true
	s.ladder = ladder.ToKill(s.ladder, now)
	last := s.usageErr
	if last == "" {
		last = errNoPID.Error()
	}
	l.s.event(ctx, slog.LevelWarn, evStartStalled, append(s.attrs(), "error", last, "start_timeout_s", timeout.Seconds())...)
	reason := "start stalled"
	l.reason = reason
	err := l.s.act(ctx, s.target, ladder.ActKill, reason)
	if ending(ctx) {
		return true
	}
	if err != nil {
		l.fail("kill", err)
	}
	l.fail("start stalled", fmt.Errorf("no answer from the control mod within %s of the start (last usage error: %s)", timeout, last))
	return true
}

// reap handles a dead pane: record how it ended, then kill the session so the
// name is free. It reports whether the session is gone.
func (l *assistantLoop) reap(ctx context.Context, now time.Time) bool {
	info, derr := l.s.dead(ctx, l.a.ID)
	if ending(ctx) {
		return false
	}
	if derr != nil {
		l.fail("tmux dead pane", derr)
	} else if err := WriteLastExit(l.s.cfg.Paths.StateDir, l.a.ID, info, uptime(l.sess), now); err != nil {
		l.fail("last exit", err)
	}
	if err := l.s.kill(ctx, l.a.ID); err != nil {
		if !ending(ctx) {
			l.fail("tmux kill", err)
		}
		return false
	}
	var d *tmux.DeadInfo
	if derr == nil {
		d = &info
	}
	l.ended(ctx, now, d)
	return true
}

// uptime is how long s was seen alive: its last sighting minus its start, 0
// for a session never seen alive (or none).
func uptime(s *session) time.Duration {
	if s == nil || s.aliveAt.IsZero() {
		return 0
	}
	return s.aliveAt.Sub(s.startedAt)
}

// ended accounts for a session that is gone, dead pane d (nil when it
// vanished or could not be read). A requested exit clears the request and
// leaves the backoff alone, so the next session starts at once; any other
// end under restart.min_uptime, or of a stalled start, grows the backoff,
// and a longer one resets it.
func (l *assistantLoop) ended(ctx context.Context, now time.Time, d *tmux.DeadInfo) {
	s := l.sess
	l.sess = nil
	up := uptime(s)
	requested := s != nil && s.exitSeen
	if !requested {
		ok, err := HasExitRequest(l.s.cfg.Paths.StateDir, l.a.ID)
		if err != nil {
			l.fail("exit request", err)
		}
		requested = ok // a request made just before the session ended
	}
	t := target{name: l.a.ID}
	if s != nil {
		t = s.target
	}
	args := append(t.attrs(), "uptime_s", up.Seconds(), "exit_requested", requested)
	if d != nil {
		args = append(args, "status", d.Status, "signal", d.Signal)
		l.lastExit = &LastExit{Status: d.Status, Signal: d.Signal, UptimeS: up.Seconds()}
	}
	l.s.event(ctx, slog.LevelInfo, evExited, args...)
	switch {
	case requested:
		if err := ClearExitRequest(l.s.cfg.Paths.StateDir, l.a.ID); err != nil {
			l.fail("exit request", err)
		}
	case s == nil:
		// A dead pane found before any session was known (left by an earlier
		// daemon): its uptime is unknown, so the backoff stays as it is.
	case s.stalled || up < l.s.cfg.Restart.MinUptime:
		l.backoff = l.s.backoff(l.backoff)
		l.notBefore = now.Add(l.backoff)
	default:
		l.backoff = 0
	}
}

// notRunning starts an enabled assistant unless it is in its crash backoff,
// a block holds starts, or its directory is missing; the status says which.
func (l *assistantLoop) notRunning(ctx context.Context, now time.Time) {
	if !l.a.Enabled {
		return
	}
	if now.Before(l.notBefore) {
		l.wait, l.waitUntil = waitBackoff, l.notBefore
		return
	}
	if held, until := l.s.d.Blocks.Hold(now); held {
		l.wait, l.waitUntil = waitBlockHold, until
		return
	}
	if !l.cwdExists(ctx) {
		l.wait = waitMissingCwd
		return
	}
	l.start(ctx, now)
}

// cwdExists checks the assistant's directory before a start: tmux would
// silently start a missing one in $HOME. A missing directory is logged as
// start_failed once until its error changes, and checked again every tick
// without growing the backoff.
func (l *assistantLoop) cwdExists(ctx context.Context) bool {
	fi, err := l.s.d.Stat(l.a.Cwd)
	if err == nil && !fi.IsDir() {
		err = fmt.Errorf("%s is not a directory", l.a.Cwd)
	}
	if err == nil {
		l.cwdErr = ""
		return true
	}
	l.fail("cwd", err)
	if msg := err.Error(); msg != l.cwdErr {
		l.cwdErr = msg
		l.s.event(ctx, slog.LevelWarn, evStartFailed, "assistant", l.a.ID, "step", "cwd", "error", msg)
	}
	return false
}

// start launches a new session: trust the directory if it is not, clear any
// leftover exit request, then tmux new-session.
func (l *assistantLoop) start(ctx context.Context, now time.Time) {
	a, p := l.a, l.s.cfg.Paths
	// x/claudetrust resolves the directory to the key Claude looks up.
	key := a.Cwd
	if ok, err := l.s.d.Trust.IsTrusted(key); err != nil || !ok {
		if err := l.s.d.Trust.Ensure(key); err != nil {
			l.startFailed(ctx, now, "trust", err)
			return
		}
	}
	if err := ClearExitRequest(p.StateDir, a.ID); err != nil {
		l.startFailed(ctx, now, "exit request", err)
		return
	}
	id := l.s.d.NewID()
	err := bounded(ctx, tmuxTimeout, func(ctx context.Context) error {
		return l.s.d.Tmux.NewSession(ctx, a.ID, a.Cwd, LaunchEnv(a, p.StateDir, l.s.cfg.BlankEnv), LaunchArgv(p, a, id))
	})
	if err != nil {
		if !ending(ctx) {
			l.startFailed(ctx, now, "new-session", err)
		}
		return
	}
	l.sess = &session{target: target{name: a.ID, sessionID: id}, startedAt: now}
	l.reason = ""
	l.readPID(ctx)
	l.s.event(ctx, slog.LevelInfo, evStart, l.sess.attrs()...)
}

// startFailed records a start that failed after its checks passed; it counts
// as a crash.
func (l *assistantLoop) startFailed(ctx context.Context, now time.Time, step string, err error) {
	l.backoff = l.s.backoff(l.backoff)
	l.notBefore = now.Add(l.backoff)
	l.wait, l.waitUntil = waitBackoff, l.notBefore
	l.fail("start ("+step+")", err)
	l.s.event(ctx, slog.LevelWarn, evStartFailed, "assistant", l.a.ID, "step", step, "error", err.Error(),
		"backoff_s", l.backoff.Seconds())
}

// publish puts the assistant's status and the block readings on the board
// and asks the heartbeat to write the file.
func (l *assistantLoop) publish(now time.Time) {
	l.lastTick = now
	l.s.d.Status.Set(l.status(now))
	l.s.d.Status.SetBlocks(l.s.d.Blocks.Snapshot(now))
	l.s.requestFlush()
}

func (l *assistantLoop) status(now time.Time) AssistantStatus {
	st := AssistantStatus{
		ID: l.a.ID, Enabled: l.a.Enabled, Reason: l.reason,
		WaitUntil: l.waitUntil, WaitReason: l.wait,
		LastError: l.lastError, LastExit: l.lastExit, LastTick: now,
	}
	s := l.sess
	if s == nil {
		return st
	}
	st.Running, st.PID, st.SessionID, st.StartedAt = true, s.pid, s.sessionID, s.startedAt
	st.ContextTokens, st.ContextFraction, st.ContextAt = s.contextTokens, s.contextFraction, s.contextAt
	st.QuotaFraction, st.QuotaDetail = s.quotaFraction, s.quotaDetail
	st.Ladder, st.Rung = kindName(s.ladder.Kind), rungName(s.ladder.Rung)
	var hints []string
	if s.noControl >= noControlTicks {
		hints = append(hints, controlModHint(l.a.ID))
	}
	if s.noContext >= noContextTicks {
		hints = append(hints, noContextHint(l.a.ID))
	}
	if s.ladder.NudgeIgnored {
		hints = append(hints, permissionPromptHint)
	}
	st.Hint = strings.Join(hints, "; ")
	return st
}

// kindName and rungName are the status file's ladder and rung: empty before
// the ladder has a kind or a rung.
func kindName(k ladder.Kind) string {
	if k == ladder.KindNone {
		return ""
	}
	return k.String()
}

func rungName(r ladder.Rung) string {
	if r == ladder.RungNone {
		return ""
	}
	return r.String()
}
