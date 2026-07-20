package detectors

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
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
func RepoLabelFor(cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}
	out, err := exec.Command("git", "-C", cwd, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		gcd, gErr := exec.Command("git", "-C", cwd, "rev-parse", "--git-common-dir").Output()
		if gErr != nil {
			return "", false
		}
		abs, _ := filepath.Abs(strings.TrimSpace(string(gcd)))
		sum := sha256.Sum256([]byte(abs))
		return "local:" + hex.EncodeToString(sum[:6]), true
	}
	return NormaliseOrigin(strings.TrimSpace(string(out))), true
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
