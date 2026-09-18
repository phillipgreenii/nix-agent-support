// Package github is pg-connector-pr-github's GitHub VCS provider — ported
// unchanged from packages/pg-pr/pkg/provider/vcs/github (bead pg2-2j5ac's
// "carries over pg-pr's existing GitHub logic unchanged" decision), re-homed
// here so packages/pg-connector's go.mod need not depend on packages/pg-pr.
// This package is compiler-enforced invisible outside
// cmd/pg-connector-pr-github (it lives under that binary's own internal/
// tree).
//
// It implements the read paths pkg/provider/pr's Provider needs (GetPR,
// ListComments, ListReviews, CheckAuth — see internal/provider.go's
// ghProvider seam) plus the twelve write methods below (ListMyPRs,
// ListTeamPRs, CreatePR, UpdatePR, SetDraft, SetAutomerge, Merge, Close,
// AddComment, ReplyToThread, ResolveThread, PostReview), carried over
// alongside the read paths for parity with pg-pr's own vcs.Provider shape.
// None of the twelve is called by anything in pg-connector today — only
// `var _ vcs.Provider = (*Provider)(nil)` below keeps them reachable — but
// unlike this package's former enrich.go/fingerprint.go/pending.go (deleted
// as dead surface with no design citation, bead pg2-lh3c4), every one of
// these twelve has a design-stated future pg-connector destination, so they
// were kept rather than deleted:
//   - ListMyPRs/ListTeamPRs -> a future `pg-connector pr list` (the design's
//     verb->destination table; live-vs-cache is the one still-open question
//     about HOW it's implemented, not WHETHER it exists).
//   - CreatePR/UpdatePR/SetDraft/SetAutomerge/Merge/Close -> `pg-connector pr
//     create`/`update`/`draft`/`automerge`/`merge`/`close` (the design's
//     table: "the rest of this list does not yet [ship]").
//   - AddComment/ReplyToThread/ResolveThread -> `pg-connector pr comment
//     add`/`resolve` (the design's table: "new write verbs on the `pr`
//     capability").
//   - PostReview -> `pg-connector pr review draft`/`post`/`submit` (same
//     table entry, same rationale).
//
// All shell out to the `gh` CLI for its authentication and rate-limit
// handling. The CLI invocation layer is abstracted by ghRunner so tests can
// inject canned JSON without spawning real subprocesses.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"unicode"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Provider is the builtin GitHub VCS provider.
type Provider struct {
	gh ghRunner
}

// New constructs a GitHub VCS provider backed by the gh CLI on PATH, reached
// only through the token-protected CLI gateway (see ghexec.go).
func New() *Provider {
	return &Provider{gh: NewCLI()}
}

// NewWithRunner constructs a Provider with an injected ghRunner — used by
// tests to feed canned JSON.
func NewWithRunner(r ghRunner) *Provider {
	return &Provider{gh: r}
}

// ghRunner abstracts the `gh` CLI. Implementations return stdout bytes.
type ghRunner interface {
	Run(ctx context.Context, args ...string) (stdout []byte, err error)
	// RunStdin invokes gh with the given args while feeding stdin to the
	// subprocess. Used by write paths that POST JSON via `--input -`.
	RunStdin(ctx context.Context, stdin []byte, args ...string) (stdout []byte, err error)
}

// cliGHRunner is the production runner that invokes the real `gh` binary. It
// resolves a GitHub token once (lazily, success-cached) via its TokenSource and
// injects GH_TOKEN into every child env so gh never reads the macOS keychain at
// runtime — fixing intermittent 401s from concurrent keychain reads under the
// launchd agent.
type cliGHRunner struct {
	src  TokenSource
	mu   sync.Mutex
	tok  string
	have bool
}

// token returns the resolved token, resolving (and caching) it at most once.
// Failures are NOT cached so a transient resolution error can be retried.
func (r *cliGHRunner) token(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.have {
		return r.tok, nil
	}
	t, err := r.src.Token(ctx)
	if err != nil {
		return "", err // do NOT cache failure
	}
	r.tok, r.have = t, true
	return t, nil
}

func (r *cliGHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.RunStdin(ctx, nil, args...)
}

func (r *cliGHRunner) RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	// The token-resolution preflight lives in command() (ghexec.go) — the one
	// choke point every gh invocation in this module goes through.
	cmd, err := r.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// st (untruncated) drives isAuthFailure's classification; only
			// the copy folded into the returned error message itself is
			// capped, so a verbose gh failure cannot inflate an error
			// string without bound [bead pg2-332z8 #26].
			st := strings.TrimSpace(stderr.String())
			folded := scriptout.TruncateForFold(stderr.Bytes())
			if isAuthFailure(exitErr.ExitCode(), st) {
				return stdout.Bytes(), fmt.Errorf("gh %s: %s: run `gh auth login`: %w",
					strings.Join(args, " "), folded, ErrGHAuthInvalid)
			}
			return stdout.Bytes(), fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, folded)
		}
		return stdout.Bytes(), fmt.Errorf("gh %s: %w (is gh on PATH?)", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// Common JSON field set requested from gh for PR-list endpoints.
//
// mergeable/mergeStateStatus/statusCheckRollup were added by bead
// pg2-2j5ac.28.2's PR-facts design bullet — all three are documented
// `gh pr view --json` fields (gh's own PullRequest export shape), so no
// separate GraphQL enrich call is needed to populate them.
//
// reviews was added by bead pg2-2j5ac.30.6, purely to COUNT (never to
// inspect) this PR's reviews: List's own supplemental per-matched-PR fetch
// (provider.go's mergeSupplementalFields) reuses this SAME call (rather
// than inventing a second gh invocation shape) to fill in ReviewCount —
// one of the three fingerprint-needed fields gh search prs' own --json
// field list cannot carry (verified 2026-09-15 against the real gh
// binary — see searchPRFields' own doc comment below). ghPR.Reviews below
// decodes each element as an empty struct: only len() is ever read.
var prListFields = "number,title,headRefName,headRefOid,baseRefName,url,author,isDraft,state,mergedAt,closedAt,additions,deletions,changedFiles,body,labels,reviewRequests,assignees,mergeable,mergeStateStatus,statusCheckRollup,reviews"

// ghPR is the JSON shape returned by `gh pr list/view --json prListFields`.
type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	URL         string `json:"url"`
	Author      struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"author"`
	IsDraft      bool   `json:"isDraft"`
	State        string `json:"state"`
	MergedAt     string `json:"mergedAt"`
	ClosedAt     string `json:"closedAt"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changedFiles"`
	Body         string `json:"body"`
	Labels       []struct {
		Name string `json:"name"`
	} `json:"labels"`
	// ReviewRequests is gh's reviewRequests array. Requested accounts (users, bots,
	// mannequins) carry a login; TEAMS carry name/slug (no login) and are ignored
	// for "requested of me". Slug (bead pg2-2j5ac.28.2) is populated only for a
	// team entry — gh's own `{"__typename": "Team", "name": ..., "slug": ...}`
	// shape (verified against this repo's existing github_test.go fixture) — and
	// is what toAPI's new ReviewRequests ("logins and team slugs") field falls
	// back to when Login is empty.
	ReviewRequests []struct {
		Login string `json:"login"`
		Slug  string `json:"slug"`
	} `json:"reviewRequests"`
	// Assignees is gh's assignees array. Every entry gh returns for this field
	// carries a login (only teams — which cannot be assigned to a PR — would
	// lack one); entries with no login are filtered out defensively, mirroring
	// how ReviewRequests filters out teams.
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	// Mergeable/MergeStateStatus/StatusCheckRollup were added by bead
	// pg2-2j5ac.28.2 — see prListFields' own doc comment.
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	// StatusCheckRollup is gh's flattened array of the head commit's check
	// runs (CheckRun) and/or legacy commit statuses (StatusContext) — see
	// checksRollupFromContexts below for how this folds into one summary
	// value.
	StatusCheckRollup []struct {
		// Status/Conclusion are CheckRun's own fields (status: QUEUED |
		// IN_PROGRESS | COMPLETED; conclusion: SUCCESS | FAILURE |
		// NEUTRAL | CANCELLED | TIMED_OUT | ACTION_REQUIRED | STALE |
		// SKIPPED, populated only once Status is COMPLETED).
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		// State is StatusContext's own field (the classic commit-status
		// API: SUCCESS | FAILURE | PENDING | ERROR) — absent/empty on a
		// CheckRun entry.
		State string `json:"state"`
	} `json:"statusCheckRollup"`
	// Reviews is gh's reviews array (bead pg2-2j5ac.30.6) — decoded only
	// to count via len(); no element field is ever read (see prListFields'
	// own doc comment on why this call, rather than a second gh
	// invocation, is List's own supplemental-fetch source for review
	// count).
	Reviews []struct{} `json:"reviews"`
}

// checksRollupFromContexts folds gh's flattened statusCheckRollup array
// into one of "none" | "pending" | "failure" | "success" (schema.PR.
// ChecksRollup's closed value set) — bead pg2-2j5ac.28.2. How this fold is
// computed is a freedom-boundary choice (design pins only the field's
// existence and closed value set, not the algorithm): failure wins over
// pending, which wins over success, so a run still in flight alongside an
// already-failed run reports "failure" rather than masking it as
// "pending".
func checksRollupFromContexts(contexts []struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
},
) string {
	if len(contexts) == 0 {
		return "none"
	}
	sawFailure := false
	sawPending := false
	for _, c := range contexts {
		switch {
		case c.State != "": // StatusContext (legacy commit-status API)
			switch c.State {
			case "FAILURE", "ERROR":
				sawFailure = true
			case "PENDING":
				sawPending = true
			}
		case c.Status != "" && c.Status != "COMPLETED": // CheckRun still running
			sawPending = true
		default: // CheckRun completed — classify by conclusion
			switch c.Conclusion {
			case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STALE":
				sawFailure = true
			case "SUCCESS", "NEUTRAL", "SKIPPED":
				// Passing/non-blocking conclusions — no-op.
			default:
				// "" (no conclusion reported yet) or an unrecognized
				// value — treat as still pending rather than silently
				// counting toward success.
				sawPending = true
			}
		}
	}
	switch {
	case sawFailure:
		return "failure"
	case sawPending:
		return "pending"
	default:
		return "success"
	}
}

func (p ghPR) toAPI(repo string) api.PR {
	out := api.PR{
		Repo:             repo,
		Number:           p.Number,
		Title:            p.Title,
		State:            strings.ToLower(p.State),
		Branch:           p.HeadRefName,
		Base:             p.BaseRefName,
		Author:           p.Author.Login,
		URL:              p.URL,
		Draft:            p.IsDraft,
		Merged:           p.MergedAt != "",
		MergedAt:         p.MergedAt,
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
		HeadSHA:          p.HeadRefOid,
		Body:             p.Body,
		Mergeable:        p.Mergeable,
		MergeStateStatus: p.MergeStateStatus,
		ChecksRollup:     checksRollupFromContexts(p.StatusCheckRollup),
		ReviewCount:      len(p.Reviews),
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	for _, rr := range p.ReviewRequests {
		if rr.Login != "" { // accounts (users/bots/mannequins) have a login; teams do not
			out.RequestedReviewers = append(out.RequestedReviewers, rr.Login)
			out.ReviewRequests = append(out.ReviewRequests, rr.Login)
			continue
		}
		if rr.Slug != "" { // a team entry: no login, but ReviewRequests wants its slug too
			out.ReviewRequests = append(out.ReviewRequests, rr.Slug)
		}
	}
	for _, a := range p.Assignees {
		if a.Login != "" {
			out.Assignees = append(out.Assignees, a.Login)
		}
	}
	return out
}

// GetPR fetches a single PR's metadata.
func (p *Provider) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	args := []string{
		"pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", prListFields,
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var pr ghPR
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("github: parse gh pr view JSON: %w", err)
	}
	out := pr.toAPI(repo)
	return &out, nil
}

// ghPRFile is the JSON shape of one entry in `gh pr view --json files`'
// flattened array.
type ghPRFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// GetFiles fetches a single PR's changed-file list (bead pg2-2j5ac.28.2,
// the "files" targeted op's backing read). A dedicated `gh pr view --json
// files` call, kept separate from GetPR's own prListFields call: folding
// files into every GetPR call would fetch this potentially-large
// connection on every Show, where today only "files" itself needs it.
func (p *Provider) GetFiles(ctx context.Context, repo string, number int) ([]api.File, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	raw, err := p.gh.Run(ctx, "pr", "view", fmt.Sprintf("%d", number), "--repo", repo, "--json", "files")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Files []ghPRFile `json:"files"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("github: parse gh pr view --json files: %w", err)
	}
	out := make([]api.File, 0, len(resp.Files))
	for _, f := range resp.Files {
		out = append(out, api.File{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions})
	}
	return out, nil
}

// ghPRCommit is the JSON shape of one entry in `gh pr view --json commits`'
// flattened array. Authors carries every git-identity author on the
// commit (co-authors included); GetCommits uses only the first entry's
// login, matching pg-pr's own existing convention for a commit's "the"
// author (packages/pg-pr/pkg/provider/vcs.EnrichedPR.CommitAuthors' doc
// comment: "author.user.login").
type ghPRCommit struct {
	OID             string `json:"oid"`
	MessageHeadline string `json:"messageHeadline"`
	Authors         []struct {
		Login string `json:"login"`
	} `json:"authors"`
}

// GetCommits fetches a single PR's commit list (bead pg2-2j5ac.28.2, the
// "commits" targeted op's backing read). Design's own binding decision:
// "commits MUST carry each commit's own author login" — Author is left
// empty (rather than erroring) when GitHub has no linked account for a
// commit's author identity (e.g. an email with no matching GitHub user),
// matching RequestedReviewers' own "no login means excluded/empty"
// convention elsewhere in this file.
func (p *Provider) GetCommits(ctx context.Context, repo string, number int) ([]api.Commit, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	raw, err := p.gh.Run(ctx, "pr", "view", fmt.Sprintf("%d", number), "--repo", repo, "--json", "commits")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Commits []ghPRCommit `json:"commits"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("github: parse gh pr view --json commits: %w", err)
	}
	out := make([]api.Commit, 0, len(resp.Commits))
	for _, c := range resp.Commits {
		var author string
		if len(c.Authors) > 0 {
			author = c.Authors[0].Login
		}
		out = append(out, api.Commit{SHA: c.OID, Author: author, Message: c.MessageHeadline})
	}
	return out, nil
}

// ListMyPRs returns open PRs authored by the configured self_login.
func (p *Provider) ListMyPRs(ctx context.Context, repo string) ([]api.PR, error) {
	return p.listForAuthor(ctx, repo, "@me")
}

// ListTeamPRs returns open PRs authored by any of the given members.
// Falls back to multiple `--author=<login>` invocations and merges by
// number; gh's `pr list` does not natively support OR on author.
func (p *Provider) ListTeamPRs(ctx context.Context, repo string, members []string) ([]api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	seen := map[int]struct{}{}
	out := make([]api.PR, 0)
	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		prs, err := p.listForAuthor(ctx, repo, m)
		if err != nil {
			return nil, fmt.Errorf("github: list team prs (author=%s): %w", m, err)
		}
		for _, pr := range prs {
			if _, dup := seen[pr.Number]; dup {
				continue
			}
			seen[pr.Number] = struct{}{}
			out = append(out, pr)
		}
	}
	return out, nil
}

// listForAuthor is the shared author-filter implementation.
func (p *Provider) listForAuthor(ctx context.Context, repo, author string) ([]api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	args := []string{
		"pr", "list",
		"--repo", repo,
		"--state", "open",
		"--author", author,
		"--json", prListFields,
		"--limit", "100",
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var prs []ghPR
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("github: parse gh pr list JSON: %w", err)
	}
	out := make([]api.PR, 0, len(prs))
	for _, p := range prs {
		out = append(out, p.toAPI(repo))
	}
	return out, nil
}

// searchPRFields is the JSON field set requested from `gh search prs
// --json` (bead pg2-2j5ac.28.1, design's "implement List" Files-
// section note) — verified live against the real gh binary's own field
// list (`gh search prs --json x` names it, 2026-09-10; no query was
// actually issued, so no network round trip was needed to confirm the
// field NAMES themselves). Deliberately narrower than ghPR/prListFields
// (gh pr view/list's own JSON shape): gh search prs carries no
// branch/head-sha/merged-at information at all, so a List-matched PR's
// Branch/Base/HeadSHA/Merged fields are always their zero value
// [freedom boundary: "list" is an enumeration op, not a full-detail
// read — a caller wanting that detail calls "show" on the matched id].
//
// updatedAt/commentsCount were added by bead pg2-2j5ac.30.6: two of the
// six raw fields a future GitHub fingerprint cursor needs (the parked
// sibling packet pg2-2j5ac.30.3's own GitHubPRSnapshot field set) that
// gh search prs' own --json field list already supports directly, at no
// extra call cost — re-verified against the real gh binary's field-name
// error 2026-09-15: gh search prs --json supports exactly assignees,
// author, authorAssociation, body, closedAt, commentsCount, createdAt,
// id, isDraft, isLocked, isPullRequest, labels, number, repository,
// state, title, updatedAt, url. The remaining three fingerprint fields
// (head OID, checks rollup, review count) are NOT in that list — List's
// own supplemental per-matched-PR GetPR fetch (provider.go's
// mergeSupplementalFields) fills those in instead, rather than switching
// this call to a bespoke raw-GraphQL search query: GetPR's own
// prListFields call already carries head OID/checks rollup today, and
// bounding the per-PR fan-out via the SAME parallelMap helper
// ListAttention's own per-candidate GetPR fan-out already uses (bead
// pg2-zutee) keeps this packet's fetch mechanism a straight reuse of two
// already-existing, already-tested code paths rather than a new one.
var searchPRFields = "number,title,url,state,body,isDraft,author,labels,repository,updatedAt,commentsCount"

// ghSearchPR is `gh search prs --json <searchPRFields>`'s own decoded
// shape.
type ghSearchPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Body    string `json:"body"`
	IsDraft bool   `json:"isDraft"`
	Author  struct {
		Login string `json:"login"`
	} `json:"author"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	// UpdatedAt/CommentsCount back api.PR.UpdatedAt/CommentCount (bead
	// pg2-2j5ac.30.6) — see searchPRFields' own doc comment.
	UpdatedAt     string `json:"updatedAt"`
	CommentsCount int    `json:"commentsCount"`
}

func (p ghSearchPR) toAPI() api.PR {
	out := api.PR{
		Repo:         p.Repository.NameWithOwner,
		Number:       p.Number,
		Title:        p.Title,
		State:        strings.ToLower(p.State),
		Author:       p.Author.Login,
		URL:          p.URL,
		Draft:        p.IsDraft,
		Body:         p.Body,
		UpdatedAt:    p.UpdatedAt,
		CommentCount: p.CommentsCount,
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

// splitSearchQualifiers splits a bare GitHub search-syntax string into its
// individual space-separated qualifier tokens, so each becomes its OWN gh
// CLI positional argument rather than one argument containing embedded
// spaces [bug pg2-76vsd].
//
// This split exists because `gh search prs` parses each POSITIONAL
// ARGUMENT it receives as one keyword: a token containing a colon (e.g.
// "is:open") is read as a qualifier, and everything AFTER that first colon
// becomes the qualifier's value verbatim — including any further spaces
// and colons still inside the SAME argument. Handing gh one argument that
// already contains multiple space-separated qualifiers (e.g. exec'ing
// `gh` with a single argv element "is:open repo:X" — no shell is
// involved, so no shell re-splits it for us) makes gh treat "open
// repo:X" as the (space-containing) VALUE of "is:", which it then quotes:
// the request becomes `q=is:"open repo:X"`, a qualifier value nothing
// matches — reproduced 2026-09-11 against the real `gh search prs`
// binary and confirmed via GH_DEBUG=api request tracing. Splitting the
// query into one gh argv element per qualifier here (mirroring what a
// shell would already have done had the query arrived pre-tokenized)
// makes gh parse each qualifier independently, matching GitHub's own
// documented multi-qualifier search syntax. A single qualifier with no
// embedded space was never affected — there was nothing for the bug to
// bite on.
//
// A double-quoted substring (GitHub's own exact-phrase / quoted-value
// syntax, e.g. `label:"needs review"`) is kept as ONE token — its
// embedded space must survive, since that's the one case where the value
// truly is meant to contain a space — by tracking quote state rather than
// splitting on every space unconditionally.
func splitSearchQualifiers(query string) []string {
	var tokens []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range query {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			cur.WriteRune(r)
		case unicode.IsSpace(r) && !inQuotes:
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// SearchPRs runs one `gh search prs -- <query...>` call (query is a bare
// GitHub search-syntax string, e.g. "repo:owner/name is:open
// author:@me" — GitHub's OWN qualifier syntax, never a compiled
// cross-backend query language of pg-connector's own; see
// pkg/provider/pr.Provider.List's own doc comment and this backend's
// internal/provider.go List for the caller side) and returns every
// matched PR. query is split into one gh positional argument per
// qualifier via splitSearchQualifiers (bug pg2-76vsd: gh reads a whole
// unsplit multi-qualifier string as a single, mostly-quoted keyword and
// matches nothing). The "--" separator lets the first qualifier be a
// "-"-prefixed exclusion (gh's own documented convention, "gh search prs
// -- -label:bug") without gh's flag parser misreading it as an
// unrecognized flag.
func (p *Provider) SearchPRs(ctx context.Context, query string) ([]api.PR, error) {
	// Flags MUST precede "--": everything after "--" is positional
	// arguments (the query's own qualifiers), never re-parsed as a flag —
	// putting --json/--limit after "--" would make gh treat them as extra
	// query text instead of flags.
	args := []string{
		"search", "prs",
		"--json", searchPRFields,
		"--limit", "100",
		"--",
	}
	args = append(args, splitSearchQualifiers(query)...)
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var results []ghSearchPR
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("github: parse gh search prs JSON: %w", err)
	}
	out := make([]api.PR, 0, len(results))
	for _, r := range results {
		out = append(out, r.toAPI())
	}
	return out, nil
}

// searchBatchedQuery is the batched GraphQL query SearchPRsEnriched issues
// once per configured search string (bead pg2-aehpr, design doc
// "pg-connector-pr-github: replace N+1 GraphQL fetch with one batched
// query per search string", section 6): every matched PR's
// HeadSHA/ChecksRollup — the two fields List's own now-removed
// per-matched-PR GetPR fan-out used to fetch separately — are nested
// directly on the search result, so one call per query string replaces
// what used to be 1 (search) + 2*N (GetPR, ReviewThreadCount) calls.
//
// reviews{totalCount}/reviewThreads{totalCount} are deliberately NOT
// requested: the design doc traced every consumer of the old fan-out's
// ReviewCount/ReviewThreadCount fields and found none once
// FingerprintCursor (List's own cursor emission) stops being computed —
// see List's own doc comment in provider.go. Adding them back here would
// be dead weight dressed up as fidelity to the old shape.
//
// repository{nameWithOwner} is included even though the design doc's own
// query snippet omits it: this backend's id convention (formatPRID,
// "<owner>/<repo>#<number>") and api.PR.Repo both need the matched PR's
// owning repo — GitHub's search() connection can span multiple repos in
// one query — and the design's field audit covered wire-load-bearing PR
// content fields, not this pre-existing id-construction plumbing (the old
// ghSearchPR.toAPI path already read the identical field).
//
// labels(first: 20) has no pagination of its own (unlike the outer
// search(first: 100) connection, which SearchPRsEnriched below DOES
// paginate) — this backend assumes no single matched PR carries more
// than 20 labels, the design doc's own explicitly-sanctioned alternative
// to implementing a second, nested pagination loop (design doc section 6,
// "either add pagination for labels too, or explicitly size the constant
// to a bound ... and document that assumption inline"). A PR with more
// than 20 labels would silently lose the excess ones; this is a known,
// documented limitation, not an oversight.
const searchBatchedQuery = `
query($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number title url state body isDraft updatedAt
        author { login }
        repository { nameWithOwner }
        labels(first: 20) { nodes { name } }
        comments { totalCount }
        headRefOid
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}
`

// ghBatchedSearchNode is one `... on PullRequest` node in
// searchBatchedQuery's own response shape. A node whose fragment did not
// match (i.e. the matched issue-typed search() result was not actually a
// PullRequest) decodes with every field at its zero value — Number stays
// 0, which SearchPRsEnriched treats as "not a PR" and skips, matching the
// gh CLI's own defensive posture elsewhere in this file.
type ghBatchedSearchNode struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	State     string `json:"state"`
	Body      string `json:"body"`
	IsDraft   bool   `json:"isDraft"`
	UpdatedAt string `json:"updatedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	HeadRefOid string `json:"headRefOid"`
	Commits    struct {
		Nodes []struct {
			Commit struct {
				// StatusCheckRollup decodes to its zero value (State == "")
				// both when the sub-object is genuinely absent AND when
				// GitHub returns it as JSON null (verified empirically per
				// the design doc: "the 'zero check runs' edge case is
				// real ... statusCheckRollup itself comes back null ...
				// when a commit has no checks at all") — encoding/json
				// leaves a non-pointer field at its zero value on a JSON
				// null rather than erroring, so both cases fold to the same
				// "no rollup" state statusStateToChecksRollup below maps to
				// "none".
				StatusCheckRollup struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// ghBatchedSearchResponse is searchBatchedQuery's own full response
// envelope.
type ghBatchedSearchResponse struct {
	Data struct {
		Search struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []ghBatchedSearchNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
}

// statusStateToChecksRollup maps GraphQL's Commit.statusCheckRollup.state
// (schema enum StatusState: EXPECTED | ERROR | FAILURE | PENDING | SUCCESS,
// verified live against the real schema per the design doc's own
// "verify, do not assume" note) onto schema.PR.ChecksRollup's existing
// four-value set ("none" | "pending" | "failure" | "success") — the
// design doc's own recommended mapping, chosen to mirror
// checksRollupFromContexts' existing StatusContext.State handling one
// level down: a check that hasn't started yet (EXPECTED) is not yet a
// failure, so it folds to "pending" alongside PENDING; ERROR folds to
// "failure" alongside FAILURE. state == "" covers both "statusCheckRollup
// itself was JSON null" (no check runs on the head commit at all) and "no
// commits connection returned" (SearchPRsEnriched never populates a
// commits node in that case) — both fold to "none".
func statusStateToChecksRollup(state string) string {
	switch state {
	case "":
		return "none"
	case "PENDING", "EXPECTED":
		return "pending"
	case "SUCCESS":
		return "success"
	case "FAILURE", "ERROR":
		return "failure"
	default:
		// An unrecognized/future StatusState value: treat as pending
		// rather than silently reporting success or masking a real
		// failure — mirrors checksRollupFromContexts' own "unrecognized
		// value" fallback.
		return "pending"
	}
}

func (n ghBatchedSearchNode) toAPI() api.PR {
	out := api.PR{
		Repo:         n.Repository.NameWithOwner,
		Number:       n.Number,
		Title:        n.Title,
		State:        strings.ToLower(n.State),
		Author:       n.Author.Login,
		URL:          n.URL,
		Draft:        n.IsDraft,
		Body:         n.Body,
		UpdatedAt:    n.UpdatedAt,
		CommentCount: n.Comments.TotalCount,
		HeadSHA:      n.HeadRefOid,
		ChecksRollup: statusStateToChecksRollup(n.headCommitStatusState()),
	}
	for _, l := range n.Labels.Nodes {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

// headCommitStatusState returns the head commit's own
// statusCheckRollup.state, or "" when the commits connection returned no
// node at all (a PR with no commits — not expected in practice, but
// decoded defensively rather than panicking on an empty slice).
func (n ghBatchedSearchNode) headCommitStatusState() string {
	if len(n.Commits.Nodes) == 0 {
		return ""
	}
	return n.Commits.Nodes[0].Commit.StatusCheckRollup.State
}

// SearchPRsEnriched runs searchBatchedQuery once per call for query,
// returning every matched PR already fully enriched with
// HeadSHA/ChecksRollup — List's own replacement (bead pg2-aehpr) for the
// old SearchPRs + per-matched-PR GetPR/ReviewThreadCount fan-out (see
// provider.go's List doc comment). SearchPRs itself (immediately above)
// is UNCHANGED and stays in use by List's own ids_only path, by Search(),
// and by ListAttention() — none of which this design touches (design doc
// section 6: "The ids_only=true path is unchanged", and Search/
// ListAttention are never mentioned in scope at all).
//
// query is GitHub's own bare search-syntax string (the same shape
// SearchPRs' own doc comment describes) — this method prepends "is:pr "
// unconditionally before sending it as the $q variable: gh's own `search
// prs` subcommand (SearchPRs' underlying CLI call) implicitly narrows
// GitHub's ISSUE-typed search() connection to pull requests only; talking
// to that same search(type: ISSUE) connection directly via raw GraphQL
// means this method must add that same qualifier itself, or it would
// silently start matching plain issues too — a correctness point the
// design doc's own query snippet does not spell out but the underlying
// API contract requires. A query that already contains "is:pr" is
// harmless to double up (GitHub's search qualifiers are idempotent).
//
// Internally paginates past GitHub's 100-result-per-page search() bound
// (design doc section 6's own MUST-cover pagination point) so callers
// always see one query string's complete match set in a single return,
// with no truncated:true signal needed — a genuine improvement over the
// old SearchPRs' fixed --limit 100 (which silently capped at 100 with no
// truncation signal of its own).
func (p *Provider) SearchPRsEnriched(ctx context.Context, query string) ([]api.PR, error) {
	searchQuery := "is:pr " + query
	var out []api.PR
	after := ""
	for {
		args := []string{
			"api", "graphql",
			"-F", "query=" + searchBatchedQuery,
			"-f", "q=" + searchQuery,
		}
		if after != "" {
			args = append(args, "-f", "after="+after)
		}
		raw, err := p.gh.Run(ctx, args...)
		if err != nil {
			return nil, err
		}
		var resp ghBatchedSearchResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("github: parse batched search graphql response: %w", err)
		}
		for _, n := range resp.Data.Search.Nodes {
			if n.Number == 0 {
				// The matched search() node's inline PullRequest fragment
				// did not match (see ghBatchedSearchNode's own doc
				// comment) — skip rather than emit a bogus zero-identity
				// PR. Not expected once "is:pr " is always prepended
				// above, but defensive rather than assumed.
				continue
			}
			out = append(out, n.toAPI())
		}
		if !resp.Data.Search.PageInfo.HasNextPage {
			break
		}
		after = resp.Data.Search.PageInfo.EndCursor
		if after == "" {
			// Defensive: hasNextPage true but no cursor to advance with —
			// stop rather than re-request the same page forever.
			break
		}
	}
	return out, nil
}

// rateLimitWire is the shape of `gh api graphql -f query='{ rateLimit {
// remaining } }'`'s own stdout — the standard GitHub GraphQL response
// envelope, {"data": {"rateLimit": {"remaining": N}}}.
type rateLimitWire struct {
	Data struct {
		RateLimit struct {
			Remaining int `json:"remaining"`
		} `json:"rateLimit"`
	} `json:"data"`
}

// RateLimitRemaining reads the GraphQL API's current rate-limit
// remainder (bead pg2-2j5ac.28.1, design's "Rate protection"
// bullet) via a dedicated, minimal GraphQL query — mirroring CheckAuth's
// own `gh api graphql -f query=...` call shape exactly.
func (p *Provider) RateLimitRemaining(ctx context.Context) (int, error) {
	raw, err := p.gh.Run(ctx, "api", "graphql", "-f", "query={ rateLimit { remaining } }")
	if err != nil {
		return 0, err
	}
	var wire rateLimitWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return 0, fmt.Errorf("github: parse rate limit JSON: %w", err)
	}
	return wire.Data.RateLimit.Remaining, nil
}

func validateRepo(repo string) error {
	if repo == "" {
		return errors.New("github: repo is required")
	}
	if !strings.Contains(repo, "/") {
		return fmt.Errorf("github: repo %q is not in owner/name form", repo)
	}
	return nil
}

// ---------------------------------------------------------------------
// Write paths (Phase 3).
// ---------------------------------------------------------------------

// CreatePR opens a new pull request via `gh pr create`. The body is fed on
// stdin (via `--body-file -`) so multi-line bodies work without quoting
// headaches. Returns the freshly created PR shape. reviewers and labels
// are pushed via gh's `--reviewer` and `--label` flags (gh accepts each
// repeated for multiple values).
func (p *Provider) CreatePR(ctx context.Context, repo string, draft bool, title, body, branch, base string, reviewers, labels []string) (*api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("github: PR title is required")
	}
	if strings.TrimSpace(branch) == "" {
		return nil, errors.New("github: PR head branch is required")
	}
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("github: PR base branch is required")
	}
	args := []string{
		"pr", "create",
		"--repo", repo,
		"--title", title,
		"--body-file", "-",
		"--head", branch,
		"--base", base,
	}
	if draft {
		args = append(args, "--draft")
	}
	for _, r := range reviewers {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		args = append(args, "--reviewer", r)
	}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		args = append(args, "--label", l)
	}
	out, err := p.gh.RunStdin(ctx, []byte(body), args...)
	if err != nil {
		return nil, fmt.Errorf("github: create PR: %w", err)
	}
	// `gh pr create` prints the new PR URL on stdout. Parse the trailing
	// number out of it; fall back to a follow-up `pr view` if parsing fails.
	url := strings.TrimSpace(string(out))
	num, perr := parsePRNumberFromURL(url)
	if perr != nil || num == 0 {
		// As a fallback, try to discover the most recent PR for the branch.
		return p.lookupPRByBranch(ctx, repo, branch, url)
	}
	pr, err := p.GetPR(ctx, repo, num)
	if err != nil {
		// Return what we know if GetPR fails.
		return &api.PR{Repo: repo, Number: num, URL: url, Branch: branch, Base: base, Draft: draft}, nil
	}
	return pr, nil
}

// parsePRNumberFromURL extracts the trailing /pull/<n> number from a gh PR
// URL. Returns (0, error) when the URL is empty or doesn't match.
func parsePRNumberFromURL(url string) (int, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return 0, errors.New("empty url")
	}
	idx := strings.LastIndex(url, "/")
	if idx < 0 || idx == len(url)-1 {
		return 0, fmt.Errorf("unrecognized PR URL %q", url)
	}
	var n int
	if _, err := fmt.Sscanf(url[idx+1:], "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// lookupPRByBranch is the fallback used when parsing a PR number from the
// `gh pr create` stdout fails.
func (p *Provider) lookupPRByBranch(ctx context.Context, repo, branch, url string) (*api.PR, error) {
	args := []string{
		"pr", "list",
		"--repo", repo,
		"--head", branch,
		"--state", "open",
		"--json", prListFields,
		"--limit", "1",
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("github: lookup created PR: %w", err)
	}
	var prs []ghPR
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("github: parse pr list JSON: %w", err)
	}
	if len(prs) == 0 {
		return &api.PR{Repo: repo, URL: url, Branch: branch}, nil
	}
	out := prs[0].toAPI(repo)
	return &out, nil
}

// UpdatePR edits the PR body via `gh pr edit --body-file -`.
func (p *Provider) UpdatePR(ctx context.Context, repo string, number int, body string) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	_, err := p.gh.RunStdin(
		ctx, []byte(body),
		"pr", "edit", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--body-file", "-",
	)
	if err != nil {
		return fmt.Errorf("github: update PR: %w", err)
	}
	return nil
}

// SetDraft toggles a PR's draft state via `gh pr ready` (mark ready) or
// `gh pr ready --undo` (convert back to draft).
func (p *Provider) SetDraft(ctx context.Context, repo string, number int, draft bool) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	args := []string{
		"pr", "ready", fmt.Sprintf("%d", number),
		"--repo", repo,
	}
	if draft {
		args = append(args, "--undo")
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: set draft=%v: %w", draft, err)
	}
	return nil
}

// SetAutomerge enables or disables PR automerge via `gh pr merge --auto` or
// `gh pr merge --disable-auto`.
func (p *Provider) SetAutomerge(ctx context.Context, repo string, number int, enabled bool) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	args := []string{
		"pr", "merge", fmt.Sprintf("%d", number),
		"--repo", repo,
	}
	if enabled {
		args = append(args, "--auto", "--squash")
	} else {
		args = append(args, "--disable-auto")
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: set automerge=%v: %w", enabled, err)
	}
	return nil
}

// Merge merges the PR immediately. Phase 3 defaults to squash; a future
// phase can plumb the strategy through config.
func (p *Provider) Merge(ctx context.Context, repo string, number int) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	if _, err := p.gh.Run(
		ctx,
		"pr", "merge", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--squash",
	); err != nil {
		return fmt.Errorf("github: merge PR: %w", err)
	}
	return nil
}

// Close closes a PR without merging via `gh pr close`.
func (p *Provider) Close(ctx context.Context, repo string, number int) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	if _, err := p.gh.Run(
		ctx,
		"pr", "close", fmt.Sprintf("%d", number),
		"--repo", repo,
	); err != nil {
		return fmt.Errorf("github: close PR: %w", err)
	}
	return nil
}

// ghIssueComment is the JSON shape returned by the issue-comments endpoint
// (top-level PR comments).
type ghIssueComment struct {
	NodeID string `json:"node_id"`
	Body   string `json:"body"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	AuthorAssociation string `json:"author_association"`
	CreatedAt         string `json:"created_at"`
}

// ghReviewComment is the JSON shape returned by the pulls comments endpoint
// (review-thread / inline file comments). PullRequestReviewID below is the
// owning review's REST decimal id, NOT its GraphQL node id — ListComments
// translates it via reviewNodeIDsByDatabaseID before setting
// api.Comment.ReviewID [bug pg2-flaes].
type ghReviewComment struct {
	NodeID string `json:"node_id"`
	Body   string `json:"body"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Path                string `json:"path"`
	Line                int    `json:"line"`
	OriginalLine        int    `json:"original_line"`
	PullRequestReviewID int64  `json:"pull_request_review_id"`
	InReplyToID         int64  `json:"in_reply_to_id"`
	AuthorAssociation   string `json:"author_association"`
	CreatedAt           string `json:"created_at"`
}

// ListComments returns all PR comments (top-level + inline review-thread).
// Top-level (issue) comments are tagged with empty Path/Line; inline
// comments carry their Path / Line / ThreadID.
func (p *Provider) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}

	out := make([]api.Comment, 0)

	// 1. Top-level PR comments (the "issue" comment endpoint).
	issueRaw, err := p.gh.Run(
		ctx,
		"api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
		"--paginate",
	)
	if err != nil {
		return nil, fmt.Errorf("github: list issue comments: %w", err)
	}
	if len(bytes.TrimSpace(issueRaw)) > 0 {
		var ics []ghIssueComment
		if err := json.Unmarshal(issueRaw, &ics); err != nil {
			return nil, fmt.Errorf("github: parse issue-comments JSON: %w", err)
		}
		for _, c := range ics {
			out = append(out, api.Comment{
				ID:         c.NodeID,
				Author:     c.User.Login,
				AuthorRole: strings.ToLower(c.AuthorAssociation),
				Body:       c.Body,
				CreatedAt:  c.CreatedAt,
			})
		}
	}

	// 2. Inline / review-thread comments (the "pulls comments" endpoint).
	reviewRaw, err := p.gh.Run(
		ctx,
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/comments", repo, number),
		"--paginate",
	)
	if err != nil {
		return nil, fmt.Errorf("github: list review comments: %w", err)
	}
	if len(bytes.TrimSpace(reviewRaw)) > 0 {
		var rcs []ghReviewComment
		if err := json.Unmarshal(reviewRaw, &rcs); err != nil {
			return nil, fmt.Errorf("github: parse review-comments JSON: %w", err)
		}
		// reviewNodeIDs maps each review's REST decimal id (pull_request_review_id,
		// below) to its GraphQL node id. Lazily fetched at most once, and only
		// when a comment actually needs it, via reviewNodeIDsByDatabaseID — see
		// that function's doc comment for why this translation exists
		// [bug pg2-flaes].
		var reviewNodeIDs map[int64]string
		for _, c := range rcs {
			line := c.Line
			if line == 0 {
				line = c.OriginalLine
			}
			// ThreadID: NodeID for the root comment of a thread; for replies,
			// gh exposes only `in_reply_to_id` (numeric). We use the NodeID
			// uniformly — Phase 3 will refine when resolveThread mutation
			// requires the actual review_thread node id.
			var reviewID string
			if c.PullRequestReviewID != 0 {
				if reviewNodeIDs == nil {
					reviewNodeIDs, err = p.reviewNodeIDsByDatabaseID(ctx, repo, number)
					if err != nil {
						return nil, err
					}
				}
				reviewID = reviewNodeIDs[c.PullRequestReviewID]
			}
			out = append(out, api.Comment{
				ID:         c.NodeID,
				Author:     c.User.Login,
				AuthorRole: strings.ToLower(c.AuthorAssociation),
				Body:       c.Body,
				Path:       c.Path,
				Line:       line,
				ThreadID:   c.NodeID,
				CreatedAt:  c.CreatedAt,
				ReviewID:   reviewID,
			})
		}
	}

	return out, nil
}

// reviewNodeIDsByDatabaseID maps each of the PR's reviews' REST decimal id
// (aka GitHub's "database id", the same value ghReviewComment.PullRequestReviewID
// carries) to that review's GraphQL node id (e.g. "PRR_kwDOKtdWE88AAAABL3blsA") —
// the id space ListReviews/PostReview already put on api.Review.ID (from `gh pr
// view --json reviews` and the POST reviews response's node_id, respectively).
//
// The pulls-comments endpoint (ListComments' review-comment source) only ever
// exposes the decimal pull_request_review_id, never a node id for the owning
// review, so ListComments calls this to translate before setting
// api.Comment.ReviewID — otherwise the two ids never match and
// provider.go's join silently drops every inline review comment
// [bug pg2-flaes]. The `repos/<repo>/pulls/<number>/reviews` REST endpoint is
// the one GitHub response that carries both id shapes for a review at once.
func (p *Provider) reviewNodeIDsByDatabaseID(ctx context.Context, repo string, number int) (map[int64]string, error) {
	raw, err := p.gh.Run(
		ctx,
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
		"--paginate",
	)
	if err != nil {
		return nil, fmt.Errorf("github: list reviews for comment join: %w", err)
	}
	out := map[int64]string{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	var entries []struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("github: parse reviews JSON for comment join: %w", err)
	}
	for _, e := range entries {
		out[e.ID] = e.NodeID
	}
	return out, nil
}

// AddComment posts a top-level PR comment via the gh CLI.
//
// Phase 2: minimal implementation using the `repos/.../issues/<n>/comments`
// REST endpoint. The returned api.Comment only carries the new comment's
// NodeID and Body; richer fields land in Phase 3.
func (p *Provider) AddComment(ctx context.Context, repo string, number int, body string) (*api.Comment, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("github: comment body is empty")
	}
	raw, err := p.gh.Run(
		ctx,
		"api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
		"--method", "POST",
		"-f", fmt.Sprintf("body=%s", body),
	)
	if err != nil {
		return nil, fmt.Errorf("github: add comment: %w", err)
	}
	var c ghIssueComment
	if err := json.Unmarshal(raw, &c); err != nil {
		// Some gh versions return empty stdout on success; treat as
		// fire-and-forget rather than a hard error.
		return &api.Comment{Body: body}, nil
	}
	return &api.Comment{
		ID:     c.NodeID,
		Author: c.User.Login,
		Body:   c.Body,
	}, nil
}

// addPullRequestReviewThreadReplyMutation is the GraphQL mutation used by
// ReplyToThread. The thread node id is fed in as a -F field; the reply body
// is passed via stdin (-F body=@-).
const addPullRequestReviewThreadReplyMutation = `
mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $threadId, body: $body}) {
    comment {
      id
      body
      author { login }
    }
  }
}
`

// resolveReviewThreadMutation marks a review thread as resolved.
const resolveReviewThreadMutation = `
mutation($threadId: ID!) {
  resolveReviewThread(input: {threadId: $threadId}) {
    thread { id isResolved }
  }
}
`

// ReplyToThread posts a reply on an existing review thread via GraphQL.
// threadID is the GitHub review-thread node id (PRRT_…).
func (p *Provider) ReplyToThread(ctx context.Context, repo, threadID, body string) (*api.Comment, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if strings.TrimSpace(threadID) == "" {
		return nil, errors.New("github: thread id is required")
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("github: reply body is empty")
	}
	args := []string{
		"api", "graphql",
		"-F", "query=" + addPullRequestReviewThreadReplyMutation,
		"-F", "threadId=" + threadID,
		"-F", "body=" + body,
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("github: reply to thread: %w", err)
	}
	var resp struct {
		Data struct {
			AddPullRequestReviewThreadReply struct {
				Comment struct {
					ID     string `json:"id"`
					Body   string `json:"body"`
					Author struct {
						Login string `json:"login"`
					} `json:"author"`
				} `json:"comment"`
			} `json:"addPullRequestReviewThreadReply"`
		} `json:"data"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	c := resp.Data.AddPullRequestReviewThreadReply.Comment
	return &api.Comment{
		ID:       c.ID,
		Author:   c.Author.Login,
		Body:     c.Body,
		ThreadID: threadID,
	}, nil
}

// minimizeCommentMutation hides a comment with the given classifier
// (OUTDATED|RESOLVED|OFF_TOPIC|SPAM|ABUSE|DUPLICATE). Mirrors
// resolveReviewThreadMutation.
const minimizeCommentMutation = `
mutation($id: ID!, $classifier: ReportedContentClassifiers!) {
  minimizeComment(input: {subjectId: $id, classifier: $classifier}) {
    minimizedComment { isMinimized }
  }
}
`

// MinimizeComment marks a comment minimized with the given classifier. nodeID is
// the comment's GraphQL node id.
func (p *Provider) MinimizeComment(ctx context.Context, nodeID, classifier string) error {
	args := []string{
		"api", "graphql",
		"-F", "query=" + minimizeCommentMutation,
		"-f", "id=" + nodeID,
		"-f", "classifier=" + classifier,
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: minimize comment: %w", err)
	}
	return nil
}

// ResolveThread marks a review thread as resolved.
func (p *Provider) ResolveThread(ctx context.Context, repo, threadID string) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if strings.TrimSpace(threadID) == "" {
		return errors.New("github: thread id is required")
	}
	args := []string{
		"api", "graphql",
		"-F", "query=" + resolveReviewThreadMutation,
		"-F", "threadId=" + threadID,
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: resolve thread: %w", err)
	}
	return nil
}

// reviewComment is the on-wire shape sent inside POST /reviews's `comments[]`.
//
// StartLine/StartSide describe a MULTI-line anchor: `start_line` is the first
// line of the span and `line` its last. Both are omitempty, so a single-line
// comment's payload carries neither (pg2-3c8mo). StartSide is sent alongside
// StartLine rather than left to GitHub's documented "defaults to the value of
// side" because that same field is also documented as required for multi-line
// comments; sending it explicitly satisfies both readings.
type reviewComment struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Line      int    `json:"line,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	Side      string `json:"side,omitempty"`
	StartSide string `json:"start_side,omitempty"`
}

// PostReview creates a pending PR review with optional comments.
//
// The wire format mirrors GitHub's review-create REST endpoint
// (`POST repos/.../pulls/<n>/reviews`). `event` is left unspecified so the
// review is created in PENDING state — agents/humans submit explicitly.
//
// commitID, when non-empty, is sent as `commit_id` so inline `line` comments
// anchor to the exact reviewed commit rather than the PR's latest commit — a
// mismatch (a reviewed head the PR has since advanced past) is a 422 "line must
// be part of the diff" (bead pg2-pipw). The daemon team-sink passes the reviewed
// head SHA; the CLI post path passes "" (anchor to latest).
//
// A comment that cannot be anchored to a diff line — no Path, or a Path with no
// Line — is FOLDED into the review body. GitHub's reviews-create comments[]
// schema requires path+line; the previously-sent `subject_type:"file"` is not a
// field of that endpoint and was rejected with 422 (pg2-pipw). The caller already
// dedups against existing review-comments before calling.
//
// A comment carrying api.Comment.StartLine posts as a MULTI-line comment spanning
// StartLine..Line (pg2-3c8mo).
func (p *Provider) PostReview(ctx context.Context, repo string, number int, commitID, body string, comments []api.Comment) (*api.Review, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}

	rcs := make([]reviewComment, 0, len(comments))
	for _, c := range comments {
		if c.Path == "" || c.Line <= 0 {
			// Un-anchorable (PR-level, or whole-file): fold into the review body.
			note := c.Body
			if c.Path != "" {
				note = c.Path + ": " + c.Body
			}
			if body != "" {
				body += "\n\n"
			}
			body += note
			continue
		}
		rc := reviewComment{Path: c.Path, Body: c.Body, Line: c.Line, Side: "RIGHT"}
		// Multi-line anchor. GitHub 422s a span whose start_line is not strictly
		// before line, so an out-of-range StartLine degrades to the single-line
		// comment it already is rather than failing the whole review. The input
		// decoder (internal/reviewinput) already rejects such a span loudly; this
		// guard covers `review post`, which reads the staged FILE with a plain
		// json.Unmarshal and so can be hand-edited past that boundary.
		if c.StartLine > 0 && c.StartLine < c.Line {
			rc.StartLine = c.StartLine
			rc.StartSide = "RIGHT"
		}
		rcs = append(rcs, rc)
	}

	// Empty guard: a PENDING review with neither a body nor comments is rejected
	// by GitHub with 422 (pg2-pipw). Nothing to post → no-op (the caller then
	// clears the staged draft; there is nothing to retry).
	if body == "" && len(rcs) == 0 {
		return &api.Review{State: "pending"}, nil
	}

	payload := map[string]any{}
	if commitID != "" {
		payload["commit_id"] = commitID
	}
	if body != "" {
		payload["body"] = body
	}
	if len(rcs) > 0 {
		payload["comments"] = rcs
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("github: marshal review payload: %w", err)
	}

	raw, err := p.gh.RunStdin(
		ctx, payloadJSON,
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
		"--method", "POST",
		"--input", "-",
	)
	if err != nil {
		return nil, fmt.Errorf("github: post review: %w", err)
	}
	var resp struct {
		NodeID string `json:"node_id"`
		State  string `json:"state"`
		Body   string `json:"body"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	return &api.Review{
		ID:    resp.NodeID,
		State: strings.ToLower(resp.State),
		Body:  resp.Body,
	}, nil
}

// ghReviewEntry is the JSON shape of each element in the `reviews` array
// returned by `gh pr view --json reviews`. id is a GraphQL node-id string
// (e.g. "PRR_kwDOKtdWE88AAAABL3blsA"), never a bare integer.
type ghReviewEntry struct {
	ID     string `json:"id"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	State string `json:"state"`
	Body  string `json:"body"`
}

// ListReviews fetches the review summaries for a PR. State is one of
// APPROVED, CHANGES_REQUESTED, COMMENTED. Body is the review-summary text;
// Comments is left empty — inline comments are fetched via ListComments.
func (p *Provider) ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	raw, err := p.gh.Run(
		ctx,
		"pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "reviews",
	)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Reviews []ghReviewEntry `json:"reviews"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("github: parse gh pr view reviews JSON: %w", err)
	}
	out := make([]api.Review, 0, len(envelope.Reviews))
	for _, r := range envelope.Reviews {
		out = append(out, api.Review{
			ID:     r.ID,
			Author: r.Author.Login,
			State:  r.State,
			Body:   r.Body,
		})
	}
	return out, nil
}

// CheckAuth verifies the resolved token works with one cheap authenticated
// GraphQL call. errors.Is(err, ErrGHAuthInvalid) distinguishes a bad token
// from a transient/network failure.
func (p *Provider) CheckAuth(ctx context.Context) error {
	_, err := p.gh.Run(ctx, "api", "graphql", "-f", "query={ viewer { login } }")
	return err
}

// ViewerLogin resolves the authenticated GitHub account's own login via
// the same "viewer { login }" GraphQL query CheckAuth already issues (bead
// pg2-7wqkr), but actually decodes the response instead of discarding it —
// ListAttention's own ported mine-vs-team NeedsAttention predicate
// (provider.go's needsAttentionForPR) needs to know "self" to distinguish
// the viewer's own review from a teammate's, and this backend is
// stateless (D3, no local self_login configuration of its own the way
// pg-pr's sync layer carries) so it resolves that fresh on every call.
func (p *Provider) ViewerLogin(ctx context.Context) (string, error) {
	raw, err := p.gh.Run(ctx, "api", "graphql", "-f", "query={ viewer { login } }")
	if err != nil {
		return "", err
	}
	var resp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("github: parse viewer login graphql response: %w", err)
	}
	if resp.Data.Viewer.Login == "" {
		return "", errors.New("github: viewer login graphql response carried no login")
	}
	return resp.Data.Viewer.Login, nil
}

// reviewsWithCommitQuery fetches a PR's reviews together with the commit
// SHA each was submitted against (GraphQL's own
// PullRequestReview.commit.oid) — `gh pr view --json reviews`
// (ListReviews's own call, ghReviewEntry above) exposes no such
// sub-field, so ListAttention's own head-staleness check (bead pg2-7wqkr,
// porting packages/pg-pr/internal/snapshot/attention.go's NeedsAttention
// CONCEPT) needs this dedicated query instead, mirroring
// ReplyToThread's/MinimizeComment's own existing ad-hoc "api graphql"
// call style elsewhere in this file. A review whose commit was since
// deleted (e.g. a force-pushed-away head) reports a null commit per
// GitHub's own documented behavior, decoding here as an empty OID —
// api.Review.CommitOID's own zero value, which ReviewsWithCommit's
// caller (provider.go's reviewIsStale) already treats as "does not stand
// for the current head" (conservatively correct, never a crash).
const reviewsWithCommitQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviews(first: 100) {
        nodes {
          state
          commit { oid }
          author { login }
        }
      }
    }
  }
}
`

// ReviewsWithCommit runs reviewsWithCommitQuery for repo/number and
// returns each review's author/state/commit-oid (Body/ID left unset —
// ListAttention's own predicate never reads either).
func (p *Provider) ReviewsWithCommit(ctx context.Context, repo string, number int) ([]api.Review, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("github: repo %q is not in owner/name form", repo)
	}
	args := []string{
		"api", "graphql",
		"-F", "query=" + reviewsWithCommitQuery,
		"-f", "owner=" + owner,
		"-f", "name=" + name,
		"-F", fmt.Sprintf("number=%d", number),
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					Reviews struct {
						Nodes []struct {
							State  string `json:"state"`
							Commit struct {
								OID string `json:"oid"`
							} `json:"commit"`
							Author struct {
								Login string `json:"login"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("github: parse reviews-with-commit graphql response: %w", err)
	}
	nodes := resp.Data.Repository.PullRequest.Reviews.Nodes
	out := make([]api.Review, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, api.Review{
			Author:    n.Author.Login,
			State:     n.State,
			CommitOID: n.Commit.OID,
		})
	}
	return out, nil
}

// reviewThreadCountQuery fetches a PR's inline code-review comment thread
// count (GraphQL's own PullRequest.reviewThreads.totalCount — distinct
// from CommentCount's issue-level comments) — originally the 8th and
// final raw field List's own FingerprintCursor composition needed (bead
// pg2-2j5ac.30.7). Neither `gh search prs --json` nor `gh pr view --json`
// support a reviewThreads field (verified live 2026-09-15: both error
// "Unknown JSON field" and list their full supported field sets, neither
// containing reviewThreads), so this mirrors pg-pr's own proven mechanism
// for this exact field (packages/pg-pr/pkg/provider/vcs/github/
// fingerprint.go's fingerprintQuery requests the identical
// `reviewThreads { totalCount }` sub-selection with no pagination args),
// narrowed here to a single PR via the same repository/pullRequest node
// shape reviewsWithCommitQuery above already uses.
//
// No longer called by internal/provider.go's List (bead pg2-aehpr:
// List's per-matched-PR GetPR/ReviewThreadCount fan-out was replaced by
// SearchPRsEnriched's own single batched query, which does not request
// reviewThreads at all — see SearchPRsEnriched's own doc comment on why
// not). Kept, untouched and still tested, as a general-purpose read with
// no current caller — the same treatment this file's package doc comment
// already gives its twelve carried-over write methods.
const reviewThreadCountQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads { totalCount }
    }
  }
}
`

// ReviewThreadCount runs reviewThreadCountQuery for repo/number and returns
// reviewThreads.totalCount — originally List's own supplemental-fetch
// source for the one fingerprint field (bead pg2-2j5ac.30.6's own
// mergeSupplementalFields extension left uncovered) that neither gh
// search prs nor gh pr view can carry; no longer called by List as of
// bead pg2-aehpr (see reviewThreadCountQuery's own doc comment above).
// Mirrors ReviewsWithCommit's own error handling exactly: a `gh`
// failure (transient GraphQL error, auth failure, …) is returned unwrapped
// so a caller can classify and fail the whole call the same way a failed
// GetPR supplemental fetch already does — see
// provider.go's List doc comment on that all-or-nothing semantics.
func (p *Provider) ReviewThreadCount(ctx context.Context, repo string, number int) (int, error) {
	if err := validateRepo(repo); err != nil {
		return 0, err
	}
	if number <= 0 {
		return 0, fmt.Errorf("github: invalid PR number %d", number)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return 0, fmt.Errorf("github: repo %q is not in owner/name form", repo)
	}
	args := []string{
		"api", "graphql",
		"-F", "query=" + reviewThreadCountQuery,
		"-f", "owner=" + owner,
		"-f", "name=" + name,
		"-F", fmt.Sprintf("number=%d", number),
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						TotalCount int `json:"totalCount"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, fmt.Errorf("github: parse review-thread-count graphql response: %w", err)
	}
	return resp.Data.Repository.PullRequest.ReviewThreads.TotalCount, nil
}

// Compile-time check that Provider satisfies vcs.Provider.
var _ vcs.Provider = (*Provider)(nil)

// Compile-time check that Provider satisfies vcs.AuthChecker.
var _ vcs.AuthChecker = (*Provider)(nil)
