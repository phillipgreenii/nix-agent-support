// provider.go: Backend implements pkg/provider/pr.Provider against GitHub,
// gluing together internal/github's ported GitHub logic ("carries over
// pg-pr's existing GitHub logic unchanged" — bead pg2-2j5ac's carry-over
// decision) with a live GitHub read on every call — this backend keeps no
// local store (statelessness, D3; bead pg2-2j5ac.28.7 removed the
// categorize/feedback_set writes and their backend-local JSON-file store,
// since category/disposition are re-derived by pg-desk's interpreter
// rather than persisted here). Backend also implements
// pkg/provider.AuthChecker via internal/github.Provider.CheckAuth, carried
// over from pg-pr's existing env-then-gh auth token chain (INV-AUTH-1).
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ghProvider is the subset of internal/github.Provider's method set Backend
// needs — a small seam so tests can inject a fake without spawning `gh`.
// SearchPRs/RateLimitRemaining were added by bead pg2-2j5ac.28.1 (design
// ) for List's own use.
type ghProvider interface {
	GetPR(ctx context.Context, repo string, number int) (*api.PR, error)
	ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error)
	ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error)
	CheckAuth(ctx context.Context) error
	// SearchPRs runs one GitHub search-syntax query (design's
	// "implement List" note) and returns every matched PR.
	SearchPRs(ctx context.Context, query string) ([]api.PR, error)
	// RateLimitRemaining reads the GraphQL API's current rate-limit
	// remainder (design's "Rate protection" bullet).
	RateLimitRemaining(ctx context.Context) (int, error)
	// GetFiles/GetCommits back the "files"/"commits" targeted ops (bead
	// pg2-2j5ac.28.2's PR-facts design bullet).
	GetFiles(ctx context.Context, repo string, number int) ([]api.File, error)
	GetCommits(ctx context.Context, repo string, number int) ([]api.Commit, error)
}

// Backend is pg-connector-pr-github's concrete pr.Provider implementation.
type Backend struct {
	gh ghProvider
}

// New returns a Backend wrapping gh. Production wiring passes a
// *github.Provider (internal/github.New()); tests pass a fake satisfying
// ghProvider.
func New(gh ghProvider) *Backend {
	return &Backend{gh: gh}
}

// Compile-time checks that Backend satisfies the pr capability's Provider
// interface, the search capability's Provider interface (bead pg2-8hcnx —
// this backend's own SearchPRs already exists; it was simply never wired to
// the cross-capability "search" op before this), and pg-connector's optional
// AuthChecker capability.
var (
	_ pr.Provider          = (*Backend)(nil)
	_ search.Provider      = (*Backend)(nil)
	_ provider.AuthChecker = (*Backend)(nil)
)

// formatPRID formats repo/number into this backend's id convention:
// "<owner>/<repo>#<number>" — a freedom-boundary choice (the design does not
// mandate an id shape); it round-trips through parsePRID below.
func formatPRID(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

// parsePRID parses this backend's id convention back into repo+number.
func parsePRID(id string) (repo string, number int, err error) {
	i := strings.LastIndex(id, "#")
	if i < 0 {
		return "", 0, fmt.Errorf("pg-connector-pr-github: id %q is not in \"<owner>/<repo>#<number>\" form", id)
	}
	repo = id[:i]
	if !strings.Contains(repo, "/") {
		return "", 0, fmt.Errorf("pg-connector-pr-github: id %q's repo part %q is not in owner/name form", id, repo)
	}
	n, convErr := strconv.Atoi(id[i+1:])
	if convErr != nil || n <= 0 {
		return "", 0, fmt.Errorf("pg-connector-pr-github: id %q's number part is not a positive integer", id)
	}
	return repo, n, nil
}

// Show implements pr.Provider.Show: fetches the PR's live GitHub state
// (metadata, comments, reviews) via the ported GitHub logic (interfaces.md's
// pr op catalog). Every call is a fresh, uncached GitHub read, so the
// response's schema.PR.AsOf is always this call's own completion time and
// schema.PR.Stale is always false (bead pg2-681xo) — see toSchemaPR.
func (b *Backend) Show(ctx context.Context, id string) (*schema.PR, error) {
	repo, number, err := parsePRID(id)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	ghPR, err := b.gh.GetPR(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	comments, err := b.gh.ListComments(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	reviews, err := b.gh.ListReviews(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	return toSchemaPR(id, ghPR, comments, reviews, time.Now().UTC()), nil
}

// CheckAuth implements pkg/provider.AuthChecker via internal/github's
// ported CheckAuth — GitHub's existing env-then-gh auth token chain
// (INV-AUTH-1).
func (b *Backend) CheckAuth(ctx context.Context) error {
	return b.gh.CheckAuth(ctx)
}

// defaultRateReservePoints is design's own stated default (1000)
// when config.rate_reserve_points is absent.
const defaultRateReservePoints = 1000

// rateReservePoints resolves the configured rate-limit reserve from
// config's own "rate_reserve_points" key (design's own key name;
// absent — no config block registered, no key, or a malformed one —
// means the default). config is the request's own opaque per-backend
// config block (scriptout.ConfigFromContext), the SAME source List's own
// query-name resolution reads its "queries" key from (bead
// pg2-2j5ac.28.1, design's "Rate protection" bullet).
func rateReservePoints(config json.RawMessage) int {
	if len(config) == 0 {
		return defaultRateReservePoints
	}
	var cfg struct {
		RateReservePoints *int `json:"rate_reserve_points"`
	}
	if err := scriptout.Decode(config, &cfg); err != nil || cfg.RateReservePoints == nil {
		return defaultRateReservePoints
	}
	return *cfg.RateReservePoints
}

// List implements pr.Provider.List against GitHub search (bead
// pg2-2j5ac.28.1). query has ALREADY been resolved
// from the request's own config.queries block by
// pkg/provider/pr/dispatch.go's "list" handler — query_not_recognized is
// never this method's concern. Each element of query is a bare GitHub
// search-syntax string (ghProvider.SearchPRs' own doc comment); every
// expression's matches are unioned, deduplicated by this backend's own
// "<owner>/<repo>#<number>" id convention (formatPRID) — design's
// "run each, union results deduplicated by id" rule.
//
// Before searching, this checks the GraphQL rate-limit remainder
// (design's "Rate protection" bullet) against
// config.rate_reserve_points (rateReservePoints, reading the SAME
// per-backend config member every op receives via
// scriptout.ConfigFromContext) — falling below it answers unavailable
// rather than a partial/misleading result set, since a caller cannot
// otherwise tell "genuinely zero matches" apart from "GitHub throttled
// this call partway through." Truncated is always false: SearchPRs
// itself does not report a partial/truncated flag of its own (gh search
// prs' own --limit is fixed at 100 per call by SearchPRs, not exposed
// as a per-query knob here) [freedom boundary].
//
// "list" is an enumeration/matching op, not a full-detail read (see
// schema.PRListResult's own doc comment), so a caller wanting a matched
// PR's full comment/review detail calls "show" on its id [freedom
// boundary].
func (b *Backend) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error) {
	remaining, err := b.gh.RateLimitRemaining(ctx)
	if err != nil {
		return nil, classifyGHError(err)
	}
	if reserve := rateReservePoints(scriptout.ConfigFromContext(ctx)); remaining < reserve {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf(
			"pg-connector-pr-github: GraphQL rate limit remaining (%d) is below the configured reserve (%d)", remaining, reserve,
		))
	}

	seen := make(map[string]bool)
	entities := make([]schema.PR, 0)
	asOf := time.Now().UTC()
	for _, q := range query {
		prs, err := b.gh.SearchPRs(ctx, q)
		if err != nil {
			return nil, classifyGHError(err)
		}
		for i := range prs {
			ghPR := prs[i]
			id := formatPRID(ghPR.Repo, ghPR.Number)
			if seen[id] {
				continue
			}
			seen[id] = true
			entities = append(entities, *toSchemaPR(id, &ghPR, nil, nil, asOf))
		}
	}
	ids := make([]string, 0, len(entities))
	for _, e := range entities {
		ids = append(ids, e.ID)
	}
	result := &schema.PRListResult{Entities: entities, PresentIDs: ids, Cursor: nil, Truncated: false}
	if idsOnly {
		result.Entities = nil
	}
	return result, nil
}

// Search implements the search capability's search.Provider via the same
// ghProvider.SearchPRs GitHub search-syntax call List already uses (bead
// pg2-8hcnx) — query is a single bare GitHub search-syntax string, passed
// straight through with no config.queries resolution (the search wire op,
// unlike list, receives its query argument directly from the caller — see
// pkg/provider/search/dispatch.go's own "search" handler). fields is
// unused: this backend populates no Attributes beyond SearchResult's own
// core set [freedom boundary — no additional search_attributes vocabulary
// declared today]. The same rate-limit reserve check List applies before
// every SearchPRs call applies here too, since both hit the same GraphQL
// quota.
func (b *Backend) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search: query required")
	}
	remaining, err := b.gh.RateLimitRemaining(ctx)
	if err != nil {
		return nil, classifyGHError(err)
	}
	if reserve := rateReservePoints(scriptout.ConfigFromContext(ctx)); remaining < reserve {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf(
			"pg-connector-pr-github: GraphQL rate limit remaining (%d) is below the configured reserve (%d)", remaining, reserve,
		))
	}
	prs, err := b.gh.SearchPRs(ctx, query)
	if err != nil {
		return nil, classifyGHError(err)
	}
	out := make([]schema.SearchResult, 0, len(prs))
	for i := range prs {
		p := prs[i]
		out = append(out, schema.SearchResult{
			Type:   "pr",
			ID:     formatPRID(p.Repo, p.Number),
			Title:  p.Title,
			URL:    p.URL,
			Source: "pg-connector-pr-github",
		})
	}
	return out, nil
}

// Files implements pr.Provider.Files: fetches id's changed-file list from
// GitHub (bead pg2-2j5ac.28.2). A targeted op, resolved to the one backend
// that owns id.
func (b *Backend) Files(ctx context.Context, id string) (*schema.PRFilesResult, error) {
	repo, number, err := parsePRID(id)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	files, err := b.gh.GetFiles(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	out := make([]schema.PRFile, 0, len(files))
	for _, f := range files {
		out = append(out, schema.PRFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions})
	}
	return &schema.PRFilesResult{ID: id, Files: out}, nil
}

// Commits implements pr.Provider.Commits: fetches id's commit list from
// GitHub (bead pg2-2j5ac.28.2), each carrying its own author login
// (design's binding decision — see api.Commit's doc comment).
func (b *Backend) Commits(ctx context.Context, id string) (*schema.PRCommitsResult, error) {
	repo, number, err := parsePRID(id)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	commits, err := b.gh.GetCommits(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	out := make([]schema.PRCommit, 0, len(commits))
	for _, c := range commits {
		out = append(out, schema.PRCommit{SHA: c.SHA, Author: c.Author, Message: c.Message})
	}
	return &schema.PRCommitsResult{ID: id, Commits: out}, nil
}

// classifyGHError maps a ported GitHub-provider error onto scriptout's
// closed error taxonomy: an auth failure becomes unauthenticated; a
// genuine "the PR/comment/review genuinely doesn't exist" response from
// GitHub becomes not_found (INV-ERR-2; bug pg2-r9iok); everything else
// passes through unwrapped to scriptout's own codeForError fallback
// ("unavailable") [freedom boundary, part 4].
func classifyGHError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, github.ErrGHAuthInvalid) {
		return scriptout.WrapError(scriptout.ErrUnauthenticated, err.Error())
	}
	if isGHNotFound(err) {
		return scriptout.WrapError(scriptout.ErrNotFound, err.Error())
	}
	return err
}

// isGHNotFound reports whether err's message carries one of the two error
// phrasings verified empirically against real `gh` 2.99.0 for "the entity
// genuinely doesn't exist" (as opposed to an auth/rate-limit/transport
// failure): a GraphQL unresolved-node error from an id-based op like `gh pr
// view <number>` (`GraphQL: Could not resolve to a PullRequest with the
// number of 999999999. (repository.pullRequest)`, exit 1), or a REST 404
// from a path-based op like `gh api repos/<repo>/issues/<n>/comments`
// (stderr `gh: Not Found (HTTP 404)`, exit 1). Matched by substring on the
// already-stderr-inclusive error message (ghexec.go/RunStdin folds gh's
// stderr into the returned error), the same style token.go's own
// isAuthFailure uses for auth classification, deliberately not by exit
// code: gh does not use a distinct exit code for "not found" the way it
// does (4) for "no credential" [bug pg2-r9iok].
func isGHNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not resolve to a") || strings.Contains(msg, "http 404")
}

// toSchemaPR assembles the pr capability's wire shape from the ported
// GitHub provider's own read results. Top-level (issue) comments have no
// owning review and land in PR.Comments; inline/review-thread comments
// nest under their owning PRReview.Comments via api.Comment.ReviewID
// (falling back to PR.Comments when GitHub's response left ReviewID
// unpopulated, so no comment is ever silently dropped).
//
// asOf is the freshness timestamp for this assembled read (bead pg2-681xo)
// — the caller's own call-completion time, since every field above came
// from a live GitHub read performed just before this call. Stale is
// therefore always false: this backend has no local cache of GitHub's PR
// facts to ever serve a stale copy of.
func toSchemaPR(id string, in *api.PR, comments []api.Comment, reviews []api.Review, asOf time.Time) *schema.PR {
	out := &schema.PR{
		ID:     id,
		Repo:   in.Repo,
		Number: in.Number,
		Title:  in.Title,
		State:  in.State,
		Branch: in.Branch,
		Base:   in.Base,
		Author: in.Author,
		URL:    in.URL,
		Draft:  in.Draft,
		Merged: in.Merged,
		Body:   in.Body,
		Labels: in.Labels,
		AsOf:   asOf.Format(time.RFC3339),
		Stale:  false,

		// bead pg2-2j5ac.28.2's additive PR-facts fields, carried straight
		// through from the ported GitHub read (api.PR already carries
		// these — see internal/api/pr.go and internal/github/github.go's
		// prListFields).
		HeadSHA:          in.HeadSHA,
		Additions:        in.Additions,
		Deletions:        in.Deletions,
		ChangedFiles:     in.ChangedFiles,
		Mergeable:        in.Mergeable,
		MergeStateStatus: in.MergeStateStatus,
		ReviewRequests:   in.ReviewRequests,
		ChecksRollup:     in.ChecksRollup,
	}

	byReview := make(map[string][]schema.PRComment, len(reviews))
	for _, c := range comments {
		pc := toSchemaComment(c)
		if c.ReviewID != "" {
			byReview[c.ReviewID] = append(byReview[c.ReviewID], pc)
			continue
		}
		out.Comments = append(out.Comments, pc)
	}

	for _, r := range reviews {
		out.Reviews = append(out.Reviews, schema.PRReview{
			ID:       r.ID,
			Author:   r.Author,
			State:    r.State,
			Body:     r.Body,
			Comments: byReview[r.ID],
		})
	}

	return out
}

// toSchemaComment maps one api.Comment onto its schema.PRComment shape.
func toSchemaComment(c api.Comment) schema.PRComment {
	return schema.PRComment{
		ID:       c.ID,
		Author:   c.Author,
		Body:     c.Body,
		Path:     c.Path,
		Line:     c.Line,
		ThreadID: c.ThreadID,
		Resolved: c.Resolved,
	}
}
