package drain

// pg-gwm isolation mode for `pb drain isolate`, `pb drain reclaim` and
// `pb drain release`.
//
// A repo whose pg-gwm configuration names it AND whose worktree-root is
// writable gets its drain worktree from pg-gwm (a lease named drain/<bead> on
// branch drain/<bead>); every other repo keeps the legacy .worktrees/<bead>
// behavior. In the pg-gwm path pb NEVER calls wtdone or `git worktree
// remove/prune`: pg-gwm owns the worktree, so cleanup is `pg-gwm release`
// (clean work) or `pg-gwm expire` (unsaved work, so the reaper preserves it on
// a pg-gwm/dirty-reaped-* branch).
//
// DECISION (lease lifetime after a pull-request landing): the lease is
// RELEASED and the branch is KEPT. A pull-request landing pushes the branch
// and leaves the worktree clean, so releasing loses nothing, while a lease that
// outlived the run would hold one of the repo's few worktree slots (max) for as
// long as the review takes and then lapse at its 4h TTL anyway. A later
// review-fix session runs `pb drain isolate` again, which leases drain/<bead>
// afresh on the kept branch. After a ff-merge-to-main landing the lease is
// released too. In both cases the BRANCH IS NOT DELETED by pb: pg-gwm keeps a
// released worktree as a reusable slot that still has the branch checked out,
// so `git branch -d` refuses it (observed against the real binary), and moving
// the slot off the branch is pg-gwm's job, not pb's. A merged drain/<bead>
// branch therefore lingers until its slot is reused or reaped; deleting such a
// branch later is the integrate-branch FF-4 follow-up, not a pb step.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/phillipgreenii/pb/internal/run"
)

// Isolation modes (Params.Isolation, Result.Isolation).
const (
	IsolationAuto   = "auto"
	IsolationLegacy = "legacy"
	IsolationGwm    = "pg-gwm"
)

// gwmTTL is the lease time-to-live handed to pg-gwm.
const gwmTTL = "4h"

// gwmRetryWait is the --wait of the single retry after a max-reached refusal.
const gwmRetryWait = "10m"

// ErrGwmMaxReached: every worktree slot of the repo is leased (pg-gwm exit 20),
// also after one retry with --wait. Transient: the caller defers the bead.
// CLI exit 12.
var ErrGwmMaxReached = errors.New("pg-gwm: max worktrees reached")

// ErrGwmRefused: pg-gwm refused with a code pb does not recover from.
var ErrGwmRefused = errors.New("pg-gwm refused")

// gwmError is one failed pg-gwm call, decoded from its JSON error envelope.
type gwmError struct {
	code    int
	name    string
	message string
	hint    string
	details map[string]any
}

func (e *gwmError) Error() string {
	s := fmt.Sprintf("pg-gwm %s (exit %d): %s", e.name, e.code, e.message)
	if e.hint != "" {
		s += " (hint: " + e.hint + ")"
	}
	return s
}

func decodeGwmError(res run.Result, runErr error) *gwmError {
	e := &gwmError{code: res.ExitCode, message: runErr.Error()}
	var env struct {
		Error struct {
			Name    string         `json:"name"`
			Message string         `json:"message"`
			Hint    *string        `json:"hint"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(res.Stdout), &env) == nil && env.Error.Name != "" {
		e.name, e.message, e.details = env.Error.Name, env.Error.Message, env.Error.Details
		if env.Error.Hint != nil {
			e.hint = *env.Error.Hint
		}
	}
	return e
}

type gwmLease struct {
	ID       string `json:"id"`
	LeaseID  string `json:"lease_id"`
	Path     string `json:"worktree_path"`
	Branch   string `json:"branch"`
	Name     string `json:"name"`
	Managed  bool   `json:"managed"`
	Renewed  bool   `json:"renewed"`
	Status   string `json:"status"`
	LeaseTTL string `json:"lease_ttl"`
}

// gwmCall runs pg-gwm <verb> args... --repo <repo> --format=json and returns
// stdout. A non-zero exit becomes a *gwmError.
func gwmCall(ctx context.Context, r run.Runner, repo string, timeout time.Duration, verb string, args ...string) (string, error) {
	full := append([]string{verb}, args...)
	full = append(full, "--repo", repo, "--format=json")
	res, err := r.Run(ctx, "pg-gwm", full, run.Options{Timeout: timeout})
	if err != nil {
		if errors.Is(err, run.ErrTimeout) || res.ExitCode == 0 {
			return res.Stdout, fmt.Errorf("pg-gwm %s: %w", verb, err)
		}
		return res.Stdout, decodeGwmError(res, err)
	}
	return res.Stdout, nil
}

// gwmConfigPath is where pg-gwm reads its configuration file.
func gwmConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pg-gwm", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pg-gwm", "config.toml")
}

var (
	gwmRepoHeader  = regexp.MustCompile(`^\s*\[repos\.(?:"([^"]*)"|'([^']*)')\]\s*(?:#.*)?$`)
	gwmAnyHeader   = regexp.MustCompile(`^\s*\[`)
	gwmRootKeyLine = regexp.MustCompile(`^\s*worktree-root\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// gwmRepoRoot reads the pg-gwm configuration file and returns the worktree-root
// of the table `[repos."<repo>"]` (pg-gwm's default, <repo>.worktrees, when the
// table omits it). found is false when the file has no table for repo. It is a
// line scan, not a TOML parse: pb carries no TOML dependency, and the only
// keys read are the table header and worktree-root; `pg-gwm config check`
// (run first by the caller) is the authority on whether the file is valid.
func gwmRepoRoot(configPath, repo string) (root string, found bool, err error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		if m := gwmRepoHeader.FindStringSubmatch(line); m != nil {
			key := m[1] + m[2]
			if r, e := filepath.EvalSymlinks(key); e == nil {
				key = r
			}
			in = filepath.Clean(key) == filepath.Clean(repo)
			if in {
				found = true
				root = repo + ".worktrees"
			}
			continue
		}
		if gwmAnyHeader.MatchString(line) {
			in = false
			continue
		}
		if in {
			if m := gwmRootKeyLine.FindStringSubmatch(line); m != nil {
				root = m[1] + m[2]
			}
		}
	}
	return root, found, nil
}

// writableAncestor reports whether a file can be created in the nearest
// existing ancestor of dir (pg-gwm creates the root on demand). It probes the
// filesystem only; whether a Claude session's permission settings also allow
// the path is not observable from here (the skill states that precondition).
func writableAncestor(dir string) bool {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			f, err := os.CreateTemp(d, ".pb-gwm-probe-")
			if err != nil {
				return false
			}
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

// useGwm decides the isolation mode for repo and, when it is not pg-gwm in auto
// mode, says why (reason is for diagnostics only).
func useGwm(ctx context.Context, r run.Runner, repo, mode string, timeout time.Duration) (bool, string) {
	switch mode {
	case IsolationLegacy:
		return false, "forced legacy"
	case IsolationGwm:
		return true, ""
	}
	if _, err := r.Run(ctx, "pg-gwm", []string{"config", "check"}, run.Options{Timeout: timeout}); err != nil {
		return false, "pg-gwm config check failed or pg-gwm is absent: " + err.Error()
	}
	root, found, err := gwmRepoRoot(gwmConfigPath(), repo)
	switch {
	case err != nil:
		return false, "cannot read the pg-gwm config: " + err.Error()
	case !found:
		return false, "no pg-gwm config table for " + repo
	case !writableAncestor(root):
		return false, "worktree-root " + root + " is not writable"
	}
	return true, ""
}

// isolateGwm leases drain/<bead> from pg-gwm. The branch is passed with -b only
// when it does not exist (as the pg-router executor does); an existing branch
// is the commit-ish to check out. --or-renew makes a redispatch renew the lease
// instead of colliding with its own name.
func isolateGwm(ctx context.Context, r run.Runner, repo string, p Params, res Result) (Result, error) {
	owner := p.Actor
	if owner == "" {
		return res, fmt.Errorf("pg-gwm isolation needs an owner: pass --actor (or set BEADS_ACTOR)")
	}
	session := p.Session
	if session == "" {
		session = owner
	}
	branch := res.Branch
	exists := branchExists(ctx, r, repo, "refs/heads/"+branch)
	args := func(extra ...string) []string {
		a := []string{
			"--name", branch, "--or-renew", "--owner", owner, "--ttl", gwmTTL,
			"--metadata", "bead=" + p.BeadID, "--metadata", "session=" + session, "--metadata", "role=drain",
		}
		a = append(a, extra...)
		if exists {
			return append(a, branch)
		}
		return append(a, "-b", branch)
	}
	timeout := p.GitTimeout
	if timeout <= 0 {
		timeout = DefaultGitTimeout
	}
	out, err := gwmCall(ctx, r, repo, timeout, "lease", args()...)
	var ge *gwmError
	if errors.As(err, &ge) {
		switch ge.code {
		case 20: // max-reached: one retry, waiting for a slot
			out, err = gwmCall(ctx, r, repo, timeout+10*time.Minute, "lease", args("--wait", gwmRetryWait)...)
			if errors.As(err, &ge) && ge.code == 20 {
				return res, fmt.Errorf("%w: %v", ErrGwmMaxReached, ge)
			}
		case 25: // lease-lost: the reaper preserved the work; re-lease the preserved branch
			out, err = releaseLost(ctx, r, repo, p, branch, exists, owner, session, timeout)
		}
	}
	if err != nil {
		if errors.As(err, &ge) {
			switch ge.code {
			case 21, 22: // held by someone else / branch checked out elsewhere
				return res, fmt.Errorf("%w: %v", ErrConflict, ge)
			}
			return res, fmt.Errorf("%w: %v", ErrGwmRefused, ge)
		}
		return res, err
	}
	var l gwmLease
	if err := json.Unmarshal([]byte(out), &l); err != nil || l.Path == "" {
		return res, fmt.Errorf("pg-gwm lease: cannot read the lease record: %v: %q", err, out)
	}
	res.Worktree, res.Isolation, res.LeaseName, res.LeaseID = l.Path, IsolationGwm, l.Name, l.LeaseID
	switch {
	case l.Renewed:
		res.Reused = "worktree"
	case exists:
		res.Reused = "branch"
	default:
		res.Reused = "none"
	}
	return res, nil
}

// releaseLost recovers a lease-lost refusal: the reaper removed the worktree and
// kept unsaved work on a pg-gwm/dirty-reaped-* branch named in
// `pg-gwm preserved list`. The branch is leased back under drain/<bead> (-B
// resets drain/<bead> to it) only when drain/<bead> is an ancestor of it, so no
// commit is dropped; otherwise the refusal is returned for a person to resolve.
func releaseLost(ctx context.Context, r run.Runner, repo string, p Params, branch string, exists bool, owner, session string, timeout time.Duration) (string, error) {
	lost := &gwmError{code: 25, name: "lease-lost", message: "the lease expired and its work is preserved"}
	out, err := gwmCall(ctx, r, repo, timeout, "preserved", "list")
	if err != nil {
		return "", err
	}
	var pl struct {
		Preserved []struct {
			Branch string `json:"branch"`
			Name   string `json:"name"`
			At     string `json:"at"`
		} `json:"preserved"`
	}
	if json.Unmarshal([]byte(out), &pl) != nil {
		return "", lost
	}
	latest := ""
	for _, e := range pl.Preserved { // list is oldest first; keep the last match
		if e.Name == branch {
			latest = e.Branch
		}
	}
	if latest == "" {
		return "", lost
	}
	if exists {
		if _, err := r.Run(ctx, "git", []string{"-C", repo, "merge-base", "--is-ancestor", "refs/heads/" + branch, "refs/heads/" + latest}, run.Options{}); err != nil {
			lost.message = fmt.Sprintf("preserved branch %s does not contain %s; resolve by hand", latest, branch)
			return "", lost
		}
	}
	a := []string{
		"--name", branch, "--or-renew", "--owner", owner, "--ttl", gwmTTL,
		"--metadata", "bead=" + p.BeadID, "--metadata", "session=" + session, "--metadata", "role=drain",
	}
	if exists {
		a = append(a, "-B", branch, latest)
	} else {
		a = append(a, "-b", branch, latest)
	}
	return gwmCall(ctx, r, repo, timeout, "lease", a...)
}

// Landing kinds for Release.
const (
	LandedFFMerge     = "ff-merge"
	LandedPullRequest = "pull-request"
)

// ReleaseParams selects the lease to give back after a landing.
type ReleaseParams struct {
	RepoPath string
	BeadID   string
	// Landing is LandedFFMerge or LandedPullRequest.
	Landing    string
	GitTimeout time.Duration
}

// ReleaseResult reports what Release did.
type ReleaseResult struct {
	Branch    string `json:"branch"`
	Isolation string `json:"isolation"`
	// Outcome: released | none (no pg-gwm lease, nothing to do).
	Outcome string `json:"outcome"`
}

// ErrReleaseDirty: pg-gwm refused to release a worktree holding unsaved work.
// The caller saves the work or runs `pb drain reclaim` (which expires it).
var ErrReleaseDirty = errors.New("worktree holds unsaved work; not released")

// Release gives the bead's pg-gwm lease back after a landing, with `pg-gwm
// release` (never wtdone, never `git worktree remove`). Both landing kinds
// release and keep the branch: see the DECISION comment at the top of this
// file. The landing kind is validated and returned for the caller's record.
func Release(ctx context.Context, inner run.Runner, p ReleaseParams) (ReleaseResult, error) {
	timeout := p.GitTimeout
	if timeout <= 0 {
		timeout = DefaultGitTimeout
	}
	r := gitBound{inner: inner, timeout: timeout}
	repo, err := resolveRepo(p.RepoPath)
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("%s is not a git repo: %w", p.RepoPath, err)
	}
	branch := "drain/" + p.BeadID
	res := ReleaseResult{Branch: branch, Isolation: IsolationGwm, Outcome: "none"}
	if p.Landing != LandedFFMerge && p.Landing != LandedPullRequest {
		return res, fmt.Errorf("landing %q: want %s or %s", p.Landing, LandedFFMerge, LandedPullRequest)
	}
	_, err = gwmCall(ctx, r, repo, timeout, "release", "--name", branch)
	var ge *gwmError
	switch {
	case errors.As(err, &ge) && ge.code == 4: // not found: no lease (already released, or legacy)
		res.Isolation = IsolationLegacy
	case errors.As(err, &ge) && ge.code == 23:
		return res, fmt.Errorf("%w: %v", ErrReleaseDirty, ge)
	case err != nil:
		return res, err
	default:
		res.Outcome = "released"
	}
	return res, nil
}

// gwmManaged reports whether wt is a worktree pg-gwm manages (a lease holds or
// held it) and returns its lease name. A missing pg-gwm or an unmanaged path
// reads false, so reclaim falls back to the legacy path.
func gwmManaged(ctx context.Context, r run.Runner, repo, wt string, timeout time.Duration) (name string, ok bool) {
	out, err := gwmCall(ctx, r, repo, timeout, "info", "--path", wt)
	if err != nil {
		return "", false
	}
	var l gwmLease
	if json.Unmarshal([]byte(out), &l) != nil || !l.Managed {
		return "", false
	}
	return l.Name, true
}
