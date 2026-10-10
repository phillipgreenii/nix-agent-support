// enrich.go: the commit enrichment step. For the commits that survive the
// author and range filters it adds the two details the base commit read does
// not carry: the branch:<name> label (only when exactly one LOCAL branch head
// contains the commit) and the line counts git reports for it.
//
// Cost model (pg2-sgj06). enrichCommits reads a whole repo's candidates in a
// CONSTANT number of git calls through the backend's Runner (so the
// PATH-and-HOME-only environment applies), independent of the commit count:
//
//   - line counts: one `git log --no-walk=unsorted -m --numstat` per
//     enrichChunk commits (the same numbers the per-commit `diff-tree` read
//     yields: the root diff for a root commit, "-" columns for a binary file,
//     and for a merge the sum over its parents, see countsOf);
//   - branch labels: one `for-each-ref` for the local heads, one
//     `merge-base --octopus` for the candidates' common ancestor, and one
//     `rev-list --parents --branches --not <ancestor>^@` for the commit
//     graph strictly below the heads down to that ancestor. Containment is
//     then decided in memory: a head contains a candidate exactly when the
//     candidate is reachable from the head's tip inside that region, and every
//     candidate descends from the ancestor, so nothing outside the region can
//     matter.
//
// The old per-commit read (enrichCommit: for-each-ref --contains plus
// diff-tree, two spawns per commit under one shared deadline) stays as the
// reference the tests pin the batch against, and as the fallback when a batch
// call cannot answer (unrelated roots so no common ancestor, a region over
// maxRegionCommits, or an older git): the fallback runs it with at most
// enrichWorkers calls in flight.
package internal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const localHeadPrefix = "refs/heads/"

// enrichWorkers bounds the per-commit fallback's concurrency.
const enrichWorkers = 6

// Variables only so tests can shrink them; production never changes them.
var (
	// enrichChunk is the most commits one batched git call names on its
	// command line.
	enrichChunk = 500
	// maxRegionCommits caps the commit graph the branch labelling reads; a
	// larger region (an ancient common ancestor) falls back to per-commit.
	maxRegionCommits = 100000
)

// commitDetails is what enrichment learns about one commit.
type commitDetails struct {
	// branch is the short name of the single local head that contains the
	// commit, or "" when zero or several heads do (never a guess).
	branch                string
	insertions, deletions int
}

// enrichCommit is the reference per-commit read: the branch heads containing
// sha and its line counts, two git calls. enrichCommits must agree with it.
func enrichCommit(ctx context.Context, r Runner, repo, sha string) (commitDetails, error) {
	var d commitDetails
	var err error
	if d.branch, err = branchOf(ctx, r, repo, sha); err != nil {
		return d, err
	}
	var c lineCounts
	if c, err = countsOf(ctx, r, repo, sha); err != nil {
		return d, err
	}
	d.insertions, d.deletions = c.ins, c.del
	return d, nil
}

type lineCounts struct{ ins, del int }

func branchOf(ctx context.Context, r Runner, repo, sha string) (string, error) {
	heads, err := r.Run(ctx, repo, "for-each-ref", "--format=%(refname)", "--contains", sha, "refs/heads")
	if err != nil {
		return "", fmt.Errorf("branches containing %s: %w", sha, err)
	}
	return singleBranch(heads), nil
}

func countsOf(ctx context.Context, r Runner, repo, sha string) (lineCounts, error) {
	// --root covers a root commit. Renames are not detected so the counts are
	// plain per-path. Observed git behavior (pinned by the batch tests): with
	// -m, diff-tree ignores --first-parent, so a merge commit's counts are
	// its diffs against EVERY parent summed, not only against the first. That
	// is the behavior items have always carried; enrichCommits reproduces it.
	numstat, err := r.Run(ctx, repo, "diff-tree", "--numstat", "--no-commit-id", "--root", "-r", "-m", "--first-parent", "--no-renames", sha)
	if err != nil {
		return lineCounts{}, fmt.Errorf("line counts of %s: %w", sha, err)
	}
	ins, del := sumNumstat(numstat)
	return lineCounts{ins, del}, nil
}

// enrichCommits enriches every sha (distinct) of one repo. The result has an
// entry per sha. A batch that cannot answer falls back to the per-commit read
// for that half (branches or counts); a cancelled or expired ctx is returned
// as the error, never retried.
func enrichCommits(ctx context.Context, r Runner, repo string, shas []string) (map[string]commitDetails, error) {
	out := make(map[string]commitDetails, len(shas))
	if len(shas) == 0 {
		return out, nil
	}

	counts, err := batchLineCounts(ctx, r, repo, shas)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		counts, err = eachBounded(ctx, shas, func(sha string) (lineCounts, error) { return countsOf(ctx, r, repo, sha) })
		if err != nil {
			return nil, err
		}
	}
	branches, err := batchBranches(ctx, r, repo, shas)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		branches, err = eachBounded(ctx, shas, func(sha string) (string, error) { return branchOf(ctx, r, repo, sha) })
		if err != nil {
			return nil, err
		}
	}
	for _, sha := range shas {
		out[sha] = commitDetails{branch: branches[sha], insertions: counts[sha].ins, deletions: counts[sha].del}
	}
	return out, nil
}

// eachBounded runs fn for every sha with at most enrichWorkers in flight and
// returns the first error (cancelling the rest).
func eachBounded[T any](ctx context.Context, shas []string, fn func(sha string) (T, error)) (map[string]T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(map[string]T, len(shas))
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	work := make(chan string)
	for i := 0; i < enrichWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sha := range work {
				if ctx.Err() != nil {
					continue
				}
				v, err := fn(sha)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
				} else {
					out[sha] = v
				}
				mu.Unlock()
			}
		}()
	}
	for _, sha := range shas {
		work <- sha
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// chunks splits xs into slices of at most n.
func chunks(xs []string, n int) [][]string {
	var out [][]string
	for len(xs) > n {
		out = append(out, xs[:n])
		xs = xs[n:]
	}
	return append(out, xs)
}

// batchLineCounts reads the line counts of every sha, enrichChunk per call.
// -m (one diff per parent, summed), --no-renames and log's default root diff
// match the per-commit diff-tree read. diff-tree is plumbing and ignores the
// operator's porcelain diff settings while log honors them, so the plumbing
// defaults are pinned explicitly: Myers (diff.algorithm=histogram moved real
// counts by a line), the indent heuristic, no submodule ignoring, no textconv,
// no external differ, no signature output.
func batchLineCounts(ctx context.Context, r Runner, repo string, shas []string) (map[string]lineCounts, error) {
	out := make(map[string]lineCounts, len(shas))
	for _, chunk := range chunks(shas, enrichChunk) {
		args := append([]string{
			"log", "--no-walk=unsorted", "--numstat", "--no-renames",
			"-m", "--diff-algorithm=myers", "--indent-heuristic", "--ignore-submodules=none",
			"--no-textconv", "--no-ext-diff", "--no-show-signature",
			"--format=%x1e%H",
		}, chunk...)
		args = append(args, "--")
		text, err := r.Run(ctx, repo, args...)
		if err != nil {
			return nil, fmt.Errorf("line counts of %d commits: %w", len(chunk), err)
		}
		for _, rec := range strings.Split(text, recordSep) {
			head, body, _ := strings.Cut(rec, "\n")
			sha := strings.TrimSpace(head)
			if sha == "" {
				continue
			}
			// A merge commit is reported once per parent under -m: sum them.
			ins, del := sumNumstat(body)
			c := out[sha]
			out[sha] = lineCounts{c.ins + ins, c.del + del}
		}
	}
	for _, sha := range shas {
		if _, ok := out[sha]; !ok {
			return nil, fmt.Errorf("line counts: git log did not report %s", sha)
		}
	}
	return out, nil
}

var errRegionTooLarge = errors.New("commit graph region too large to label branches in memory")

// batchBranches returns, per sha, the single local head containing it ("" for
// zero or several), decided in memory over one rev-list of the heads' history
// down to the shas' common ancestor.
func batchBranches(ctx context.Context, r Runner, repo string, shas []string) (map[string]string, error) {
	refsText, err := r.Run(ctx, repo, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads")
	if err != nil {
		return nil, fmt.Errorf("local heads: %w", err)
	}
	type head struct{ tip, name string }
	var heads []head
	for _, line := range strings.Split(refsText, "\n") {
		tip, ref, ok := strings.Cut(strings.TrimSpace(line), " ")
		if name, isHead := strings.CutPrefix(ref, localHeadPrefix); ok && isHead && name != "" {
			heads = append(heads, head{tip, name})
		}
	}

	bases, err := commonAncestors(ctx, r, repo, shas)
	if err != nil {
		return nil, err
	}
	// ^<base>^@ excludes everything at or below each base's parents. Every
	// candidate descends from every base, so none is excluded, and no path
	// from a head to a candidate leaves the region.
	args := []string{"rev-list", "--parents", "--max-count=" + strconv.Itoa(maxRegionCommits+1), "--branches", "--not"}
	for _, b := range bases {
		args = append(args, b+"^@")
	}
	args = append(args, "--")
	regionText, err := r.Run(ctx, repo, args...)
	if err != nil {
		return nil, fmt.Errorf("commit graph of %s: %w", repo, err)
	}
	lines := strings.Split(regionText, "\n")
	if len(lines) > maxRegionCommits {
		return nil, errRegionTooLarge
	}
	index := make(map[string]int, len(lines))
	parents := make([][]string, 0, len(lines))
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		index[f[0]] = len(parents)
		parents = append(parents, f[1:])
	}

	candidate := make(map[string]bool, len(shas))
	for _, s := range shas {
		candidate[s] = true
	}
	holders := make(map[string]int, len(shas))
	holder := make(map[string]string, len(shas))
	stamp := make([]int, len(parents))
	shaAt := make([]string, len(parents))
	for s, i := range index {
		shaAt[i] = s
	}
	for k, h := range heads {
		start, ok := index[h.tip]
		if !ok {
			continue // tip is at or below the base: it holds no candidate
		}
		stack := []int{start}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if stamp[n] == k+1 {
				continue
			}
			stamp[n] = k + 1
			if s := shaAt[n]; candidate[s] {
				holders[s]++
				holder[s] = h.name
			}
			for _, p := range parents[n] {
				if pi, ok := index[p]; ok {
					stack = append(stack, pi)
				}
			}
		}
	}
	out := make(map[string]string, len(shas))
	for _, s := range shas {
		if holders[s] == 1 {
			out[s] = holder[s]
		}
	}
	return out, nil
}

// commonAncestors returns the merge bases of all shas (an octopus merge-base,
// reduced chunk by chunk so a long list never overflows the command line).
// Candidates with no common ancestor (unrelated roots) are an error: the
// caller falls back to the per-commit read.
func commonAncestors(ctx context.Context, r Runner, repo string, shas []string) ([]string, error) {
	cur := shas
	for {
		var next []string
		seen := map[string]bool{}
		for _, chunk := range chunks(cur, enrichChunk) {
			text, err := r.Run(ctx, repo, append([]string{"merge-base", "--octopus"}, chunk...)...)
			if err != nil {
				return nil, fmt.Errorf("common ancestor: %w", err)
			}
			for _, b := range strings.Fields(text) {
				if !seen[b] {
					seen[b] = true
					next = append(next, b)
				}
			}
		}
		if len(next) == 0 {
			return nil, errors.New("common ancestor: none reported")
		}
		if len(cur) <= enrichChunk {
			return next, nil
		}
		cur = next
	}
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
