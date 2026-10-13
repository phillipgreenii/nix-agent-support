package gate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/phillipgreenii/pb/internal/bd"
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
	// Description and Acceptance are the child's description and acceptance
	// criteria. A blank value is derived (childDescription, childAcceptance), so
	// the child is always born with both and is claimable once its gates resolve.
	Description string
	Acceptance  string
}

// acceptanceLabel marks a bead the drain workers may claim. The child is always
// created with acceptance criteria, so it always carries it.
const acceptanceLabel = "has-acceptance-criteria"

// WorkflowStateLabels are the labels that record where ONE bead stands in a
// workflow. They describe the implementation bead, not the verification work,
// and are never copied onto the child: a copied human or escalated label parks
// the child out of the drain queue for good, and a copied docket label makes
// plan-decompose treat a task as a docket epic. Everything else on the
// implementation bead is scope (repo, project) or topic and is inherited; pb
// keeps no registry of repo or project labels, which is why this is a deny-list.
// Provenance: ADR 0089's opt-in precheck labels, claude-marketplace/pb/queues.json,
// the beads-lifecycle skill and plan-decompose's audit-docket-label-leak.sh.
// acceptanceLabel is not copied either: the child sets it from its own criteria.
var WorkflowStateLabels = []string{
	"human", "human-focus-required", "needs-split-review", "escalated", "refactor-campaign",
	"worker-ready", "mine", "worktree-review", "auto-session-wrapped",
	"docket", "phase", "phase-trigger",
	acceptanceLabel,
}

// childLabels is the child's label set: the implementation bead's labels minus
// WorkflowStateLabels, plus acceptanceLabel, sorted and de-duplicated.
func childLabels(implLabels []string) []string {
	out := []string{acceptanceLabel}
	for _, l := range implLabels {
		if l != "" && !slices.Contains(WorkflowStateLabels, l) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// childDescription is the child's description when the caller gives none: what
// it verifies, what holds it back, and where the context lives.
func childDescription(impl bd.Bead, p AttachParams) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Post-deploy verification of %s: %s\n\n", p.ImplID, impl.Title)
	b.WriteString("This bead stays out of `bd ready` until `pn workspace apply` has applied:\n")
	for _, g := range p.Gates {
		fmt.Fprintf(&b, "- %s=%s\n", g.Repo, g.Commit)
	}
	fmt.Fprintf(&b, "\n`bd show %s` has what changed (description, acceptance criteria, close reason). Verify it on the live machine as the title says.\n", p.ImplID)
	return b.String()
}

// childAcceptance is the child's acceptance criteria when the caller gives none.
func childAcceptance(p AttachParams) string {
	return "- On the live machine, after the gating `pn workspace apply`: " + p.Title + "\n" +
		"- The evidence (commands and their output) is recorded in a comment on this bead, which is then closed with the verdict; any finding becomes a follow-up bead."
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
// workable and ungated. The child is born with its labels (childLabels), a
// description and acceptance criteria, so it needs no hand edit before a drain
// worker can claim it once the gates resolve; none of that changes readiness.
func Attach(ctx context.Context, d CreateDeps, p AttachParams) (AttachResult, error) {
	// Before ANY external call: an over-long title would otherwise only fail at
	// bd create, after pn/bd probes (pg2-cjakt).
	if err := bd.ValidateTitle(p.Title); err != nil {
		return AttachResult{}, err
	}
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
	// Read the implementation bead BEFORE the child exists: a failure here, like a
	// bad --gate repo above, is an exit-1 mistake with nothing to clean up.
	impl, err := d.BD.ShowBead(ctx, dbDir, p.ImplID)
	if err != nil {
		return AttachResult{}, err
	}
	content := bd.CreateOpts{
		Labels:      childLabels(impl.Labels),
		Description: p.Description,
		Acceptance:  p.Acceptance,
	}
	if strings.TrimSpace(content.Description) == "" {
		content.Description = childDescription(impl, p)
	}
	if strings.TrimSpace(content.Acceptance) == "" {
		content.Acceptance = childAcceptance(p)
	}

	childID, err := d.BD.CreateBead(ctx, dbDir, p.Title, childDeferUntil,
		"discovered-from:"+p.ImplID, p.Actor, content)
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
