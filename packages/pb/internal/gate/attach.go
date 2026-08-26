package gate

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrGatingIncomplete: the child exists but is not fully gated; it was LEFT
	// DEFERRED, so no peer can claim it. Safe to retry; the impl bead must not
	// be closed. The CLI maps this to exit 3.
	ErrGatingIncomplete = errors.New("gating incomplete; child left deferred")
	// ErrChildMayBeWorkable: the child could not be proven absent from bd ready.
	// A peer draining the queue could claim it and "verify" unapplied code. The
	// CLI maps this to exit 4; the impl bead must NOT be closed.
	ErrChildMayBeWorkable = errors.New("child may be workable")
)

// childDeferUntil is a far-future ABSOLUTE date: --defer takes --due's formats,
// which have no year unit.
const childDeferUntil = "2126-01-01"

type GateSpec struct {
	Repo   string
	Commit string
}

type AttachParams struct {
	WorkspaceDir string
	ImplID       string
	Title        string
	Gates        []GateSpec
	Actor        string
	Reason       string
}

type AttachResult struct {
	ChildID       string        `json:"child"`
	Gates         []CreatedGate `json:"gates"`
	CommentFailed bool          `json:"comment_failed,omitempty"`
}

// Attach runs the deferred-first verified-child sequence: create the child
// DEFERRED, prove it is not workable, attach one pn:applied gate per GateSpec,
// un-defer, prove the gates now hold it, and record the link on the impl bead.
// The ordering closes the fleet-claim race: the child is never simultaneously
// workable and ungated.
func Attach(ctx context.Context, d CreateDeps, p AttachParams) (AttachResult, error) {
	if len(p.Gates) == 0 {
		return AttachResult{}, errors.New("at least one gate is required")
	}
	info, err := d.PN.Info(ctx, p.WorkspaceDir)
	if err != nil {
		return AttachResult{}, err
	}
	// Fail on a bad --gate repo key BEFORE the child exists: gate.Create would
	// catch it per gate, but only after CreateBead — turning an invocation typo
	// into a STUCK-routed exit 3.
	for _, g := range p.Gates {
		if _, ok := info.RepoByName(g.Repo); !ok {
			return AttachResult{}, fmt.Errorf("repo %q is not in workspace %q", g.Repo, info.Root)
		}
	}
	dbDir, err := resolveBeadDB(ctx, d, info, p.ImplID)
	if err != nil {
		return AttachResult{}, err
	}

	childID, err := d.BD.CreateBead(ctx, dbDir, p.Title, childDeferUntil,
		"discovered-from:"+p.ImplID, p.Actor)
	if err != nil {
		return AttachResult{}, err
	}
	res := AttachResult{ChildID: childID}

	if err := confirmNotReady(ctx, d, dbDir, childID); err != nil {
		// One repair attempt: re-apply the defer, re-check.
		if uerr := d.BD.UpdateDefer(ctx, dbDir, childID, childDeferUntil, p.Actor); uerr != nil {
			return res, fmt.Errorf("%w: %s: defer re-apply failed: %v", ErrChildMayBeWorkable, childID, uerr)
		}
		if err := confirmNotReady(ctx, d, dbDir, childID); err != nil {
			return res, fmt.Errorf("%w: %s: %v", ErrChildMayBeWorkable, childID, err)
		}
	}

	for _, g := range p.Gates {
		out, err := Create(ctx, d, CreateParams{
			WorkspaceDir: p.WorkspaceDir, BeadID: childID,
			Repo: g.Repo, Commit: g.Commit, Reason: p.Reason,
		})
		res.Gates = append(res.Gates, out.Gates...)
		if err != nil {
			return res, fmt.Errorf("%w: gate for %s@%s: %v", ErrGatingIncomplete, g.Repo, g.Commit, err)
		}
	}

	if err := d.BD.UpdateDefer(ctx, dbDir, childID, "", p.Actor); err != nil {
		return res, fmt.Errorf("%w: %s: un-defer failed: %v", ErrGatingIncomplete, childID, err)
	}
	// The gates, not the defer, must now hold the child out of bd ready.
	if err := confirmNotReady(ctx, d, dbDir, childID); err != nil {
		return res, fmt.Errorf("%w: %s after un-defer: %v", ErrChildMayBeWorkable, childID, err)
	}

	if err := d.BD.Comment(ctx, dbDir, p.ImplID,
		fmt.Sprintf("post-deploy verification gated as %s (pn:applied).", childID),
		p.Actor); err != nil {
		res.CommentFailed = true // gating is complete and safe; only the record failed
	}
	return res, nil
}

// confirmNotReady proves childID is absent from the UNCAPPED bd ready set. A
// successful parse of the envelope is itself the positive control (the prose
// procedure needed a non-empty queue because a text agent cannot verify the
// envelope; the client can, so an empty queue — normal when draining the last
// bead — passes).
func confirmNotReady(ctx context.Context, d CreateDeps, dbDir, childID string) error {
	ids, err := d.BD.ReadyIDs(ctx, dbDir)
	if err != nil {
		return fmt.Errorf("could not prove absence: %v", err)
	}
	for _, id := range ids {
		if id == childID {
			return fmt.Errorf("child %s is present in bd ready", childID)
		}
	}
	return nil
}
