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

// Options configures New. Later packets add fields (the git runner) rather
// than change New's signature.
type Options struct {
	// Stderr receives one line per skipped configured path. Nil means
	// os.Stderr.
	Stderr io.Writer
}

// Backend implements activity.Provider.
type Backend struct {
	stderr io.Writer
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
	b := &Backend{stderr: stderr}
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
	return &schema.ActivityListResult{Items: items, Truncated: truncated}, nil
}

// collectCommits is the collector seam: later packets fill in reading commits
// from the configured repos. For now it returns an empty, well-formed result.
func (b *Backend) collectCommits(_ context.Context, _ Config, _ []string, _, _ time.Time) (items []schema.ActivityItem, truncated bool, err error) {
	return []schema.ActivityItem{}, false, nil
}
