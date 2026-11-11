// Package worktrees selects and checks the three worktree roles of an
// operation (destination, source, work) and raises the fail-fast refusals
// that need no lock. It never creates, switches or removes a worktree.
package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Operation is the kind of operation a selection is for.
type Operation string

// Operations.
const (
	Land   Operation = "land"
	Rebase Operation = "rebase"
)

// Role is a worktree role.
type Role string

// Roles.
const (
	Destination Role = "destination"
	Source      Role = "source"
	Work        Role = "work"
)

// DetailForeignRebase is the dirty detail for a rebase, merge or other
// operation in progress that no record accounts for.
const DetailForeignRebase = "foreign-rebase"

// MainAdminName is the lock name of the main worktree.
const MainAdminName = "_main"

// Refusal is a refusal raised by selection or checking. Reason is exit 5.
type Refusal struct {
	Reason outcome.RefusalReason
	// Role names the role the refusal is about (source, destination or
	// work); when a worktree holds several roles, the first in that order.
	Role Role
	// Path is the worktree or flag value the refusal is about.
	Path string
	// Detail is the refusal's detail word, for example foreign-rebase.
	Detail string
	// Message says what is wrong.
	Message string
}

func (r *Refusal) Error() string {
	s := string(r.Reason)
	if r.Detail != "" {
		s += " (" + r.Detail + ")"
	}
	if r.Role != "" {
		s += " [" + string(r.Role) + " worktree]"
	}
	return s + ": " + r.Message
}

// Input is everything the selection needs. Flag values arrive here already
// resolved against the environment variables by the CLI.
type Input struct {
	Op     Operation
	Branch string
	// Target is the land's TARGET; empty for a rebase.
	Target string
	// TargetFlag, SourceFlag and WorkFlag are --target-worktree,
	// --source-worktree and --work-worktree (or their variables); empty
	// when absent.
	TargetFlag, SourceFlag, WorkFlag string
	// Cwd is the current directory.
	Cwd string
	// KeepSourceWorktree is --keep-source-worktree.
	KeepSourceWorktree bool
	// Accounted reports whether a record accounts for the rebase or other
	// operation in progress in the worktree at path. Nil means none does.
	Accounted func(path string) bool
}

// Chosen is one chosen worktree.
type Chosen struct {
	Path string
	// AdminName is the lock name: the basename of the private git dir, or
	// MainAdminName for the main worktree.
	AdminName string
	Roles     []Role
	// Start is the starting state: the branch name, or the detached oid.
	Start string
	// StartDetached is true when Start is a detached oid.
	StartDetached bool
	// OnBranch is true when the worktree was on BRANCH when the tool started.
	OnBranch bool
	// IsMain is true for the main worktree (first entry of git worktree list).
	IsMain bool
}

// Has reports whether the worktree holds role r.
func (c *Chosen) Has(r Role) bool {
	for _, x := range c.Roles {
		if x == r {
			return true
		}
	}
	return false
}

// Selection is the result of Select.
type Selection struct {
	// Destination is nil for a rebase and when none was found.
	Destination *Chosen
	Source      *Chosen
	Work        *Chosen
	// DestinationFallback is true when the DW was found only by
	// --target-worktree or the current directory (TARGET is checked out
	// nowhere): it is a fallback root, to be checked, locked and switched
	// only when something needs it.
	DestinationFallback bool
	// TargetCheckedOut is true when TARGET was checked out somewhere when the
	// selection ran (the starting state).
	TargetCheckedOut bool
	// SourceDerived is true when BRANCH was checked out in the SW and
	// --source-worktree was not passed.
	SourceDerived bool
	// SourceExplicit is true when --source-worktree was passed (consent to
	// remove the SW).
	SourceExplicit bool
	// Warnings are warning words for the pipeline to attach to the outcome.
	Warnings []outcome.Warning
	// Worktrees lists each distinct chosen worktree once, in the order
	// source, destination, work.
	Worktrees []*Chosen
}

// Selector selects worktrees over a gitexec.Git.
type Selector struct {
	Git *gitexec.Git
}

// Select runs the cascade of design section 11.1 and the checks. It is
// callable again after the locks are held and re-runs everything. A refusal
// is returned as *Refusal; any other error is a git or I/O failure.
func (s *Selector) Select(ctx context.Context, in Input) (*Selection, error) {
	wts, err := s.Git.Worktrees(ctx, in.Cwd)
	if err != nil {
		return nil, err
	}
	if len(wts) == 0 {
		return nil, fmt.Errorf("git worktree list returned no worktrees")
	}
	if err := s.checkLayout(ctx, in.Cwd, wts); err != nil {
		return nil, err
	}
	if err := s.fixSeparateGitDirMain(ctx, in.Cwd, wts); err != nil {
		return nil, err
	}
	main := wts[0].Path

	branchRef := "refs/heads/" + in.Branch
	branchWT := findByBranch(wts, branchRef)
	var targetWT *gitexec.Worktree
	if in.Op == Land {
		targetWT = findByBranch(wts, "refs/heads/"+in.Target)
	}
	cwdWT, err := s.cwdWorktree(ctx, in.Cwd, wts)
	if err != nil {
		return nil, err
	}

	sel := &Selection{TargetCheckedOut: targetWT != nil}
	byPath := map[string]*Chosen{}
	var order []*Chosen
	choose := func(w *gitexec.Worktree, role Role) (*Chosen, error) {
		c, ok := byPath[w.Path]
		if !ok {
			var err error
			c, err = s.describe(ctx, w, main, branchRef)
			if err != nil {
				return nil, err
			}
			byPath[w.Path] = c
			order = append(order, c)
		}
		if !c.Has(role) {
			c.Roles = append(c.Roles, role)
		}
		return c, nil
	}

	// Destination worktree (a land only).
	var dw *gitexec.Worktree
	if in.Op == Land {
		switch {
		case targetWT != nil:
			if in.TargetFlag != "" && !samePath(in.TargetFlag, targetWT.Path) {
				return nil, &Refusal{
					Reason: outcome.RefTargetWTMismatch, Role: Destination, Path: in.TargetFlag,
					Message: fmt.Sprintf("--target-worktree %s is not %s, where %s is checked out", in.TargetFlag, targetWT.Path, in.Target),
				}
			}
			dw = targetWT
		case in.TargetFlag != "":
			dw = findByPath(wts, in.TargetFlag)
			if dw == nil {
				return nil, notAWorktree(Destination, in.TargetFlag)
			}
			sel.DestinationFallback = true
		case cwdWT != nil:
			dw = cwdWT
			sel.DestinationFallback = true
		}
	}

	// Source worktree.
	var sw *gitexec.Worktree
	switch {
	case branchWT != nil:
		if in.SourceFlag != "" && !samePath(in.SourceFlag, branchWT.Path) {
			return nil, &Refusal{
				Reason: outcome.RefSourceWTMismatch, Role: Source, Path: in.SourceFlag,
				Message: fmt.Sprintf("--source-worktree %s is not %s, where %s is checked out", in.SourceFlag, branchWT.Path, in.Branch),
			}
		}
		sw = branchWT
		sel.SourceDerived = in.SourceFlag == ""
	case in.SourceFlag != "":
		sw = findByPath(wts, in.SourceFlag)
		if sw == nil {
			return nil, notAWorktree(Source, in.SourceFlag)
		}
	case in.Op == Land && dw != nil:
		sw = dw
	case in.Op == Rebase && cwdWT != nil:
		sw = cwdWT
	default:
		return nil, &Refusal{
			Reason: outcome.RefNoWorktree, Role: Source, Path: in.Cwd,
			Message: fmt.Sprintf("%s is checked out nowhere and no worktree was named or found from %s", in.Branch, in.Cwd),
		}
	}
	sel.SourceExplicit = in.SourceFlag != ""

	// Work worktree.
	var ww *gitexec.Worktree
	switch {
	case in.WorkFlag != "":
		ww = findByPath(wts, in.WorkFlag)
		if ww == nil {
			return nil, notAWorktree(Work, in.WorkFlag)
		}
	default:
		ww = sw
	}

	var err2 error
	if sel.Source, err2 = choose(sw, Source); err2 != nil {
		return nil, err2
	}
	if dw != nil {
		if sel.Destination, err2 = choose(dw, Destination); err2 != nil {
			return nil, err2
		}
	}
	if sel.Work, err2 = choose(ww, Work); err2 != nil {
		return nil, err2
	}
	sel.Worktrees = order

	// Fail fast, before anything else is looked at that could cost time.
	if in.Op == Land {
		if r := s.failFast(in, sel); r != nil {
			return nil, r
		}
	}

	// Checks: every chosen worktree except a fallback-only destination.
	for _, c := range order {
		if sel.DestinationFallback && sel.Destination == c && len(c.Roles) == 1 {
			continue
		}
		if r, err := s.CheckClean(ctx, c, in.Accounted); err != nil {
			return nil, err
		} else if r != nil {
			return nil, r
		}
	}
	if sparse, err := s.sparse(ctx, sel.Work.Path); err != nil {
		return nil, err
	} else if sparse {
		return nil, &Refusal{
			Reason: outcome.RefUnsupportedLayout, Role: Work, Path: sel.Work.Path, Detail: "sparse-checkout",
			Message: "the work worktree uses sparse checkout",
		}
	}

	if sel.Work.IsMain && !sel.Work.OnBranch {
		sel.Warnings = append(sel.Warnings, outcome.WarnWorkInDestination)
	}
	return sel, nil
}

// failFast raises cwd-would-be-removed and main-worktree-removal.
func (s *Selector) failFast(in Input, sel *Selection) *Refusal {
	sw := sel.Source
	if !sw.OnBranch || in.KeepSourceWorktree {
		return nil
	}
	if sw.IsMain {
		return &Refusal{
			Reason: outcome.RefMainWorktreeRemoval, Role: Source, Path: sw.Path,
			Message: "the source worktree is the main worktree, which is never removed",
		}
	}
	if sel.SourceDerived && inside(in.Cwd, sw.Path) {
		return &Refusal{
			Reason: outcome.RefCwdWouldBeRemoved, Role: Source, Path: sw.Path,
			Message: fmt.Sprintf("the current directory %s would be removed with the source worktree %s: run from another path; pass --source-worktree %s to confirm the removal; or pass --keep-source-worktree", in.Cwd, sw.Path, sw.Path),
		}
	}
	return nil
}

func (s *Selector) checkLayout(ctx context.Context, cwd string, wts []gitexec.Worktree) error {
	if wts[0].Bare {
		return &Refusal{Reason: outcome.RefUnsupportedLayout, Detail: "bare", Path: wts[0].Path, Message: "the clone is bare"}
	}
	r := s.Git.Run(ctx, cwd, "config", "--get", "core.worktree")
	if r.OK() {
		return &Refusal{
			Reason: outcome.RefUnsupportedLayout, Detail: "core.worktree", Path: wts[0].Path,
			Message: "core.worktree is set",
		}
	}
	return nil
}

// fixSeparateGitDirMain handles a clone made with --separate-git-dir: git
// lists its main worktree under the git dir's path, not the checkout's. When
// the current directory is inside that checkout, the checkout's top level
// replaces the entry; when it is not, the checkout cannot be found from git
// and the entry is left as git printed it (it is then only usable as an
// identity, never as a root).
func (s *Selector) fixSeparateGitDirMain(ctx context.Context, cwd string, wts []gitexec.Worktree) error {
	common, err := s.Git.CommonDir(ctx, cwd)
	if err != nil {
		return err
	}
	if !samePath(wts[0].Path, common) {
		return nil
	}
	gd, err := s.gitDir(ctx, cwd)
	if err != nil || !samePath(gd, common) {
		return nil // cwd is not in the main checkout
	}
	r := s.Git.Run(ctx, cwd, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if r.OK() {
		wts[0].Path = strings.TrimSpace(string(r.Stdout))
	}
	return nil
}

func (s *Selector) sparse(ctx context.Context, dir string) (bool, error) {
	r := s.Git.Run(ctx, dir, "config", "--bool", "--get", "core.sparseCheckout")
	if r.OK() {
		return strings.TrimSpace(string(r.Stdout)) == "true", nil
	}
	if r.Code == 1 { // key not set
		return false, nil
	}
	return false, fmt.Errorf("git config core.sparseCheckout in %s: exit %d: %s", dir, r.Code, strings.TrimSpace(string(r.Stderr)))
}

// cwdWorktree returns the worktree of this clone that cwd is in (not in its
// git dir), or nil.
func (s *Selector) cwdWorktree(ctx context.Context, cwd string, wts []gitexec.Worktree) (*gitexec.Worktree, error) {
	r := s.Git.Run(ctx, cwd, "rev-parse", "--is-inside-work-tree")
	if !r.OK() || strings.TrimSpace(string(r.Stdout)) != "true" {
		return nil, nil
	}
	var best *gitexec.Worktree
	for i := range wts {
		w := &wts[i]
		if inside(cwd, w.Path) && (best == nil || len(w.Path) > len(best.Path)) {
			best = w
		}
	}
	return best, nil
}

func (s *Selector) describe(ctx context.Context, w *gitexec.Worktree, main, branchRef string) (*Chosen, error) {
	c := &Chosen{Path: w.Path, IsMain: w.Path == main, OnBranch: w.Branch == branchRef}
	if w.Detached || w.Branch == "" {
		c.Start, c.StartDetached = w.Head, true
	} else {
		c.Start = strings.TrimPrefix(w.Branch, "refs/heads/")
	}
	name, err := s.AdminName(ctx, w.Path, c.IsMain)
	if err != nil {
		return nil, err
	}
	c.AdminName = name
	return c, nil
}

// AdminName is the lock name of the worktree at path: the basename of its
// private git dir (<common>/worktrees/<name>), or MainAdminName for the main
// worktree. git's own name is canonical, so a typed path's case or symlinks
// cannot give one worktree two names.
func (s *Selector) AdminName(ctx context.Context, path string, isMain bool) (string, error) {
	if isMain {
		return MainAdminName, nil
	}
	gd, err := s.gitDir(ctx, path)
	if err != nil {
		return "", err
	}
	return filepath.Base(gd), nil
}

func (s *Selector) gitDir(ctx context.Context, path string) (string, error) {
	r := s.Git.Run(ctx, path, "rev-parse", "--path-format=absolute", "--git-dir")
	if !r.OK() {
		return "", fmt.Errorf("git rev-parse --git-dir in %s: exit %d: %s", path, r.Code, strings.TrimSpace(string(r.Stderr)))
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

// inProgressMarkers are the entries of a git dir that mean an operation is in
// progress.
var inProgressMarkers = []string{
	"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG",
}

// CheckClean runs the dirty check for one chosen worktree: no operation in
// progress that no record accounts for, and no tracked changes. It returns a
// *Refusal, or an error for a git failure.
func (s *Selector) CheckClean(ctx context.Context, c *Chosen, accounted func(string) bool) (*Refusal, error) {
	role := firstRole(c)
	gd, err := s.gitDir(ctx, c.Path)
	if err != nil {
		return nil, err
	}
	if accounted == nil || !accounted(c.Path) {
		for _, m := range inProgressMarkers {
			if _, err := os.Lstat(filepath.Join(gd, m)); err == nil {
				return &Refusal{
					Reason: outcome.RefDirty, Role: role, Path: c.Path, Detail: DetailForeignRebase,
					Message: fmt.Sprintf("%s holds an operation in progress (%s) that no record accounts for", c.Path, m),
				}, nil
			}
		}
	}
	r := s.Git.Run(ctx, c.Path, "status", "--porcelain", "--untracked-files=no")
	if !r.OK() {
		return nil, fmt.Errorf("git status in %s: exit %d: %s", c.Path, r.Code, strings.TrimSpace(string(r.Stderr)))
	}
	if strings.TrimSpace(string(r.Stdout)) != "" {
		return &Refusal{
			Reason: outcome.RefDirty, Role: role, Path: c.Path,
			Message: fmt.Sprintf("%s has tracked changes", c.Path),
		}, nil
	}
	return nil, nil
}

func firstRole(c *Chosen) Role {
	for _, r := range []Role{Source, Destination, Work} {
		if c.Has(r) {
			return r
		}
	}
	return ""
}

func notAWorktree(role Role, path string) *Refusal {
	return &Refusal{
		Reason: outcome.RefNotAWorktree, Role: role, Path: path,
		Message: fmt.Sprintf("%s is not a worktree of this clone", path),
	}
}

func findByBranch(wts []gitexec.Worktree, ref string) *gitexec.Worktree {
	for i := range wts {
		if wts[i].Branch == ref {
			return &wts[i]
		}
	}
	return nil
}

func findByPath(wts []gitexec.Worktree, p string) *gitexec.Worktree {
	for i := range wts {
		if samePath(p, wts[i].Path) {
			return &wts[i]
		}
	}
	return nil
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

func samePath(a, b string) bool { return resolve(a) == resolve(b) }

// inside reports whether p is dir or below it.
func inside(p, dir string) bool {
	p, dir = resolve(p), resolve(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
