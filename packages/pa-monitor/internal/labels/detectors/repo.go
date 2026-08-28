package detectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
	"github.com/phillipgreenii/x/gitclient"
)

// Repo identifies a session's repository via canonical git origin URL.
// Different clones of the same remote produce the same value; worktrees
// of one clone also share the value. When no git remote is set, falls
// back to a stable hash of the git-common-dir absolute path.
type Repo struct {
	// Cache, when non-nil, supplies the workspace.repo label from a shared
	// per-cwd provider (LongLived), deduping the git-config subprocess across
	// sessions in one cwd. Nil runs the inline git-config path unchanged, so the
	// zero-value Repo{} keeps working for every existing caller/test.
	Cache interface {
		RepoLabel(cwd string) (string, bool)
	}
}

func (Repo) Name() string { return "repo" }

func (r Repo) Detect(s labels.Session) labels.Set {
	if s.CWD == "" {
		return nil
	}
	if r.Cache != nil {
		if v, ok := r.Cache.RepoLabel(s.CWD); ok {
			return labels.Set{"workspace.repo": v}
		}
		return nil
	}
	if v, ok := RepoLabelFor(s.CWD); ok {
		return labels.Set{"workspace.repo": v}
	}
	return nil
}

// RepoLabelFor returns the canonical workspace.repo value for cwd via git:
// the origin URL (NormaliseOrigin), or on error a stable local:<hash> of the
// git-common-dir. Returns ("", false) for an empty cwd or a non-git dir. This is
// the git-subprocess fetch shared by the inline detector path and the provider's
// per-cwd RepoLabel cache (buildPoller), so both produce identical labels.
//
// RepoLabelFor takes no context because both its callers -- the inline
// Detect path above and provider.Cache.FetchRepoLabel (wired in
// cmd/pa-monitor/daemon.go) -- have none to thread through; a package-scoped
// context.Background() is used for the git calls, matching this function's
// pre-migration lack of any deadline.
//
// Migrated onto x/gitclient's Locator role (bead pg2-lv9jc, per epic
// pg2-svfbb's design section 4.5). Two deliberate, documented behavior notes
// from that design (section 4.2):
//   - The no-remote fallback below uses Locator.CommonDir, which always runs
//     `rev-parse --path-format=absolute --git-common-dir` anchored at cwd.
//     The pre-migration call ran plain `rev-parse --git-common-dir` (no
//     --path-format=absolute) and absolutized the result against the
//     PROCESS's cwd rather than the cwd argument -- a latent bug this
//     migration fixes. It changes the local:<hash> value emitted in that
//     fallback path; no consumer (dashboards, alerting, or stored session
//     history) was found to pin a specific local:<hash> value -- see the
//     bead's close reason for the check performed.
//   - RemoteURL wraps the same raw `config --get remote.<remote>.url` read
//     (no insteadOf expansion), so the primary (has-a-remote) path's label
//     values are unchanged.
//
// DECISION (pg2-vc5bp's fail-vs-silently-return question, recorded here per
// its acceptance criteria; same disposition as gh.ExecBranchResolver.
// CurrentBranch's DECISION 2): RepoLabelFor is NOT changed to error when
// cwd's resolved anchor differs from the expected repository. That mismatch
// was only reachable through the env-leak vector (a leaked GIT_DIR making
// `-C cwd`-shaped calls silently answer about a different repository) that
// this migration closes by construction -- gitclient.Discover walks up from
// cwd through the filesystem under its own hermetic, allowlisted
// environment (PATH/HOME/SSH_AUTH_SOCK only; no GIT_* var inherited), so its
// anchor is always cwd or a real ancestor of it, never a repository selected
// by leaked environment state. Adding an anchor-mismatch check here would
// guard a state gitclient no longer makes representable. Regression-tested
// by TestRepoLabelFor_IgnoresLeakedGitDir.
func RepoLabelFor(cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}
	ctx := context.Background()
	client, err := gitclient.Discover(ctx, cwd)
	if err != nil {
		return "", false
	}
	url, err := client.RemoteURL(ctx, "origin")
	if err == nil {
		return NormaliseOrigin(url), true
	}
	if !errors.Is(err, gitclient.ErrNoRemote) {
		return "", false
	}
	gcd, err := client.CommonDir(ctx)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(gcd))
	return "local:" + hex.EncodeToString(sum[:6]), true
}

// NormaliseOrigin maps common git remote URL forms to a canonical
// host/path-without-.git string. SSH and HTTPS forms of the same remote
// produce the same value. Exported so the provider's repo-label fetch closure
// (buildPoller) reuses the exact normalization DRY.
func NormaliseOrigin(url string) string {
	url = strings.TrimSuffix(url, ".git")
	// SSH form: git@host:path
	if strings.HasPrefix(url, "git@") {
		rest := strings.TrimPrefix(url, "git@")
		return strings.Replace(rest, ":", "/", 1)
	}
	for _, prefix := range []string{"ssh://", "https://", "http://"} {
		if strings.HasPrefix(url, prefix) {
			rest := strings.TrimPrefix(url, prefix)
			if at := strings.Index(rest, "@"); at != -1 {
				rest = rest[at+1:]
			}
			return rest
		}
	}
	return url
}
