package beadhandler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Metadata keys of the live-state snapshot written at filing time, next to
// metaFingerprint. A consumer reads these instead of parsing the body.
const (
	metaHost      = "pg_rescue_host"
	metaCwd       = "pg_rescue_cwd"
	metaFiledAt   = "pg_rescue_filed_at"
	metaState     = "pg_rescue_state"
	metaStateKind = "pg_rescue_state_kind"
)

// Location classes recorded in pg_rescue_state_kind.
const (
	KindCanonicalClone = "canonical-clone"
	KindWorktree       = "worktree"
	KindOtherDir       = "other-dir"
	KindNotARepo       = "not-a-repo"
)

// operationMarkers are the per-worktree git state entries whose presence says
// an operation is in progress, in the order they are reported.
var operationMarkers = []struct{ entry, name string }{
	{"rebase-merge", "rebase"},
	{"rebase-apply", "rebase"},
	{"MERGE_HEAD", "merge"},
	{"CHERRY_PICK_HEAD", "cherry-pick"},
	{"REVERT_HEAD", "revert"},
	{"BISECT_LOG", "bisect"},
}

// State is the snapshot of the live working state a failure left behind. It
// is compact on purpose: counts and digests, never file contents.
type State struct {
	// Exists is false when the working directory is gone.
	Exists bool `json:"exists"`
	// Toplevel is the git toplevel, "" outside a repository or in a bare one.
	Toplevel string `json:"toplevel,omitempty"`
	// Branch is the current branch, "detached" when HEAD is detached.
	Branch string `json:"branch,omitempty"`
	// Head is the HEAD object id, "" when there is no commit yet.
	Head string `json:"head,omitempty"`
	// Operation is the in-progress operation (rebase, merge, cherry-pick,
	// revert, bisect), "" when none.
	Operation string `json:"operation,omitempty"`
	// StatusCount is the number of `git status --porcelain=v2` lines.
	StatusCount int `json:"status_count"`
	// StatusDigest is the sha256 of that output.
	StatusDigest string `json:"status_digest,omitempty"`
}

// Snapshot probes dir with read-only git commands and returns the state and
// its location class. It never changes anything and never fails: a probe that
// cannot run reads as "not a repository".
func Snapshot(dir string) (State, string) {
	var st State
	if dir == "" {
		return st, KindNotARepo
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return st, KindNotARepo
	}
	st.Exists = true
	out, err := gitOut(dir, "rev-parse", "--path-format=absolute", "--absolute-git-dir", "--git-common-dir", "--is-bare-repository")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || len(lines) != 3 {
		return st, KindNotARepo
	}
	gitDir, commonDir, bare := filepath.Clean(lines[0]), filepath.Clean(lines[1]), lines[2] == "true"
	if bare {
		return st, KindOtherDir
	}
	if top, err := gitOut(dir, "rev-parse", "--show-toplevel"); err == nil {
		st.Toplevel = strings.TrimSpace(top)
	}
	if b, err := gitOut(dir, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		st.Branch = strings.TrimSpace(b)
	} else {
		st.Branch = "detached"
	}
	if h, err := gitOut(dir, "rev-parse", "-q", "--verify", "HEAD"); err == nil {
		st.Head = strings.TrimSpace(h)
	}
	for _, m := range operationMarkers {
		if _, err := os.Stat(filepath.Join(gitDir, m.entry)); err == nil {
			st.Operation = m.name
			break
		}
	}
	if s, err := gitOut(dir, "--no-optional-locks", "status", "--porcelain=v2"); err == nil {
		st.StatusCount = strings.Count(s, "\n")
		sum := sha256.Sum256([]byte(s))
		st.StatusDigest = hex.EncodeToString(sum[:])
	}
	if gitDir == commonDir {
		return st, KindCanonicalClone
	}
	return st, KindWorktree
}

// JSON is the compact JSON object stored in pg_rescue_state.
func (s State) JSON() string {
	b, _ := json.Marshal(s)
	return string(b)
}

// gitOut runs git in dir and returns stdout. The GIT_* variables that
// redirect git elsewhere are dropped so the answer is about dir.
func gitOut(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	return string(out), err
}

// snapshotBlock is the text appended to a dedup-hit comment: the most recent
// observation, which a consumer verifies against.
func snapshotBlock(filedAt, kind, stateJSON string) string {
	return "State snapshot at " + filedAt + " (kind " + kind + "):\n\n" + stateJSON + "\n"
}
