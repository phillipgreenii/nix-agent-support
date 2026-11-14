// Package status implements the read-only status and log subcommands of
// pg-git-lander (design sections 14.3 and 14.4): it lists the lock refs, the
// stopped operations, the orphan work branches, the stale ref .lock files and
// the configuration in effect, and prints an operation's log, so that nobody
// needs to read files under .git/.
package status

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
)

// Schema is the version of the status --json object.
const Schema = 1

// ErrNotARepo means the directory is not inside a git repository.
var ErrNotARepo = errors.New("not inside a git repository")

// Input is everything Collect reads. Nothing in it is written.
type Input struct {
	Git      *gitexec.Git
	Clock    seams.Clock
	Liveness seams.Liveness
	Host     seams.Host
	Paths    seams.ConfigPaths
	// Dir is any directory of the repository.
	Dir string
	// Branch restricts the lock and stopped-operation lists to one BRANCH;
	// empty lists the whole clone. Orphans, stale files and the configuration
	// belong to no branch and are always listed.
	Branch string
}

// Command is a running command of an operation.
type Command struct {
	Argv0    string `json:"argv0"`
	PID      int    `json:"pid"`
	Deadline string `json:"deadline"`
}

// Owner is the process holding a lock.
type Owner struct {
	PID  int    `json:"pid"`
	Host string `json:"host"`
}

// Lock is one lock ref.
type Lock struct {
	Ref            string   `json:"ref"`
	Role           string   `json:"role"`
	Operation      string   `json:"operation"`
	Branch         string   `json:"branch"`
	State          string   `json:"state"`
	OpID           string   `json:"op_id"`
	Stage          string   `json:"stage"`
	StageStartedAt string   `json:"stage_started_at"`
	Command        *Command `json:"command"`
	Owner          *Owner   `json:"owner"`
	AgeS           int64    `json:"age_s"`
	// Freshness is fresh, unobserved, dead or foreign-host for a running
	// record, empty otherwise.
	Freshness  string `json:"freshness"`
	Unreadable bool   `json:"unreadable"`
	Error      string `json:"error"`
}

// Stopped is one stopped operation.
type Stopped struct {
	OpID      string         `json:"op_id"`
	Operation string         `json:"operation"`
	Branch    string         `json:"branch"`
	Target    string         `json:"target"`
	Reason    string         `json:"reason"`
	Stage     string         `json:"stage"`
	Since     string         `json:"since"`
	Worktree  string         `json:"worktree"`
	Context   map[string]any `json:"context"`
	Log       string         `json:"log"`
	Next      []string       `json:"next"`
}

// Orphan is a pg-git-lander/* branch no record names.
type Orphan struct {
	Branch    string   `json:"branch"`
	Worktrees []string `json:"worktrees"`
}

// StaleLock is a ref .lock file left by a killed git.
type StaleLock struct {
	// Namespace is "pg-git-lander" (refs/pg-git-lander/) or "heads"
	// (refs/heads/).
	Namespace string `json:"namespace"`
	Path      string `json:"path"`
	AgeS      int64  `json:"age_s"`
}

// Source is one configuration file in effect.
type Source struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
}

// Config is the configuration in effect.
type Config struct {
	Sources          []Source `json:"sources"`
	Primary          string   `json:"primary"`
	TrustRepoConfig  bool     `json:"trust_repo_config"`
	IgnoreRepoConfig bool     `json:"ignore_repo_config"`
	LeaseTTLS        int      `json:"lease_ttl_s"`
	WaitS            int      `json:"wait_s"`
	// Error is why the layered configuration could not be loaded (for
	// example an untrusted repo file); empty when it loaded.
	Error string `json:"error"`
}

// Report is the status --json object.
type Report struct {
	Schema    int         `json:"schema"`
	Outcome   string      `json:"outcome"`
	Repo      string      `json:"repo"`
	Branch    string      `json:"branch"`
	Locks     []Lock      `json:"locks"`
	Stopped   []Stopped   `json:"stopped"`
	Orphans   []Orphan    `json:"orphans"`
	StaleLock []StaleLock `json:"stale_locks"`
	Config    Config      `json:"config"`
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Collect reads the repository and builds the report. It writes nothing.
func Collect(ctx context.Context, in Input) (*Report, error) {
	common, err := in.Git.CommonDir(ctx, in.Dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotARepo, in.Dir)
	}
	l := &locks.Layer{Git: in.Git, Clock: in.Clock, Dir: in.Dir, Liveness: in.Liveness, Host: in.Host}
	wts, err := in.Git.Worktrees(ctx, in.Dir)
	if err != nil || len(wts) == 0 {
		return nil, fmt.Errorf("listing worktrees: %v", err)
	}
	rep := &Report{
		Schema: Schema, Outcome: string(outcome.Status), Repo: wts[0].Path, Branch: in.Branch,
		Locks: []Lock{}, Stopped: []Stopped{}, Orphans: []Orphan{}, StaleLock: []StaleLock{},
	}
	now := in.Clock.Now()

	r := in.Git.Run(ctx, in.Dir, "for-each-ref", "--format=%(refname)", locks.RefPrefix)
	if !r.OK() {
		return nil, fmt.Errorf("git for-each-ref: %s", strings.TrimSpace(string(r.Stderr)))
	}
	named := map[string]bool{}
	for _, ref := range strings.Fields(string(r.Stdout)) {
		st, err := l.Read(ctx, ref)
		if err != nil {
			return nil, err
		}
		if st.Absent {
			// Released between the listing and this read: not a lock any more.
			continue
		}
		if st.Rec != nil {
			if st.Rec.WorkBranch != "" {
				named[st.Rec.WorkBranch] = true
			}
			if st.Rec.PlaceholderBranch != "" {
				named[st.Rec.PlaceholderBranch] = true
			}
		}
		if in.Branch != "" && (st.Rec == nil || st.Rec.Branch != in.Branch) {
			continue
		}
		rep.Locks = append(rep.Locks, lockOf(l, st, now))
		if s := stoppedOf(common, st.Rec); s != nil {
			rep.Stopped = append(rep.Stopped, *s)
		}
	}
	sort.SliceStable(rep.Locks, func(i, j int) bool { return rep.Locks[i].Ref < rep.Locks[j].Ref })
	sort.SliceStable(rep.Stopped, func(i, j int) bool { return rep.Stopped[i].OpID < rep.Stopped[j].OpID })

	if rep.Orphans, err = orphans(ctx, in, wts, named); err != nil {
		return nil, err
	}
	rep.StaleLock = staleLocks(common, now)
	rep.Config = configOf(ctx, in)
	return rep, nil
}

func lockOf(l *locks.Layer, st locks.ReadState, now time.Time) Lock {
	lk := Lock{Ref: st.Ref}
	if st.Unreadable != nil {
		lk.Unreadable, lk.Error = true, st.Unreadable.Error()
		return lk
	}
	r := st.Rec
	lk.Role, lk.Operation, lk.Branch, lk.State, lk.OpID = string(r.Role), r.Operation, r.Branch, string(r.State), r.OpID
	lk.Stage, lk.StageStartedAt = r.Stage, stamp(r.StageStartedAt)
	if cc := r.CurrentCommand; cc != nil {
		lk.Command = &Command{Argv0: cc.Argv0, PID: cc.PID, Deadline: stamp(cc.Deadline)}
	}
	lk.Owner = &Owner{PID: r.Owner.PID, Host: r.Owner.Host}
	since := r.StartedAt
	if since.IsZero() && r.Lease != nil {
		since = r.Lease.RenewedAt
	}
	if !since.IsZero() {
		lk.AgeS = int64(now.Sub(since).Seconds())
	}
	lk.Freshness = string(l.Classify(st))
	return lk
}

func stoppedOf(common string, r *locks.Record) *Stopped {
	if r == nil || r.Role != locks.RoleBranch || r.State != locks.StateStopped {
		return nil
	}
	s := &Stopped{
		OpID: r.OpID, Operation: r.Operation, Branch: r.Branch, Target: r.Target, Stage: r.Stage,
		Context: r.Context, Next: []string{},
	}
	if s.Context == nil {
		s.Context = map[string]any{}
	}
	flags := []string{outcome.FlagAbort, outcome.FlagQuit}
	if r.Stop != nil {
		s.Reason, s.Since = r.Stop.Reason, stamp(r.Stop.At)
		if info, ok := outcome.Stops[outcome.StopReason(r.Stop.Reason)]; ok {
			flags = info.Next
		}
	}
	for _, w := range r.Worktrees {
		for _, role := range w.Roles {
			if role == outcome.RoleWork {
				s.Worktree = w.Path
			}
		}
	}
	if _, err := os.Stat(filepath.Join(common, "pg-git-lander", "logs", r.OpID+".log")); err == nil {
		s.Log = r.OpID
	}
	s.Next = outcome.NextCommands(r.Operation, r.Branch, "", flags)
	return s
}

func orphans(ctx context.Context, in Input, wts []gitexec.Worktree, named map[string]bool) ([]Orphan, error) {
	r := in.Git.Run(ctx, in.Dir, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads/"+workbranch.Namespace+"/")
	if !r.OK() {
		return nil, fmt.Errorf("git for-each-ref: %s", strings.TrimSpace(string(r.Stderr)))
	}
	out := []Orphan{}
	for _, b := range strings.Fields(string(r.Stdout)) {
		if named[b] {
			continue
		}
		o := Orphan{Branch: b, Worktrees: []string{}}
		for _, w := range wts {
			if w.Branch == "refs/heads/"+b {
				o.Worktrees = append(o.Worktrees, w.Path)
			}
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Branch < out[j].Branch })
	return out, nil
}

// staleLocks lists .lock files older than locks.StaleLockAge under
// refs/pg-git-lander/ and refs/heads/. Neither kind is removed here.
func staleLocks(common string, now time.Time) []StaleLock {
	out := []StaleLock{}
	for _, ns := range []struct{ name, dir string }{
		{"pg-git-lander", filepath.Join(common, "refs", "pg-git-lander")},
		{"heads", filepath.Join(common, "refs", "heads")},
	} {
		_ = filepath.WalkDir(ns.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".lock") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if age := now.Sub(info.ModTime()); age > locks.StaleLockAge {
				out = append(out, StaleLock{Namespace: ns.name, Path: p, AgeS: int64(age.Seconds())})
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func configOf(ctx context.Context, in Input) Config {
	c := Config{Sources: []Source{}}
	l := &config.Loader{Git: in.Git, Paths: in.Paths}
	pre, err := l.LoadXDG(ctx, in.Dir)
	if err != nil {
		c.Error = err.Error()
		c.Sources = append(c.Sources, Source{Kind: "xdg", Path: l.XDGPath()})
		return c
	}
	x := pre.XDGSource()
	c.Sources = append(c.Sources, Source{Kind: x.Kind, Path: x.Path, Present: x.Present})
	c.Primary = pre.ResolvePrimary(ctx)
	c.TrustRepoConfig, c.IgnoreRepoConfig = pre.TrustRepoConfig, pre.IgnoreRepoConfig
	c.LeaseTTLS, c.WaitS = int(pre.Lock.LeaseTTL.Seconds()), int(pre.Lock.Wait.Seconds())
	cfg, err := pre.Load(ctx, pre.RebaseTip(ctx))
	if err != nil {
		c.Error = err.Error()
		return c
	}
	c.Sources = c.Sources[:0]
	for _, s := range cfg.Sources {
		c.Sources = append(c.Sources, Source{Kind: s.Kind, Path: s.Path, Present: s.Present})
	}
	return c
}
