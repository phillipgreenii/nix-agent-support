// Package cleanup is step 13 of an operation: once the result is in place it
// disposes of the source worktree, retires or moves BRANCH, restores every
// other worktree the tool switched and deletes the work branch (design
// sections 7.2 and 11.3). Every action is idempotent and detects from git
// state that it is already done, so a resume repeats the whole step.
package cleanup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/branchmove"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/landmove"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// IdlePrefix is the namespace of the placeholder branches.
const IdlePrefix = workbranch.Namespace + "/idle/"

// Input is the operation's recorded facts.
type Input struct {
	// Operation is worktrees.Land or worktrees.Rebase.
	Operation worktrees.Operation
	// NothingToLand is true for a land that had no result to land.
	NothingToLand bool
	OpID          string
	Branch        string
	Target        string // empty for a rebase
	// Tips are the recorded ref tips (tips.branch, tips.target, tips.result).
	Tips outcome.Tips
	// KeepSourceWorktree and KeepSourceBranch are the persisted keep flags.
	KeepSourceWorktree bool
	KeepSourceBranch   bool
	// Worktrees are the chosen worktrees with their starting states.
	Worktrees []*worktrees.Chosen
	// Work is the work worktree, where the work branch is checked out.
	Work *worktrees.Chosen
	// Dir is any worktree of the repository, to run git in.
	Dir string
	// Fence is verified in the transaction that retires or moves BRANCH
	// (empty in unit tests).
	Fence landmove.Fence
	// Chdir changes the process working directory; nil means os.Chdir.
	Chdir func(string) error
}

// Warning is a warning word with its detail.
type Warning struct {
	Word   outcome.Warning
	Detail string
}

// Stop is a stop raised by the cleanup.
type Stop struct {
	Reason outcome.StopReason
	Detail string
}

// Result is what the cleanup did.
type Result struct {
	// BranchFate is outcome.BranchDeleted, BranchMoved or BranchKept.
	BranchFate string
	// WorktreeFate is empty when no source worktree was on BRANCH.
	WorktreeFate outcome.Fate
	// OldBranchTip is BRANCH's tip before the operation.
	OldBranchTip string
	Restored     []outcome.Restored
	Warnings     []Warning
	// Stop is non-nil when the cleanup stopped before finishing.
	Stop *Stop
}

// Cleaner performs the cleanup.
type Cleaner struct {
	Git *gitexec.Git
}

func branchRef(b string) string { return "refs/heads/" + b }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// PlaceholderName is the placeholder branch of a worktree admin name.
func PlaceholderName(adminName string) string {
	s := strings.Trim(unsafeName.ReplaceAllString(adminName, "-"), ".-")
	s = strings.ReplaceAll(s, "..", "-")
	s = strings.TrimSuffix(s, ".lock")
	if s == "" {
		s = "worktree"
	}
	return IdlePrefix + s
}

func (c *Cleaner) out(ctx context.Context, dir string, args ...string) (string, gitexec.Result) {
	r := c.Git.Run(ctx, dir, args...)
	return strings.TrimSpace(string(r.Stdout)), r
}

func errText(r gitexec.Result) string {
	if r.TimedOut {
		return gitexec.ErrTimeout.Error()
	}
	s := strings.TrimSpace(string(r.Stderr))
	if s == "" && r.StartErr != nil {
		s = r.StartErr.Error()
	}
	if s == "" {
		s = fmt.Sprintf("exit %d", r.Code)
	}
	return s
}

// tip returns the oid of a branch, or "" when it does not exist.
func (c *Cleaner) tip(ctx context.Context, dir, branch string) (string, error) {
	o, r := c.out(ctx, dir, "rev-parse", "--verify", "--quiet", branchRef(branch)+"^{commit}")
	if r.TimedOut {
		return "", gitexec.ErrTimeout
	}
	if r.OK() {
		return o, nil
	}
	if r.Code == 1 {
		return "", nil
	}
	return "", fmt.Errorf("cannot read %s: %s", branchRef(branch), errText(r))
}

func (c *Cleaner) isAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	_, r := c.out(ctx, dir, "merge-base", "--is-ancestor", a, b)
	if r.TimedOut {
		return false, gitexec.ErrTimeout
	}
	switch {
	case r.OK():
		return true, nil
	case r.Code == 1:
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %s", errText(r))
}

func (c *Cleaner) listed(ctx context.Context, dir string) ([]gitexec.Worktree, error) {
	return c.Git.Worktrees(ctx, dir)
}

func findPath(wts []gitexec.Worktree, p string) *gitexec.Worktree {
	rp := resolve(p)
	for i := range wts {
		if resolve(wts[i].Path) == rp {
			return &wts[i]
		}
	}
	return nil
}

func resolve(p string) string {
	if r, err := evalSymlinks(p); err == nil {
		return r
	}
	return p
}

func gone(w *gitexec.Worktree) bool {
	if w == nil || w.Prunable {
		return true
	}
	_, err := os.Stat(w.Path)
	return err != nil
}

// Run performs the cleanup.
func (c *Cleaner) Run(ctx context.Context, in Input) Result {
	res := Result{OldBranchTip: in.Tips.Branch}
	stop := func(reason outcome.StopReason, f string, a ...any) Result {
		res.Stop = &Stop{Reason: reason, Detail: fmt.Sprintf(f, a...)}
		return res
	}
	wts, err := c.listed(ctx, in.Dir)
	if err != nil {
		return stop(outcome.StopGitError, "%v", err)
	}
	if len(wts) == 0 {
		return stop(outcome.StopGitError, "git worktree list returned nothing")
	}
	root := wts[0].Path

	var sw *worktrees.Chosen
	if in.Operation == worktrees.Land {
		for _, w := range in.Worktrees {
			if w.Has(worktrees.Source) && w.OnBranch {
				sw = w
				break
			}
		}
	}
	// swLeftOnBranch: the source worktree stays where BRANCH goes.
	swLeftOnBranch := false
	swRemoved := false

	// 1. The source worktree.
	if in.Operation == worktrees.Land {
		for _, w := range in.Worktrees {
			if w != sw && w.Has(worktrees.Source) {
				res.WorktreeFate = outcome.Fate{Fate: outcome.WorktreeKept}
			}
		}
	}
	if sw != nil {
		switch {
		case in.KeepSourceWorktree:
			res.WorktreeFate = outcome.Fate{Fate: outcome.WorktreeKept}
			swLeftOnBranch = in.KeepSourceBranch
		case gone(findPath(wts, sw.Path)):
			res.WorktreeFate = outcome.Fate{Fate: outcome.WorktreeRemoved}
			swRemoved = true
		default:
			chdir := in.Chdir
			if chdir == nil {
				chdir = os.Chdir
			}
			if err := chdir(root); err != nil {
				return stop(outcome.StopInternalError, "cannot change directory to %s: %v", root, err)
			}
			_, r := c.out(ctx, root, "worktree", "remove", sw.Path)
			if r.OK() {
				res.WorktreeFate = outcome.Fate{Fate: outcome.WorktreeRemoved}
				swRemoved = true
			} else {
				reason := errText(r)
				res.WorktreeFate = outcome.Fate{Fate: outcome.WorktreeRemoveFailed, Reason: reason}
				res.Warnings = append(res.Warnings, Warning{outcome.WarnSourceWorktreeNotRemove, reason})
				swLeftOnBranch = in.KeepSourceBranch
			}
		}
		if swRemoved {
			wts, err = c.listed(ctx, in.Dir)
			if err != nil {
				return stop(outcome.StopGitError, "%v", err)
			}
		} else if !swLeftOnBranch {
			if st := c.park(ctx, in, sw, &res); st != nil {
				res.Stop = st
				return res
			}
		}
	}

	// 2. BRANCH.
	if st := c.branch(ctx, in, wts, sw, swRemoved, &res); st != nil {
		res.Stop = st
		return res
	}

	// 3. Every other worktree the tool switched.
	for _, w := range in.Worktrees {
		if w == sw && in.Operation == worktrees.Land {
			continue
		}
		lw := findPath(wts, w.Path)
		if gone(lw) {
			continue
		}
		name, head, detached, err := c.state(ctx, w.Path)
		if err != nil {
			return stop(outcome.StopGitError, "%v", err)
		}
		if (w.StartDetached && detached && head == w.Start) || (!w.StartDetached && !detached && name == w.Start) {
			continue
		}
		if err := (&workbranch.Brancher{Git: c.Git}).Restore(ctx, w); err != nil {
			return stop(outcome.StopGitError, "restoring %s: %v", w.Path, err)
		}
		res.Restored = append(res.Restored, outcome.Restored{Worktree: w.Path, State: w.Start})
	}

	// 4. The work branch.
	if err := (&workbranch.Brancher{Git: c.Git}).Delete(ctx, in.Dir, in.OpID); err != nil {
		return stop(outcome.StopGitError, "deleting the work branch: %v", err)
	}
	return res
}

// state is the current branch name (empty when detached) and HEAD oid.
func (c *Cleaner) state(ctx context.Context, path string) (name, head string, detached bool, err error) {
	head, r := c.out(ctx, path, "rev-parse", "HEAD")
	if !r.OK() {
		return "", "", false, fmt.Errorf("git rev-parse HEAD in %s: %s", path, errText(r))
	}
	n, r := c.out(ctx, path, "symbolic-ref", "--short", "-q", "HEAD")
	switch {
	case r.OK():
		return n, head, false, nil
	case r.Code == 1:
		return "", head, true, nil
	}
	return "", "", false, fmt.Errorf("git symbolic-ref in %s: %s", path, errText(r))
}

// park puts the source worktree on its placeholder branch, based on the
// landed TARGET. git switch -C runs inside the worktree itself, because git
// refuses it from any other.
func (c *Cleaner) park(ctx context.Context, in Input, sw *worktrees.Chosen, res *Result) *Stop {
	base, err := c.tip(ctx, in.Dir, in.Target)
	if err != nil {
		return &Stop{outcome.StopGitError, err.Error()}
	}
	if base == "" {
		base = in.Tips.Result
	}
	if base == "" {
		base = in.Tips.Target
	}
	name := PlaceholderName(sw.AdminName)
	if _, r := c.out(ctx, sw.Path, "switch", "-C", name, base); !r.OK() {
		return &Stop{outcome.StopGitError, fmt.Sprintf("parking %s on %s: %s", sw.Path, name, errText(r))}
	}
	return nil
}

func (c *Cleaner) branch(ctx context.Context, in Input, wts []gitexec.Worktree, sw *worktrees.Chosen, swRemoved bool, res *Result) *Stop {
	cur, err := c.tip(ctx, in.Dir, in.Branch)
	if err != nil {
		return &Stop{outcome.StopGitError, err.Error()}
	}
	if in.Operation == worktrees.Land && !in.KeepSourceBranch {
		return c.retire(ctx, in, cur, res)
	}
	res.BranchFate = outcome.BranchKept
	if cur == "" {
		return &Stop{outcome.StopGitError, "BRANCH " + in.Branch + " no longer exists"}
	}
	result := in.Tips.Result
	if in.NothingToLand {
		result = in.Tips.Branch // BRANCH stays where it is
	}
	if in.Operation == worktrees.Rebase && cur == in.Tips.Result {
		res.BranchFate = outcome.BranchMoved
		return nil
	}
	mv := &branchmove.Mover{Git: c.Git}
	mi := branchmove.Input{
		Operation: in.Operation, Branch: in.Branch, TipsBranch: in.Tips.Branch,
		Result: result, Dir: in.Dir, Fence: in.Fence,
	}
	for _, w := range wts {
		if w.Branch == branchRef(in.Branch) && (in.Work == nil || resolve(w.Path) != resolve(in.Work.Path)) {
			mi.Holder = &worktrees.Chosen{Path: w.Path}
		}
	}
	target := sw
	if target == nil {
		target = in.Work
	}
	if target != nil && !swRemoved && mi.Holder == nil {
		if name, _, det, err := c.state(ctx, target.Path); err == nil && !det && name != in.Branch {
			mi.Switch = target
		}
	}
	moved := outcome.BranchMoved
	if in.NothingToLand {
		moved = outcome.BranchKept
	}
	if cur == result {
		// Already moved by an earlier pass: only the switch may be left.
		if mi.Switch != nil {
			if _, r := c.out(ctx, mi.Switch.Path, "switch", in.Branch); !r.OK() {
				return c.moveFailure(in, res, outcome.StopCheckoutChanged, errText(r))
			}
		}
		res.BranchFate = moved
		return nil
	}
	r := mv.Move(ctx, mi)
	switch r.Status {
	case branchmove.Moved:
		res.BranchFate = moved
	case branchmove.NotMoved:
		res.Warnings = append(res.Warnings, Warning{outcome.WarnBranchNotMoved, r.Detail})
	default:
		return &Stop{r.Stop, r.Detail}
	}
	return nil
}

func (c *Cleaner) moveFailure(in Input, res *Result, reason outcome.StopReason, detail string) *Stop {
	if in.Operation == worktrees.Rebase {
		return &Stop{reason, detail}
	}
	res.Warnings = append(res.Warnings, Warning{outcome.WarnBranchNotMoved, detail})
	return nil
}

// retire is the compare-and-delete of BRANCH after a land.
func (c *Cleaner) retire(ctx context.Context, in Input, cur string, res *Result) *Stop {
	if cur == "" {
		res.BranchFate = outcome.BranchDeleted // an earlier pass deleted it
		return nil
	}
	res.BranchFate = outcome.BranchKept
	keep := func(reason, detail string) *Stop {
		res.Warnings = append(res.Warnings, Warning{outcome.WarnBranchNotDeleted, reason + ": " + detail})
		return nil
	}
	if cur != in.Tips.Branch {
		return keep(outcome.NotDeletedMoved, fmt.Sprintf("%s is now %s, was %s; its new commits were not landed", in.Branch, cur, in.Tips.Branch))
	}
	landed := in.Tips.Result
	if in.NothingToLand || landed == "" {
		landed = in.Tips.Branch
	}
	ok, err := c.isAncestor(ctx, in.Dir, landed, branchRef(in.Target))
	if err != nil {
		return &Stop{outcome.StopGitError, err.Error()}
	}
	if !ok {
		return keep(outcome.NotDeletedNotContained, fmt.Sprintf("%s does not contain %s", in.Target, landed))
	}
	wts, err := c.listed(ctx, in.Dir)
	if err != nil {
		return &Stop{outcome.StopGitError, err.Error()}
	}
	for _, w := range wts {
		if w.Branch == branchRef(in.Branch) {
			return keep(outcome.NotDeletedMoved, fmt.Sprintf("%s is checked out in %s", in.Branch, w.Path))
		}
	}
	var b strings.Builder
	b.WriteString("start\n")
	for _, l := range in.Fence.Checks() {
		fmt.Fprintf(&b, "verify %s %s\n", l.Ref, l.Oid)
	}
	fmt.Fprintf(&b, "delete %s %s\nprepare\ncommit\n", branchRef(in.Branch), in.Tips.Branch)
	r := c.Git.RunInput(ctx, in.Dir, []byte(b.String()), "update-ref", "--stdin")
	if r.OK() {
		res.BranchFate = outcome.BranchDeleted
		return nil
	}
	if r.TimedOut {
		return &Stop{outcome.StopGitError, "git update-ref: " + gitexec.ErrTimeout.Error()}
	}
	if now, err := c.tip(ctx, in.Dir, in.Branch); err == nil {
		if now == "" {
			res.BranchFate = outcome.BranchDeleted
			return nil
		}
		if now != in.Tips.Branch {
			return keep(outcome.NotDeletedMoved, fmt.Sprintf("%s moved from %s to %s", in.Branch, in.Tips.Branch, now))
		}
	}
	if checks := in.Fence.Checks(); len(checks) > 0 {
		var v strings.Builder
		v.WriteString("start\n")
		for _, l := range checks {
			fmt.Fprintf(&v, "verify %s %s\n", l.Ref, l.Oid)
		}
		v.WriteString("prepare\ncommit\n")
		if !c.Git.RunInput(ctx, in.Dir, []byte(v.String()), "update-ref", "--stdin").OK() {
			return &Stop{outcome.StopLockLost, "a lock record changed"}
		}
	}
	return &Stop{outcome.StopGitError, "git update-ref delete failed: " + errText(r)}
}

func evalSymlinks(p string) (string, error) { return filepath.EvalSymlinks(p) }
