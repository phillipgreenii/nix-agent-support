// commits.go: the per-repo commit collector. For one configured repo it reads
// the operator's commits from every LOCAL branch head and returns one commit
// activity item per commit.
//
// Range semantics. The acceptance range is [since, before) on AUTHOR date.
// git log's own --since/--until filter on COMMITTER date, so this collector
// passes neither: a commit amended or rebased after before, or one whose
// committer date predates since, must still be found. The final inclusion
// decision is made here on the parsed author date. Likewise git's --author is
// a substring match, so the exact author email is re-checked against the
// configured addresses. With since omitted the walk covers the whole history,
// which is the caller's explicit choice.
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
)

// logSkip records one skipped configured path: exactly one line, naming the
// path and why, on this backend's own stderr (never fatal, no wire carrier).
func (b *Backend) logSkip(path, reason string) {
	line := fmt.Sprintf("pg-connector-activity-git: skipping %q: %s\n", path, reason)
	_, _ = fmt.Fprint(b.stderr, line)
}

// collectCommits is the collector seam ListActivity calls: it reads every
// configured repo_paths entry in order and concatenates the per-repo items.
// Later packets add further repo sources here. Dedupe by id is NOT done here;
// ListActivity does it over the final list.
func (b *Backend) collectCommits(ctx context.Context, cfg Config, emails []string, since, before time.Time) ([]schema.ActivityItem, bool, error) {
	asOf := b.now().UTC().Format(time.RFC3339)
	items := []schema.ActivityItem{}
	for _, repo := range cfg.RepoPaths {
		got, ok, err := b.collectRepoCommits(ctx, repo, cfg.IncludeMerges, emails, since, before, asOf)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		items = append(items, got...)
	}
	return items, false, nil
}

// collectRepoCommits reads one repo. ok is false when the path was skipped
// (missing or not a repo; already logged). An error means a git failure on a
// valid repo, which is never silent.
func (b *Backend) collectRepoCommits(ctx context.Context, repo string, includeMerges bool, emails []string, since, before time.Time, asOf string) (items []schema.ActivityItem, ok bool, err error) {
	st, statErr := os.Stat(repo)
	switch {
	case statErr != nil:
		b.logSkip(repo, "path does not exist or is not readable")
		return nil, false, nil
	case !st.IsDir():
		b.logSkip(repo, "path is not a directory")
		return nil, false, nil
	}
	if _, err := b.runner.Run(ctx, repo, "rev-parse", "--git-dir"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, false, fmt.Errorf("pg-connector-activity-git: git is not available: %w", err)
		}
		b.logSkip(repo, "not a git repository")
		return nil, false, nil
	}

	args := []string{"log", "--branches", "--fixed-strings", "--regexp-ignore-case"}
	if !includeMerges {
		args = append(args, "--no-merges")
	}
	for _, e := range emails {
		args = append(args, "--author="+e)
	}
	args = append(args, logFormat)
	out, err := b.runner.Run(ctx, repo, args...)
	if err != nil {
		return nil, false, fmt.Errorf("pg-connector-activity-git: read commits of %s: %w", repo, err)
	}
	commits, err := parseLog(out)
	if err != nil {
		return nil, false, fmt.Errorf("pg-connector-activity-git: read commits of %s: %w", repo, err)
	}

	ident := repoIdent(ctx, b.runner, repo)
	seen := map[string]bool{}
	items = []schema.ActivityItem{}
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
		det, err := enrichCommit(ctx, b.runner, repo, c.sha)
		if err != nil {
			return nil, false, fmt.Errorf("pg-connector-activity-git: enrich commit of %s: %w", repo, err)
		}
		labels := []string{"repo:" + ident}
		if det.branch != "" {
			labels = append(labels, "branch:"+det.branch)
		}
		fields, _ := json.Marshal(map[string]any{
			"sha":          c.sha,
			"repo_path":    repo,
			"author_email": c.email,
			"insertions":   det.insertions,
			"deletions":    det.deletions,
			"refs":         extractRefs(c.subject, c.body),
		})
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
	return items, true, nil
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
