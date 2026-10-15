// commits.go: the per-repo commit collector. For one configured repo it reads
// the operator's commits from every LOCAL branch head and returns one commit
// activity item per commit.
//
// Range semantics. The acceptance range is [since, before) on AUTHOR date.
// git log's own --since/--until filter on COMMITTER date, so the final
// inclusion decision is made here on the parsed author date, and git is only
// asked to bound the walk. It gets no --until, ever: a commit amended or
// rebased after before keeps its in-range author date and must still be
// found. It gets --since only as a committer-date FLOOR, since minus
// commitDateMargin, never since itself: the floor makes the walk cost
// O(window) instead of O(history) (unbounded, a large monorepo clone's walk
// took about 10s of CPU and starved the 60s list_activity budget). git stops
// walking a path at the first commit whose committer date is older than the
// floor, so the margin is what keeps two clock-skew shapes in the result: a
// commit whose committer date predates its in-range author date, and a commit
// reachable only through a descendant whose committer date is older than its
// own (out-of-order committer dates). Skew beyond the margin is the accepted
// loss. The same floor bounds the branch-label region enrichment reads (every
// commit this walk found is inside it, so no common-ancestor read is needed),
// and enrichment runs under its own budget, the op deadline minus
// enrichReserve, so a host stall that spends it still returns the commits
// read (truncated, without labels or line counts) instead of nothing
// (pg2-p5asw). With since omitted no floor is passed and the walk covers the whole
// history, which is the caller's explicit choice. Likewise git's --author is a
// substring match, so the exact author email is re-checked against the
// configured addresses.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

const (
	recordSep = "\x1e"
	fieldSep  = "\x1f"
	// logFormat: record separator, then sha, author date (strict ISO 8601),
	// author email, parent shas, subject, body (the body is last: it is
	// free text spanning lines).
	logFormat = "--pretty=format:%x1e%H%x1f%aI%x1f%ae%x1f%P%x1f%s%x1f%b"

	// commitDateMargin widens the committer-date floor passed to git log as
	// --since: the floor is since minus this margin (see the range semantics
	// in this file's header). It covers the out-of-order committer date the
	// author-date fixture pins (11.6 days before since) with room to spare.
	commitDateMargin = 14 * 24 * time.Hour
	// gitDateLayout is git's strict ISO 8601 (the %cI layout, numeric
	// offset), which git's date parser reads exactly.
	gitDateLayout = "2006-01-02T15:04:05-07:00"
)

// sinceFloorArg returns the --since argument that bounds the walk for since:
// the committer-date floor since minus commitDateMargin, in UTC. ok is false
// when no floor may be passed: a zero since (open-ended, the caller's
// explicit choice), or a floor outside the years git's date parser reads
// exactly (1970-2099), where git would silently guess a different date
// instead of failing.
func sinceFloorArg(since time.Time) (arg string, ok bool) {
	if since.IsZero() {
		return "", false
	}
	floor := since.Add(-commitDateMargin).UTC()
	if y := floor.Year(); y < 1970 || y > 2099 {
		return "", false
	}
	return "--since=" + floor.Format(gitDateLayout), true
}

// enrichReserve is the tail of the op deadline that enrichment never uses: the
// commit read is the part of a pull worth keeping, and a host stall that eats
// the whole budget (pg2-p5asw) would otherwise take it down with the
// enrichment. A variable only so tests can widen it.
var enrichReserve = 5 * time.Second

// enrichBudget derives the context enrichment runs under: ctx's deadline minus
// enrichReserve. When the op has no deadline, or leaves less than the reserve,
// the budget is the op's own deadline moved earlier, possibly into the past
// (then enrichment is skipped outright and the commits come back plain).
func enrichBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok {
		return context.WithDeadline(ctx, dl.Add(-enrichReserve))
	}
	return context.WithCancel(ctx)
}

// logSkip records one skipped configured path: exactly one line, naming the
// path and why, on this backend's own stderr (never fatal, no wire carrier).
func (b *Backend) logSkip(path, reason string) {
	line := fmt.Sprintf("pg-connector-activity-git: skipping %q: %s\n", path, reason)
	_, _ = fmt.Fprint(b.stderr, line)
}

// collectCommits is the collector seam ListActivity calls: it reads every
// configured repo_paths entry in order, then every repo discovered under
// repo_search_paths (see repoList), and concatenates the per-repo items.
// Dedupe by id is NOT done here;
// ListActivity does it over the final list. When the op deadline fires after
// at least one repo was read, the repos already read come back with
// truncated=true instead of an error.
func (b *Backend) collectCommits(ctx context.Context, cfg Config, emails []string, since, before time.Time) ([]schema.ActivityItem, bool, error) {
	asOf := b.now().UTC().Format(time.RFC3339)
	items := []schema.ActivityItem{}
	truncated := false
	for _, repo := range b.repoList(cfg) {
		got, ok, partial, err := b.collectRepoCommits(ctx, repo, cfg.IncludeMerges, emails, since, before, asOf)
		if err != nil {
			// The op deadline (or a cancel) fired mid-read: keep every repo
			// already read and say so with truncated=true, which the caller
			// MUST treat as incomplete coverage, rather than discarding them.
			// With nothing read yet there is nothing to keep: the error stands.
			if ctx.Err() != nil && len(items) > 0 {
				b.logSkip(repo, "deadline reached; returning the repos already read as a truncated result")
				return items, true, nil
			}
			return nil, false, err
		}
		if !ok {
			continue
		}
		items = append(items, got...)
		truncated = truncated || partial
	}
	return items, truncated, nil
}

// collectRepoCommits reads one repo. ok is false when the path was skipped
// (missing or not a repo; already logged). An error means a git failure on a
// valid repo, which is never silent. partial is true when the commits were
// read but their enrichment ran out of its budget (see enrichBudget): the
// items then carry no branch label and no line counts, and the caller MUST
// report the result as truncated.
func (b *Backend) collectRepoCommits(ctx context.Context, repo string, includeMerges bool, emails []string, since, before time.Time, asOf string) (items []schema.ActivityItem, ok, partial bool, err error) {
	st, statErr := os.Stat(repo)
	switch {
	case statErr != nil:
		b.logSkip(repo, "path does not exist or is not readable")
		return nil, false, false, nil
	case !st.IsDir():
		b.logSkip(repo, "path is not a directory")
		return nil, false, false, nil
	}
	if _, err := b.runner.Run(ctx, repo, "rev-parse", "--git-dir"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, false, false, fmt.Errorf("pg-connector-activity-git: git is not available: %w", err)
		}
		b.logSkip(repo, "not a git repository")
		return nil, false, false, nil
	}

	args := []string{"log", "--branches", "--fixed-strings", "--regexp-ignore-case"}
	if !includeMerges {
		args = append(args, "--no-merges")
	}
	floor, bounded := sinceFloorArg(since)
	if bounded {
		args = append(args, floor)
	}
	for _, e := range emails {
		args = append(args, "--author="+e)
	}
	args = append(args, logFormat)
	out, err := b.runner.Run(ctx, repo, args...)
	if err != nil {
		return nil, false, false, fmt.Errorf("pg-connector-activity-git: read commits of %s: %w", repo, err)
	}
	commits, err := parseLog(out)
	if err != nil {
		return nil, false, false, fmt.Errorf("pg-connector-activity-git: read commits of %s: %w", repo, err)
	}

	// Filter first, then enrich the survivors in one batch: the git cost of
	// enrichment must not scale with the commit count (pg2-sgj06).
	seen := map[string]bool{}
	var kept []commitRecord
	for _, c := range commits {
		if seen[c.sha] || !emailListed(emails, c.email) {
			continue
		}
		seen[c.sha] = true
		if !since.IsZero() && c.when.Before(since) {
			continue
		}
		if !c.when.Before(before) {
			continue
		}
		kept = append(kept, c)
	}
	shas := make([]string, len(kept))
	for i, c := range kept {
		shas[i] = c.sha
	}
	// The branch region is bounded by the same committer-date floor the log
	// walk used (every kept commit was found by that walk), not by the
	// candidates' common ancestor (pg2-p5asw, see enrich.go).
	enrichFloor := ""
	if bounded {
		enrichFloor = floor
	}
	enrichCtx, cancelEnrich := enrichBudget(ctx)
	defer cancelEnrich()
	details, err := enrichCommitsSince(enrichCtx, b.runner, repo, shas, enrichFloor)
	if err != nil {
		if enrichCtx.Err() == nil || ctx.Err() != nil {
			return nil, false, false, fmt.Errorf("pg-connector-activity-git: enrich commits of %s: %w", repo, err)
		}
		// Only enrichment's own budget ran out: the op deadline is still
		// ahead. The commits are already read; keep them without the extras.
		b.logSkip(repo, "enrichment budget spent; returning its commits without branch labels and line counts as a truncated result")
		details, partial = nil, true
	}

	ident := repoIdent(ctx, b.runner, repo)
	items = []schema.ActivityItem{}
	for _, c := range kept {
		det, enriched := details[c.sha]
		labels := []string{"repo:" + ident}
		if det.branch != "" {
			labels = append(labels, "branch:"+det.branch)
		}
		fm := map[string]any{
			"sha":          c.sha,
			"repo_path":    repo,
			"author_email": c.email,
			"refs":         extractRefs(c.subject, c.body),
		}
		if enriched {
			fm["insertions"], fm["deletions"] = det.insertions, det.deletions
		} else {
			// Never a made-up 0: a later full read replaces this entry.
			fm["enriched"] = false
		}
		fields, _ := json.Marshal(fm)
		entityID := ident + "@" + c.sha
		items = append(items, schema.ActivityItem{
			ID:         entityID,
			Kind:       KindCommit,
			EntityType: KindCommit,
			EntityID:   entityID,
			OccurredAt: c.when.Format(time.RFC3339),
			Summary:    c.subject,
			Labels:     labels,
			Fields:     fields,
			AsOf:       asOf,
			Stale:      false,
		})
	}
	return items, true, partial, nil
}

type commitRecord struct {
	sha, email, subject, body string
	when                      time.Time
}

// parseLog parses logFormat output.
func parseLog(out string) ([]commitRecord, error) {
	var recs []commitRecord
	for _, rec := range strings.Split(out, recordSep) {
		rec = strings.Trim(rec, "\r\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, fieldSep, 6)
		if len(f) != 6 {
			return nil, fmt.Errorf("unparseable git log record %q", rec)
		}
		when, err := time.Parse(time.RFC3339, f[1])
		if err != nil {
			return nil, fmt.Errorf("commit %s: unparseable author date %q: %w", f[0], f[1], err)
		}
		recs = append(recs, commitRecord{sha: f[0], when: when, email: f[2], subject: f[4], body: f[5]})
	}
	return recs, nil
}

// emailListed reports whether email is exactly one of the configured
// addresses (case-insensitively).
func emailListed(emails []string, email string) bool {
	for _, e := range emails {
		if strings.EqualFold(e, email) {
			return true
		}
	}
	return false
}
