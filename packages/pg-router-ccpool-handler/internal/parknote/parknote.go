// Package parknote builds the comment the handler writes on a bead it parks as
// `human` because the session working it is gone (it died before completing, or
// it hit the needs_input stall bound twice in a row; bead pg2-78mxs).
//
// Why a comment: `human` is meant for a blocker only a person can clear. A
// handler park carries the same label but no person-question, so without a
// comment the human queue and an audit cannot tell "the session died; resume or
// land its work" from "a person must decide X", and nothing points at the drain
// worktree or branch the dead session left behind. The comment states the
// trigger, the session, the worktree and branch with their commit state, and
// that no person-question was identified.
//
// Worktree and branch discovery is best effort and bounded: every git child is
// built by gitenv.Command (hermetic environment) under its own timeout, the
// whole discovery under another, and any failure degrades to a line saying so.
// Discovery never fails and never blocks the park.
package parknote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/gitenv"
)

// Marker opens every park note, so the note is findable in a bead's comments.
const Marker = "Handler park note:"

// The bounds are variables only so the tests can shorten them.
var (
	// discoverTimeout bounds the whole worktree and branch discovery.
	discoverTimeout = 20 * time.Second
	// gitTimeout bounds each git child.
	gitTimeout = 5 * time.Second
)

const (
	// gitWaitDelay bounds how long a killed child's inherited pipes are waited on.
	gitWaitDelay = 2 * time.Second
	// drainBranchPrefix is the branch a drain worker's per-bead worktree is on.
	drainBranchPrefix = "drain/"
)

// Git runs `git -C dir args...` and returns its stdout. Tests substitute a fake.
type Git interface {
	Output(ctx context.Context, dir string, args ...string) (string, error)
}

// OSGit is the production Git: hermetic environment, explicit WaitDelay.
type OSGit struct{}

// Output implements Git.
func (OSGit) Output(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitenv.Command(ctx, dir, args...)
	cmd.WaitDelay = gitWaitDelay
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// Input is everything a park note states.
type Input struct {
	// Trigger is the one-line cause, e.g. "session died before completing".
	Trigger string
	// Detail lines follow the trigger (close reason, exit facts, stall bound).
	Detail []string
	// Session is the ccpool session's external id; Display its stable name.
	Session, Display string
	Role, Bead       string
	// Dirs are the checkouts searched for the bead's worktree and branch
	// (the repo root, and the session's working directory when it differs).
	Dirs []string
	// Git is the git seam (nil means OSGit).
	Git Git
}

// Work is one worktree and/or branch the session left behind.
type Work struct {
	Path   string // "" for a branch with no worktree
	Branch string // "" for a detached worktree
	// Ahead is the commit count ahead of the primary branch; AheadKnown is
	// false when it could not be determined.
	Ahead      int
	AheadKnown bool
	// Dirty is "yes", "no" or "unknown" (uncommitted changes in Path).
	Dirty string
}

// Build discovers the bead's drain worktree and branch and renders the note.
func Build(ctx context.Context, in Input) string {
	g := in.Git
	if g == nil {
		g = OSGit{}
	}
	dctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	found, primary, searched, problems := discover(dctx, g, in.Bead, in.Dirs)
	return render(in, found, primary, searched, problems)
}

func render(in Input, found []Work, primary string, searched, problems []string) string {
	var b strings.Builder
	b.WriteString(Marker + " this bead was parked as human by the pg-router-ccpool-handler, not by a person or by the worker.\n\n")
	fmt.Fprintf(&b, "Trigger: %s\n", in.Trigger)
	for _, d := range in.Detail {
		fmt.Fprintf(&b, "  %s\n", d)
	}
	sess := in.Session
	if in.Display != "" && in.Display != in.Session {
		sess += " (" + in.Display + ")"
	}
	fmt.Fprintf(&b, "Session: %s, role %s\n", orUnknown(sess), orUnknown(in.Role))
	b.WriteString("Drain work left behind:\n")
	if len(found) == 0 {
		fmt.Fprintf(&b, "  none found for %s (searched: %s)\n", in.Bead, orNone(strings.Join(searched, ", ")))
	}
	for _, w := range found {
		b.WriteString("  - " + describe(w, primary) + "\n")
	}
	for _, p := range problems {
		fmt.Fprintf(&b, "  (not determined: %s)\n", p)
	}
	b.WriteString("\nNo person-question was identified. This is resume-or-land work, not a decision: ")
	b.WriteString("resume the work from the worktree or branch above, or land its commits, and release the bead. ")
	b.WriteString("Do not wait for a person to answer anything.\n")
	return b.String()
}

func describe(w Work, primary string) string {
	var loc string
	switch {
	case w.Path != "" && w.Branch != "":
		loc = fmt.Sprintf("worktree %s (branch %s)", w.Path, w.Branch)
	case w.Path != "":
		loc = fmt.Sprintf("worktree %s (detached HEAD)", w.Path)
	default:
		loc = fmt.Sprintf("branch %s (no worktree)", w.Branch)
	}
	ahead := "commits ahead of the primary branch: not determined"
	if w.AheadKnown {
		switch {
		case w.Ahead == 0:
			ahead = fmt.Sprintf("no commits ahead of %s", primary)
		default:
			ahead = fmt.Sprintf("%d commit(s) ahead of %s", w.Ahead, primary)
		}
	}
	s := loc + ": " + ahead
	if w.Path != "" {
		s += "; uncommitted changes: " + orUnknown(w.Dirty)
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "no directory known"
	}
	return s
}

// discover finds the worktrees and branches belonging to bead in dirs. It
// returns what it found, the primary branch name used for the ahead counts, the
// directories it searched and a list of things it could not determine.
func discover(ctx context.Context, g Git, bead string, dirs []string) (found []Work, primary string, searched, problems []string) {
	if bead == "" {
		return nil, "", nil, []string{"no bead id"}
	}
	seen := map[string]bool{}
	seenDir := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		searched = append(searched, dir)
		if ctx.Err() != nil {
			problems = append(problems, "discovery timed out")
			break
		}
		out, err := gitOut(ctx, g, dir, "worktree", "list", "--porcelain")
		if err != nil {
			problems = append(problems, fmt.Sprintf("worktree list in %s failed", dir))
			continue
		}
		if primary == "" {
			primary = primaryBranch(ctx, g, dir)
		}
		haveBranch := false
		for _, e := range parseWorktrees(out) {
			if e.branch != drainBranchPrefix+bead && filepath.Base(e.path) != bead {
				continue
			}
			if e.branch == drainBranchPrefix+bead {
				haveBranch = true
			}
			if seen[e.path] {
				continue
			}
			seen[e.path] = true
			found = append(found, measure(ctx, g, dir, primary, e.path, e.branch))
		}
		if !haveBranch && !seen["branch:"+drainBranchPrefix+bead] {
			if _, err := gitOut(ctx, g, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+drainBranchPrefix+bead); err == nil {
				seen["branch:"+drainBranchPrefix+bead] = true
				found = append(found, measure(ctx, g, dir, primary, "", drainBranchPrefix+bead))
			}
		}
	}
	if primary == "" {
		primary = "main"
	}
	return found, primary, searched, problems
}

func measure(ctx context.Context, g Git, dir, primary, path, branch string) Work {
	w := Work{Path: path, Branch: branch, Dirty: "unknown"}
	if branch != "" && primary != "" {
		out, err := gitOut(ctx, g, dir, "rev-list", "--count", primary+".."+branch)
		if err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(out)); perr == nil {
				w.Ahead, w.AheadKnown = n, true
			}
		}
	}
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if out, err := gitOut(ctx, g, path, "status", "--porcelain"); err == nil {
				w.Dirty = "no"
				if strings.TrimSpace(out) != "" {
					w.Dirty = "yes"
				}
			}
		}
	}
	return w
}

// primaryBranch resolves the repo's primary branch: the
// pgii-integrate-branch.primaryBranch config, else origin's HEAD, else main.
// A name that is not a local branch makes the ahead count "not determined".
func primaryBranch(ctx context.Context, g Git, dir string) string {
	name := ""
	if out, err := gitOut(ctx, g, dir, "config", "--get", "pgii-integrate-branch.primaryBranch"); err == nil {
		name = strings.TrimSpace(out)
	}
	if name == "" {
		if out, err := gitOut(ctx, g, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
			name = strings.TrimPrefix(strings.TrimSpace(out), "origin/")
		}
	}
	if name == "" {
		name = "main"
	}
	return name
}

func gitOut(ctx context.Context, g Git, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	return g.Output(cctx, dir, args...)
}

type wtEntry struct{ path, branch string }

// parseWorktrees reads `git worktree list --porcelain`.
func parseWorktrees(out string) []wtEntry {
	var res []wtEntry
	var cur *wtEntry
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			res = append(res, wtEntry{path: strings.TrimPrefix(line, "worktree ")})
			cur = &res[len(res)-1]
		case strings.HasPrefix(line, "branch ") && cur != nil:
			cur.branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	return res
}
