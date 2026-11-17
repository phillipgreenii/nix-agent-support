package supervisor

import (
	"context"
	"log/slog"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/ladder"
	"github.com/phillipgreenii/x/tmux"
)

// An orphan is a session on the assistants' tmux server that no valid
// assistant supervises: one removed from the config, renamed (which would
// otherwise put a second Claude in the same directory), or with a config
// error. The sweep lists the sessions at start and every global tick, and
// gives each orphan its own loop, which runs the disabled ladder on it (an
// exit request still comes first) until the session is gone.

// The orphan notes, in the orphan event.
const (
	noteNotInConfig = "not in config"
	noteConfigError = "config error"
)

// sweep lists the server's sessions every global tick, starts an orphan loop
// for each orphan without one, and records the orphans in the status board.
func (s *supervisor) sweep(ctx context.Context) {
	ticker(ctx, s.cfg.Tick, func(time.Time) bool {
		s.sweepOnce(ctx)
		return false
	})
}

func (s *supervisor) sweepOnce(ctx context.Context) {
	var names []string
	err := bounded(ctx, tmuxTimeout, func(ctx context.Context) (err error) {
		names, err = s.d.Tmux.List(ctx)
		return err
	})
	if err != nil {
		// Logged when it starts and when its message changes; the orphan
		// list keeps its last value until a list succeeds.
		if !ending(ctx) && err.Error() != s.listErr {
			s.listErr = err.Error()
			s.event(ctx, slog.LevelWarn, evActionFailed, "action", "list_sessions", "error", s.listErr)
		}
		return
	}
	s.listErr = ""
	var orphans []string
	for _, name := range names {
		if s.valid[name] {
			continue
		}
		orphans = append(orphans, name)
		s.watchOrphan(ctx, name)
	}
	s.d.Status.SetOrphans(orphans)
}

// watchOrphan starts the orphan loop for name unless one is running, and logs
// the orphan event once per loop.
func (s *supervisor) watchOrphan(ctx context.Context, name string) {
	s.mu.Lock()
	running := s.orphans[name]
	s.orphans[name] = true
	s.mu.Unlock()
	if running {
		return
	}
	note := noteNotInConfig
	if s.problem[name] {
		note = noteConfigError
	}
	s.event(ctx, slog.LevelWarn, evOrphan, "assistant", name, "note", note)
	o := &orphanLoop{s: s, t: target{name: name}}
	loop := "orphan:" + name
	run := s.guard(loop, name, nil, o.run)
	s.d.Go(loop, func(ctx context.Context) error {
		defer func() {
			s.mu.Lock()
			delete(s.orphans, name)
			s.mu.Unlock()
		}()
		return run(ctx)
	})
}

// orphanLoop runs the disabled ladder on one orphan session, on the global
// tick, until the session is gone. Only its own goroutine touches it.
type orphanLoop struct {
	s        *supervisor
	t        target
	ladder   ladder.State
	exitSeen bool
}

func (o *orphanLoop) run(ctx context.Context) {
	ticker(ctx, o.s.cfg.Tick, func(now time.Time) bool { return o.tick(ctx, now) })
}

// tick takes the ladder's next step and reports whether the session is gone.
func (o *orphanLoop) tick(loopCtx context.Context, now time.Time) (gone bool) {
	s := o.s
	ctx, cancel := context.WithTimeout(loopCtx, s.cfg.Tick+s.cfg.Grace.Kill+tickMargin)
	defer cancel()
	st, err := s.state(ctx, o.t.name)
	switch {
	case err != nil:
		return false // tried again on the next tick
	case st.Exists && st.Dead:
		info, derr := s.dead(ctx, o.t.name)
		if s.kill(ctx, o.t.name) != nil {
			return false
		}
		if derr != nil {
			o.ended(ctx, nil)
		} else {
			o.ended(ctx, &info)
		}
		return true
	case !st.Exists:
		o.ended(ctx, nil)
		return true
	}
	if o.t.pid == 0 {
		if pid, err := s.panePID(ctx, o.t.name); err == nil {
			o.t.pid = pid
		}
	}
	in := ladder.Input{Now: now, Enabled: false, Orphan: true, ExitRequested: o.exitRequested()}
	next, act, reason := ladder.Step(o.ladder, in, s.grace)
	o.ladder = next
	_ = s.act(ctx, o.t, act, reason) // logged by act; the ladder moves on either way
	return false
}

// exitRequested reads an exit request left for the orphan's name, which a
// session started under that id can still write.
func (o *orphanLoop) exitRequested() bool {
	if !ValidID(o.t.name) {
		return false
	}
	ok, err := HasExitRequest(o.s.cfg.Paths.StateDir, o.t.name)
	if err != nil {
		return false
	}
	if ok {
		o.exitSeen = true
	}
	return ok
}

// ended logs the orphan's end and clears the exit request it was stopped for.
func (o *orphanLoop) ended(ctx context.Context, d *tmux.DeadInfo) {
	if ending(ctx) {
		return
	}
	args := append(o.t.attrs(), "orphan", true, "exit_requested", o.exitSeen)
	if d != nil {
		args = append(args, "status", d.Status, "signal", d.Signal)
	}
	o.s.event(ctx, slog.LevelInfo, evExited, args...)
	if o.exitSeen {
		_ = ClearExitRequest(o.s.cfg.Paths.StateDir, o.t.name)
	}
}
