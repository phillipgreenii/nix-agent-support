package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oprecord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// This file is what a waiter does when the lock it needs is held by a STOPPED
// operation (design section 8.9): wait for a young retryable stop, whose
// owner is expected to resume it at once (the waiter does not check that the
// owner is alive: the stopping command has exited by then, so the wait is
// bounded only by YoungStop and --wait), finish or abort a stop whose owner
// died, and refuse anything that needs its owner's decision. There is no
// queue and no fairness: the waiter only acts on the record it finds.

// YoungStop is how long after it stopped a retryable stop still counts as
// young: its owner (an agent that runs --continue within seconds) is expected
// to take it back (nothing checks that it is alive), so a waiter waits for it, up to --wait, instead of
// refusing. An older stop needs a decision and is refused at once. It is a
// constant, not a setting: the 41 stops of the audit of 2026-11-13 were all
// resumed or aborted within 44 s.
const YoungStop = 2 * time.Minute

// maxSettle bounds how many stopped operations of other branches one waiter
// finishes or aborts for a single lock step.
const maxSettle = 3

type stoppedAction int

const (
	// actRefuse: the record needs its owner's decision.
	actRefuse stoppedAction = iota
	// actWait: the owner is expected to resume it shortly.
	actWait
	// actAbort: the owner is dead and the operation did not move the target.
	actAbort
	// actFinish: the owner is dead and the operation moved the target (or
	// was moving it): only the clean-up is left.
	actFinish
)

func (a stoppedAction) String() string {
	return [...]string{"refuse", "wait", "abort", "finish"}[a]
}

// waitsForStop reports whether a retryable stop of reason at stage is one the
// waiter waits for: a transient failure (the host-load exit 75), or a lost
// lock at the clean-up, after the move succeeded.
func waitsForStop(reason, stage string) bool {
	switch outcome.StopReason(reason) {
	case outcome.StopTransient:
		return true
	case outcome.StopLockLost:
		return stage == outcome.StageCleanup
	}
	return false
}

// judgeStopped decides what to do about a lock refused because a stopped
// record holds it. holder names the operation (from the short record that
// refused). It returns the action and the stopped BRANCH record when known.
func (d Deps) judgeStopped(ctx context.Context, layer *locks.Layer, holder outcome.Holder) (stoppedAction, *locks.Record, error) {
	if holder.OpID == "" || holder.Branch == "" {
		return actRefuse, nil, nil
	}
	st, err := layer.Read(ctx, locks.BranchRef(holder.Branch))
	if err != nil {
		return actRefuse, nil, err
	}
	rec := st.Rec
	if rec == nil || rec.Role != locks.RoleBranch || rec.OpID != holder.OpID {
		return actRefuse, nil, nil
	}
	if rec.State == locks.StateRunning {
		// A resume is taking its records back one by one: it is alive.
		return actWait, rec, nil
	}
	if rec.Owner.Token != "" && rec.Owner.Token == d.Owner.Token {
		return actRefuse, rec, nil // this very process stopped it
	}
	stop := rec.Stop
	if stop == nil || !layer.OnThisHost(rec) {
		// Another host, or no stop object: liveness cannot be judged, so the
		// owner counts as alive and old.
		return actRefuse, rec, nil
	}
	if outcome.StopReason(stop.Reason) == outcome.StopHolderDied {
		if !layer.OwnerGone(rec) {
			return actRefuse, rec, nil
		}
		// Finish (--continue) only when the move already happened, so only the
		// clean-up is left; a dead owner whose move did not happen is aborted,
		// never continued: a waiter MUST NOT move someone else's branch onto
		// the target.
		if d.moveHappened(ctx, layer, rec) {
			return actFinish, rec, nil
		}
		return actAbort, rec, nil
	}
	young := !stop.At.IsZero() && d.Clock.Now().Sub(stop.At) < YoungStop
	if stop.Retryable && young && waitsForStop(stop.Reason, rec.Stage) {
		return actWait, rec, nil
	}
	return actRefuse, rec, nil
}

// moveHappened reports whether the stopped operation's move of TARGET already
// happened: the stop says so (moved_already), the stage is past the move, or
// the target tip already is (or contains) the recorded result.
func (d Deps) moveHappened(ctx context.Context, layer *locks.Layer, rec *locks.Record) bool {
	if rec.Stop != nil && rec.Stop.MovedAlready {
		return true
	}
	switch rec.Stage {
	case oprecord.StageAfter, oprecord.StageCleanup:
		return true
	}
	if rec.Operation != oprecord.OperationLand || rec.Move.Intent == nil || rec.Move.ExpectedResult == nil {
		return false
	}
	exp := *rec.Move.ExpectedResult
	r := d.Git.Run(ctx, layer.Dir, "rev-parse", "--verify", "-q", "refs/heads/"+rec.Target+"^{commit}")
	if !r.OK() {
		return false
	}
	tip := strings.TrimSpace(string(r.Stdout))
	return tip == exp || d.Git.Run(ctx, layer.Dir, "merge-base", "--is-ancestor", exp, tip).OK()
}

// SettleFailedError is returned by a waiter that tried to finish or abort the
// stopped operation of a dead owner and did not manage: the operation stopped
// again (or was refused). It wraps the refusal that started the attempt, so
// callers that only know that refusal still handle it, and carries a message
// naming the branch, the op id and the reason of the failed attempt.
type SettleFailedError struct {
	Refused *locks.RefusedError
	Message string
}

func (e *SettleFailedError) Error() string { return e.Message }
func (e *SettleFailedError) Unwrap() error { return e.Refused }

func isStoppedRefusal(r outcome.RefusalReason) bool {
	return r == outcome.RefStoppedPending || r == outcome.RefTargetStopped || r == outcome.RefHolderDied
}

func (d Deps) sleep(ctx context.Context, dur time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, dur)
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// acquireSettled runs try (a lock acquisition through layer) and, while it is
// refused because a stopped operation holds the lock, applies judgeStopped:
// it waits (polling once a second, within wait in total), aborts or finishes
// an operation whose owner died by calling the resume machinery with a
// synthetic request for the holder's branch, and then tries again. A refusal
// that needs the owner's decision is returned as it is. Waiting that runs
// past wait ends in *locks.BusyError. layer's wait is clamped to what remains
// while try runs, so wait bounds the whole step.
func (d Deps) acquireSettled(ctx context.Context, layer *locks.Layer, ev *oplog.Events, wait time.Duration, repoFlag string, try func(context.Context) error) error {
	start := d.Clock.Now()
	orig := layer.Config.Wait
	defer func() { layer.Config.Wait = orig }()
	settled, nextMsg := 0, time.Duration(0)
	for {
		remaining := wait - d.Clock.Since(start)
		if remaining < 0 {
			remaining = 0
		}
		layer.Config.Wait = remaining
		err := try(ctx)
		var ref *locks.RefusedError
		if err == nil || !errors.As(err, &ref) || !isStoppedRefusal(ref.Reason) {
			return err
		}
		act, rec, jerr := d.judgeStopped(ctx, layer, ref.Holder)
		if jerr != nil {
			return jerr
		}
		h := ref.Holder
		if rec != nil {
			h.Stage, h.Operation = rec.Stage, rec.Operation
		}
		if d.BeforeSettle != nil {
			d.BeforeSettle(act.String())
		}
		emit := func(action, detail string) {
			if ev == nil {
				return
			}
			st := oplog.Settle{
				Action: action, StoppedOpID: ref.Holder.OpID, Branch: ref.Holder.Branch, Stage: h.Stage,
				HolderState: ref.Holder.State, HolderAgeS: ref.Holder.AgeS, Detail: detail,
			}
			if rec != nil && rec.Stop != nil {
				st.StopReason = rec.Stop.Reason
			}
			_ = ev.Settle(context.WithoutCancel(ctx), st)
		}
		if act == actWait || act == actRefuse {
			emit(act.String(), "")
		}
		switch act {
		case actWait:
			elapsed := d.Clock.Since(start)
			if elapsed >= wait {
				return &locks.BusyError{Ref: ref.Ref, Holder: h}
			}
			if d.Out != nil && elapsed >= nextMsg {
				fmt.Fprintf(d.Out, "waiting for stopped %s %s (stage %s) to be resumed: %ds elapsed\n", h.Operation, h.Branch, h.Stage, int(elapsed/time.Second))
				nextMsg += locks.MessageEvery
			}
			if err := d.sleep(ctx, min(locks.PollInterval, wait-elapsed)); err != nil {
				return err
			}
		case actAbort, actFinish:
			if settled >= maxSettle {
				return err
			}
			flag := outcome.FlagAbort
			if act == actFinish {
				flag = outcome.FlagContinue
			}
			if d.Out != nil {
				fmt.Fprintf(d.Out, "stopped %s %s: its owner (pid %d) is gone; %s it\n", h.Operation, h.Branch, h.PID,
					map[stoppedAction]string{actAbort: "aborting", actFinish: "finishing"}[act])
			}
			emit(act.String(), "")
			// A waiter that stops the operation again must leave a record no
			// later waiter treats as young: its stop is written not retryable.
			rd := d
			rd.Settling = true
			out, rerr := Resume(ctx, rd, ResumeRequest{
				Flag: flag, Operation: worktrees.Operation(rec.Operation), Branch: rec.Branch, Dir: layer.Dir, RepoFlag: repoFlag,
			})
			if rerr != nil {
				return rerr
			}
			if out != nil && out.Outcome == outcome.Refused && out.Reason == string(outcome.RefLostToConcurrent) {
				// Another waiter took the record back first: it finishes it;
				// go back to waiting.
				emit("lost-to-concurrent", "")
				if d.Out != nil {
					fmt.Fprintf(d.Out, "stopped %s %s was taken back by another invocation; waiting for it\n", h.Operation, h.Branch)
				}
				if err := d.sleep(ctx, locks.PollInterval); err != nil {
					return err
				}
				if d.Clock.Since(start) >= wait {
					return &locks.BusyError{Ref: ref.Ref, Holder: h}
				}
				continue
			}
			if out != nil && out.Outcome == outcome.Refused && out.Reason == string(outcome.RefNothingStopped) {
				settled++ // the record is gone: try the lock again
				continue
			}
			if out == nil || out.Outcome != outcome.Landed && out.Outcome != outcome.Aborted {
				verb := map[stoppedAction]string{actAbort: "abort", actFinish: "finish"}[act]
				reason, msg := "no outcome", ""
				if out != nil {
					reason, msg = string(out.Outcome)+" "+out.Reason, out.Message
				}
				emit("settle-failed", verb+": "+reason)
				return &SettleFailedError{Refused: ref, Message: fmt.Sprintf(
					"tried to %s the stopped %s %s (op %s) whose owner died, and it stopped again (%s: %s); it needs its owner's decision: %s",
					verb, h.Operation, h.Branch, h.OpID, reason, msg, strings.Join(outcome.NextCommands(h.Operation, h.Branch, repoFlag, []string{outcome.FlagContinue, outcome.FlagAbort}), " or "),
				)}
			}
			settled++
		default:
			return err
		}
	}
}
