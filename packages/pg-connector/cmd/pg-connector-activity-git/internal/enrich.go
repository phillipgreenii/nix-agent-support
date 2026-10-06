// enrich.go: the per-commit enrichment step. For one commit it adds the two
// details the base commit read does not carry: the branch:<name> label (only
// when exactly one LOCAL branch head contains the commit) and the line counts
// git reports for it.
//
// Cost. Two git calls per qualifying commit, both through the backend's
// Runner (so the PATH-and-HOME-only environment applies): one for-each-ref
// --contains for the heads, one diff-tree --numstat for the counts. Only the
// commits that survive the author and range filters are enriched.
package internal

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const localHeadPrefix = "refs/heads/"

// commitDetails is what enrichCommit learns about one commit.
type commitDetails struct {
	// branch is the short name of the single local head that contains the
	// commit, or "" when zero or several heads do (never a guess).
	branch                string
	insertions, deletions int
}

// enrichCommit reads the branch heads containing sha and its line counts.
func enrichCommit(ctx context.Context, r Runner, repo, sha string) (commitDetails, error) {
	var d commitDetails

	heads, err := r.Run(ctx, repo, "for-each-ref", "--format=%(refname)", "--contains", sha, "refs/heads")
	if err != nil {
		return d, fmt.Errorf("branches containing %s: %w", sha, err)
	}
	d.branch = singleBranch(heads)

	// --first-parent with -m reports a merge commit against its first parent
	// (the change the merge brought onto the branch); --root covers a root
	// commit. Renames are not detected so the counts are plain per-path.
	numstat, err := r.Run(ctx, repo, "diff-tree", "--numstat", "--no-commit-id", "--root", "-r", "-m", "--first-parent", "--no-renames", sha)
	if err != nil {
		return d, fmt.Errorf("line counts of %s: %w", sha, err)
	}
	d.insertions, d.deletions = sumNumstat(numstat)
	return d, nil
}

// singleBranch returns the short name when refs (one full refname per line)
// holds exactly one local head, otherwise "".
func singleBranch(refs string) string {
	var names []string
	for _, line := range strings.Split(refs, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, localHeadPrefix); ok && name != "" {
			names = append(names, name)
		}
	}
	if len(names) != 1 {
		return ""
	}
	return names[0]
}

// sumNumstat totals the added and removed columns of `git diff-tree
// --numstat` output. A binary file reports "-" for both columns; it counts as
// zero lines (git has no line count for it) and never fails the read. A line
// that does not parse is likewise counted as zero.
func sumNumstat(out string) (insertions, deletions int) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 3 {
			continue
		}
		if n, err := strconv.Atoi(f[0]); err == nil {
			insertions += n
		}
		if n, err := strconv.Atoi(f[1]); err == nil {
			deletions += n
		}
	}
	return insertions, deletions
}
