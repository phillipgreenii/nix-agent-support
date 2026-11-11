// Package workbranch manages the real work branch pg-git-lander/<op_id>: its
// creation in the work worktree, the restoring of every worktree the tool
// switched, its deletion, and the destination-worktree switches the fenced
// move needs when the destination and work worktree are the same.
package workbranch

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// Namespace is the branch namespace the tool owns.
const Namespace = "pg-git-lander"

// Name is the work branch name for an operation id.
func Name(opID string) string { return Namespace + "/" + opID }

// InNamespace reports whether name is pg-git-lander or under pg-git-lander/.
func InNamespace(name string) bool {
	return name == Namespace || strings.HasPrefix(name, Namespace+"/")
}

// CheckNamespace refuses a BRANCH or TARGET in the tool's namespace with
// namespace-collision. It needs no lock. target may be empty (a rebase).
func CheckNamespace(branch, target string) *worktrees.Refusal {
	for _, n := range []string{branch, target} {
		if n != "" && InNamespace(n) {
			return &worktrees.Refusal{
				Reason: outcome.RefNamespaceCollision, Path: n,
				Message: fmt.Sprintf("%s is in the %s branch namespace, which the tool owns", n, Namespace),
			}
		}
	}
	return nil
}

// Brancher switches worktrees over a gitexec.Git.
type Brancher struct {
	Git *gitexec.Git
}

func (b *Brancher) run(ctx context.Context, dir string, args ...string) (string, error) {
	r := b.Git.Run(ctx, dir, args...)
	switch {
	case r.TimedOut:
		return "", fmt.Errorf("git %s: %w", args[0], gitexec.ErrTimeout)
	case !r.OK():
		return string(r.Stderr), fmt.Errorf("git %s in %s: exit %d: %s%v", args[0], dir, r.Code, strings.TrimSpace(string(r.Stderr)), r.StartErr)
	}
	return string(r.Stdout), nil
}

// Create creates the work branch pg-git-lander/<opID> at tip and checks it
// out in the worktree c with git switch -c. When the switch fails because an
// untracked file collides with a tracked one of tip, the worktree is restored
// and a switch-refused *worktrees.Refusal naming the path is returned; any
// other failure is a plain error (after a restore attempt).
func (b *Brancher) Create(ctx context.Context, c *worktrees.Chosen, opID, tip string) error {
	name := Name(opID)
	stderr, err := b.run(ctx, c.Path, "switch", "-c", name, tip)
	if err == nil {
		return nil
	}
	rerr := b.Restore(ctx, c)
	if paths := collidingPaths(stderr); len(paths) > 0 {
		return &worktrees.Refusal{
			Reason: outcome.RefSwitchRefused, Path: paths[0], Role: worktrees.Work,
			Message: fmt.Sprintf("switching %s to %s would overwrite untracked %s", c.Path, name, strings.Join(paths, ", ")),
		}
	}
	if rerr != nil {
		return fmt.Errorf("%w (restore: %v)", err, rerr)
	}
	return err
}

// collidingPaths parses the paths git lists after its untracked-file
// collision message ("would be overwritten by checkout").
func collidingPaths(stderr string) []string {
	var paths []string
	in := false
	for _, line := range strings.Split(stderr, "\n") {
		switch {
		case strings.Contains(line, "untracked working tree files would be overwritten"):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			paths = append(paths, strings.TrimSpace(line))
		case in:
			in = false
		}
	}
	return paths
}

// Restore returns the worktree c to its recorded starting state: the branch
// name, or the detached oid. It is idempotent.
func (b *Brancher) Restore(ctx context.Context, c *worktrees.Chosen) error {
	return b.switchTo(ctx, c.Path, c.Start, c.StartDetached)
}

// RestoreAll restores every worktree, continuing past failures and returning
// the first error.
func (b *Brancher) RestoreAll(ctx context.Context, cs []*worktrees.Chosen) error {
	var first error
	for _, c := range cs {
		if err := b.Restore(ctx, c); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (b *Brancher) switchTo(ctx context.Context, dir, start string, detached bool) error {
	if detached {
		_, err := b.run(ctx, dir, "switch", "--detach", start)
		return err
	}
	_, err := b.run(ctx, dir, "switch", start)
	return err
}

// Delete deletes the work branch; no worktree may have it checked out. A
// missing branch is not an error. dir is any worktree of the repository.
func (b *Brancher) Delete(ctx context.Context, dir, opID string) error {
	name := Name(opID)
	if r := b.Git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); !r.OK() && r.Code == 1 {
		return nil
	}
	_, err := b.run(ctx, dir, "branch", "-D", name)
	return err
}

// SwitchDestinationToTarget switches the destination worktree, where the work
// branch is checked out, back to target before the fast-forward.
func (b *Brancher) SwitchDestinationToTarget(ctx context.Context, dw *worktrees.Chosen, target string) error {
	return b.switchTo(ctx, dw.Path, target, false)
}

// SwitchDestinationToWork switches the destination worktree to the work
// branch again, after a failure past the switch to target.
func (b *Brancher) SwitchDestinationToWork(ctx context.Context, dw *worktrees.Chosen, opID string) error {
	return b.switchTo(ctx, dw.Path, Name(opID), false)
}
