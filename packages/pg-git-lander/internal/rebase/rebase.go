// Package rebase holds the rebase mechanics of the pipeline: the path decision
// from the snapshot tips, running git rebase on the work branch in the work
// worktree, telling a finished rebase from a conflict stop or another git
// stop by the worktree's rebase state, and the merge-driver diagnostics.
package rebase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Path is the decision of step 7.
type Path string

// Path values.
const (
	// NothingToLand: TARGET already contains BRANCH (land), or BRANCH already
	// contains ONTO (rebase, reported as up to date).
	NothingToLand Path = "nothing-to-land"
	UpToDate      Path = "up-to-date"
	// FastForward: BRANCH contains TARGET, so the rebase is skipped (land).
	FastForward Path = "fast-forward"
	// NeedsRebase: git rebase must run.
	NeedsRebase Path = "rebase"
)

// Rebaser runs git for the rebase mechanics.
type Rebaser struct {
	Git *gitexec.Git
}

// contains reports whether tip a contains tip b (b is an ancestor of a).
func (r *Rebaser) contains(ctx context.Context, dir, a, b string) (bool, error) {
	res := r.Git.Run(ctx, dir, "merge-base", "--is-ancestor", b, a)
	switch {
	case res.TimedOut:
		return false, fmt.Errorf("merge-base --is-ancestor: %w", gitexec.ErrTimeout)
	case res.OK():
		return true, nil
	case res.Code == 1:
		return false, nil
	}
	return false, fmt.Errorf("merge-base --is-ancestor %s %s: exit %d: %s%v", b, a, res.Code, strings.TrimSpace(string(res.Stderr)), res.StartErr)
}

// DecideLand decides the path of a land from the snapshot tips.
func (r *Rebaser) DecideLand(ctx context.Context, dir, branchTip, targetTip string) (Path, error) {
	if ok, err := r.contains(ctx, dir, targetTip, branchTip); err != nil || ok {
		if err != nil {
			return "", err
		}
		return NothingToLand, nil
	}
	if ok, err := r.contains(ctx, dir, branchTip, targetTip); err != nil || ok {
		if err != nil {
			return "", err
		}
		return FastForward, nil
	}
	return NeedsRebase, nil
}

// DecideRebase decides the path of a rebase of branchTip onto onto.
func (r *Rebaser) DecideRebase(ctx context.Context, dir, branchTip, onto string) (Path, error) {
	ok, err := r.contains(ctx, dir, branchTip, onto)
	if err != nil {
		return "", err
	}
	if ok {
		return UpToDate, nil
	}
	return NeedsRebase, nil
}

// InProgress reports whether git's rebase is in progress in the worktree at
// dir, read from the rebase state directories, never from the work branch ref.
func (r *Rebaser) InProgress(ctx context.Context, dir string) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		res := r.Git.Run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", name)
		if !res.OK() {
			return false, fmt.Errorf("rev-parse --git-path %s: exit %d: %s%v", name, res.Code, strings.TrimSpace(string(res.Stderr)), res.StartErr)
		}
		if st, err := os.Stat(strings.TrimSpace(string(res.Stdout))); err == nil && st.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// Result is the outcome of Rebase. Exactly one of Finished, Conflict and Stop
// describes it.
type Result struct {
	// Finished is true when the rebase completed.
	Finished bool
	// Tip is the work branch's tip after a finished rebase.
	Tip string
	// Replayed lists the commits (new ids) now on the work branch above base.
	Replayed []string
	// Dropped lists the old commits dropped as already upstream.
	Dropped []string
	// BaseMoved is true when at least one commit was replayed onto a base that
	// differs from the branch's merge base (the after_rebase trigger).
	BaseMoved bool
	// AutoMerged lists paths git reported as auto-merged.
	AutoMerged []string

	// Conflict holds the conflicted paths when the rebase stopped on a
	// conflict.
	Conflict []string
	// Stop is set for any other reason a rebase stopped: reason git-error.
	Stop *Stop
}

// Stop is a rebase that ended in a stop other than a conflict.
type Stop struct {
	Reason  outcome.StopReason
	LogTail string
}

// Rebase runs git rebase base on the work branch checked out in dir.
func (r *Rebaser) Rebase(ctx context.Context, dir, base string) (*Result, error) {
	oldTip, err := r.revParse(ctx, dir, "HEAD")
	if err != nil {
		return nil, err
	}
	baseOid, err := r.revParse(ctx, dir, base+"^{commit}")
	if err != nil {
		return nil, err
	}
	mb, err := r.git(ctx, dir, "merge-base", base, oldTip)
	if err != nil {
		return nil, err
	}
	mb = strings.TrimSpace(mb)
	dropped, err := r.upstreamEquivalent(ctx, dir, base, oldTip)
	if err != nil {
		return nil, err
	}

	res := r.Git.Run(ctx, dir, "rebase", base)
	out := string(res.Stdout) + string(res.Stderr)
	auto := AutoMergedPaths(out)
	if both, err := r.touchedBothSides(ctx, dir, mb, base, oldTip); err == nil {
		auto = mergeUnique(auto, both)
	}
	if res.OK() && !res.TimedOut {
		tip, err := r.revParse(ctx, dir, "HEAD")
		if err != nil {
			return nil, err
		}
		list, err := r.git(ctx, dir, "rev-list", "--reverse", baseOid+".."+tip)
		if err != nil {
			return nil, err
		}
		replayed := fields(list)
		return &Result{
			Finished: true, Tip: tip, Replayed: replayed, Dropped: dropped,
			BaseMoved: len(replayed) > 0 && mb != baseOid, AutoMerged: auto,
		}, nil
	}
	if res.TimedOut {
		return &Result{Stop: &Stop{Reason: outcome.StopGitError, LogTail: "git rebase timed out"}, AutoMerged: auto}, nil
	}
	in, err := r.InProgress(ctx, dir)
	if err != nil {
		return nil, err
	}
	if in {
		paths, err := r.Unmerged(ctx, dir)
		if err != nil {
			return nil, err
		}
		if len(paths) > 0 {
			return &Result{Conflict: paths, AutoMerged: without(auto, paths)}, nil
		}
	}
	return &Result{Stop: &Stop{Reason: outcome.StopGitError, LogTail: StripHints(out)}, AutoMerged: auto}, nil
}

// touchedBothSides lists the paths changed both on the base side and on the
// branch side since their merge base: git merges those content-wise, and
// reports them as auto-merged only when it prints no conflict for them. A
// non-conflicting rebase prints no Auto-merging lines, so this is what lets a
// finished rebase name them.
func (r *Rebaser) touchedBothSides(ctx context.Context, dir, mb, base, tip string) ([]string, error) {
	a, err := r.git(ctx, dir, "diff", "--name-only", mb, base)
	if err != nil {
		return nil, err
	}
	b, err := r.git(ctx, dir, "diff", "--name-only", mb, tip)
	if err != nil {
		return nil, err
	}
	inA := map[string]bool{}
	for _, p := range fields(a) {
		inA[p] = true
	}
	var both []string
	for _, p := range fields(b) {
		if inA[p] {
			both = append(both, p)
		}
	}
	return both, nil
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append(append([]string{}, a...), b...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func without(paths, drop []string) []string {
	d := map[string]bool{}
	for _, p := range drop {
		d[p] = true
	}
	var out []string
	for _, p := range paths {
		if !d[p] {
			out = append(out, p)
		}
	}
	return out
}

// Unmerged returns the conflicted paths of the worktree at dir.
func (r *Rebaser) Unmerged(ctx context.Context, dir string) ([]string, error) {
	out, err := r.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	return fields(out), nil
}

// upstreamEquivalent lists the commits of tip that git cherry reports as
// already present in base (patch-equivalent), which a rebase drops.
func (r *Rebaser) upstreamEquivalent(ctx context.Context, dir, base, tip string) ([]string, error) {
	out, err := r.git(ctx, dir, "cherry", base, tip)
	if err != nil {
		return nil, err
	}
	var dropped []string
	for _, l := range strings.Split(out, "\n") {
		if oid, ok := strings.CutPrefix(l, "- "); ok {
			dropped = append(dropped, strings.TrimSpace(oid))
		}
	}
	return dropped, nil
}

func (r *Rebaser) revParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := r.git(ctx, dir, "rev-parse", "--verify", rev)
	return strings.TrimSpace(out), err
}

func (r *Rebaser) git(ctx context.Context, dir string, args ...string) (string, error) {
	res := r.Git.Run(ctx, dir, args...)
	switch {
	case res.TimedOut:
		return "", fmt.Errorf("git %s: %w", args[0], gitexec.ErrTimeout)
	case !res.OK():
		return "", fmt.Errorf("git %s: exit %d: %s%v", args[0], res.Code, strings.TrimSpace(string(res.Stderr)), res.StartErr)
	}
	return string(res.Stdout), nil
}

func fields(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// StripHints removes the hint: lines git prints from a message.
func StripHints(s string) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.HasPrefix(l, "hint:") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// AutoMergedPaths extracts the paths of "Auto-merging <path>" lines from git's
// rebase output, in order and without duplicates.
func AutoMergedPaths(out string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "Auto-merging "); ok && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// builtinDrivers are the merge attribute values git knows without a
// merge.<name>.driver setting.
var builtinDrivers = map[string]bool{"text": true, "binary": true, "union": true}

// Diagnostics is the step-4 and conflict-time merge-driver report.
type Diagnostics struct {
	// Lines are the settings lines (core.attributesFile, core.hooksPath and
	// every merge.*.driver) and the check-attr lines, ready to log.
	Lines []string
	// Drivers maps each defined merge.<name>.driver to its command.
	Drivers map[string]string
	// Attrs maps a checked path to its merge attribute value.
	Attrs map[string]string
	// Undefined lists the driver names an attribute names that have no
	// merge.<name>.driver, sorted.
	Undefined []string
}

// Warnings returns the warning words the diagnostics carry.
func (d *Diagnostics) Warnings() []outcome.Warning {
	if len(d.Undefined) > 0 {
		return []outcome.Warning{outcome.WarnUndefinedMergeDriver}
	}
	return nil
}

// Settings returns the step-4 settings lines: core.attributesFile,
// core.hooksPath and every merge.*.driver git resolves, in dir.
func (r *Rebaser) Settings(ctx context.Context, dir string) (*Diagnostics, error) {
	d := &Diagnostics{Drivers: map[string]string{}, Attrs: map[string]string{}}
	for _, key := range []string{"core.attributesFile", "core.hooksPath"} {
		res := r.Git.Run(ctx, dir, "config", "--get", key)
		if res.TimedOut {
			return nil, fmt.Errorf("git config: %w", gitexec.ErrTimeout)
		}
		val := strings.TrimSpace(string(res.Stdout))
		if !res.OK() || val == "" {
			val = "(unset)"
		}
		d.Lines = append(d.Lines, key+"="+val)
	}
	res := r.Git.Run(ctx, dir, "config", "--get-regexp", `^merge\..*\.driver$`)
	if res.TimedOut {
		return nil, fmt.Errorf("git config: %w", gitexec.ErrTimeout)
	}
	if !res.OK() && res.Code != 1 {
		return nil, fmt.Errorf("git config --get-regexp: exit %d: %s", res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	for _, l := range fields(string(res.Stdout)) {
		key, val, _ := strings.Cut(l, " ")
		name := strings.TrimSuffix(strings.TrimPrefix(key, "merge."), ".driver")
		d.Drivers[name] = val
		d.Lines = append(d.Lines, key+"="+val)
	}
	return d, nil
}

// CheckAttrs runs git check-attr merge for every path and adds the lines, the
// attribute values and any undefined drivers to d.
func (r *Rebaser) CheckAttrs(ctx context.Context, dir string, d *Diagnostics, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"check-attr", "-z", "merge", "--"}, paths...)
	out, err := r.git(ctx, dir, args...)
	if err != nil {
		return err
	}
	parts := strings.Split(out, "\x00")
	undef := map[string]bool{}
	for _, n := range d.Undefined {
		undef[n] = true
	}
	for i := 0; i+2 < len(parts); i += 3 {
		path, val := filepath.ToSlash(parts[i]), parts[i+2]
		d.Attrs[path] = val
		d.Lines = append(d.Lines, fmt.Sprintf("check-attr merge -- %s: %s", path, val))
		switch val {
		case "set", "unset", "unspecified":
		default:
			if _, ok := d.Drivers[val]; !ok && !builtinDrivers[val] {
				undef[val] = true
			}
		}
	}
	d.Undefined = d.Undefined[:0]
	for n := range undef {
		d.Undefined = append(d.Undefined, n)
	}
	sort.Strings(d.Undefined)
	return nil
}
