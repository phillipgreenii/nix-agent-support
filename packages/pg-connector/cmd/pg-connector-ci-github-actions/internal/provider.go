// provider.go: Backend implements pkg/provider/ci.Provider against GitHub
// Actions by carrying over
// packages/pg-pr/pkg/provider/cicd/ghactions's existing ListRuns/GetLogs/
// RerunFailed GitHub calls unchanged, adapted to ci.Provider's id-only
// signatures and schema.CIRun result type [contract: carry-over basis;
// design: §2, §5.1, §5.2]. Backend also implements pkg/provider.AuthChecker
// via the same env-then-gh-auth-token chain the pg-connector-pr-github
// backend already uses, since both are GitHub-backed [design: §4.6].
package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-ci-github-actions/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/ci"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ProviderName tags every CIRun this backend returns, carried over
// unchanged from packages/pg-pr/pkg/provider/cicd/ghactions.ProviderName.
const ProviderName = "github-actions"

// ghRunner abstracts the gh CLI for tests — the same seam
// packages/pg-pr/pkg/provider/cicd/ghactions.Provider uses, now satisfied
// directly by *internal/github.CLI (whose Run method already performs the
// token-first choke point and auth-failure classification), rather than by
// a second wrapping type the way ghactions.go's own cliGHRunner did
// [carry-over basis, adapted].
type ghRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Backend is pg-connector-ci-github-actions's concrete ci.Provider
// implementation. Unlike the sibling pg-connector-pr-github backend,
// Backend keeps no local store: every field on schema.CIRun is read
// straight from GitHub, with no categorize/feedback_set-style write-back
// this capability needs to persist [design: §2].
type Backend struct {
	gh ghRunner
	pr PRResolver
}

// New returns a Backend wired for production: the token-protected gh CLI
// gateway (internal/github.NewCLI), shared between run-list/logs/rerun and
// the PRResolver (resolver.go), which resolves a PR id directly against
// GitHub — never by shelling out to pg-connector or any other backend
// binary [design: §4.4].
func New() *Backend {
	gh := github.NewCLI()
	return &Backend{gh: gh, pr: newGHPRResolver(gh)}
}

// NewWithDeps constructs a Backend with injected dependencies — used by
// tests to avoid spawning real `gh`/`pg-connector` subprocesses.
func NewWithDeps(gh ghRunner, pr PRResolver) *Backend {
	return &Backend{gh: gh, pr: pr}
}

// Compile-time checks that Backend satisfies both the ci capability's
// Provider interface and pg-connector's optional AuthChecker capability.
var (
	_ ci.Provider          = (*Backend)(nil)
	_ provider.AuthChecker = (*Backend)(nil)
)

// ghRun is the JSON shape returned by `gh run list --json …`, carried over
// unchanged from ghactions.go.
type ghRun struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	HeadBranch string `json:"headBranch"`
	HeadSHA    string `json:"headSha"`
}

// toSchema converts one gh run into this capability's wire shape, setting
// PRID so every returned CIRun is self-describing [design: §2] — the one
// addition ghactions.go's own toAPI never needed, since its caller supplied
// prNumber out of band via a separate argument.
func (r ghRun) toSchema(prID string) schema.CIRun {
	return schema.CIRun{
		ID:         fmt.Sprintf("%d", r.DatabaseID),
		Name:       r.Name,
		Status:     strings.ToLower(r.Status),
		Conclusion: strings.ToLower(r.Conclusion),
		URL:        r.URL,
		Provider:   ProviderName,
		HeadSHA:    r.HeadSHA,
		PRID:       prID,
	}
}

// runListFields is the JSON projection requested from gh, carried over
// unchanged from ghactions.go.
const runListFields = "databaseId,name,status,conclusion,url,headBranch,headSha"

// ListRuns implements ci.Provider.ListRuns: resolves prID's repo and head
// branch via pr (resolver.go), then enumerates workflow runs for that
// branch — gh's `run list` filters by branch, not PR, exactly as
// ghactions.go's own ListRuns already handled via its injectable
// PRResolver hook [carry-over basis]. Every returned CIRun carries prID as
// PRID [design: §2].
func (b *Backend) ListRuns(ctx context.Context, prID string) ([]schema.CIRun, error) {
	if strings.TrimSpace(prID) == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "pg-connector-ci-github-actions: pr id is required")
	}
	repo, branch, err := b.pr.Resolve(ctx, prID)
	if err != nil {
		// err already carries a scriptout sentinel (resolver.go's
		// sentinelForWireCode) or is a plain exec/decode failure that
		// scriptout's own codeForError fallback classifies as
		// "unavailable" — nothing further to translate here.
		return nil, err
	}
	return b.listRunsByBranch(ctx, prID, repo, branch)
}

// listRunsByBranch is ghactions.go's own ListRunsByBranch, carried over
// unchanged in its gh call shape, adapted to this capability's schema and
// to stamp prID onto every result.
func (b *Backend) listRunsByBranch(ctx context.Context, prID, repo, branch string) ([]schema.CIRun, error) {
	if err := validateRepo(repo); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
	}
	if strings.TrimSpace(branch) == "" {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pg-connector-ci-github-actions: branch is required")
	}
	raw, err := b.gh.Run(
		ctx,
		"run", "list",
		"--repo", repo,
		"--branch", branch,
		"--json", runListFields,
		"--limit", "100",
	)
	if err != nil {
		return nil, classifyGHError(err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var runs []ghRun
	if err := json.Unmarshal(raw, &runs); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("pg-connector-ci-github-actions: parse runs JSON: %v", err))
	}
	out := make([]schema.CIRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.toSchema(prID))
	}
	return out, nil
}

// GetLogs implements ci.Provider.GetLogs, carried over unchanged from
// ghactions.go: no repo/PR context is needed or added, matching that
// packet's own behavior exactly — this packet's contract requires "no
// behavioral drift on the ported operations."
func (b *Backend) GetLogs(ctx context.Context, runID string) ([]byte, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "pg-connector-ci-github-actions: run ID is required")
	}
	raw, err := b.gh.Run(ctx, "run", "view", runID, "--log")
	if err != nil {
		return nil, classifyGHError(err)
	}
	return raw, nil
}

// rerunnableConclusions is the set of ghRun.Conclusion values RerunFailed
// treats as "this run needs a rerun." ghactions.go's original logic (and
// this backend's own port of it, unchanged until now) matched only
// "failure", so a run that ended "timed_out", "startup_failure", or
// "cancelled" was invisible to the loop below and RerunFailed answered
// not_found even though `gh run rerun <id> --failed` would have worked fine
// on that run [bug pg2-mzymd]. The widened set covers every terminal
// conclusion GitHub Actions reports for a run that did not complete
// successfully through no fault of its own correctness: "failure" (a job
// genuinely failed), "timed_out" (a job hit its timeout), "startup_failure"
// (the run failed before any job could execute — e.g. a bad workflow file
// or a runner provisioning failure), and "cancelled" (the run was stopped
// mid-flight, by a user, a concurrency-group supersede, or GitHub itself).
// Deliberately excluded: "success" (nothing to rerun), "skipped"/"neutral"/
// "stale" (the run never attempted work, so rerunning is meaningless), and
// "action_required" (the run is paused pending manual approval — approving
// it, not rerunning it, is the correct next action).
var rerunnableConclusions = map[string]bool{
	"failure":         true,
	"timed_out":       true,
	"startup_failure": true,
	"cancelled":       true,
}

// RerunFailed implements ci.Provider.RerunFailed, carried over from
// ghactions.go's own "pick the most recent failed run, `gh run rerun <id>
// --failed`" logic, adapted to resolve via prID instead of a separate
// repo+prNumber pair, and widened to match rerunnableConclusions rather
// than the literal string "failure" [bug pg2-mzymd].
//
// Rerunning only the single newest matching run (never every matching run)
// is preserved unchanged from ghactions.go: that was this operation's
// original, documented behavior ("rer-uns the latest failed workflow run
// for a PR"), TestRerunFailed_PicksLatestFailedRun already pins it, and
// ci.Provider's own doc comment ("re-runs the failed portion of the CI
// run(s) for the PR") is acknowledging that a PR can have multiple runs at
// all, not mandating that RerunFailed act on every one of them — nothing in
// the interface doc, this capability's fan-out/targeted split (ListRuns is
// fan-out-shaped; GetLogs/RerunFailed are targeted, resolving to the one
// backend that owns the id [design: §4.5]), or the existing test suite
// implies "rerun every matching run" was ever the intended contract, so
// pg2-mzymd's fix stops at widening the conclusion match.
func (b *Backend) RerunFailed(ctx context.Context, prID string) error {
	runs, err := b.ListRuns(ctx, prID)
	if err != nil {
		return err
	}
	// ListRuns is most-recent-first per gh's default ordering.
	var target string
	for _, r := range runs {
		if rerunnableConclusions[r.Conclusion] {
			target = r.ID
			break
		}
	}
	if target == "" {
		return scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("pg-connector-ci-github-actions: no failed runs to rerun for %s", prID))
	}
	if _, err := b.gh.Run(ctx, "run", "rerun", target, "--failed"); err != nil {
		return classifyGHError(err)
	}
	return nil
}

// CheckAuth implements pkg/provider.AuthChecker via one cheap authenticated
// GraphQL call, carried over from the sibling pg-connector-pr-github
// backend's own internal/github.Provider.CheckAuth convention
// [design: §4.6].
func (b *Backend) CheckAuth(ctx context.Context) error {
	_, err := b.gh.Run(ctx, "api", "graphql", "-f", "query={ viewer { login } }")
	return err
}

// classifyGHError maps a ported gh-call error onto scriptout's closed error
// taxonomy: an auth failure becomes unauthenticated; a genuine "the PR/run
// genuinely doesn't exist" response from GitHub becomes not_found [design:
// §4.5, bug pg2-r9iok]; everything else passes through unwrapped to
// scriptout's own codeForError fallback ("unavailable") — mirroring the
// sibling pg-connector-pr-github backend's own classifyGHError [freedom
// boundary, part 4].
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
// genuinely doesn't exist": a GraphQL unresolved-node error from an
// id-based op like `gh pr view <number>` (used by resolver.go's own
// Resolve to translate a PR id into a head branch — `GraphQL: Could not
// resolve to a PullRequest with the number of 999999999.
// (repository.pullRequest)`, exit 1), or a REST 404 from a path-based op
// like `gh run view <id> --log`/`gh run rerun <id>` (stderr `failed to get
// run: HTTP 404: Not Found (...)`, exit 1) — mirroring the sibling
// pg-connector-pr-github backend's own isGHNotFound [bug pg2-r9iok].
func isGHNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not resolve to a") || strings.Contains(msg, "http 404")
}

// validateRepo is carried over unchanged from ghactions.go.
func validateRepo(repo string) error {
	if repo == "" {
		return errors.New("pg-connector-ci-github-actions: repo is required")
	}
	if !strings.Contains(repo, "/") {
		return fmt.Errorf("pg-connector-ci-github-actions: repo %q is not in owner/name form", repo)
	}
	return nil
}
