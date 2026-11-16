package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// preservedStem is the start of every preserved-work branch name; the full
// prefix of a ref is refs/heads/ plus this.
const preservedStem = preservedPrefix + "dirty-reaped-"

// preserveStamp is the UTC timestamp layout inside a preserved branch name.
const preserveStamp = "20060102T150405Z"

// refSafe makes one component of a branch name safe: letters, digits, dot,
// underscore and hyphen stay; everything else (a slash included) becomes a
// hyphen, and the sequences git forbids are broken up.
func refSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r), r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", ".-")
	}
	return out
}

// preservedBranchName is pg-gwm/dirty-reaped-<UTC stamp>-<branch>-<name or
// lease id>, each component made ref-safe; an empty branch reads "detached".
func preservedBranchName(at time.Time, branch, nameOrLeaseID string) string {
	if branch == "" {
		branch = "detached"
	}
	n := preservedStem + at.UTC().Format(preserveStamp) + "-" + refSafe(branch) + "-" + refSafe(nameOrLeaseID)
	n = strings.TrimRight(n, ".")
	if strings.HasSuffix(n, ".lock") {
		n += "-"
	}
	return n
}

// preserveMessage is the body of the preservation commit: it names the lease,
// says commit hooks were skipped, and gives the commands that take the commit
// back off while keeping its changes staged.
func preserveMessage(name, leaseID, branch, preservedBranch string) string {
	if branch == "" {
		branch = "(detached HEAD)"
	}
	if name == "" {
		name = "(unnamed)"
	}
	return fmt.Sprintf(`pg-gwm saved the unsaved work of lease %q (%s) on branch %s.
Commit hooks were skipped for this commit.

To take the commit back off and keep its changes staged, make a branch of your own from this one, then undo the commit:

  pg-gwm lease -b <your-branch> %s
  git reset --soft HEAD~1
`, name, leaseID, branch, preservedBranch)
}

// preserveIfNeeded saves w's unsaved work on a preserved branch before the
// worktree is reclaimed or handed out. It holds no lock while git runs and
// takes the repository lock only to record the result. It returns the branch
// written, or "" when nothing was unsaved. A merge, rebase, cherry-pick or
// submodule change returns provision.ErrPreserveBlocked and touches nothing;
// the caller marks the worktree broken.
func (m *Manager) preserveIfNeeded(ctx context.Context, w state.Worktree) (branch string, err error) {
	un, err := m.Prov.Unsaved(ctx, w.Path)
	if err != nil {
		return "", err
	}
	if !un.Any() {
		return "", nil
	}
	if un.Blocker != "" {
		return "", fmt.Errorf("%w: %s", provision.ErrPreserveBlocked, un.Blocker)
	}
	name, leaseID, owner := "", "", ""
	if w.Lease != nil {
		name, leaseID, owner = w.Lease.Name, w.Lease.ID, w.Lease.Owner
	}
	label := name
	if label == "" {
		label = leaseID
	}
	if label == "" {
		label = w.ID
	}
	at := m.now()
	pb := preservedBranchName(at, w.Branch, label)
	if err := m.Prov.Preserve(ctx, w.Path, pb, preserveMessage(name, leaseID, w.Branch, pb)); err != nil {
		return "", err
	}

	lk, cancel, err := m.lockDetached(ctx)
	if err != nil {
		return "", err
	}
	defer cancel()
	defer lk.Unlock()
	lk.State().Preserved = append(lk.State().Preserved, state.Preserved{Branch: pb, LeaseID: leaseID, Name: name, At: at})
	if err := lk.Commit(); err != nil {
		return "", err
	}
	m.event(state.Event{
		Kind: "preserve", WorktreeID: w.ID, LeaseID: leaseID, Name: name, Owner: owner, Outcome: "ok",
		Details: map[string]any{"branch": pb, "files": len(un.Files), "unreachable": len(un.Unreachable)},
	})
	return pb, nil
}

// PreservedList lists the preserved branches: the recorded entries whose
// branch still exists, plus any branch under refs/heads/pg-gwm/dirty-reaped-
// the state does not know (empty name and lease id, the time from its name).
func (m *Manager) PreservedList(ctx context.Context) ([]state.Preserved, error) {
	live, err := m.preservedRefs(ctx)
	if err != nil {
		return nil, err
	}
	st, err := m.Store.ReadUnlocked()
	if err != nil {
		return nil, err
	}
	rec := map[string]state.Preserved{}
	for _, p := range st.Preserved {
		if old, ok := rec[p.Branch]; !ok || !p.At.Before(old.At) {
			rec[p.Branch] = p
		}
	}
	out := make([]state.Preserved, 0, len(live))
	for _, r := range live {
		if p, ok := rec[r.branch]; ok {
			out = append(out, p)
			continue
		}
		out = append(out, state.Preserved{Branch: r.branch, At: r.at})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Branch < out[j].Branch
	})
	return out, nil
}

// preservedRef is one live preserved branch with the time it was written.
type preservedRef struct {
	branch string
	at     time.Time
}

// preservedRefs reads refs/heads/pg-gwm/dirty-reaped-*. The time is the
// stamp in the branch name (when the work was preserved), else the commit's
// committer time.
func (m *Manager) preservedRefs(ctx context.Context) ([]preservedRef, error) {
	out, err := m.git(ctx, m.Repo.Main, "for-each-ref", "--format=%(refname)%09%(committerdate:unix)", "refs/heads/"+preservedStem+"*")
	if err != nil {
		return nil, err
	}
	var refs []preservedRef
	for _, line := range strings.Split(out, "\n") {
		ref, unix, _ := strings.Cut(strings.TrimSpace(line), "\t")
		b, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok {
			continue
		}
		at := stampOf(b)
		if at.IsZero() {
			var sec int64
			if _, err := fmt.Sscan(unix, &sec); err == nil {
				at = time.Unix(sec, 0).UTC()
			}
		}
		refs = append(refs, preservedRef{branch: b, at: at})
	}
	return refs, nil
}

// stampOf is the time in a preserved branch's name, or the zero time.
func stampOf(b string) time.Time {
	rest := strings.TrimPrefix(b, preservedStem)
	if len(rest) < len(preserveStamp) {
		return time.Time{}
	}
	t, err := time.Parse(preserveStamp, rest[:len(preserveStamp)])
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// PreservedPrune deletes preserved branches that are older than olderThan
// (when given) or whose commit another local branch contains. It touches only
// branches under pg-gwm/dirty-reaped- and never one checked out in any
// worktree. dryRun lists what would go and deletes nothing.
func (m *Manager) PreservedPrune(ctx context.Context, olderThan *time.Duration, dryRun bool) ([]string, error) {
	live, err := m.preservedRefs(ctx)
	if err != nil {
		return nil, err
	}
	wl, err := m.git(ctx, m.Repo.Main, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	checked := map[string]bool{}
	for _, g := range parseWorktreeList(wl) {
		if g.Branch != "" {
			checked[g.Branch] = true
		}
	}
	now := m.now()
	deleted := []string{}
	for _, r := range live {
		if checked[r.branch] {
			continue
		}
		old := olderThan != nil && !r.at.IsZero() && now.Sub(r.at) > *olderThan
		if !old {
			contained, err := m.containedElsewhere(ctx, r.branch)
			if err != nil {
				return deleted, err
			}
			if !contained {
				continue
			}
		}
		deleted = append(deleted, r.branch)
	}
	if dryRun || len(deleted) == 0 {
		return deleted, nil
	}
	var done []string
	for _, b := range deleted {
		if _, err := m.git(ctx, m.Repo.Main, "branch", "-D", b); err != nil {
			return done, err
		}
		done = append(done, b)
	}
	lk, err := m.lockRepo(ctx)
	if err != nil {
		return done, err
	}
	defer lk.Unlock()
	gone := map[string]bool{}
	for _, b := range done {
		gone[b] = true
	}
	kept := lk.State().Preserved[:0]
	for _, p := range lk.State().Preserved {
		if !gone[p.Branch] {
			kept = append(kept, p)
		}
	}
	lk.State().Preserved = kept
	if err := lk.Commit(); err != nil {
		return done, err
	}
	return done, nil
}

// containedElsewhere reports whether another local branch contains the
// commit branch b points at.
func (m *Manager) containedElsewhere(ctx context.Context, b string) (bool, error) {
	out, err := m.git(ctx, m.Repo.Main, "for-each-ref", "--contains", "refs/heads/"+b, "--format=%(refname)", "refs/heads")
	if err != nil {
		return false, err
	}
	for _, o := range strings.Fields(out) {
		if o != "refs/heads/"+b {
			return true, nil
		}
	}
	return false, nil
}
