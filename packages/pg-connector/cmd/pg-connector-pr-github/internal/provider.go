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
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/posted"
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
	// ListCommentsReport/ListReviewsReport are ListComments/ListReviews plus
	// the truncation report of each paged, capped connection; Show uses them so
	// a response never silently omits what a cap cut.
	ListCommentsReport(ctx context.Context, repo string, number int) (*api.CommentsResult, error)
	ListReviewsReport(ctx context.Context, repo string, number int) (*api.ReviewsResult, error)
	CheckAuth(ctx context.Context) error
	// SearchPRs runs one GitHub search-syntax query (design's
	// "implement List" note) and returns every matched PR. Still used by
	// List's own ids_only path (unchanged by bead pg2-aehpr's batched-query
	// rewrite — see List's own doc comment) and by Search.
	SearchPRs(ctx context.Context, query string) ([]api.PR, error)
	// SearchPRsEnriched runs one batched `gh api graphql` query per call
	// (bead pg2-aehpr, design doc "pg-connector-pr-github: replace N+1
	// GraphQL fetch with one batched query per search string") and
	// returns every matched PR already fully enriched with
	// HeadSHA/ChecksRollup — List's own non-ids_only path uses this
	// instead of the old SearchPRs + per-matched-PR GetPR/ReviewThreadCount
	// fan-out.
	//
	// The gated form (bead pg2-cw6b3.3, spec section 14 D5) also reads the
	// rate-limit state out of that same document and offers it to gate after
	// every page; a gate error refuses the read and is returned as-is. This
	// is how List enforces the reserve without a separate probe call.
	SearchPRsEnrichedGated(ctx context.Context, query string, gate github.RateGate) ([]api.PR, github.RateLimit, error)
	// SearchPRsActivity runs one GitHub search-syntax query for the activity
	// capability: the result carries createdAt/closedAt, and limit is the
	// result cap (GitHub search caps any query at 1000).
	SearchPRsActivity(ctx context.Context, query string, limit int) ([]api.PR, error)
	// ReadRateLimit reads the GraphQL API's current rate-limit state —
	// remainder and reset time (design's "Rate protection" bullet; reset
	// added by bead pg2-ph0o4 for the backend's own event log).
	ReadRateLimit(ctx context.Context) (github.RateLimit, error)
	// GetFiles/GetCommits back the "files"/"commits" targeted ops (bead
	// pg2-2j5ac.28.2's PR-facts design bullet).
	GetFiles(ctx context.Context, repo string, number int) ([]api.File, error)
	GetCommits(ctx context.Context, repo string, number int) ([]api.Commit, error)
	// ViewerLogin resolves the authenticated viewer, whom the activity
	// capability scopes to.
	ViewerLogin(ctx context.Context) (string, error)
	// ListReviewsSubmitted is ListReviews plus each review's SubmittedAt (empty
	// for a pending, unsubmitted review); it backs the pr.reviewed activity
	// kind. A separate read so ListReviews' own output stays unchanged.
	ListReviewsSubmitted(ctx context.Context, repo string, number int) ([]api.Review, error)
	// GetPendingReview backs review_pending (PendingReview, review_pending.go)
	// and is the read review_submit starts from and reconciles with.
	GetPendingReview(ctx context.Context, repo string, number int) (*github.PendingReviewData, error)
	// CreateBodyOnlyPendingReview, WriteReviewItems and UpdateReviewBody are
	// the write primitives review_submit (SubmitReview, review_submit.go) is
	// built from. None deletes, replaces or submits a review.
	CreateBodyOnlyPendingReview(ctx context.Context, repo string, number int, commitID, body string) (*github.CreatedReview, error)
	WriteReviewItems(ctx context.Context, reviewID string, items []github.ReviewWriteItem) ([]github.ReviewWriteResult, error)
	UpdateReviewBody(ctx context.Context, reviewID, body string) error
	// GetPRHistory and GetComparedFiles are the reads review_submit needs to
	// save a review at an EARLIER head of the PR (INV-REVHEAD-1..3): the PR's
	// commit list with its base tip, and the patches of the files that differ
	// between that base tip and the earlier head.
	GetPRHistory(ctx context.Context, repo string, number int) (*github.PRHistory, error)
	GetComparedFiles(ctx context.Context, repo, base, head string, wanted []string) ([]github.ComparedFile, error)
}

// Backend is pg-connector-pr-github's concrete pr.Provider implementation.
type Backend struct {
	gh ghProvider
	// posted reads the per-PR posted-sidecar review_pending takes last_append
	// from. The zero Store has no directory, in which case no last_append is
	// reported.
	posted pgposted.Store
	// locker serializes review_submit runs per PR. The zero Locker has no
	// directory, in which case review_submit cannot run (it reports
	// unavailable rather than writing without the lock).
	locker pgposted.Locker
}

// New returns a Backend wrapping gh. Production wiring passes a
// *github.Provider (internal/github.New()); tests pass a fake satisfying
// ghProvider.
//
// A gh that retries transient read failures (a *github.Provider) is handed the
// GraphQL rate-limit reserve check as its retry guard, so a retry is never
// issued once the budget has dropped below config.rate_reserve_points: the
// reserve is checked once before the first attempt (List/Search)
// and again before every retry (bead pg2-daktd).
func New(gh ghProvider) *Backend {
	b := &Backend{gh: gh}
	if g, ok := gh.(retryGuardSetter); ok {
		g.SetRetryGuard(b.checkRateReserve)
	}
	return b
}

// retryGuardSetter is implemented by a gh provider that retries transient
// read failures and accepts a check to run before each retry.
type retryGuardSetter interface {
	SetRetryGuard(func(ctx context.Context) error)
}

// WithLocker sets the per-PR lock review_submit runs under, and returns b.
// Production wiring passes posted.LockerFromEnv.
func (b *Backend) WithLocker(l pgposted.Locker) *Backend {
	b.locker = l
	return b
}

// WithPostedStore sets where the per-PR posted-sidecar is read from, and
// returns b. Production wiring passes posted.StoreFromEnv.
func (b *Backend) WithPostedStore(s pgposted.Store) *Backend {
	b.posted = s
	return b
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
	if err := b.checkRateReserve(ctx); err != nil {
		return nil, err
	}
	ghPR, err := b.gh.GetPR(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	comments, err := b.gh.ListCommentsReport(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	reviews, err := b.gh.ListReviewsReport(ctx, repo, number)
	if err != nil {
		return nil, classifyGHError(err)
	}
	out := toSchemaPR(id, ghPR, comments.Comments, reviews.Reviews, time.Now().UTC())
	out.Connections = &schema.PRConnections{
		Reviews:  toSchemaConnectionReport(reviews.Report),
		Threads:  toSchemaConnectionReport(comments.Threads),
		Comments: toSchemaConnectionReport(comments.CommentsReport),
	}
	return out, nil
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

// checkRateReserve is the shared "Rate protection" gate List, Search, Show, Files, Commits and PendingReview each run before their first GitHub read (bead pg2-8wg9a: the pg-desk gather's show/files/commits were the unguarded majority of the daily GraphQL spend; the rateLimit query itself is uncharged): it reads the GraphQL
// rate-limit state, records it on the call's event (eventlog.RecordRateLimit,
// bead pg2-ph0o4 — the budget dipping under the reserve is what starved
// pg-desk's My Work panel, and this is the one place every guarded call
// reads it), and answers unavailable when the remainder is below the
// configured reserve. A failed read is classified like any other gh failure.
func (b *Backend) checkRateReserve(ctx context.Context) error {
	rl, err := b.gh.ReadRateLimit(ctx)
	if err != nil {
		return classifyGHError(err)
	}
	return enforceRateReserve(ctx, rl)
}

// enforceRateReserve applies the reserve to an already-taken reading: it
// records the reading on the call's event and answers unavailable when the
// remainder is below config.rate_reserve_points. checkRateReserve (a probe
// read) and List's folded gate (a reading carried by the search response, bead
// pg2-cw6b3.3) share it so the two paths cannot drift.
func enforceRateReserve(ctx context.Context, rl github.RateLimit) error {
	reserve := rateReservePoints(scriptout.ConfigFromContext(ctx))
	eventlog.RecordRateLimit(ctx, rl.Remaining, rl.ResetAt, reserve)
	if rl.Remaining < reserve {
		return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf(
			"pg-connector-pr-github: GraphQL rate limit remaining (%d) is below the configured reserve (%d)", rl.Remaining, reserve,
		))
	}
	return nil
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
// This checks the GraphQL rate-limit remainder
// (design's "Rate protection" bullet) against
// config.rate_reserve_points (rateReservePoints, reading the SAME
// per-backend config member every op receives via
// scriptout.ConfigFromContext) — falling below it answers unavailable
// rather than a partial/misleading result set, since a caller cannot
// otherwise tell "genuinely zero matches" apart from "GitHub throttled
// this call partway through." The ids_only path probes before searching; the
// enriched path reads the remainder out of each search response instead
// (bead pg2-cw6b3.3, spec section 14 D5: a latency and simplicity change, the
// probe it replaces was uncharged), so a below-reserve answer there costs the
// one search that carried the reading, and its results are discarded and no
// further page or query string is requested. Truncated is always false: both
// SearchPRs and SearchPRsEnriched fully paginate their own results
// internally, so there is never a partial page to report [freedom
// boundary].
//
// "list" is an enumeration/matching op, not a full-detail read (see
// schema.PRListResult's own doc comment), so a caller wanting a matched
// PR's full comment/review detail calls "show" on its id [freedom
// boundary].
//
// idsOnly's own search step deliberately stays on ghProvider.SearchPRs
// (the cheaper, unenriched `gh search prs` call) rather than
// SearchPRsEnriched below — PresentIDs never needs anything beyond
// identity, matching the design doc's own "the ids_only=true path is
// unchanged" MUST-cover point (design doc "pg-connector-pr-github:
// replace N+1 GraphQL fetch with one batched query per search string",
// section 6) — this mirrors the interface's own "MAY still choose to
// populate Entities anyway (harmless, just wasted work) but need not"
// freedom boundary now that there IS a non-trivial cost to skip.
//
// The non-ids_only path (bead pg2-aehpr) instead calls
// ghProvider.SearchPRsEnriched once per query string: that method's own
// batched GraphQL query already returns every matched PR fully enriched
// (HeadSHA/ChecksRollup nested on every returned node), replacing what
// used to be a per-matched-PR GetPR + ReviewThreadCount fan-out entirely
// — see SearchPRsEnriched's own doc comment in internal/github/github.go
// for the full N+1-elimination rationale.
//
// cursor is accepted for pr.Provider.List's own interface conformance
// but is no longer read: List always answers Cursor: nil now (bead
// pg2-aehpr) — the FingerprintCursor mechanism (internal/github's
// DecodeCursor/EncodeCursor/RefreshCursor codec) became vestigial for the
// same reason, since SearchPRsEnriched already fetches every field fresh
// on every call, leaving no more per-PR "did this change" decision for a
// cursor to inform. This matches pr.Provider.List's own doc comment,
// which already sanctioned exactly this: "A backend that does not
// support incremental listing MUST simply ignore whatever it is handed
// and MUST always answer with PRListResult.Cursor nil." internal/github's
// fingerprint.go/fingerprint_test.go were deleted outright as a follow-up
// cleanup (bead pg2-c0vs3) once nothing outside them still referenced the
// codec.
func (b *Backend) List(ctx context.Context, query schema.QueryExpr, idsOnly bool, cursor json.RawMessage) (*schema.PRListResult, error) {
	// Ranged list (bead pg2-ttk9t, WT-D18): list_since/list_before narrow
	// each search and then cut precisely by updatedAt (listrange.go). A
	// malformed bound is answered invalid_argument before any GitHub call.
	rng, rngErr := scriptout.ListRangeFromContext(ctx)
	if rngErr != nil {
		return nil, rngErr
	}
	// idsOnly searches via `gh search prs` (REST), which carries no rate-limit
	// reading, so it keeps the up-front probe. The enriched path below does
	// NOT probe: its batched GraphQL document selects rateLimit, and each page's
	// reading is gated by enforceRateReserve before the page is used or the next
	// is requested (bead pg2-cw6b3.3, spec section 14 D5).
	if idsOnly {
		if err := b.checkRateReserve(ctx); err != nil {
			return nil, err
		}
	}
	// imprecise: some PR's updatedAt could not be judged against the bound,
	// so the result is flagged truncated rather than claimed exact.
	imprecise := false

	if idsOnly {
		seen := make(map[string]bool)
		ids := make([]string, 0)
		for _, q := range query {
			prs, err := b.gh.SearchPRs(ctx, withUpdatedQualifier(q, rng))
			if err != nil {
				return nil, classifyGHError(err)
			}
			for i := range prs {
				keep, imp := prInRange(prs[i].UpdatedAt, rng)
				imprecise = imprecise || imp
				if !keep {
					continue
				}
				id := formatPRID(prs[i].Repo, prs[i].Number)
				if seen[id] {
					continue
				}
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return &schema.PRListResult{PresentIDs: ids, Cursor: nil, Truncated: imprecise}, nil
	}

	gate := func(rl github.RateLimit) error { return enforceRateReserve(ctx, rl) }
	seen := make(map[string]bool)
	matched := make([]api.PR, 0)
	for _, q := range query {
		prs, _, err := b.gh.SearchPRsEnrichedGated(ctx, withUpdatedQualifier(q, rng), gate)
		if err != nil {
			return nil, classifyGHError(err)
		}
		for i := range prs {
			ghPR := prs[i]
			keep, imp := prInRange(ghPR.UpdatedAt, rng)
			imprecise = imprecise || imp
			if !keep {
				continue
			}
			id := formatPRID(ghPR.Repo, ghPR.Number)
			if seen[id] {
				continue
			}
			seen[id] = true
			matched = append(matched, ghPR)
		}
	}

	ids := make([]string, 0, len(matched))
	entities := make([]schema.PR, 0, len(matched))
	asOf := time.Now().UTC()
	for i := range matched {
		ghPR := matched[i]
		id := formatPRID(ghPR.Repo, ghPR.Number)
		ids = append(ids, id)
		entities = append(entities, *toSchemaPR(id, &ghPR, nil, nil, asOf))
	}

	return &schema.PRListResult{Entities: entities, PresentIDs: ids, Cursor: nil, Truncated: imprecise}, nil
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
	if err := b.checkRateReserve(ctx); err != nil {
		return nil, err
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

// maxParallelWorkers bounds how many concurrent `gh` subprocesses one fan-out
// (parallelMap) spawns. 8 is a modest bound: enough concurrency to bring a few
// dozen per-PR calls comfortably under scriptout's own 30s DefaultExecTimeout
// (packages/pg-connector/pkg/scriptout/limits.go), without spawning so many
// `gh` processes/TLS handshakes at once that it looks like a burst against a
// single GitHub token (this is a fan-out WITHIN one backend's own
// external-service calls, not the umbrella's own deliberately-serial
// cross-BACKEND fan-out).
const maxParallelWorkers = 8

// parallelMap runs fn(items[i]) for every i using a bounded pool of at
// most maxParallelWorkers goroutines (or len(items), if smaller),
// preserving items' order in the returned slice — so a caller that
// dedupes/aggregates over the results in order sees the exact same
// sequence a purely-sequential loop would have produced. Every fn call
// still runs to completion (unlike an errgroup-style fail-fast group):
// with no test or caller here relying on early-abort-on-first-error, this
// keeps the pool trivially free of "which in-flight goroutines are safe
// to abandon" concerns. The first non-nil error (by items' own index
// order) is returned; ctx is passed through unmodified so every
// underlying `gh` call (internal/github's exec.CommandContext-based
// wrappers) still observes the caller's own cancellation/deadline.
func parallelMap[T, R any](ctx context.Context, items []T, fn func(ctx context.Context, item T) (R, error)) ([]R, error) {
	if len(items) == 0 {
		return nil, nil
	}
	workers := maxParallelWorkers
	if workers > len(items) {
		workers = len(items)
	}

	results := make([]R, len(items))
	errs := make([]error, len(items))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i, item := range items {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, item T) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = fn(ctx, item)
		}(i, item)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// Files implements pr.Provider.Files: fetches id's changed-file list from
// GitHub (bead pg2-2j5ac.28.2). A targeted op, resolved to the one backend
// that owns id.
func (b *Backend) Files(ctx context.Context, id string) (*schema.PRFilesResult, error) {
	repo, number, err := parsePRID(id)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	if err := b.checkRateReserve(ctx); err != nil {
		return nil, err
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
	if err := b.checkRateReserve(ctx); err != nil {
		return nil, err
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

		// bead pg2-2j5ac.52.6.1's summary fields. Each is carried from what
		// the GitHub layer decoded and left empty or zero when it did not;
		// none is synthesized (NodeID in particular is never derived from
		// the "<repo>#<number>" id).
		NodeID:         in.NodeID,
		UpdatedAt:      in.UpdatedAt,
		ReviewDecision: in.ReviewDecision,
		CommentCount:   topLevelCommentCount(in, comments),
		ReviewCount:    in.ReviewCount,

		// bead pg2-x3h8c.2: the list's review-thread and label totals; the
		// show path leaves ReviewThreadCount zero (it carries the threads).
		ReviewThreadCount: in.ReviewThreadCount,
		LabelCount:        in.LabelCount,

		// bead pg2-2j5ac.52.6.2: the base commit, filled by GetPR on the show
		// path only; the list path's api.PR never carries it.
		BaseSHA: in.BaseSHA,
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
		// bead pg2-w7zai.2: reviews reach here from the GraphQL reviews read,
		// which always selects commit { oid }, so the oid is always reported;
		// an empty one is GitHub's null commit (the reviewed commit was since
		// deleted), carried as a pointer to "" and not as an absent field.
		commitOID := r.CommitOID
		out.Reviews = append(out.Reviews, schema.PRReview{
			ID:       r.ID,
			Author:   r.Author,
			State:    r.State,
			Body:     r.Body,
			Comments: byReview[r.ID],
			// pg2-4jmw2: lets a consumer order reviews without trusting array order.
			SubmittedAt: r.SubmittedAt,
			CommitOID:   &commitOID,
		})
	}

	return out
}

// topLevelCommentCount is schema.PR.CommentCount: the number of top-level PR
// comments, never review-thread comments, on both read paths.
//
// The list path hands toSchemaPR a nil comments slice and already carries the
// count from the batched query's comments { totalCount }, so in.CommentCount
// is used as is. The show path cannot get it from GetPR (ghPR has no comments
// field, so in.CommentCount is always 0 there); it counts the issue-endpoint
// comments it is handed instead: entries with no thread and no path, which
// are exactly the issue (conversation) comments ListComments returns.
// len(out.Comments) is NOT that count: an inline review comment whose
// review could not be joined (empty ReviewID) also lands in PR.Comments. A
// non-nil empty slice (a PR with no comments at all) counts as 0.
func topLevelCommentCount(in *api.PR, comments []api.Comment) int {
	if comments == nil {
		return in.CommentCount
	}
	n := 0
	for _, c := range comments {
		if c.ThreadID == "" && c.Path == "" {
			n++
		}
	}
	return n
}

// toSchemaConnectionReport maps one api.ConnectionReport onto its wire shape.
func toSchemaConnectionReport(r api.ConnectionReport) schema.PRConnectionReport {
	return schema.PRConnectionReport{Truncated: r.Truncated, Total: r.Total, Returned: r.Returned}
}

// toSchemaComment maps one api.Comment onto its schema.PRComment shape.
func toSchemaComment(c api.Comment) schema.PRComment {
	return schema.PRComment{
		ID:             c.ID,
		Author:         c.Author,
		Body:           c.Body,
		Path:           c.Path,
		Line:           c.Line,
		ThreadID:       c.ThreadID,
		ReviewThreadID: c.ReviewThreadID,
		Resolved:       c.Resolved,
		ThreadOutdated: c.ThreadIsOutdated,
	}
}
