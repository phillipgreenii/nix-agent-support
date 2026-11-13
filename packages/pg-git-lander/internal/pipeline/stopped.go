package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oprecord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// This file is what a waiter does when the lock it needs is held by a STOPPED
// operation (design section 8.9): wait for a young retryable stop whose owner
// is expected to resume it at once, finish or abort a stop whose owner died,
// and refuse anything that needs its owner's decision. There is no queue and
// no fairness: the waiter only acts on the record it finds.

// YoungStop is how long after it stopped a retryable stop still counts as
// young: its owner (an agent that runs --continue within seconds) is expected
// to take it back, so a waiter waits for it, up to --wait, instead of
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
		if stop.MovedAlready || oprecord.TakesTargetLocks(rec.Stage) {
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
func (d Deps) acquireSettled(ctx context.Context, layer *locks.Layer, wait time.Duration, repoFlag string, try func(context.Context) error) error {
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
			out, rerr := Resume(ctx, d, ResumeRequest{
				Flag: flag, Operation: worktrees.Operation(rec.Operation), Branch: rec.Branch, Dir: layer.Dir, RepoFlag: repoFlag,
			})
			if rerr != nil {
				return rerr
			}
			if out != nil && out.Outcome == outcome.Refused && out.Reason == string(outcome.RefLostToConcurrent) {
				// Another waiter took the record back first: it finishes it;
				// go back to waiting.
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
			settled++
		default:
			return err
		}
	}
}
