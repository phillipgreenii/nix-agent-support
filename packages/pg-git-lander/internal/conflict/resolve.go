package conflict

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/rebase"
)

// Details of a resolve-failed stop (design section 6.3).
const (
	DetailResolverFailed = "resolver-failed"
	DetailVerifyFailed   = "verify-failed"
	DetailMarkersLeft    = "markers-left"
)

// Result words of a resolution entry.
const (
	ResultResolved = "resolved"
	ResultMerged   = "merged"
)

// ExitTransient is the exit code of a resolver that asks for a later retry.
const ExitTransient = hooks.ExitTempFail

// Stop is a conflict loop that ended in a stop.
type Stop struct {
	Reason outcome.StopReason
	// Detail is set for resolve-failed.
	Detail string
	// Paths are the paths the stop concerns.
	Paths []string
	// Rule is the id of the rule that failed, when one did.
	Rule    string
	LogTail string
}

// Result is the end of the conflict loop. Exactly one of Finished and Stop
// is set.
type Result struct {
	Finished bool
	Stop     *Stop
	// Rounds is the updated conflict_rounds count.
	Rounds int
	// Resolved lists the resolutions made (including merge-driver merges).
	Resolved []outcome.Resolution
	// Dropped lists the replayed commits an empty resolution dropped.
	Dropped []string
}

// Resolver runs the conflict loop.
type Resolver struct {
	Git    *gitexec.Git
	Hooks  *hooks.Runner
	Rebase *rebase.Rebaser
	Config *config.Config
	// Facts is the operation's command environment facts.
	Facts hooks.Facts
}

// Input is one call of the loop.
type Input struct {
	// Dir is the work worktree, with git's rebase stopped on a conflict.
	Dir string
	// Rounds is the number of conflict stops so far (kept across --continue).
	Rounds int
	// AutoMerged lists the paths git auto-merged before the first stop.
	AutoMerged []string
	// Diag holds the merge-driver diagnostics (may be nil).
	Diag *rebase.Diagnostics
}

// Resolve resolves the conflict stop in in.Dir and keeps going until the
// rebase finishes or a stop.
func (r *Resolver) Resolve(ctx context.Context, in Input) (*Result, error) {
	return r.loop(ctx, in, &Result{Rounds: in.Rounds}, in.AutoMerged)
}

// Continue resumes after a person handled the conflict stop by hand (design
// section 7.3). The paths are resolved and staged: the tool commits the
// resolution with the replayed commit's message (or, when the resolution left
// the replayed commit empty, skips it and lists it in Dropped), continues git's
// rebase and goes on with the conflict loop. With skip the replayed commit is
// dropped without looking at the index (--skip).
func (r *Resolver) Continue(ctx context.Context, in Input, skip bool) (*Result, error) {
	res := &Result{Rounds: in.Rounds}
	commit, err := r.replayed(ctx, in.Dir)
	if err != nil {
		return nil, err
	}
	var out string
	var stop *Stop
	if skip {
		res.Dropped = append(res.Dropped, commit)
		out, stop, err = r.finishStep(ctx, in.Dir, r.Git.Run(ctx, in.Dir, "rebase", "--skip"))
	} else if hand, herr := r.HandCommitted(ctx, in.Dir); herr == nil && hand {
		out, stop, err = r.finishStep(ctx, in.Dir, r.Git.Run(ctx, in.Dir, "rebase", "--continue"))
	} else {
		out, stop, err = r.commitAndContinue(ctx, in.Dir, commit, res)
	}
	if err != nil {
		return nil, err
	}
	if stop != nil {
		res.Stop = stop
		return res, nil
	}
	return r.loop(ctx, in, res, rebase.AutoMergedPaths(out))
}

// HandCommitted reports whether the caller already committed the resolution
// of the replayed commit by hand: the index equals HEAD and HEAD carries the
// replayed commit's subject (a resolution that left the commit empty leaves
// HEAD at the previous commit).
func (r *Resolver) HandCommitted(ctx context.Context, dir string) (bool, error) {
	commit, err := r.replayed(ctx, dir)
	if err != nil {
		return false, err
	}
	if empty := r.Git.Run(ctx, dir, "diff", "--cached", "--quiet"); !empty.OK() {
		return false, nil
	}
	head := r.Git.Run(ctx, dir, "log", "-1", "--format=%an%x00%at%x00%s", "HEAD")
	rep := r.Git.Run(ctx, dir, "log", "-1", "--format=%an%x00%at%x00%s", commit)
	return head.OK() && rep.OK() && string(head.Stdout) == string(rep.Stdout), nil
}

func (r *Resolver) loop(ctx context.Context, in Input, res *Result, pending []string) (*Result, error) {
	diag := in.Diag
	if diag == nil {
		diag = &rebase.Diagnostics{Drivers: map[string]string{}, Attrs: map[string]string{}}
	}
	for {
		paths, err := r.Rebase.Unmerged(ctx, in.Dir)
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			// Nothing conflicted (a stop that is not a conflict).
			ok, err := r.Rebase.InProgress(ctx, in.Dir)
			if err != nil {
				return nil, err
			}
			if !ok {
				res.Finished = true
				return res, nil
			}
			return nil, errors.New("conflict: rebase in progress with no conflicted path")
		}
		res.Rounds++
		if res.Rounds > r.Config.MaxConflictRounds {
			res.Stop = &Stop{Reason: outcome.StopMaxConflictRounds, Paths: paths}
			return res, nil
		}
		commit, err := r.replayed(ctx, in.Dir)
		if err != nil {
			return nil, err
		}
		// Design section 9.4: every conflicted path is checked for the merge
		// attribute too, so an attribute naming an undefined driver is found.
		var unchecked []string
		for _, p := range paths {
			if _, ok := diag.Attrs[p]; !ok {
				unchecked = append(unchecked, p)
			}
		}
		if err := r.Rebase.CheckAttrs(ctx, in.Dir, diag, unchecked); err != nil {
			return nil, err
		}
		if err := r.driverEntries(ctx, in.Dir, diag, pending, commit, res); err != nil {
			return nil, err
		}
		matches, none := MatchRules(r.Config, paths)
		if len(none) > 0 {
			res.Stop = &Stop{Reason: outcome.StopConflict, Paths: paths}
			return res, nil
		}
		stages, err := r.stages(ctx, in.Dir)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			stop, err := r.runRule(ctx, in.Dir, commit, m, stages, res)
			if err != nil {
				return nil, err
			}
			if stop != nil {
				res.Stop = stop
				return res, nil
			}
		}
		out, stop, err := r.commitAndContinue(ctx, in.Dir, commit, res)
		if err != nil {
			return nil, err
		}
		if stop != nil {
			res.Stop = stop
			return res, nil
		}
		pending = rebase.AutoMergedPaths(out)
	}
}

func (r *Resolver) replayed(ctx context.Context, dir string) (string, error) {
	g := r.Git.Run(ctx, dir, "rev-parse", "--verify", "REBASE_HEAD")
	if !g.OK() {
		return "", fmt.Errorf("rev-parse REBASE_HEAD: exit %d: %s", g.Code, strings.TrimSpace(string(g.Stderr)))
	}
	return strings.TrimSpace(string(g.Stdout)), nil
}

// driverEntries records the paths a named merge driver merged.
func (r *Resolver) driverEntries(ctx context.Context, dir string, d *rebase.Diagnostics, pending []string, commit string, res *Result) error {
	if len(pending) == 0 {
		return nil
	}
	var unknown []string
	for _, p := range pending {
		if _, ok := d.Attrs[p]; !ok {
			unknown = append(unknown, p)
		}
	}
	if len(unknown) > 0 {
		if err := r.Rebase.CheckAttrs(ctx, dir, d, unknown); err != nil {
			return err
		}
	}
	for _, p := range pending {
		if name := d.Attrs[p]; name != "" {
			if _, ok := d.Drivers[name]; ok {
				res.Resolved = append(res.Resolved, outcome.Resolution{Commit: commit, Path: p, Driver: name, Result: ResultMerged})
			}
		}
	}
	return nil
}

type stageSet struct{ base, ours, theirs string } // blob ids, empty when absent

// stages reads the index's conflict stages: 1 base, 2 onto side, 3 replayed.
func (r *Resolver) stages(ctx context.Context, dir string) (map[string]stageSet, error) {
	g := r.Git.Run(ctx, dir, "ls-files", "-u", "-z")
	if !g.OK() {
		return nil, fmt.Errorf("ls-files -u: exit %d: %s", g.Code, strings.TrimSpace(string(g.Stderr)))
	}
	out := map[string]stageSet{}
	for _, rec := range strings.Split(string(g.Stdout), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 3 {
			continue
		}
		s := out[p]
		switch f[2] {
		case "1":
			s.base = f[1]
		case "2":
			s.ours = f[1]
		case "3":
			s.theirs = f[1]
		}
		out[p] = s
	}
	return out, nil
}

// unit is one resolver invocation.
type unit struct {
	paths []string // the matched paths it covers
	path  string   // file mode
	dir   string   // dir mode (repo-relative, "." for the root)
	cwd   string
}

func units(dir string, m Match) []unit {
	var us []unit
	if m.Rule.RunPer == "dir" {
		by := map[string][]string{}
		var order []string
		for _, p := range m.Paths {
			d := path.Dir(p)
			if _, ok := by[d]; !ok {
				order = append(order, d)
			}
			by[d] = append(by[d], p)
		}
		sort.Strings(order)
		for _, d := range order {
			us = append(us, unit{paths: by[d], dir: d, cwd: filepath.Join(dir, filepath.FromSlash(d))})
		}
		return us
	}
	for _, p := range m.Paths {
		us = append(us, unit{paths: []string{p}, path: p, cwd: dir})
	}
	return us
}

func (r *Resolver) runRule(ctx context.Context, dir, commit string, m Match, st map[string]stageSet, res *Result) (*Stop, error) {
	for _, u := range units(dir, m) {
		stop, err := r.runUnit(ctx, dir, commit, m.Rule, u, st, res)
		if err != nil || stop != nil {
			return stop, err
		}
	}
	return nil, nil
}

func (r *Resolver) runUnit(ctx context.Context, dir, commit string, rule *config.Rule, u unit, st map[string]stageSet, res *Result) (*Stop, error) {
	tmp, err := os.MkdirTemp("", "pg-git-lander-conflict-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	extra := map[string]string{
		hooks.EnvPrefix + "REPLAYED_COMMIT": commit,
		hooks.EnvPrefix + "RULE":            rule.ID,
	}
	if rule.RunPer == "dir" {
		extra[hooks.EnvPrefix+"DIR"] = u.dir
	} else {
		extra[hooks.EnvPrefix+"PATH"] = u.path
		s := st[u.path]
		for name, oid := range map[string]string{"BASE_FILE": s.base, "ONTO_FILE": s.ours, "BRANCH_FILE": s.theirs} {
			f := filepath.Join(tmp, name)
			var content []byte
			if oid != "" {
				g := r.Git.Run(ctx, dir, "cat-file", "blob", oid)
				if !g.OK() {
					return nil, fmt.Errorf("cat-file blob %s: exit %d", oid, g.Code)
				}
				content = g.Stdout
			}
			if err := os.WriteFile(f, content, 0o600); err != nil {
				return nil, err
			}
			extra[hooks.EnvPrefix+name] = f
		}
	}
	vars := r.Facts.Vars("conflict")
	for k, v := range extra {
		vars[k] = v
	}
	env, _ := hooks.BuildEnv(r.Git.Env.Environ(), vars, nil)

	fail, code, tail, err := r.exec(ctx, rule.Resolve, u.cwd, env, rule.Timeout)
	if err != nil {
		return nil, err
	}
	if stop := r.classify(fail, code, tail, DetailResolverFailed, rule, u); stop != nil {
		return stop, nil
	}

	for _, p := range u.paths {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err == nil && HasMarkers(b) {
			return &Stop{Reason: outcome.StopResolveFailed, Detail: DetailMarkersLeft, Paths: []string{p}, Rule: rule.ID}, nil
		}
	}
	for _, p := range u.paths {
		var g gitexec.Result
		if _, serr := os.Lstat(filepath.Join(dir, filepath.FromSlash(p))); serr == nil {
			g = r.Git.Run(ctx, dir, "add", "--", p)
		} else {
			g = r.Git.Run(ctx, dir, "rm", "-q", "--", p)
		}
		if !g.OK() {
			return nil, fmt.Errorf("git stage %s: exit %d: %s", p, g.Code, strings.TrimSpace(string(g.Stderr)))
		}
	}
	if len(rule.Verify) > 0 {
		fail, code, tail, err := r.exec(ctx, rule.Verify, u.cwd, env, rule.Timeout)
		if err != nil {
			return nil, err
		}
		if stop := r.classify(fail, code, tail, DetailVerifyFailed, rule, u); stop != nil {
			return stop, nil
		}
	}
	for _, p := range u.paths {
		res.Resolved = append(res.Resolved, outcome.Resolution{Commit: commit, Path: p, Rule: rule.ID, Result: ResultResolved})
	}
	return nil, nil
}

type failKind int

const (
	ok failKind = iota
	exited
	timedOut
)

// exec runs argv under timeout in its own process group.
func (r *Resolver) exec(ctx context.Context, argv []string, cwd string, env []string, timeout time.Duration) (failKind, int, string, error) {
	if len(argv) == 0 {
		return exited, hooks.ExitNotFound, "empty command", nil
	}
	if timeout <= 0 {
		timeout = hooks.DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cr := r.Git.Runner(cctx, cmdrun.Call{
		Name: argv[0], Args: argv[1:], Dir: cwd,
		Env: env, EnvMode: cmdrun.EnvExact, OwnProcGroup: true,
	})
	if ctx.Err() != nil {
		return ok, 0, "", ctx.Err()
	}
	tail := hooks.LogTail(string(cr.Stdout), string(cr.Stderr))
	switch {
	case errors.Is(cr.CtxErr, context.DeadlineExceeded):
		return timedOut, cr.Code, tail, nil
	case cr.StartErr != nil:
		return exited, hooks.ExitNotFound, tail + cr.StartErr.Error(), nil
	case cr.OK():
		return ok, 0, tail, nil
	}
	return exited, cr.Code, tail, nil
}

func (r *Resolver) classify(k failKind, code int, tail, detail string, rule *config.Rule, u unit) *Stop {
	paths := u.paths
	switch {
	case k == ok:
		return nil
	case k == timedOut:
		return &Stop{Reason: outcome.StopTimeout, Paths: paths, Rule: rule.ID, LogTail: tail}
	case code == hooks.ExitTempFail && detail == DetailResolverFailed:
		return &Stop{Reason: outcome.StopTransient, Paths: paths, Rule: rule.ID, LogTail: tail}
	}
	return &Stop{Reason: outcome.StopResolveFailed, Detail: detail, Paths: paths, Rule: rule.ID, LogTail: tail}
}

// commitAndContinue commits the resolution (or skips the replayed commit when
// the resolution left it empty) and continues the rebase. It returns git's
// output of the continuing command.
func (r *Resolver) commitAndContinue(ctx context.Context, dir, commit string, res *Result) (string, *Stop, error) {
	empty := r.Git.Run(ctx, dir, "diff", "--cached", "--quiet")
	var g gitexec.Result
	switch {
	case empty.OK():
		res.Dropped = append(res.Dropped, commit)
		g = r.Git.Run(ctx, dir, "rebase", "--skip")
	case empty.Code == 1:
		cr := r.Hooks.Commit(ctx, hooks.CommitSpec{Dir: dir, Args: []string{"-C", commit}})
		if cr.Err != nil {
			return "", nil, cr.Err
		}
		if cr.Stop != nil {
			return "", &Stop{Reason: *cr.Stop, LogTail: cr.LogTail}, nil
		}
		g = r.Git.Run(ctx, dir, "rebase", "--continue")
	default:
		return "", nil, fmt.Errorf("git diff --cached: exit %d: %s", empty.Code, strings.TrimSpace(string(empty.Stderr)))
	}
	return r.finishStep(ctx, dir, g)
}

// finishStep classifies the result of the git rebase --continue or --skip that
// ended a conflict round.
func (r *Resolver) finishStep(ctx context.Context, dir string, g gitexec.Result) (string, *Stop, error) {
	out := string(g.Stdout) + string(g.Stderr)
	if g.TimedOut {
		return out, &Stop{Reason: outcome.StopGitError, LogTail: "git rebase timed out"}, nil
	}
	if g.OK() {
		return out, nil, nil
	}
	// A further conflict: the next round handles it.
	if in, err := r.Rebase.InProgress(ctx, dir); err == nil && in {
		if p, err := r.Rebase.Unmerged(ctx, dir); err == nil && len(p) > 0 {
			return out, nil, nil
		}
	}
	return out, &Stop{Reason: outcome.StopGitError, LogTail: rebase.StripHints(out)}, nil
}
