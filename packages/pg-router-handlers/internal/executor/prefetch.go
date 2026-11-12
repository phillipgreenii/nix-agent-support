package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/gitenv"
	"github.com/phillipgreenii/pg-router-handlers/internal/prompt"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// Handler-side worktree pre-fetch (bead pg2-hh32y).
//
// A review session used to spend its own clock on `git fetch pull/N/head`
// (~43s), `git checkout` (~33s, up to 128s) and `git diff`, and a session under
// dontAsk could be denied the checkout outright (bead isg0l.2: it fell back to
// reviewing the fetched commit). With isolation.prefetch configured, the
// handler does those steps once, after the worktree exists and BEFORE the
// session is launched, so no pool slot is held while it runs and the prompt
// finds the worktree already at the pinned commit with the diff on disk.
//
// Best effort by design: the handler is a launchd daemon and may lack the
// agent/cert an interactive shell has (docs/runbooks/dispatched-session-git-
// origin-auth.md), so a failed fetch must not fail the dispatch. The outcome is
// written to a manifest in the worktree and exposed to the prompt as
// {{.Prefetch}} (prompt.PrefetchInfo); OK == false tells the prompt to do the
// steps itself.

const (
	// prefetchDirName is the directory, inside the worktree, holding the diff,
	// numstat and manifest files. It is added to the repository's
	// info/exclude, because every non-force worktree removal this handler
	// performs (cleanup, orphan reclaim, sweep) refuses a worktree holding
	// untracked files; an ignored file does not block removal.
	prefetchDirName      = ".pg-router"
	prefetchManifestName = "prefetch.json"
	prefetchDiffName     = "diff.patch"
	prefetchNumstatName  = "numstat.txt"

	defaultPrefetchTimeout = 5 * time.Minute
	defaultPrefetchRemote  = "origin"

	// prefetchGitWaitDelay bounds how long a cancelled git child gets to react to
	// SIGTERM (and its own children, e.g. a smudge filter, to close the pipes)
	// before it is killed outright (bead pg2-lpctwk).
	prefetchGitWaitDelay = 15 * time.Second

	// prefetchCheckoutMarker is a file in the worktree's gitdir that exists
	// only while the pre-fetch checkout is unfinished. A checkout that was
	// SIGKILLed leaves it behind, and the next pre-fetch reads it as "this
	// handler-owned scratch worktree is half-updated" (bead pg2-lpctwk).
	prefetchCheckoutMarker = "pg-router-prefetch-checkout"

	// prefetchSkipOriginUnavailable is the manifest outcome of a pre-fetch whose
	// fetch was skipped because pg-origin-guard reported the origin unavailable;
	// it is distinct from a fetch that ran and failed.
	prefetchSkipOriginUnavailable = "origin-unavailable"
)

// guardSkipsFetch reports whether pg-origin-guard says the origin is
// unavailable, so the network fetch must be skipped. A guard fault (error)
// fails open: the fetch runs and the fault is logged.
func (r *ccpoolRun) guardSkipsFetch(ctx context.Context, wt string, d DispatchContext) bool {
	if r.deps.OriginCheck == nil {
		return false
	}
	v, err := r.deps.OriginCheck(ctx, wt)
	if err != nil {
		slog.Warn("dispatch: worktree pre-fetch: origin guard fault; fetching anyway",
			"role", d.Role.Name, "bead", d.Item.ID, "err", err)
		return false
	}
	if v.Available {
		return false
	}
	slog.Warn("dispatch: worktree pre-fetch: origin unavailable; fetch skipped",
		"role", d.Role.Name, "bead", d.Item.ID, "reason", v.Reason)
	return true
}

// GitRun runs `git <args...>` in dir, writing the child's stdout to stdout. A
// non-zero exit is an error carrying the child's stderr. It is a Deps seam so
// tests can script git; production uses osGitRun (gitenv-hermetic).
type GitRun func(ctx context.Context, dir string, stdout io.Writer, args ...string) error

// osGitRun is the production GitRun. gitenv.Command owns the child
// environment (every git child in this module MUST be built by it).
func osGitRun(ctx context.Context, dir string, stdout io.Writer, args ...string) error {
	cmd := gitenv.Command(ctx, dir, args...)
	// Cancel gently: SIGKILL (the exec.CommandContext default) leaves a stale
	// index.lock in the worktree gitdir that nothing ever removes, because git
	// cannot clean up after itself. SIGTERM lets git release its lockfiles; the
	// WaitDelay still kills a child that ignores it (bead pg2-lpctwk).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = prefetchGitWaitDelay
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (d Deps) gitRun() GitRun {
	if d.GitRun != nil {
		return d.GitRun
	}
	return osGitRun
}

// ValidatePrefetch checks a PrefetchConfig at config-load time: the templates
// parse and the timeout is a duration. It does not render them (that needs an
// item).
func ValidatePrefetch(pf roles.PrefetchConfig) error {
	if strings.TrimSpace(pf.Rev) == "" {
		return errors.New("prefetch.Rev is required")
	}
	for name, src := range map[string]string{"Remote": pf.Remote, "Refspec": pf.Refspec, "Rev": pf.Rev, "DiffBase": pf.DiffBase} {
		if _, err := prompt.Parse("prefetch."+name, src); err != nil {
			return fmt.Errorf("prefetch.%s: %w", name, err)
		}
	}
	if pf.Timeout != "" {
		d, err := time.ParseDuration(pf.Timeout)
		if err != nil {
			return fmt.Errorf("prefetch.Timeout %q: %w", pf.Timeout, err)
		}
		if d <= 0 {
			return fmt.Errorf("prefetch.Timeout %q must be positive", pf.Timeout)
		}
	}
	return nil
}

var (
	// hexRev bounds the rendered Rev: an abbreviated or full object id. Metadata
	// comes from a remote API, so it is never allowed to look like an option or a
	// revision expression.
	hexRev = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	// safeRef bounds a rendered refspec/base/remote: no leading '-', no
	// whitespace or control characters.
	safeRef = regexp.MustCompile(`^[A-Za-z0-9_][^\s\x00-\x1f]*$`)
)

func renderPrefetchField(name, src string, pctx prompt.Context) (string, error) {
	if strings.TrimSpace(src) == "" {
		return "", nil
	}
	t, err := prompt.Parse("prefetch."+name, src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	out, err := prompt.Render(t, pctx)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// prefetch pins the worktree wt to the configured commit and pre-generates the
// diff. It never returns an error: every failure is logged, the manifest
// records OK=false, and the zero/partial PrefetchInfo is returned.
func (r *ccpoolRun) prefetch(ctx context.Context, pf roles.PrefetchConfig, d DispatchContext, wt string) prompt.PrefetchInfo {
	info := prompt.PrefetchInfo{Dir: filepath.Join(wt, prefetchDirName)}
	fail := func(step string, err error) prompt.PrefetchInfo {
		slog.Warn("dispatch: worktree pre-fetch failed; the session will do these steps itself",
			"role", d.Role.Name, "bead", d.Item.ID, "worktree", wt, "step", step, "err", err)
		info.OK = false
		r.writePrefetchManifest(ctx, wt, info)
		return info
	}

	timeout := defaultPrefetchTimeout
	if pf.Timeout != "" {
		if t, err := time.ParseDuration(pf.Timeout); err == nil && t > 0 {
			timeout = t
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	git := r.deps.gitRun()

	pctx := prompt.Context{Item: d.Item, WorktreeDir: wt, SelfLogin: r.deps.Cfg.SelfLogin, RepoRoot: r.deps.Cfg.RepoRoot}
	rev, err := renderPrefetchField("Rev", pf.Rev, pctx)
	if err != nil {
		return fail("render", err)
	}
	if !hexRev.MatchString(rev) {
		return fail("render", fmt.Errorf("rev rendered to %q, want a 7-64 digit hex object id", rev))
	}
	remote, err := renderPrefetchField("Remote", pf.Remote, pctx)
	if err != nil {
		return fail("render", err)
	}
	if remote == "" {
		remote = defaultPrefetchRemote
	}
	refspec, err := renderPrefetchField("Refspec", pf.Refspec, pctx)
	if err != nil {
		return fail("render", err)
	}
	base, err := renderPrefetchField("DiffBase", pf.DiffBase, pctx)
	if err != nil {
		return fail("render", err)
	}
	for name, v := range map[string]string{"Remote": remote, "Refspec": refspec, "DiffBase": base} {
		if v != "" && !safeRef.MatchString(v) {
			return fail("render", fmt.Errorf("%s rendered to %q, which is not a plain ref", name, v))
		}
	}

	// Resolve Rev locally first: a commit the canonical clone already has (the
	// operator fetched it, or an earlier review of the same PR did) needs no
	// network at all.
	have := func(commitish string) bool {
		return git(ctx, wt, io.Discard, "rev-parse", "--verify", "--quiet", commitish+"^{commit}") == nil
	}
	if !have(rev) {
		if refspec == "" {
			return fail("resolve", fmt.Errorf("commit %s is not local and no Refspec is configured", rev))
		}
		if skip := r.guardSkipsFetch(ctx, wt, d); skip {
			info.Skipped = prefetchSkipOriginUnavailable
			return fail("origin-unavailable", errors.New("pg-origin-guard reports the origin unavailable; fetch skipped"))
		}
		if err := git(ctx, wt, io.Discard, "fetch", "--no-tags", remote, refspec); err != nil {
			return fail("fetch", err)
		}
		if !have(rev) {
			return fail("resolve", fmt.Errorf("commit %s not found after fetching %s %s", rev, remote, refspec))
		}
	}
	// A previous checkout that was killed leaves index.lock and a half-updated
	// tree in this handler-owned scratch worktree; recover it before checking
	// out, or every later dispatch fails on the same lock (bead pg2-lpctwk).
	gitDir := r.worktreeGitDir(ctx, wt)
	force := r.recoverPoisonedCheckout(ctx, gitDir, wt, d)
	marker := ""
	if gitDir != "" {
		marker = filepath.Join(gitDir, prefetchCheckoutMarker)
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			slog.Warn("dispatch: worktree pre-fetch: cannot write checkout marker", "path", marker, "err", err)
			marker = ""
		}
	}
	checkout := []string{"checkout", "--detach", "--quiet"}
	if force {
		checkout = append(checkout, "--force")
	}
	err = git(ctx, wt, io.Discard, append(checkout, rev)...)
	if err != nil && r.recoverCorruptIndex(ctx, gitDir, wt, d, err) {
		// The per-worktree index was truncated or corrupt (a checkout killed
		// mid-write); it is removed, so a forced checkout rebuilds it.
		err = git(ctx, wt, io.Discard, "checkout", "--detach", "--quiet", "--force", rev)
	}
	if err != nil {
		return fail("checkout", err)
	}
	if marker != "" {
		_ = os.Remove(marker)
	}
	info.OK, info.Rev = true, rev

	if !r.prepareOutDir(ctx, wt, info.Dir) {
		return fail("outdir", errors.New("cannot prepare the output directory"))
	}

	if base != "" {
		if !have(base) {
			// Not fatal: the checkout is pinned, only the diff files are missing.
			slog.Warn("dispatch: worktree pre-fetch: diff base does not resolve; no diff files written",
				"role", d.Role.Name, "bead", d.Item.ID, "base", base)
		} else {
			rng := base + "..." + rev
			numstat := filepath.Join(info.Dir, prefetchNumstatName)
			if err := writeGitOutput(ctx, git, wt, numstat, "diff", "--no-ext-diff", "--no-color", "--numstat", rng); err != nil {
				slog.Warn("dispatch: worktree pre-fetch: numstat failed", "bead", d.Item.ID, "err", err)
			} else {
				info.NumstatFile = numstat
			}
			diff := filepath.Join(info.Dir, prefetchDiffName)
			if err := writeGitOutput(ctx, git, wt, diff, "diff", "--no-ext-diff", "--no-color", rng); err != nil {
				slog.Warn("dispatch: worktree pre-fetch: diff failed", "bead", d.Item.ID, "err", err)
			} else {
				info.DiffFile = diff
			}
		}
	}
	r.writePrefetchManifest(ctx, wt, info)
	slog.Info("dispatch: worktree pre-fetched", "role", d.Role.Name, "bead", d.Item.ID, "worktree", wt,
		"rev", rev, "diff", info.DiffFile != "", "numstat", info.NumstatFile != "")
	return info
}

// worktreeGitDir is the absolute per-worktree gitdir of wt, or "" when it
// cannot be located (recovery is then skipped).
func (r *ccpoolRun) worktreeGitDir(ctx context.Context, wt string) string {
	var out bytes.Buffer
	if err := r.deps.gitRun()(ctx, wt, &out, "rev-parse", "--path-format=absolute", "--git-dir"); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// recoverPoisonedCheckout clears the state a killed pre-fetch checkout leaves
// in a handler-owned scratch worktree: a stale <gitdir>/index.lock and/or the
// unfinished-checkout marker. It reports whether the next checkout must be
// forced (the tree is half-updated, and the dirt is the failed checkout, not
// user work). A live session that shares the worktree may legitimately hold
// the lock, so recovery is skipped when one exists or the pool cannot be
// listed (bead pg2-lpctwk).
func (r *ccpoolRun) recoverPoisonedCheckout(ctx context.Context, gitDir, wt string, d DispatchContext) bool {
	if gitDir == "" {
		return false
	}
	lock := filepath.Join(gitDir, "index.lock")
	_, lockErr := os.Stat(lock)
	_, markErr := os.Stat(filepath.Join(gitDir, prefetchCheckoutMarker))
	if lockErr != nil && markErr != nil {
		return false
	}
	if r.deps.CC != nil && r.worktreeSharedWithLivePeer(ctx, "", wt) {
		slog.Warn("dispatch: worktree pre-fetch: poisoned checkout state but a live session shares the worktree; not recovering",
			"role", d.Role.Name, "bead", d.Item.ID, "worktree", wt)
		return false
	}
	if lockErr == nil {
		if err := os.Remove(lock); err != nil {
			slog.Warn("dispatch: worktree pre-fetch: cannot remove stale index.lock", "path", lock, "err", err)
			return false
		}
	}
	slog.Warn("dispatch: worktree pre-fetch: recovering a worktree left by a killed checkout",
		"role", d.Role.Name, "bead", d.Item.ID, "worktree", wt, "staleLock", lockErr == nil, "marker", markErr == nil)
	return true
}

// corruptIndexMessages are the fragments git prints when it cannot read a
// worktree's index file.
var corruptIndexMessages = []string{
	"index file smaller than expected",
	"index file corrupt",
	"bad index file sha1 signature",
	"bad index version",
	"unknown index entry format",
}

// recoverCorruptIndex handles a checkout that failed because the per-worktree
// index is truncated or corrupt, which a killed checkout can leave behind even
// after its lock and marker were cleared. The index is a cache of the tree, and
// this is a handler-owned scratch worktree, so the recovery deletes
// <gitdir>/index and reports true so the caller retries with --force, which
// rebuilds it. Files that the old commit tracked and the new one does not stay
// behind as untracked, so the worktree is then not clean, which only matters to
// a non-force removal. It does nothing (false) for any other failure, when the
// gitdir is unknown, or when a live session shares the worktree. It never
// repairs the info/exclude lookup: that needs a valid gitdir, so a worktree
// whose gitdir is gone stays a logged, best-effort pre-fetch failure.
func (r *ccpoolRun) recoverCorruptIndex(ctx context.Context, gitDir, wt string, d DispatchContext, checkoutErr error) bool {
	if gitDir == "" {
		return false
	}
	corrupt := false
	for _, m := range corruptIndexMessages {
		if strings.Contains(checkoutErr.Error(), m) {
			corrupt = true
			break
		}
	}
	if !corrupt {
		return false
	}
	if r.deps.CC != nil && r.worktreeSharedWithLivePeer(ctx, "", wt) {
		slog.Warn("dispatch: worktree pre-fetch: corrupt index but a live session shares the worktree; not recovering",
			"role", d.Role.Name, "bead", d.Item.ID, "worktree", wt)
		return false
	}
	idx := filepath.Join(gitDir, "index")
	if err := os.Remove(idx); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("dispatch: worktree pre-fetch: cannot remove corrupt index", "path", idx, "err", err)
		return false
	}
	slog.Warn("dispatch: worktree pre-fetch: removed a corrupt worktree index; retrying the checkout with --force",
		"role", d.Role.Name, "bead", d.Item.ID, "worktree", wt, "err", checkoutErr)
	return true
}

// writeGitOutput streams `git <args>`'s stdout to path, removing a partial
// file on failure so a half-written diff is never mistaken for a whole one.
func writeGitOutput(ctx context.Context, git GitRun, dir, path string, args ...string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	runErr := git(ctx, dir, f, args...)
	closeErr := f.Close()
	if runErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(runErr, closeErr)
	}
	return nil
}

// prepareOutDir makes dir inside wt, FIRST adding /.pg-router/ to the
// repository's info/exclude so the files in it do not make the worktree
// "unclean" for a non-force removal (git ignores ignored files there). It
// reports false, creating nothing, when the exclude entry cannot be written:
// an un-ignored directory would strand the worktree for the sweep.
func (r *ccpoolRun) prepareOutDir(ctx context.Context, wt, dir string) bool {
	var out bytes.Buffer
	if err := r.deps.gitRun()(ctx, wt, &out, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"); err != nil {
		slog.Warn("dispatch: worktree pre-fetch: cannot locate info/exclude", "worktree", wt, "err", err)
		return false
	}
	path := strings.TrimSpace(out.String())
	if path == "" {
		return false
	}
	const line = "/" + prefetchDirName + "/"
	listed := false
	if existing, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(l) == line {
				listed = true
				break
			}
		}
	}
	if !listed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			slog.Warn("dispatch: worktree pre-fetch: cannot create info dir", "path", path, "err", err)
			return false
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			slog.Warn("dispatch: worktree pre-fetch: cannot open info/exclude", "path", path, "err", err)
			return false
		}
		_, werr := f.WriteString(line + "\n")
		cerr := f.Close()
		if err := errors.Join(werr, cerr); err != nil {
			slog.Warn("dispatch: worktree pre-fetch: cannot write info/exclude", "path", path, "err", err)
			return false
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("dispatch: worktree pre-fetch: cannot create output dir", "dir", dir, "err", err)
		return false
	}
	return true
}

// writePrefetchManifest records info so a later nudge render for the same
// worktree (a duplicate absorbed after a restart) sees the same outcome. A
// failure is logged; the absent manifest then reads as OK == false.
func (r *ccpoolRun) writePrefetchManifest(ctx context.Context, wt string, info prompt.PrefetchInfo) {
	if !r.prepareOutDir(ctx, wt, info.Dir) {
		return
	}
	b, _ := json.Marshal(info)
	if err := os.WriteFile(filepath.Join(info.Dir, prefetchManifestName), b, 0o644); err != nil {
		slog.Warn("dispatch: worktree pre-fetch: cannot write manifest", "dir", info.Dir, "err", err)
	}
}

// loadPrefetchInfo reads the manifest prefetch wrote into wt. A missing or
// unreadable manifest is the zero value (OK == false).
func loadPrefetchInfo(wt string) prompt.PrefetchInfo {
	b, err := os.ReadFile(filepath.Join(wt, prefetchDirName, prefetchManifestName))
	if err != nil {
		return prompt.PrefetchInfo{}
	}
	var info prompt.PrefetchInfo
	if json.Unmarshal(b, &info) != nil {
		return prompt.PrefetchInfo{}
	}
	return info
}
