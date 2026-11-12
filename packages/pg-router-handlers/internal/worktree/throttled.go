package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/x/gitclient"
)

// Rate-limited worktree removal (stopgap for the macOS filesystem-event cost
// of deleting a very large checkout).
//
// Measured 2026-11-04 (clone spike): every delete method (`rm -rf`,
// `find -delete`, `git worktree remove`) emits the same one event per removed
// path (about 111,000 for a 100,000-file tree) and there is no cheaper delete.
// Two monorepo worktrees removed back to back produced about 284,000 unlink and
// rmdir events in 553 seconds, each one a filesystem event for every listener
// and about 33 to 48 microseconds of fseventsd CPU. A rename of the whole tree
// within one volume emits 2 events and takes about 60 ms, but only MOVES the
// burst: the later delete still emits the full count. The stopgap therefore
// renames the worktree aside (one cheap step that frees the bead's path and
// unregisters the worktree from git's point of view) and then deletes the trash
// in bounded batches with a pause between batches, so the removal spreads over
// time instead of arriving as one burst. Operator ruling (Phillip, 2026-10-05):
// core.fsmonitor stays ON for the monorepo; only the worktree's own daemon is
// stopped, right before the rename.

// DefaultTrashDirName is the directory, beside the per-bead worktrees, that
// receives a worktree before its paced delete. Its leading dot keeps it out of
// the worktree candidate scans.
const DefaultTrashDirName = ".trash"

// Removal tunes the paced removal. The zero value of a field means its default.
type Removal struct {
	// BatchSize is how many unlink/rmdir calls run before a pause
	// (default DefaultBatchSize).
	BatchSize int
	// Pause is how long to wait between batches (default DefaultPause).
	Pause time.Duration
	// Budget bounds how long one removal keeps draining; what is left stays in
	// the trash for the janitor sweep to finish (default DefaultBudget).
	Budget time.Duration
	// Sleep waits d or returns ctx's error; tests inject a recorder so no test
	// sleeps on the wall clock (default: a timer).
	Sleep func(ctx context.Context, d time.Duration) error
	// Limiter is the process-wide removal budget and pending-drain bound shared
	// by every Removal that holds the same pointer (nil: no shared budget).
	Limiter *Limiter
	// StopFsmonitor stops the worktree's git fsmonitor daemon, best effort
	// (default: `git -C <path> fsmonitor--daemon stop`).
	StopFsmonitor func(ctx context.Context, path string)
}

// Defaults: at most DefaultBatchSize/DefaultPause = 2,000 removal events per
// second of pause time (the unthrottled delete measured about 11,000 per
// second). The budget is deliberately SHORT of a whole monorepo worktree: a real
// one is about 230,000 to 240,000 entries and the paced drain measured about
// 1,000 ops per second under load (180,000 ops in the 3 minutes), so an inline
// drain of one never finishes. That is by design: the inline drain bounds how
// long a teardown holds its caller, and the janitor sweep (the designed
// finisher) purges the rest. The expected log line for that hand-off is the INFO
// "paced delete of the trash entry handed to the janitor sweep"; a WARN means a
// real error.
const (
	DefaultBatchSize = 500
	DefaultPause     = 250 * time.Millisecond
	DefaultBudget    = 3 * time.Minute
)

func (r Removal) batchSize() int {
	if r.BatchSize <= 0 {
		return DefaultBatchSize
	}
	return r.BatchSize
}

func (r Removal) pause() time.Duration {
	if r.Pause < 0 {
		return 0
	}
	if r.Pause == 0 {
		return DefaultPause
	}
	return r.Pause
}

func (r Removal) budget() time.Duration {
	if r.Budget <= 0 {
		return DefaultBudget
	}
	return r.Budget
}

func (r Removal) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DrainStats reports what a drain did.
type DrainStats struct {
	Ops     int  // unlink and rmdir calls issued
	Batches int  // pauses taken (a drain of N ops pauses floor(N/BatchSize) times)
	Done    bool // the whole tree, root included, is gone
}

// DrainTree deletes root depth-first in batches: after every BatchSize unlink or
// rmdir calls it waits Pause. Nothing is followed (a symlink is unlinked, never
// entered). An entry that vanished meanwhile (a concurrent drain of the same
// trash) counts as removed. It stops early, without error, when ctx ends or the
// Budget runs out, leaving the rest for a later pass; any other failure is
// returned after the walk finished the entries it could.
func (r Removal) DrainTree(ctx context.Context, root string) (DrainStats, error) {
	release, ok := r.Limiter.Admit()
	if !ok {
		slog.Warn("worktree: removal queue full; the tree stays in the trash for the janitor sweep", "root", root, "pending", r.Limiter.Pending())
		return DrainStats{}, nil
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, r.budget())
	defer cancel()
	d := &drainer{r: r, ctx: ctx, batch: r.batchSize(), pause: r.pause()}
	d.dir(root)
	switch {
	case d.stopped:
		return DrainStats{Ops: d.ops, Batches: d.batches}, nil
	case d.err != nil:
		return DrainStats{Ops: d.ops, Batches: d.batches}, d.err
	}
	return DrainStats{Ops: d.ops, Batches: d.batches, Done: true}, nil
}

type drainer struct {
	r       Removal
	ctx     context.Context
	batch   int
	pause   time.Duration
	ops     int
	batches int
	stopped bool
	err     error
}

// op counts one removal call and pauses at each batch boundary. It reports
// false once draining must stop.
func (d *drainer) op() bool {
	if d.stopped || d.ctx.Err() != nil {
		d.stopped = true
		return false
	}
	if d.ops%d.batch == 0 {
		// Start of a chunk of batch events: book it against the shared budget.
		if w := d.r.Limiter.Reserve(d.batch); w > 0 {
			if err := d.r.sleep(d.ctx, w); err != nil {
				d.stopped = true
				return false
			}
		}
	}
	d.ops++
	if d.ops%d.batch == 0 {
		d.batches++
		if d.pause > 0 {
			if err := d.r.sleep(d.ctx, d.pause); err != nil {
				d.stopped = true
				return false
			}
		}
	}
	return true
}

func (d *drainer) fail(err error) {
	if err != nil && !errors.Is(err, fs.ErrNotExist) && d.err == nil {
		d.err = err
	}
}

// dir empties and removes one directory; it reports whether the directory is gone.
func (d *drainer) dir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		d.fail(err)
		return false
	}
	clean := true
	for _, e := range entries {
		child := filepath.Join(path, e.Name())
		if e.Type().IsDir() {
			if !d.dir(child) {
				clean = false
			}
			if d.stopped {
				return false
			}
			continue
		}
		if !d.op() {
			return false
		}
		if err := os.Remove(child); err != nil && !errors.Is(err, fs.ErrNotExist) {
			d.fail(err)
			clean = false
		}
	}
	if !clean {
		return false
	}
	if !d.op() {
		return false
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		d.fail(err)
		return false
	}
	return true
}

// MoveToTrash renames path to <dir of path>/<trashDirName>/<base>.<unix nanos>
// (one rename within a volume) and returns the new path.
func MoveToTrash(path, trashDirName string, now time.Time) (string, error) {
	if trashDirName == "" {
		trashDirName = DefaultTrashDirName
	}
	root := filepath.Join(filepath.Dir(path), trashDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create trash directory: %w", err)
	}
	trash := filepath.Join(root, filepath.Base(path)+"."+strconv.FormatInt(now.UnixNano(), 10))
	if err := os.Rename(path, trash); err != nil {
		return "", fmt.Errorf("move %s aside: %w", path, err)
	}
	return trash, nil
}

// RemoveTree deletes a plain directory tree the paced way: rename to the trash,
// then drain. A drain cut short leaves the remainder in the trash and is not an
// error.
func (r Removal) RemoveTree(ctx context.Context, path string) error {
	trash, err := MoveToTrash(path, DefaultTrashDirName, time.Now())
	if err != nil {
		return err
	}
	return r.drainTrash(ctx, trash)
}

func (r Removal) drainTrash(ctx context.Context, trash string) error {
	st, err := r.DrainTree(ctx, trash)
	switch {
	case err != nil:
		slog.Warn("worktree: paced delete of the trash entry hit an error (the janitor sweep retries)",
			"trash", trash, "ops", st.Ops, "err", err)
	case !st.Done:
		slog.Info("worktree: paced delete of the trash entry handed to the janitor sweep (budget used up; the sweep finishes it)",
			"trash", trash, "ops", st.Ops, "batches", st.Batches, "budget", r.budget().String())
	default:
		slog.Info("worktree: trash entry deleted in paced batches", "trash", trash, "ops", st.Ops, "batches", st.Batches)
	}
	return err
}

// ThrottledOpener is GitOpener with RemoveWorktree replaced by the paced removal.
// Every other method of the returned manager is the plain gitclient.Client's.
// opts configure every gitclient it opens (tests pass a hermetic environment).
func ThrottledOpener(r Removal, opts ...gitclient.Option) Opener {
	return func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		c, err := gitclient.New(ctx, dir, opts...)
		if err != nil {
			return nil, err
		}
		return &throttledClient{Client: c, r: r, opts: opts}, nil
	}
}

// throttledClient embeds *gitclient.Client so the optional-interface assertions
// callers make (StatusReader, RefReader, BranchManager) keep working.
type throttledClient struct {
	*gitclient.Client
	r    Removal
	opts []gitclient.Option
}

// ErrNotLinkedWorktree is returned when the path is not a linked worktree
// (it has no .git file naming an admin directory).
var ErrNotLinkedWorktree = errors.New("worktree: not a linked worktree")

// ErrWorktreeDirty is returned by a non-force removal of a worktree with
// uncommitted or untracked changes (git's own refusal, kept).
var ErrWorktreeDirty = errors.New("worktree: contains modified or untracked files, use force to remove it")

// ErrWorktreeLocked is returned when git has the worktree locked.
var ErrWorktreeLocked = errors.New("worktree: locked")

// RemoveWorktree keeps `git worktree remove [--force]`'s refusals (not a linked
// worktree, locked, dirty without force), then stops the worktree's fsmonitor
// daemon, renames the tree into the trash, prunes the now-dangling registration
// from the repository, and drains the trash in paced batches.
func (t *throttledClient) RemoveWorktree(ctx context.Context, path string, force bool) error {
	admin, ok := adminDir(path)
	if !ok {
		return fmt.Errorf("%s: %w", path, ErrNotLinkedWorktree)
	}
	if _, err := os.Lstat(filepath.Join(admin, "locked")); err == nil {
		return fmt.Errorf("%s: %w", path, ErrWorktreeLocked)
	}
	if !force {
		st, ok := interface{}(t.Client).(gitclient.StatusReader)
		if !ok {
			return fmt.Errorf("%s: cannot check the worktree is clean", path)
		}
		dirty, err := st.Status(ctx)
		if err != nil {
			return fmt.Errorf("%s: status: %w", path, err)
		}
		if len(dirty) > 0 {
			return fmt.Errorf("%s: %w", path, ErrWorktreeDirty)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	common := filepath.Dir(filepath.Dir(admin)) // <common>/worktrees/<name> -> <common>
	t.stopFsmonitor(ctx, path)
	trash, err := MoveToTrash(path, DefaultTrashDirName, time.Now())
	if err != nil {
		return err
	}
	// The tree is gone from its path; from here the removal has happened.
	// Prune the dangling registration with a context the shutdown cannot cancel.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	t.prune(pctx, common, path)
	_ = t.r.drainTrash(ctx, trash)
	return nil
}

// prune runs `git worktree prune` from the repository that owns common.
func (t *throttledClient) prune(ctx context.Context, common, path string) {
	root := common
	if filepath.Base(common) == ".git" {
		root = filepath.Dir(common)
	}
	c, err := gitclient.New(ctx, root, t.opts...)
	if err != nil {
		slog.Warn("worktree: prune after removal: cannot open the repository", "worktree", path, "repo", root, "err", err)
		return
	}
	if err := c.PruneWorktrees(ctx); err != nil {
		slog.Warn("worktree: prune after removal failed", "worktree", path, "err", err)
	}
}

func (t *throttledClient) stopFsmonitor(ctx context.Context, path string) {
	if t.r.StopFsmonitor != nil {
		t.r.StopFsmonitor(ctx, path)
		return
	}
	StopFsmonitorDaemon(ctx, path)
}

// StopFsmonitorDaemon runs `git -C path fsmonitor--daemon stop`, best effort
// (a worktree with no running daemon exits non-zero; that is not a failure).
// It stops only this worktree's daemon and leaves core.fsmonitor set.
func StopFsmonitorDaemon(ctx context.Context, path string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "fsmonitor--daemon", "stop")
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") || strings.HasPrefix(kv, "GIT_INDEX_FILE=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	_ = cmd.Run()
}
