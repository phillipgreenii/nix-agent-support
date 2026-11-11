// Package runner is the run lifecycle of `pg-review job run`: window and run
// key handling, the per-(lens, scope) run lock, the run row, skip gating, the
// stage pipeline and the stale-run reaper. It gathers no evidence and calls no
// analyst itself; later work plugs in through Deps.Evidence and Deps.Analysts.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/metrics"
)

type ledgerTx = ledger.Tx

// CloseSessionHook closes a ccpool session by id. The ccpool work registers
// it; nil means there is nothing to close.
var CloseSessionHook func(ctx context.Context, sessionID string) error

// Params are the `job run` inputs.
type Params struct {
	Lens, Scope            string
	From, To, Duration, At string
	Profile, Analyst       string
	EvidenceOnly           bool
	RunKey, ScheduleID     string
}

// UsageError marks a bad invocation (exit 2).
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// AlreadyFiledError means the run key already has a Filed run (exit 3, no row).
type AlreadyFiledError struct {
	Key   string
	RunID int64
}

func (e *AlreadyFiledError) Error() string {
	return fmt.Sprintf("run key %q already has a Filed run (run %d)", e.Key, e.RunID)
}

// Stage is one step of the pipeline. Stages run in order; the first error ends
// the run (a *SkippedError as Skipped, anything else as Failed).
type Stage interface {
	Name() string
	Run(ctx context.Context, s *State) error
}

// Analyst is an analyst backend. The none backend satisfies it trivially.
type Analyst interface {
	Name() string
	Analyse(ctx context.Context, s *State) error
}

// Deps are the injectable seams of a run.
type Deps struct {
	Ledger *ledger.Ledger
	Cfg    *config.Effective
	Now    func() time.Time
	PID    int
	Proc   ProcInspector
	// CloseSession defaults to CloseSessionHook.
	CloseSession func(ctx context.Context, sessionID string) error
	// RemoveStaging defaults to a removal that refuses any path outside the evidence root
	// and the literal /tmp/pg-review-* staging directories.
	RemoveStaging func(path string) error
	// Evidence, when set, runs between the gate and the analyst.
	Evidence Stage
	// Analysts maps a backend name to its implementation; a missing entry is
	// skipped (its packet has not landed).
	Analysts map[string]Analyst
	Stdout   io.Writer
}

func (d *Deps) defaults() {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.PID == 0 {
		d.PID = os.Getpid()
	}
	if d.Proc == nil {
		d.Proc = PSInspector{}
	}
	if d.CloseSession == nil && CloseSessionHook != nil {
		d.CloseSession = CloseSessionHook
	}
	if d.RemoveStaging == nil {
		d.RemoveStaging = defaultRemoveStaging(d.Cfg.Env.CacheHome)
	}
	if d.Stdout == nil {
		d.Stdout = io.Discard
	}
	if _, ok := d.Analysts["interactive"]; !ok {
		if d.Analysts == nil {
			d.Analysts = map[string]Analyst{}
		}
		d.Analysts["interactive"] = InteractiveAnalyst{}
	}
}

// State is what stages share.
type State struct {
	Deps    *Deps
	Params  Params
	Lens    config.Lens
	Profile config.Profile
	Scope   config.Scope
	Window  Window
	Analyst string
	Run     ledger.Run
}

// Result is the outcome of a run.
type Result struct {
	Run ledger.Run
}

// stages is the one place the pipeline is listed: lock and run row, gate,
// evidence, analyst, finish. Cleanup (lock release, staging deletion, session
// close) runs after the last stage on every path.
func stages(d *Deps) []Stage {
	list := []Stage{lockStage{}, gateStage{}}
	if d.Evidence != nil {
		list = append(list, d.Evidence)
	}
	return append(list, analystStage{}, finishStage{})
}

// Run executes one run of a lens over a scope and window.
func Run(ctx context.Context, d *Deps, p Params) (Result, error) {
	d.defaults()
	s, err := resolve(d, p)
	if err != nil {
		return Result{}, err
	}
	if _, err := Reap(ctx, d); err != nil {
		return Result{}, err
	}
	if p.RunKey != "" {
		prev, err := d.Ledger.LastRunForKey(ctx, p.RunKey)
		if err == nil && prev.State == ledger.RunFiled {
			return Result{}, &AlreadyFiledError{Key: p.RunKey, RunID: prev.ID}
		}
		if err != nil && !errors.Is(err, ledger.ErrNotFound) {
			return Result{}, err
		}
	}
	// The end of every run that wrote a run row emits the gauges, whatever
	// the outcome; the push is best effort and bounded.
	defer func() {
		if s.Run.ID != 0 {
			metrics.Emit(context.WithoutCancel(ctx), d.Ledger, d.Now())
		}
	}()
	for _, st := range stages(d) {
		if err := st.Run(ctx, s); err != nil {
			return Result{Run: s.Run}, finishFailed(ctx, d, s, err)
		}
	}
	return Result{Run: s.Run}, nil
}

// finishFailed ends the run for a stage error and returns the error to report.
// Before the run row exists (the lock stage failed) there is nothing to end.
func finishFailed(ctx context.Context, d *Deps, s *State, err error) error {
	if s.Run.ID == 0 {
		return err
	}
	// Detached: the failure must be recorded even when ctx is already cancelled.
	bg := context.WithoutCancel(ctx)
	state, reason := ledger.RunFailed, err.Error()
	var sk *SkippedError
	if errors.As(err, &sk) {
		state, reason = ledger.RunSkipped, sk.Reason
	}
	cur, gerr := d.Ledger.GetRun(bg, s.Run.ID)
	if gerr == nil {
		d.cleanupRun(bg, cur)
	}
	if eerr := endRun(bg, d, s.Run.ID, state, reason); eerr != nil {
		return fmt.Errorf("%w (also failed to record the end of the run: %v)", err, eerr)
	}
	return err
}

func resolve(d *Deps, p Params) (*State, error) {
	cfg := d.Cfg
	lens, ok := cfg.Lens(p.Lens)
	if !ok {
		return nil, &UsageError{Msg: fmt.Sprintf("unknown lens %q", p.Lens)}
	}
	sc, ok := cfg.Scope(p.Scope)
	if !ok {
		var names []string
		for _, x := range cfg.Scopes {
			names = append(names, x.Name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, &UsageError{Msg: fmt.Sprintf("scope %q is not configured: no scopes are configured", p.Scope)}
		}
		return nil, &UsageError{Msg: fmt.Sprintf("scope %q is not configured; configured scopes: %s", p.Scope, strings.Join(names, ", "))}
	}
	profName := p.Profile
	if profName == "" {
		profName = lens.Profile
	}
	prof, ok := cfg.Profile(profName)
	if !ok {
		return nil, &UsageError{Msg: fmt.Sprintf("unknown profile %q", profName)}
	}
	analyst := p.Analyst
	if analyst == "" {
		analyst = prof.Backend
	}
	if p.EvidenceOnly {
		analyst = "none"
	}
	switch analyst {
	case "ccpool", "interactive", "none":
	default:
		return nil, &UsageError{Msg: fmt.Sprintf("--analyst must be ccpool, interactive or none, got %q", analyst)}
	}
	w, err := ResolveWindow(p.From, p.To, p.Duration, p.At, d.Now())
	if err != nil {
		return nil, &UsageError{Msg: err.Error()}
	}
	return &State{Deps: d, Params: p, Lens: lens, Profile: prof, Scope: sc, Window: w, Analyst: analyst}, nil
}

// lockStage takes the (lens, scope) lock and writes the run row, with the
// process pid and start time, in one transaction: a busy lock writes no row.
type lockStage struct{}

func (lockStage) Name() string { return "lock" }

func (lockStage) Run(ctx context.Context, s *State) error {
	d := s.Deps
	pid := d.PID
	started, _ := d.Proc.StartTime(pid)
	state := ledger.RunRunning
	if s.Analyst == "interactive" {
		// No pid check for an awaiting run: the process exits, the run
		// and its lock stay until filed or the TTL expires.
		state, pid, started = ledger.RunAwaitingAnalyst, 0, time.Time{}
	}
	trigger := "manual"
	if s.Params.RunKey != "" {
		trigger = "scheduled"
	}
	row := ledger.Run{
		Lens: s.Lens.ID, Scope: s.Scope.Name,
		WindowFrom: s.Window.FromText(), WindowTo: s.Window.ToText(),
		RunKey: s.Params.RunKey, ScheduleID: s.Params.ScheduleID, Trigger: trigger,
		State: state, Analyst: s.Analyst, Profile: s.Profile.Name, Model: s.Profile.Model,
		PID: pid, StartedAt: started,
	}
	return d.Ledger.WithTx(ctx, func(t *ledgerTx) error {
		r, err := t.InsertRun(ctx, row)
		if err != nil {
			return err
		}
		if err := t.AcquireLock(ctx, r.Lens, r.Scope, r.ID, pid, started); err != nil {
			return err
		}
		s.Run = r
		return nil
	})
}

// analystStage hands the run to its analyst backend when one is registered.
// An interactive run waits in AwaitingAnalyst for `agent file --run`.
type analystStage struct{}

func (analystStage) Name() string { return "analyst" }

func (analystStage) Run(ctx context.Context, s *State) error {
	if a, ok := s.Deps.Analysts[s.Analyst]; ok {
		return a.Analyse(ctx, s)
	}
	return nil
}

// finishStage ends the run: Filed (with zero items until later stages file
// some), or, for an interactive run, left AwaitingAnalyst holding its lock
// and staging.
type finishStage struct{}

func (finishStage) Name() string { return "finish" }

func (finishStage) Run(ctx context.Context, s *State) error {
	d := s.Deps
	var staging string
	err := d.Ledger.WithTx(ctx, func(t *ledgerTx) error {
		r, err := t.GetRun(ctx, s.Run.ID)
		if err != nil {
			return err
		}
		if r.State == ledger.RunAwaitingAnalyst {
			s.Run = r // keeps the lock and staging until filed or expired
			return nil
		}
		staging, r.StagingPath = r.StagingPath, ""
		r.State = ledger.RunFiled
		r.EndedAt = d.Now().UTC()
		r.ElapsedMinutes = r.EndedAt.Sub(r.CreatedAt).Minutes()
		if err := t.UpdateRun(ctx, r); err != nil {
			return err
		}
		s.Run = r
		return releaseOwnLock(ctx, t, r)
	})
	if err == nil && staging != "" {
		_ = d.RemoveStaging(staging)
	}
	return err
}
