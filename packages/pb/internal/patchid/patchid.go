// Package patchid computes and scans git patch-ids (--stable), which survive the
// local rebases this workflow uses (commit SHAs change, the diff does not).
// See the design spec "Key facts" and repo-base PoC for the verified behaviour
// (rebase-stable; within-~3-line-context rebase MISSES; squash LOSES; binary works).
package patchid

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pb/internal/run"
)

type Client struct {
	R run.Runner
}

// firstField returns the patch-id (first whitespace-delimited token) of a
// `git patch-id` output line "<patchid> <sha>".
func firstField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// Compute returns the patch-id of commitish in the repo at repoPath:
//
//	git -C repoPath show <commitish> | git -C repoPath patch-id --stable
func (c Client) Compute(ctx context.Context, repoPath, commitish string) (string, error) {
	show, err := c.R.Run(ctx, "git", []string{"-C", repoPath, "show", commitish}, run.Options{})
	if err != nil {
		return "", fmt.Errorf("git show %s: %w", commitish, err)
	}
	res, err := c.R.Run(ctx, "git", []string{"-C", repoPath, "patch-id", "--stable"},
		run.Options{Stdin: show.Stdout})
	if err != nil {
		return "", fmt.Errorf("git patch-id: %w", err)
	}
	id := firstField(strings.SplitN(strings.TrimSpace(res.Stdout), "\n", 2)[0])
	if id == "" {
		return "", fmt.Errorf("git patch-id produced no id for %s", commitish)
	}
	return id, nil
}

// IsAncestor reports whether ancestor is an ancestor of descendant.
func (c Client) IsAncestor(ctx context.Context, repoPath, ancestor, descendant string) bool {
	_, err := c.R.Run(ctx, "git",
		[]string{"-C", repoPath, "merge-base", "--is-ancestor", ancestor, descendant}, run.Options{})
	return err == nil
}

// ScanPatchIDs returns the set of patch-ids in the given log range:
//
//	git -C repoPath log -p --no-merges <revRange...> | git patch-id --stable
//
// revRange is split on spaces into git args (e.g. "base..tip" or "-n 100 tip").
// It is the key set of ScanPatchIDCommits; use that when the COMMIT carrying a
// patch is needed as well.
func (c Client) ScanPatchIDs(ctx context.Context, repoPath, revRange string) (map[string]bool, error) {
	byID, err := c.ScanPatchIDCommits(ctx, repoPath, revRange)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(byID))
	for id := range byID {
		set[id] = true
	}
	return set, nil
}

// ScanPatchIDCommits scans the same range as ScanPatchIDs and additionally reports
// which COMMIT each patch-id came from:
//
//	git -C repoPath log -p --no-merges <revRange...> | git patch-id --stable
//
// `git patch-id` prints "<patch-id> <commit-sha>" per patch, the sha taken from the
// `commit <sha>` header `git log -p` emits — so the commit mapping is a free
// by-product of the scan, and no extra git invocation (nor any extra gate metadata)
// is needed to recover it.
//
// A patch-id maps to a SLICE because one diff can appear more than once in a range
// (a cherry-pick), and the copies differ in ancestry even though they are
// diff-identical; callers that test ancestry MUST consider every entry. The slice
// may be EMPTY while the key is present: the key set is exactly ScanPatchIDs'
// answer and is preserved verbatim, so a patch whose sha could not be read still
// registers as found rather than silently vanishing from the scan.
func (c Client) ScanPatchIDCommits(ctx context.Context, repoPath, revRange string) (map[string][]string, error) {
	args := []string{"-C", repoPath, "log", "-p", "--no-merges"}
	args = append(args, strings.Fields(revRange)...)
	logRes, err := c.R.Run(ctx, "git", args, run.Options{})
	if err != nil {
		return nil, fmt.Errorf("git log -p %s: %w", revRange, err)
	}
	if strings.TrimSpace(logRes.Stdout) == "" {
		return map[string][]string{}, nil
	}
	idRes, err := c.R.Run(ctx, "git", []string{"-C", repoPath, "patch-id", "--stable"},
		run.Options{Stdin: logRes.Stdout})
	if err != nil {
		return nil, fmt.Errorf("git patch-id (scan): %w", err)
	}
	byID := map[string][]string{}
	for line := range strings.SplitSeq(idRes.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		if _, seen := byID[id]; !seen {
			byID[id] = nil
		}
		if len(fields) > 1 {
			byID[id] = append(byID[id], fields[1])
		}
	}
	return byID, nil
}
