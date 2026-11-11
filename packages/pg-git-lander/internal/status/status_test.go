package status_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/status"
)

const thisHost = "laptop.local"

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

type fx struct {
	t      *testing.T
	repo   *gitfixture.Repo
	g      *gitexec.Git
	xdg    string
	clock  *seamstest.Clock
	live   *seamstest.Liveness
	common string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	var genv []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(k, "GIT_AUTHOR_") && !strings.HasPrefix(k, "GIT_COMMITTER_") && k != "EMAIL" {
			genv = append(genv, kv)
		}
	}
	genv = append(genv, "HOME="+filepath.Join(root, "home"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"))
	f := &fx{
		t: t, repo: repo, xdg: t.TempDir(), clock: seamstest.NewClock(t0), live: seamstest.NewLiveness(),
		g: &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(genv)},
	}
	f.common = f.git(repo.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return f
}

func (f *fx) git(dir string, args ...string) string {
	f.t.Helper()
	r := f.g.Run(f.t.Context(), dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v in %s: %s", args, dir, r.Stderr)
	}
	return strings.TrimSpace(string(r.Stdout))
}

func (f *fx) input(branch string) status.Input {
	return status.Input{
		Git: f.g, Clock: f.clock, Liveness: f.live, Host: seamstest.Host(thisHost),
		Paths: seamstest.ConfigPaths(f.xdg), Dir: f.repo.Dir, Branch: branch,
	}
}

// plant writes raw as a blob and points ref at it.
func (f *fx) plant(ref string, raw []byte) {
	f.t.Helper()
	r := f.g.RunInput(f.t.Context(), f.repo.Dir, raw, "hash-object", "-w", "--stdin")
	if !r.OK() {
		f.t.Fatalf("hash-object: %s", r.Stderr)
	}
	f.git(f.repo.Dir, "update-ref", ref, strings.TrimSpace(string(r.Stdout)))
}

func (f *fx) plantRecord(ref string, rec locks.Record) {
	f.t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		f.t.Fatal(err)
	}
	f.plant(ref, b)
}

func owner(pid int, host string) locks.Owner {
	return locks.Owner{Host: host, PID: pid, PIDStart: 1, Token: "t", ToolVersion: "test"}
}

func (f *fx) touchOld(path string, age time.Duration) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		f.t.Fatal(err)
	}
	mt := t0.Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		f.t.Fatal(err)
	}
}

// populate builds the clone of the golden test: a running operation, a
// stopped one (with its log), a dead holder, a foreign-host holder, an
// unreadable record, an orphan work branch with a worktree on it, and stale
// .lock files in both namespaces (plus a fresh one that must not be listed).
func (f *fx) populate() {
	f.t.Helper()
	f.live.SetAlive(4242, "1")

	run := locks.Record{
		Schema: 1, Role: locks.RoleBranch, State: locks.StateRunning, OpID: "20261110T115800Z-aaaaaa",
		Operation: "land", Branch: "feature/a", Target: "main", Owner: owner(4242, thisHost),
		Lease:     &locks.Lease{RenewedAt: t0.Add(-5 * time.Second), TTLSec: 30},
		StartedAt: t0.Add(-120 * time.Second), Stage: "rebasing", StageStartedAt: t0.Add(-60 * time.Second),
		WorkBranch: "pg-git-lander/20261110T115800Z-aaaaaa",
		CurrentCommand: &locks.CurrentCommand{
			Argv0: "git", PID: 4300, StartedAt: t0.Add(-10 * time.Second), Deadline: t0.Add(50 * time.Second),
		},
	}
	f.plantRecord(locks.BranchRef("feature/a"), run)
	f.plantRecord(locks.BranchRef("main"), run.Short(locks.RoleTarget))

	stopped := locks.Record{
		Schema: 1, Role: locks.RoleBranch, State: locks.StateStopped, OpID: "20261110T110000Z-bbbbbb",
		Operation: "rebase", Branch: "feature/b", Owner: owner(1, thisHost),
		StartedAt: t0.Add(-3600 * time.Second), Stage: "rebasing", StageStartedAt: t0.Add(-3500 * time.Second),
		Worktrees:  []locks.WorktreeEntry{{Admin: "wt-b", Path: "/work/wt-b", Roles: []string{"work"}}},
		WorkBranch: "pg-git-lander/20261110T110000Z-bbbbbb",
		Context:    map[string]any{"bead": "x-1"},
		Stop: &locks.Stop{
			Reason: "conflict", Retryable: false, At: t0.Add(-3400 * time.Second), Stage: "rebasing",
			Paths: []string{"a.txt"}, Message: "conflict",
		},
	}
	f.plantRecord(locks.BranchRef("feature/b"), stopped)
	lg, err := oplog.Open(f.common, stopped.OpID, "feature/b", "", f.clock)
	if err != nil {
		f.t.Fatal(err)
	}
	lg.Line("NOTE", "stopped on a.txt")
	_ = lg.Close()

	dead := run
	dead.OpID, dead.Branch, dead.WorkBranch, dead.Owner = "20261110T115000Z-dddddd", "feature/d", "pg-git-lander/20261110T115000Z-dddddd", owner(777, thisHost)
	dead.CurrentCommand = nil
	f.plantRecord(locks.BranchRef("feature/d"), dead)

	foreign := run
	foreign.OpID, foreign.Branch, foreign.WorkBranch, foreign.Owner = "20261110T115100Z-ffffff", "feature/f", "pg-git-lander/20261110T115100Z-ffffff", owner(5, "other.example")
	foreign.CurrentCommand = nil
	f.plantRecord(locks.BranchRef("feature/f"), foreign)

	f.plant(locks.BranchRef("feature/c"), []byte("this is not a lock record"))

	f.git(f.repo.Dir, "worktree", "add", "-q", "-b", "pg-git-lander/orphan-1", filepath.Join(f.repo.Root(), "orphan-wt"))
	f.git(f.repo.Dir, "branch", "pg-git-lander/20261110T115800Z-aaaaaa") // named: not an orphan

	f.touchOld(filepath.Join(f.common, "refs", "pg-git-lander", "locks", "heads", "stale.lock"), 120*time.Second)
	f.touchOld(filepath.Join(f.common, "refs", "heads", "stale-branch.lock"), 300*time.Second)
	f.touchOld(filepath.Join(f.common, "refs", "heads", "fresh-branch.lock"), 10*time.Second)
}

// snapshot is every ref and every file under the common dir with its size and
// modification time.
func (f *fx) snapshot() string {
	f.t.Helper()
	var b strings.Builder
	b.WriteString(f.git(f.repo.Dir, "for-each-ref", "--format=%(refname) %(objectname)"))
	var files []string
	_ = filepath.WalkDir(f.common, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, _ := d.Info()
		files = append(files, fmt.Sprintf("%s %d %d", p, info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	sort.Strings(files)
	return b.String() + "\n" + strings.Join(files, "\n")
}

func (f *fx) normalize(s string) string {
	root := f.repo.Root()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		s = strings.ReplaceAll(s, r, "<ROOT>")
	}
	s = strings.ReplaceAll(s, root, "<ROOT>")
	return strings.ReplaceAll(s, f.xdg, "<XDG>")
}

func TestGoldenStatusJSON(t *testing.T) {
	f := newFx(t)
	f.populate()
	rep, err := status.Collect(t.Context(), f.input(""))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(f.normalize(string(raw))), "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden := filepath.Join("testdata", "status.golden.json")
	if os.Getenv("PGL_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	// The formatter may re-wrap the golden file: compare the JSON, not the
	// layout.
	var wantC, gotC bytes.Buffer
	if err := json.Compact(&wantC, want); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&gotC, pretty.Bytes()); err != nil {
		t.Fatal(err)
	}
	if wantC.String() != gotC.String() {
		t.Fatalf("status --json differs from %s (regenerate with PGL_UPDATE_GOLDEN=1):\n%s", golden, pretty.String())
	}
}

func TestStatusChangesNothing(t *testing.T) {
	f := newFx(t)
	f.populate()
	before := f.snapshot()
	for i := 0; i < 2; i++ {
		if _, err := status.Collect(t.Context(), f.input("")); err != nil {
			t.Fatal(err)
		}
	}
	if after := f.snapshot(); after != before {
		t.Fatalf("status changed the repository:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestStatusNeverStartsALeaseClock(t *testing.T) {
	f := newFx(t)
	f.populate()
	for i := 0; i < 2; i++ {
		rep, err := status.Collect(t.Context(), f.input(""))
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range rep.Locks {
			if l.Ref == locks.BranchRef("feature/a") && l.Freshness != "unobserved" {
				t.Fatalf("pass %d: freshness %q, want unobserved", i, l.Freshness)
			}
		}
	}
}

func TestStatusClassifiesEachKindOfLock(t *testing.T) {
	f := newFx(t)
	f.populate()
	rep, err := status.Collect(t.Context(), f.input(""))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]status.Lock{}
	for _, l := range rep.Locks {
		got[l.Ref] = l
	}
	for ref, want := range map[string]string{
		locks.BranchRef("feature/a"): "unobserved", locks.BranchRef("feature/d"): "dead",
		locks.BranchRef("feature/f"): "foreign-host", locks.BranchRef("feature/b"): "",
	} {
		if got[ref].Freshness != want {
			t.Errorf("%s freshness %q, want %q", ref, got[ref].Freshness, want)
		}
	}
	if c := got[locks.BranchRef("feature/c")]; !c.Unreadable || c.Error == "" {
		t.Errorf("unreadable record not reported: %+v", c)
	}
	if a := got[locks.BranchRef("feature/a")]; a.Command == nil || a.Command.Argv0 != "git" || a.AgeS != 120 {
		t.Errorf("running lock: %+v", a)
	}
}

func TestStatusForOneBranch(t *testing.T) {
	f := newFx(t)
	f.populate()
	rep, err := status.Collect(t.Context(), f.input("feature/b"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Locks) != 1 || rep.Locks[0].Branch != "feature/b" || len(rep.Stopped) != 1 {
		t.Fatalf("%+v", rep)
	}
	s := rep.Stopped[0]
	if s.Reason != "conflict" || s.Worktree != "/work/wt-b" || s.Log != "20261110T110000Z-bbbbbb" || s.Context["bead"] != "x-1" {
		t.Fatalf("%+v", s)
	}
	wantNext := []string{
		"pg-git-lander rebase feature/b --continue", "pg-git-lander rebase feature/b --skip",
		"pg-git-lander rebase feature/b --abort", "pg-git-lander rebase feature/b --quit",
	}
	if strings.Join(s.Next, "|") != strings.Join(wantNext, "|") {
		t.Fatalf("next %v", s.Next)
	}
	// An orphan belongs to no branch: still listed.
	if len(rep.Orphans) != 1 {
		t.Fatalf("orphans %+v", rep.Orphans)
	}
}

func TestOrphansAreOnlyBranchesNoRecordNames(t *testing.T) {
	f := newFx(t)
	f.populate()
	rep, err := status.Collect(t.Context(), f.input(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0].Branch != "pg-git-lander/orphan-1" || len(rep.Orphans[0].Worktrees) != 1 ||
		!strings.HasSuffix(rep.Orphans[0].Worktrees[0], "orphan-wt") {
		t.Fatalf("%+v", rep.Orphans)
	}
}

func TestStaleLockFilesInBothNamespacesOnly(t *testing.T) {
	f := newFx(t)
	f.populate()
	rep, err := status.Collect(t.Context(), f.input(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.StaleLock) != 2 {
		t.Fatalf("%+v", rep.StaleLock)
	}
	ns := map[string]int64{}
	for _, s := range rep.StaleLock {
		ns[s.Namespace] = s.AgeS
	}
	if ns["pg-git-lander"] != 120 || ns["heads"] != 300 {
		t.Fatalf("%+v", ns)
	}
}

func TestStatusReportsConfigurationAndTrust(t *testing.T) {
	f := newFx(t)
	repoDir := f.repo.Dir
	toml := "[defaults.lock]\nwait = \"3s\"\n\n[[repo]]\nroot = " + fmt.Sprintf("%q", repoDir) + "\ntrust_repo_config = true\n"
	if err := os.WriteFile(filepath.Join(f.xdg, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := status.Collect(t.Context(), f.input(""))
	if err != nil {
		t.Fatal(err)
	}
	c := rep.Config
	if !c.TrustRepoConfig || c.WaitS != 3 || len(c.Sources) < 2 || !c.Sources[0].Present || c.Error != "" {
		t.Fatalf("%+v", c)
	}
}

func TestNotARepositoryCannotBeRead(t *testing.T) {
	f := newFx(t)
	in := f.input("")
	in.Dir = t.TempDir()
	if _, err := status.Collect(t.Context(), in); !errors.Is(err, status.ErrNotARepo) {
		t.Fatalf("%v", err)
	}
}

func TestLogByOpIDAndByBranchWithTail(t *testing.T) {
	f := newFx(t)
	opID := "20261110T110000Z-bbbbbb"
	lg, err := oplog.Open(f.common, opID, "feature/b", "main", f.clock)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		lg.Line("NOTE", fmt.Sprintf("line %d", i))
	}
	_ = lg.Close()

	for _, arg := range []string{opID, "feature/b"} {
		id, path, err := status.ResolveLog(t.Context(), f.g, f.repo.Dir, arg)
		if err != nil || id != opID {
			t.Fatalf("%s: %q %v", arg, id, err)
		}
		var full bytes.Buffer
		if err := status.PrintLog(&full, path, -1); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(full.String(), "START") || !strings.Contains(full.String(), "line 1") || !strings.Contains(full.String(), "line 5") {
			t.Fatalf("%q", full.String())
		}
		var tail bytes.Buffer
		if err := status.PrintLog(&tail, path, 2); err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(strings.TrimRight(tail.String(), "\n"), "\n"); len(lines) != 2 ||
			!strings.Contains(lines[0], "line 4") || !strings.Contains(lines[1], "line 5") {
			t.Fatalf("tail: %q", tail.String())
		}
	}
	if _, _, err := status.ResolveLog(t.Context(), f.g, f.repo.Dir, "no/such"); !errors.Is(err, status.ErrNoLog) {
		t.Fatalf("%v", err)
	}
	// An op-id-shaped argument that has no log never reaches a path outside
	// the logs directory.
	if _, _, err := status.ResolveLog(t.Context(), f.g, f.repo.Dir, "../../config"); !errors.Is(err, status.ErrNoLog) {
		t.Fatalf("%v", err)
	}
}
