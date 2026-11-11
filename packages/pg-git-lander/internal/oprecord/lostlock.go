package oprecord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// LostLock is what a holder does when it learns it lost a lock, from a failed
// renewal or a fence check (design section 8.7).
type LostLock struct {
	// Reason is lock-lost, or interrupted when a signal is the cause.
	Reason outcome.StopReason
	// Repo templates the next commands ("" omits --repo).
	Repo string
	// Kill stops the current step's process group; nil sends SIGKILL to the
	// group of the record's current_command.pid.
	Kill func(pid int)
}

// LostResult is the end state of a lost lock: the exit 4 outcome and the
// records this run no longer owned.
type LostResult struct {
	Outcome *outcome.Outcome
	// NotOwned lists the lock refs the holder no longer owned.
	NotOwned []string
	// Swapped lists the refs it swapped to stopped or deleted.
	Swapped []string
}

func killGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// HandleLostLock runs the lost-lock procedure at once: it kills the current
// step's process group, swaps the holder's own BRANCH and worktree records to
// stopped, each only if it is still its own, deletes its TARGET record if it
// is still its own and returns the exit 4 outcome naming the records it no
// longer owned. The caller must stop renewal and touch no worktree after.
func (o *Op) HandleLostLock(ctx context.Context, p LostLock) (*LostResult, error) {
	ctx = context.WithoutCancel(ctx)
	reason := p.Reason
	if reason == "" {
		reason = outcome.StopLockLost
	}
	rec := o.Branch.Record()
	if rec.CurrentCommand != nil {
		kill := p.Kill
		if kill == nil {
			kill = killGroup
		}
		kill(rec.CurrentCommand.PID)
	}
	now := o.now()
	info := StopInfo{Reason: reason, Message: "lock lost"}
	if reason == outcome.StopInterrupted {
		info.Message = "interrupted"
	}
	res := &LostResult{}
	var errs []error
	for _, h := range o.Set.Locks {
		var err error
		switch h.Record().Role {
		case locks.RoleBranch:
			err = h.Update(ctx, func(r *locks.Record) { stopBranch(r, info, now) })
		case locks.RoleWorktree:
			err = h.Update(ctx, func(r *locks.Record) { r.State, r.Lease = locks.StateStopped, nil })
		case locks.RoleTarget:
			err = h.Release(ctx)
		default:
			continue
		}
		switch {
		case err == nil:
			res.Swapped = append(res.Swapped, h.Ref)
		case errors.Is(err, locks.ErrLockLost), strings.HasPrefix(err.Error(), "release "):
			res.NotOwned = append(res.NotOwned, h.Ref)
		default:
			errs = append(errs, err)
		}
	}
	out := outcome.NewStopped(reason, rec.Operation, rec.Branch, rec.Stage, p.Repo)
	out.Message = info.Message
	if len(res.NotOwned) > 0 {
		out.Message = fmt.Sprintf("%s; no longer owned: %s", info.Message, strings.Join(res.NotOwned, ", "))
		out.Paths = nil
	}
	res.Outcome = out
	return res, errors.Join(errs...)
}
