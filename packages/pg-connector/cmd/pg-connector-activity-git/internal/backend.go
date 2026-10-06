// backend.go: Backend implements pkg/provider/activity.Provider against local
// git clones. list_activity is range-shaped and stateless; nothing is stored.
//
// Config: this backend's own opaque backends.pg-connector-activity-git block,
// read inside ListActivity via scriptout.ConfigFromContext:
//
//	author_emails      list of strings; a commit qualifies when its author
//	                   email is in the list. Empty or missing means the op is
//	                   unavailable.
//	repo_paths         list of absolute clone paths.
//	repo_search_paths  list of directories scanned exactly one level deep.
//	include_merges     bool, default false.
//
// A configured repo_paths or repo_search_paths entry that is missing or not a
// repo is skipped, not fatal; the backend logs one line per skipped path to
// its own stderr (Options.Stderr). There is no wire carrier for degraded
// reasons: the result is {items, truncated} only.
//
// Attribution (operator-only): a git clone holds other people's commits too.
// Identity is the author_emails list. When it is empty or missing the op
// answers unavailable naming author_emails, before any git call, never an
// unscoped result.
//
// Seam for later packets: ListActivity validates the identity once and then
// calls collectCommits, the single collector step that later packets extend
// (commit reading, enrichment, reference extraction, repo discovery).
package internal

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ activity.Provider = (*Backend)(nil)

// KindCommit is the activity kind for a git commit.
const KindCommit = "commit"

// ActivityKinds is the vocabulary this backend contributes to capabilities
// vocabulary.activity_kinds.
var ActivityKinds = []string{KindCommit}

// Config is the shape of this backend's opaque config block.
type Config struct {
	AuthorEmails    []string `json:"author_emails"`
	RepoPaths       []string `json:"repo_paths"`
	RepoSearchPaths []string `json:"repo_search_paths"`
	IncludeMerges   bool     `json:"include_merges"`
}

// Options configures New. Later packets add fields rather than change New's
// signature.
type Options struct {
	// Stderr receives one line per skipped configured path. Nil means
	// os.Stderr.
	Stderr io.Writer
	// Runner execs git. Nil means NewExecRunner().
	Runner Runner
}

// Backend implements activity.Provider.
type Backend struct {
	stderr io.Writer
	runner Runner
	// now is the pull-time clock (as_of); a field so tests can pin it.
	now func() time.Time
	// collect is the commit collector. It defaults to collectCommits and is
	// a field only so in-package tests can observe the decoded Config.
	collect func(ctx context.Context, cfg Config, emails []string, since, before time.Time) ([]schema.ActivityItem, bool, error)
}

// New builds a Backend.
func New(opts Options) *Backend {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	runner := opts.Runner
	if runner == nil {
		runner = NewExecRunner()
	}
	b := &Backend{stderr: stderr, runner: runner, now: time.Now}
	b.collect = b.collectCommits
	return b
}

// authorEmailsFrom returns cfg's author_emails with blank entries dropped.
func authorEmailsFrom(cfg Config) []string {
	var emails []string
	for _, e := range cfg.AuthorEmails {
		if e = strings.TrimSpace(e); e != "" {
			emails = append(emails, e)
		}
	}
	return emails
}

// ListActivity implements activity.Provider. It decodes the config block,
// requires the operator's identity (author_emails) before any git call, and
// then runs the commit collector.
func (b *Backend) ListActivity(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error) {
	var cfg Config
	if err := scriptout.Decode(scriptout.ConfigFromContext(ctx), &cfg); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
			"pg-connector-activity-git: decode config: "+err.Error())
	}
	emails := authorEmailsFrom(cfg)
	if len(emails) == 0 {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable,
			"pg-connector-activity-git: list_activity needs the operator's identity: author_emails is empty or missing in this backend's config")
	}

	items, truncated, err := b.collect(ctx, cfg, emails, since, before)
	if err != nil {
		return nil, err
	}
	return &schema.ActivityListResult{Items: dedupeByID(items), Truncated: truncated}, nil
}

// dedupeByID drops every item whose id already appeared earlier in items, so
// the first occurrence (configured path order) wins. Two clones or worktrees
// of one repository share repo_ident and shas, so their items collide on id
// while differing in repo_path. It runs over the FINAL concatenated list of
// every repo the call reads, so repos added by later packets are covered.
func dedupeByID(items []schema.ActivityItem) []schema.ActivityItem {
	seen := make(map[string]bool, len(items))
	out := make([]schema.ActivityItem, 0, len(items))
	for _, it := range items {
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		out = append(out, it)
	}
	return out
}
