// resolver.go: resolves a PR id to the (repo, head branch) pair the ported
// GitHub Actions client needs — `gh run list` filters by branch, not PR
// [carry-over basis, per this packet's contract's PRResolver hook].
//
// Composition-boundary decision (bug fix, supersedes this file's original
// choice): this resolver used to shell out to the pg-connector umbrella's
// own "pr show <id>" verb, which in turn dispatched to the sibling
// pg-connector-pr-github backend. That is a Tier-2 backend (this one)
// calling back into the Tier-1 caller that dispatches it — a shape the
// design's composition-authorization for process composition never covered:
// that authorization (design §4.4) is scoped to standalone attention/search
// plugins, a fundamentally different position in the call graph (a leaf
// composing pg-connector's verbs, not one of the things being composed
// calling back into its own composer). See design §4.4's now-explicit
// "This authorization is scoped to those two implementer kinds only..."
// paragraph, added to close exactly this gap.
//
// §5.2's compiler-enforced backend isolation (independent internal/ trees
// per backend) also forecloses the alternative of importing the
// pg-connector-pr-github backend's own concrete Provider in-process: its
// construction lives under a different backend's private internal/ tree,
// which this backend cannot import even if it wanted to — only
// pkg/schema+pkg/provider+pkg/scriptout are shared, and those are
// interfaces/wire-shapes, not implementations.
//
// The repo half of what "pr show" returned was never actually an
// independent GitHub read anyway: pg-connector-pr-github's own toSchemaPR
// (provider.go) just echoes back the repo it parsed out of the PR id string
// via its formatPRID/parsePRID convention, "<owner>/<repo>#<number>". Only
// the branch is a genuine live GitHub read (gh pr view's headRefName). So
// this resolver now does directly, with no subprocess of any pg-connector
// binary, exactly what this packet's own contract already named as the
// fallback ("a minimal direct GitHub branch lookup"): parse repo+number out
// of the id itself — recognizing the same "<owner>/<repo>#<number>"
// convention independently (not by importing pg-connector-pr-github's
// private parsePRID, which §5.2 forbids anyway) because it is the natural,
// minimum-information encoding of a GitHub PR's identity, not a private
// implementation detail — then resolve the head branch with one direct `gh
// pr view` call over the SAME ghRunner gateway (provider.go) this backend
// already uses for run list/logs/rerun: one more read against a system this
// backend already talks to, no new dependency, no new trust boundary.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// PRResolver resolves a PR id to the repo and head branch GitHub Actions
// needs to enumerate its runs. The production implementation
// (ghPRResolver) resolves directly against GitHub over this backend's own
// gh gateway; tests inject a fake.
type PRResolver interface {
	Resolve(ctx context.Context, prID string) (repo, branch string, err error)
}

// ghPRResolver is the production PRResolver: repo comes from parsing the PR
// id's own "<owner>/<repo>#<number>" convention (no GitHub call needed for
// that half — see this file's doc comment); branch is read live from GitHub
// via `gh pr view`, over the same ghRunner gateway (provider.go) this
// backend's other ops already use.
type ghPRResolver struct {
	gh ghRunner
}

// newGHPRResolver returns the production PRResolver, wired to gh — the same
// ghRunner instance Backend uses for run list/logs/rerun, so there is
// exactly one gh gateway per Backend.
func newGHPRResolver(gh ghRunner) *ghPRResolver {
	return &ghPRResolver{gh: gh}
}

// parsePRID parses this system's "<owner>/<repo>#<number>" GitHub PR id
// convention — independently recognized here (not imported from the
// pg-connector-pr-github backend's own parsePRID, which §5.2's backend
// isolation makes impossible to import) because it's the natural,
// minimum-information encoding of a GitHub PR's identity: an owner/repo
// pair plus a PR number, the same shape pg-connector-pr-github's own
// formatPRID mints and parsePRID round-trips.
func parsePRID(id string) (repo string, number int, err error) {
	i := strings.LastIndex(id, "#")
	if i < 0 {
		return "", 0, fmt.Errorf("pg-connector-ci-github-actions: pr id %q is not in \"<owner>/<repo>#<number>\" form", id)
	}
	repo = id[:i]
	if !strings.Contains(repo, "/") {
		return "", 0, fmt.Errorf("pg-connector-ci-github-actions: pr id %q's repo part %q is not in owner/name form", id, repo)
	}
	n, convErr := strconv.Atoi(id[i+1:])
	if convErr != nil || n <= 0 {
		return "", 0, fmt.Errorf("pg-connector-ci-github-actions: pr id %q's number part is not a positive integer", id)
	}
	return repo, n, nil
}

// ghPRView is the slice of `gh pr view --json headRefName`'s shape this
// resolver needs.
type ghPRView struct {
	HeadRefName string `json:"headRefName"`
}

// Resolve implements PRResolver against real GitHub. classifyGHError
// (provider.go) is reused unchanged for error classification — no separate
// wire-error-code-to-sentinel map is needed here, since this resolver
// never decodes a pg-connector scriptout envelope any more.
func (r *ghPRResolver) Resolve(ctx context.Context, prID string) (string, string, error) {
	repo, number, err := parsePRID(prID)
	if err != nil {
		return "", "", err
	}
	raw, err := r.gh.Run(ctx, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", "headRefName")
	if err != nil {
		return "", "", classifyGHError(err)
	}
	var view ghPRView
	if err := json.Unmarshal(raw, &view); err != nil {
		return "", "", fmt.Errorf("pg-connector-ci-github-actions: parse gh pr view JSON for %s: %w", prID, err)
	}
	if view.HeadRefName == "" {
		return "", "", fmt.Errorf("pg-connector-ci-github-actions: gh pr view %s returned no head branch", prID)
	}
	return repo, view.HeadRefName, nil
}
