package detectors

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/labels"
)

// Repo identifies a session's repository via canonical git origin URL.
// Different clones of the same remote produce the same value; worktrees
// of one clone also share the value. When no git remote is set, falls
// back to a stable hash of the git-common-dir absolute path.
type Repo struct{}

func (Repo) Name() string { return "repo" }

func (Repo) Detect(s labels.Session) labels.Set {
	if s.CWD == "" {
		return nil
	}
	out, err := exec.Command("git", "-C", s.CWD, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		gcd, gErr := exec.Command("git", "-C", s.CWD, "rev-parse", "--git-common-dir").Output()
		if gErr != nil {
			return nil
		}
		abs, _ := filepath.Abs(strings.TrimSpace(string(gcd)))
		sum := sha256.Sum256([]byte(abs))
		return labels.Set{"workspace.repo": "local:" + hex.EncodeToString(sum[:6])}
	}
	return labels.Set{"workspace.repo": normaliseOrigin(strings.TrimSpace(string(out)))}
}

// normaliseOrigin maps common git remote URL forms to a canonical
// host/path-without-.git string. SSH and HTTPS forms of the same remote
// produce the same value.
func normaliseOrigin(url string) string {
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
