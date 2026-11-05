// Package livestate is the read-only verifier for pg-rescue deferral items
// that point at live, left-in-place git state. It recomputes a Snapshot of a
// path and compares it with the one recorded in the item, returning one of
// four outcomes. It never mutates the path and never re-runs the failed
// command: every probe is read-only git plumbing or porcelain run with
// optional locks disabled, so even the index is left untouched.
package livestate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Outcome is the result of verifying live state against a snapshot.
type Outcome string

const (
	// Resolved: the state is gone, or no operation is in progress and the tree is clean.
	Resolved Outcome = "resolved"
	// Intact: an operation marker is present and HEAD and the status digest match.
	Intact Outcome = "intact"
	// Drifted: the state exists but no longer matches the snapshot.
	Drifted Outcome = "drifted"
	// Unreachable: the state cannot be examined from here (other host, unreadable path, no git).
	Unreachable Outcome = "unreachable"
)

// Location classes, matching the pg_rescue_state_kind values.
const (
	KindCanonicalClone = "canonical-clone"
	KindWorktree       = "worktree"
)

// Snapshot is the compact record of a git working state. Its JSON form is
// what a producer stores as the item's state metadata.
type Snapshot struct {
	Host         string `json:"host,omitempty"`
	Toplevel     string `json:"toplevel"`
	Kind         string `json:"kind"`
	Branch       string `json:"branch"`
	Head         string `json:"head"`
	Operation    string `json:"operation,omitempty"`
	StatusCount  int    `json:"status_count"`
	StatusDigest string `json:"status_digest"`
}

// Result is the outcome plus the evidence a triage comment needs.
type Result struct {
	Outcome Outcome
	Reason  string
	// Observed is the freshly captured state; nil when none could be captured.
	Observed *Snapshot
}

// Options configures Verify.
type Options struct {
	// Host is this machine's name. When both it and the snapshot's Host are
	// non-empty and differ, the result is Unreachable.
	Host string
	// Timeout bounds each git call (default 10s).
	Timeout time.Duration
}

// operation markers, in the order they are reported; each is a git-dir path.
var markers = []struct{ name, path string }{
	{"rebase", "rebase-merge"},
	{"rebase", "rebase-apply"},
	{"merge", "MERGE_HEAD"},
	{"cherry-pick", "CHERRY_PICK_HEAD"},
	{"revert", "REVERT_HEAD"},
	{"bisect", "BISECT_LOG"},
}

// Verify compares the live state at path with snap. It is read-only.
func Verify(path string, snap Snapshot, opt Options) Result {
	if opt.Host != "" && snap.Host != "" && opt.Host != snap.Host {
		return Result{Outcome: Unreachable, Reason: fmt.Sprintf("snapshot host %q differs from this host %q", snap.Host, opt.Host)}
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{Outcome: Resolved, Reason: "path no longer exists"}
		}
		return Result{Outcome: Unreachable, Reason: "cannot stat path: " + err.Error()}
	}
	obs, err := Capture(path, opt)
	if err != nil {
		var ne *notRepoError
		if errors.As(err, &ne) {
			return Result{Outcome: Drifted, Reason: "path exists but is no longer a git working tree"}
		}
		return Result{Outcome: Unreachable, Reason: err.Error()}
	}
	obs.Host = snap.Host
	r := Result{Observed: &obs}
	switch {
	case obs.Operation == "" && obs.StatusCount == 0:
		r.Outcome, r.Reason = Resolved, "no operation in progress and the tree is clean"
	case snap.Operation == "":
		r.Outcome, r.Reason = Drifted, "snapshot recorded no operation in progress, but the tree is not clean"
	case obs.Operation != snap.Operation:
		r.Outcome, r.Reason = Drifted, fmt.Sprintf("operation changed from %q to %q", snap.Operation, obs.Operation)
	case obs.Toplevel != snap.Toplevel:
		r.Outcome, r.Reason = Drifted, fmt.Sprintf("toplevel changed from %q to %q", snap.Toplevel, obs.Toplevel)
	case obs.Branch != snap.Branch:
		r.Outcome, r.Reason = Drifted, fmt.Sprintf("branch changed from %q to %q", snap.Branch, obs.Branch)
	case obs.Head != snap.Head:
		r.Outcome, r.Reason = Drifted, fmt.Sprintf("HEAD changed from %s to %s", snap.Head, obs.Head)
	case obs.StatusDigest != snap.StatusDigest:
		r.Outcome, r.Reason = Drifted, "status digest changed"
	default:
		r.Outcome, r.Reason = Intact, "operation in progress; HEAD and status digest match the snapshot"
	}
	return r
}

type notRepoError struct{ msg string }

func (e *notRepoError) Error() string { return e.msg }

// Capture reads the current state at path. It is the same computation a
// producer uses to record the snapshot, so the two always agree.
func Capture(path string, opt Options) (Snapshot, error) {
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	g := gitRunner{dir: path, timeout: timeout}
	var s Snapshot

	top, err := g.run("rev-parse", "--show-toplevel")
	if err != nil {
		if g.notRepo {
			return s, &notRepoError{"not a git working tree: " + path}
		}
		return s, err
	}
	s.Toplevel = top
	gitDir, err := g.run("rev-parse", "--path-format=absolute", "--absolute-git-dir")
	if err != nil {
		return s, err
	}
	common, err := g.run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return s, err
	}
	s.Kind = KindCanonicalClone
	if filepath.Clean(gitDir) != filepath.Clean(common) {
		s.Kind = KindWorktree
	}
	if b, err := g.run("symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		s.Branch = b
	} else {
		s.Branch = "detached"
	}
	// An unborn HEAD has no object id; leave it empty.
	if h, err := g.run("rev-parse", "-q", "--verify", "HEAD"); err == nil {
		s.Head = h
	}
	for _, m := range markers {
		p, err := g.run("rev-parse", "--path-format=absolute", "--git-path", m.path)
		if err != nil {
			return s, err
		}
		if _, err := os.Stat(p); err == nil {
			s.Operation = m.name
			break
		}
	}
	st, err := g.runRaw("status", "--porcelain=v2", "--untracked-files=normal")
	if err != nil {
		return s, err
	}
	sum := sha256.Sum256(st)
	s.StatusDigest = hex.EncodeToString(sum[:])
	if t := strings.TrimRight(string(st), "\n"); t != "" {
		s.StatusCount = strings.Count(t, "\n") + 1
	}
	return s, nil
}

type gitRunner struct {
	dir     string
	timeout time.Duration
	notRepo bool
}

func (g *gitRunner) run(args ...string) (string, error) {
	out, err := g.runRaw(args...)
	return strings.TrimRight(string(out), "\r\n"), err
}

func (g *gitRunner) runRaw(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	full := append([]string{"--no-optional-locks", "-C", g.dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			g.notRepo = true
		}
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
