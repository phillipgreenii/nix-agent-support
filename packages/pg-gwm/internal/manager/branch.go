package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// preservedPrefix is the namespace that holds only preserved-work branches
// (row 13).
const preservedPrefix = "pg-gwm/"

// branchEnv is everything planBranch reads: the request, the repository's
// local refs and worktrees, the managed state, and the git questions it may
// ask (so a test can answer them without a repository).
type branchEnv struct {
	req     LeaseRequest
	heads   map[string]bool     // local branches
	remotes map[string][]string // branch name -> remotes that track it
	gitWTs  []gitWT
	st      *state.State
	now     time.Time
	main    string // the main checkout's real path

	// resolve returns the full commit of ref against local refs, or a
	// ref-unresolvable error.
	resolve func(ref string) (string, error)
	// validBranch reports whether b can be a branch name.
	validBranch func(b string) bool
}

func orHEAD(ci string) string {
	if ci == "" {
		return "HEAD"
	}
	return ci
}

// newBranchOK refuses a branch name that cannot be created: not a valid
// name, inside the preserved-work namespace (row 13), or in a directory/file
// conflict with an existing branch.
func (e branchEnv) newBranchOK(b string) error {
	if !e.validBranch(b) {
		return ErrUsage(fmt.Sprintf("%q is not a valid branch name", b),
			"pass a valid name, or -b/--detach", map[string]any{"branch": b})
	}
	if strings.HasPrefix(b, preservedPrefix) {
		return ErrUsage(fmt.Sprintf("branch %q is in the %s namespace, which holds only preserved work", b, preservedPrefix),
			"choose a branch name outside "+preservedPrefix, map[string]any{"branch": b})
	}
	parts := strings.Split(b, "/")
	for i := 1; i < len(parts); i++ {
		if p := strings.Join(parts[:i], "/"); e.heads[p] {
			return e.dfConflict(b, p)
		}
	}
	for h := range e.heads {
		if strings.HasPrefix(h, b+"/") {
			return e.dfConflict(b, h)
		}
	}
	return nil
}

// resetBranchOK is newBranchOK for -B, which also accepts an existing branch
// (outside the preserved-work namespace).
func (e branchEnv) resetBranchOK(b string) error {
	if e.heads[b] && !strings.HasPrefix(b, preservedPrefix) {
		return nil
	}
	return e.newBranchOK(b)
}

func (e branchEnv) dfConflict(b, other string) error {
	msg := fmt.Sprintf("cannot lock ref 'refs/heads/%s': 'refs/heads/%s' exists; cannot create 'refs/heads/%s'", b, other, b)
	return ErrUsage(fmt.Sprintf("branch %q cannot be created beside existing branch %q", b, other),
		"pass -b with another name, or --detach", map[string]any{"branch": b, "git": msg})
}

// held finds the worktree that has branch b checked out, if any.
func (e branchEnv) held(b string) (gw gitWT, mw *state.Worktree, ok bool) {
	for _, g := range e.gitWTs {
		if g.Branch != b {
			continue
		}
		for i := range e.st.Worktrees {
			if realPath(e.st.Worktrees[i].Path) == realPath(g.Path) {
				return g, &e.st.Worktrees[i], true
			}
		}
		return g, nil, true
	}
	return gitWT{}, nil, false
}

// existing is rows 1, 10, 11 and 12 for a branch that exists locally.
func (e branchEnv) existing(b string) (provision.BranchSpec, *state.Worktree, error) {
	g, mw, ok := e.held(b)
	switch {
	case !ok:
		return provision.BranchSpec{Branch: b}, nil, nil
	case mw == nil:
		return provision.BranchSpec{}, nil, ErrBranchInUse(b, g.Path, "", "")
	case mw.Status == state.Released:
		return provision.BranchSpec{Branch: b}, mw, nil
	}
	name, id := "", ""
	if mw.Lease != nil {
		name, id = mw.Lease.Name, mw.Lease.ID
	}
	return provision.BranchSpec{}, nil, ErrBranchInUse(b, g.Path, name, id)
}

// planBranch applies the branch table (rows 1 to 13): it returns what the
// worktree is to check out and, for row 12, the released worktree that
// already holds the branch. It changes nothing.
func planBranch(e branchEnv) (provision.BranchSpec, *state.Worktree, error) {
	r := e.req
	switch {
	case r.Detach: // row 7
		c, err := e.resolve(orHEAD(r.CommitIsh))
		if err != nil {
			return provision.BranchSpec{}, nil, err
		}
		return provision.BranchSpec{Detach: true, Commit: c}, nil, nil

	case r.Create: // rows 4, 5
		b := r.Branch
		if e.heads[b] {
			return provision.BranchSpec{}, nil, ErrUsage(fmt.Sprintf("branch %q already exists", b),
				"use -B to reset it, or check it out without -b", map[string]any{"branch": b})
		}
		if err := e.newBranchOK(b); err != nil {
			return provision.BranchSpec{}, nil, err
		}
		c, err := e.resolve(orHEAD(r.CommitIsh))
		if err != nil {
			return provision.BranchSpec{}, nil, err
		}
		return provision.BranchSpec{Branch: b, Create: true, Commit: c}, nil, nil

	case r.Reset: // row 6, with rows 10 to 12 for a branch some worktree holds
		b := r.Branch
		if g, mw, held := e.held(b); held {
			// -B never resets a branch a worktree holds; a released holder
			// is leased as it stands only when no other commit was asked for.
			if mw != nil && mw.Status == state.Released && r.CommitIsh == "" {
				return provision.BranchSpec{Branch: b}, mw, nil
			}
			name, id := "", ""
			if mw != nil && mw.Lease != nil {
				name, id = mw.Lease.Name, mw.Lease.ID
			}
			return provision.BranchSpec{}, nil, ErrBranchInUse(b, g.Path, name, id)
		}
		if err := e.resetBranchOK(b); err != nil {
			return provision.BranchSpec{}, nil, err
		}
		c, err := e.resolve(orHEAD(r.CommitIsh))
		if err != nil {
			return provision.BranchSpec{}, nil, err
		}
		return provision.BranchSpec{Branch: b, Reset: true, Commit: c}, nil, nil

	case r.CommitIsh != "": // rows 1, 2, 3
		ci := r.CommitIsh
		if e.heads[ci] {
			return e.existing(ci)
		}
		if _, err := e.resolve(ci); err != nil {
			if rs := e.remotes[ci]; len(rs) == 1 { // row 2
				start := rs[0] + "/" + ci
				if _, rerr := e.resolve(start); rerr != nil {
					return provision.BranchSpec{}, nil, rerr
				}
				if err := e.newBranchOK(ci); err != nil {
					return provision.BranchSpec{}, nil, err
				}
				// The remote-tracking name, not its commit, so git sets up
				// tracking as git worktree add does.
				return provision.BranchSpec{Branch: ci, Create: true, Commit: start}, nil, nil
			}
			return provision.BranchSpec{}, nil, err
		}
		c, _ := e.resolve(ci)
		return provision.BranchSpec{Detach: true, Commit: c}, nil, nil // row 3

	default: // row 8; row 9 never reaches here
		b := r.Name
		if !e.validBranch(b) {
			return provision.BranchSpec{}, nil, ErrUsage(
				fmt.Sprintf("the name %q is not a valid branch name, so it cannot name the branch", b),
				"pass -b <branch>, --detach, or a valid name", map[string]any{"name": b},
			)
		}
		if e.heads[b] {
			return e.existing(b)
		}
		if err := e.newBranchOK(b); err != nil {
			return provision.BranchSpec{}, nil, err
		}
		c, err := e.resolve("HEAD")
		if err != nil {
			return provision.BranchSpec{}, nil, err
		}
		return provision.BranchSpec{Branch: b, Create: true, Commit: c}, nil, nil
	}
}

// loadBranchEnv gathers the repository's refs and worktrees for planBranch.
func (m *Manager) loadBranchEnv(ctx context.Context, r LeaseRequest, st *state.State, now time.Time) (branchEnv, error) {
	base := r.BaseDir
	if base == "" {
		base = m.Repo.Main
	}
	e := branchEnv{
		req: r, st: st, now: now, main: m.Repo.Main,
		heads: map[string]bool{}, remotes: map[string][]string{},
	}

	out, err := m.git(ctx, m.Repo.Main, "worktree", "list", "--porcelain")
	if err != nil {
		return e, err
	}
	e.gitWTs = parseWorktreeList(out)

	refs, err := m.git(ctx, m.Repo.Main, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return e, err
	}
	remotesOut, err := m.git(ctx, m.Repo.Main, "remote")
	if err != nil {
		return e, err
	}
	remotes := strings.Fields(remotesOut)
	for _, ref := range strings.Split(refs, "\n") {
		if h, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			e.heads[h] = true
			continue
		}
		rest, ok := strings.CutPrefix(ref, "refs/remotes/")
		if !ok {
			continue
		}
		for _, rem := range remotes {
			if b, ok := strings.CutPrefix(rest, rem+"/"); ok && b != "HEAD" {
				e.remotes[b] = append(e.remotes[b], rem)
			}
		}
	}

	e.resolve = func(ref string) (string, error) {
		if strings.HasPrefix(ref, "-") {
			return "", ErrUsage(fmt.Sprintf("%q is not a commit-ish", ref), "", map[string]any{"commit_ish": ref})
		}
		out, err := m.git(ctx, base, "rev-parse", "--verify", ref+"^{commit}")
		if err != nil {
			var ge *gitrun.GitError
			if errors.As(err, &ge) {
				return "", ErrRefUnresolvable(ref, strings.TrimSpace(ge.Stderr))
			}
			return "", err
		}
		return strings.TrimSpace(out), nil
	}
	e.validBranch = func(b string) bool {
		if b == "" || strings.HasPrefix(b, "-") {
			return false
		}
		_, err := m.git(ctx, m.Repo.Main, "check-ref-format", "refs/heads/"+b)
		return err == nil
	}
	return e, nil
}
