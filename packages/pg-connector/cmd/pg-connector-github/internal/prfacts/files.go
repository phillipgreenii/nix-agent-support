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

// FilesReader is what FilesFetcher reads a PR's changed files through;
// *github.Provider over a metered runner (metered.NewProvider) is the one the
// daemon uses. It pages first: 100 and stops at its 3000-file cap.
type FilesReader interface {
	GetFiles(ctx context.Context, repo string, number int) ([]api.File, error)
}

// Files is the content of the files group. Its JSON is what the group stores.
type Files struct {
	// Files are the PR's changed files in GitHub's order. Never nil in a stored
	// row: a PR with no changed files stores [].
	Files []schema.PRFile `json:"files"`
}

// DecodeFiles reads the content of a stored files row.
func DecodeFiles(content []byte) (Files, error) {
	if len(content) == 0 {
		return Files{}, errors.New("prfacts: files: no content")
	}
	var f Files
	if err := json.Unmarshal(content, &f); err != nil {
		return Files{}, fmt.Errorf("prfacts: files: decode content: %w", err)
	}
	if f.Files == nil {
		f.Files = []schema.PRFile{}
	}
	return f, nil
}

// canonical returns the stored bytes of f: its JSON with a non-nil list.
func (f Files) canonical() ([]byte, error) {
	if f.Files == nil {
		f.Files = []schema.PRFile{}
	}
	return json.Marshal(f)
}

// FilesFetcher fetches the files group; it implements groups.Fetcher.
type FilesFetcher struct {
	// Provider reads the files, every page through the governor.
	Provider FilesReader
}

var _ groups.Fetcher = (*FilesFetcher)(nil)

// Group implements groups.Fetcher.
func (f *FilesFetcher) Group() string { return groups.Files }

// MaxBatch implements groups.Fetcher: one PR per Fetch call, the read being
// paged per PR.
func (f *FilesFetcher) MaxBatch() int { return 1 }

// Spend implements groups.Fetcher: one GraphQL point per target, an estimate.
func (f *FilesFetcher) Spend(class governor.Class, n int) governor.Request { return spend(class, n) }

// Fetch implements groups.Fetcher; see fetchEach for the error rules. Each
// Outcome carries the stamps its target had before the call.
func (f *FilesFetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	return fetchEach(ctx, groups.Files, call, targets, func(ctx context.Context, t groups.Target) ([]byte, error) {
		files, err := f.Provider.GetFiles(ctx, t.Ref.NameWithOwner(), t.Ref.Number)
		if err != nil {
			return nil, err
		}
		out := Files{Files: make([]schema.PRFile, 0, len(files))}
		for _, x := range files {
			out.Files = append(out.Files, schema.PRFile{Path: x.Path, Additions: x.Additions, Deletions: x.Deletions})
		}
		return out.canonical()
	})
}
