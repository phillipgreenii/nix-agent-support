// provider.go: Backend implements pkg/provider/pr.Provider against GitHub,
// gluing together internal/github's ported GitHub logic (§9's "carries over
// pg-pr's existing GitHub logic unchanged" decision) with this backend's
// own fresh local Store (store.go) for the categorize/feedback_set writes
// GitHub itself never sees [design: §6.1, §9]. Backend also implements
// pkg/provider.AuthChecker via internal/github.Provider.CheckAuth, carried
// over from pg-pr's existing env-then-gh auth token chain [design: §4.6].
package internal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ghProvider is the subset of internal/github.Provider's method set Backend
// needs — a small seam so tests can inject a fake without spawning `gh`.
type ghProvider interface {
	GetPR(ctx context.Context, repo string, number int) (*api.PR, error)
	ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error)
	ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error)
	CheckAuth(ctx context.Context) error
}

// Backend is pg-connector-pr-github's concrete pr.Provider implementation.
type Backend struct {
	gh    ghProvider
	store *Store
}

// New returns a Backend wiring gh and store together. Production wiring
// passes a *github.Provider (internal/github.New()); tests pass a fake
// satisfying ghProvider.
func New(gh ghProvider, store *Store) *Backend {
	return &Backend{gh: gh, store: store}
}

// Compile-time checks that Backend satisfies both the pr capability's
// Provider interface and pg-connector's optional AuthChecker capability.
var (
	_ pr.Provider          = (*Backend)(nil)
	_ provider.AuthChecker = (*Backend)(nil)
)

// Vocabulary is this backend's declared, non-empty category vocabulary —
// the concrete backing for the sibling "generic pr entity/capability"
// packet's vocabulary check [design: §4.3, §6.1]. A plain, backend-declared
// set (not GitHub labels): callers choose one of these values when calling
// categorize.
var Vocabulary = []string{"focus", "later", "blocked", "done"}

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
// (metadata, comments, reviews) via the ported GitHub logic, then merges in
// this backend's own persisted category/dispositions so a caller sees the
// current state of any prior categorize/feedback_set write [design: §2,
// §6.1].
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
	state, err := b.store.Get(id)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
	}
	return toSchemaPR(id, ghPR, comments, reviews, state), nil
}

// Categorize implements pr.Provider.Categorize: a plain set/overwrite into
// this backend's own store, never a GitHub label [design: §6.1]. No GitHub
// call is made — category has no GitHub-side representation.
func (b *Backend) Categorize(ctx context.Context, id, category string) (*schema.CategorizeResult, error) {
	if _, _, err := parsePRID(id); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	if err := b.store.SetCategory(id, category); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
	}
	return &schema.CategorizeResult{ID: id, Category: category}, nil
}

// FeedbackSet implements pr.Provider.FeedbackSet. A commentID that no
// longer exists on the PR (e.g. deleted upstream) is a well-formed
// not_found response, not a broken call [design: §4.5, §6.1] — checked here
// by re-fetching the PR's live comments (the same read Show uses) rather
// than trusting the caller's commentID blindly.
func (b *Backend) FeedbackSet(ctx context.Context, id, commentID string, disposition schema.Disposition) (*schema.FeedbackSetResult, error) {
	if !disposition.IsValid() {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("pg-connector-pr-github: disposition %q is not one of %v", disposition, schema.ValidDispositions))
	}
	repo, number, err := parsePRID(id)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	comments, err := b.gh.ListComments(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	found := false
	for _, c := range comments {
		if c.ID == commentID {
			found = true
			break
		}
	}
	if !found {
		return nil, scriptout.WrapError(scriptout.ErrNotFound,
			fmt.Sprintf("comment %q not found on PR %s", commentID, formatPRID(repo, number)))
	}
	if err := b.store.SetDisposition(id, commentID, disposition); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
	}
	return &schema.FeedbackSetResult{ID: id, CommentID: commentID, Disposition: disposition}, nil
}

// CheckAuth implements pkg/provider.AuthChecker via internal/github's
// ported CheckAuth — GitHub's existing env-then-gh auth token chain
// [design: §4.6, §9].
func (b *Backend) CheckAuth(ctx context.Context) error {
	return b.gh.CheckAuth(ctx)
}

// classifyGHError maps a ported GitHub-provider error onto scriptout's
// closed error taxonomy: an auth failure becomes unauthenticated; a
// genuine "the PR/comment/review genuinely doesn't exist" response from
// GitHub becomes not_found [design: §4.5, bug pg2-r9iok]; everything else
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
// GitHub provider's own read results plus this backend's persisted
// category/dispositions. Top-level (issue) comments have no owning review
// and land in PR.Comments; inline/review-thread comments nest under their
// owning PRReview.Comments via api.Comment.ReviewID (falling back to
// PR.Comments when GitHub's response left ReviewID unpopulated, so no
// comment is ever silently dropped).
func toSchemaPR(id string, in *api.PR, comments []api.Comment, reviews []api.Review, state PRState) *schema.PR {
	out := &schema.PR{
		ID:       id,
		Repo:     in.Repo,
		Number:   in.Number,
		Title:    in.Title,
		State:    in.State,
		Branch:   in.Branch,
		Base:     in.Base,
		Author:   in.Author,
		URL:      in.URL,
		Draft:    in.Draft,
		Merged:   in.Merged,
		Body:     in.Body,
		Labels:   in.Labels,
		Category: state.Category,
	}

	byReview := make(map[string][]schema.PRComment, len(reviews))
	for _, c := range comments {
		pc := toSchemaComment(c, state.Dispositions)
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

// toSchemaComment maps one api.Comment onto its schema.PRComment shape,
// merging in its persisted disposition (defaulting to DispositionOpen when
// never written, matching schema.PR's own doc convention of an
// unaddressed finding starting "open").
func toSchemaComment(c api.Comment, dispositions map[string]schema.Disposition) schema.PRComment {
	disposition := dispositions[c.ID]
	if disposition == "" {
		disposition = schema.DispositionOpen
	}
	return schema.PRComment{
		ID:          c.ID,
		Author:      c.Author,
		Body:        c.Body,
		Path:        c.Path,
		Line:        c.Line,
		ThreadID:    c.ThreadID,
		Resolved:    c.Resolved,
		Disposition: disposition,
	}
}
