package prfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// CommitsReader is what CommitsFetcher reads a PR's commits through;
// *github.Provider over a metered runner (metered.NewProvider) is the one the
// daemon uses. It pages first: 100 and stops at its 1000-commit cap, and leaves
// a commit's Author empty when its first author has no linked GitHub account.
type CommitsReader interface {
	GetCommits(ctx context.Context, repo string, number int) ([]api.Commit, error)
}

// Commits is the content of the commits group. Its JSON is what the group
// stores.
type Commits struct {
	// Commits are the PR's commits in GitHub's order. Never nil in a stored row.
	Commits []schema.PRCommit `json:"commits"`
}

// DecodeCommits reads the content of a stored commits row.
func DecodeCommits(content []byte) (Commits, error) {
	if len(content) == 0 {
		return Commits{}, errors.New("prfacts: commits: no content")
	}
	var c Commits
	if err := json.Unmarshal(content, &c); err != nil {
		return Commits{}, fmt.Errorf("prfacts: commits: decode content: %w", err)
	}
	if c.Commits == nil {
		c.Commits = []schema.PRCommit{}
	}
	return c, nil
}

// canonical returns the stored bytes of c: its JSON with a non-nil list.
func (c Commits) canonical() ([]byte, error) {
	if c.Commits == nil {
		c.Commits = []schema.PRCommit{}
	}
	return json.Marshal(c)
}

// CommitsFetcher fetches the commits group; it implements groups.Fetcher.
type CommitsFetcher struct {
	// Provider reads the commits, every page through the governor.
	Provider CommitsReader
}

var _ groups.Fetcher = (*CommitsFetcher)(nil)

// Group implements groups.Fetcher.
func (f *CommitsFetcher) Group() string { return groups.Commits }

// MaxBatch implements groups.Fetcher: one PR per Fetch call.
func (f *CommitsFetcher) MaxBatch() int { return 1 }

// Spend implements groups.Fetcher: one GraphQL point per target, an estimate.
func (f *CommitsFetcher) Spend(class governor.Class, n int) governor.Request { return spend(class, n) }

// Fetch implements groups.Fetcher; see fetchEach for the error rules. Each
// Outcome carries the stamps its target had before the call.
func (f *CommitsFetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	return fetchEach(ctx, groups.Commits, call, targets, func(ctx context.Context, t groups.Target) ([]byte, error) {
		commits, err := f.Provider.GetCommits(ctx, t.Ref.NameWithOwner(), t.Ref.Number)
		if err != nil {
			return nil, err
		}
		out := Commits{Commits: make([]schema.PRCommit, 0, len(commits))}
		for _, c := range commits {
			out.Commits = append(out.Commits, schema.PRCommit{SHA: c.SHA, Author: c.Author, Message: c.Message})
		}
		return out.canonical()
	})
}
