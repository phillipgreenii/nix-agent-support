package oplog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
)

const thisHost = "laptop.local"

type fx struct {
	t      *testing.T
	repo   *gitfixture.Repo
	clock  *seamstest.Clock
	layer  *locks.Layer
	common string
	ev     *Events
	live   *seamstest.Liveness
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
	)
	f := &fx{t: t, repo: repo, clock: seamstest.NewClock(time.Now()), live: seamstest.NewLiveness()}
	f.layer = &locks.Layer{
		Git:    &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)},
		Clock:  f.clock,
		Dir:    repo.Dir,
		Config: locks.Config{LeaseTTL: 30 * time.Second, Wait: 2 * time.Second},
		Sleep: func(_ context.Context, d time.Duration) error {
			f.clock.Advance(d)
			return nil
		},
		Liveness: f.live,
		Host:     seamstest.Host(thisHost),
	}
	common, err := f.layer.Git.CommonDir(t.Context(), repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	f.common = common
	f.ev = &Events{
		Path: EventsPath(common),
		Common: Common{
			Repo: repo.Dir, Host: thisHost, PID: 4242, ToolVersion: "26.11.10",
			OpID: "op-1", Operation: "land", Branch: "feature/x", Target: "main",
		},
		Clock: f.clock, Locks: f.layer,
		Owner: locks.Owner{Host: thisHost, PID: 4242, PIDStart: 7, Token: "tok", ToolVersion: "26.11.10"},
	}
	return f
}

func (f *fx) lines(path string) []map[string]any {
	f.t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	var out []map[string]any
	sc := bufio.NewScanner(fh)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			f.t.Fatalf("line does not parse: %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func branchRec(branch, op string, pid int) locks.Record {
	return locks.Record{
		Schema: 1, Role: locks.RoleBranch, State: locks.StateRunning, OpID: op, Operation: "land",
		Branch: branch, Target: "main",
		Owner:     locks.Owner{Host: thisHost, PID: pid, PIDStart: 5, Token: "t" + op, ToolVersion: "26.11.10"},
		StartedAt: time.Now().UTC().Truncate(time.Second), Stage: "rebasing",
	}
}

func TestLogHeaderModesAndEntries(t *testing.T) {
	f := newFx(t)
	l, err := Open(f.common, "op-1", "feature/x", "main", f.clock)
	if err != nil {
		t.Fatal(err)
	}
	l.Git([]string{"rebase", "--onto", "main", "a b"}, 1, 1500*time.Millisecond)
	l.Command("resolver", hooks.CommandRecord{Point: "x", Argv0: "res", Code: 0, Result: "pass", AddedVars: []string{"FOO"}, Stdout: "line1\nline2\n", Stderr: "oops"})
	l.LockWait(locks.LockWait{Lock: "refs/x", Result: locks.WaitBusy, Waited: 2 * time.Second})
	l.Conversion(locks.Conversion{Lock: "refs/x", Why: locks.WhyDead})
	l.MergeDriver("driver said hi")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]os.FileMode{LogsDir(f.common): 0o700, l.Path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", p, info.Mode().Perm(), want)
		}
	}
	if filepath.Base(l.Path) != "op-1.log" || filepath.Dir(l.Path) != filepath.Join(f.common, "pg-git-lander", "logs") {
		t.Errorf("path = %s", l.Path)
	}
	b, _ := os.ReadFile(l.Path)
	text := string(b)
	first, _, _ := strings.Cut(text, "\n")
	if !strings.Contains(first, "START op_id=op-1 branch=feature/x target=main") {
		t.Errorf("first line = %q", first)
	}
	for _, want := range []string{
		`GIT rebase --onto main "a b" exit=1 duration=1.5s`,
		"RESOLVER hook x[0] res exit=0 result=pass", "env=FOO", "    | line1", "    | line2", "    | oops",
		"LOCK wait refs/x result=busy", "LOCK converted refs/x why=dead", "MERGE-DRIVER driver said hi",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	for _, ln := range strings.Split(strings.TrimSpace(text), "\n") {
		if !strings.HasPrefix(ln, "    | ") {
			if _, err := time.Parse("2006-01-02T15:04:05.000Z", strings.Fields(ln)[0]); err != nil {
				t.Errorf("entry not timestamped: %q", ln)
			}
		}
	}

	h, err := ParseHeader(l.Path)
	if err != nil || h != (Header{OpID: "op-1", Branch: "feature/x", Target: "main"}) {
		t.Errorf("header = %+v, %v", h, err)
	}
}

func TestFindByBranchPicksNewest(t *testing.T) {
	f := newFx(t)
	for i, id := range []string{"op-a", "op-b", "op-c"} {
		branch := "feature/x"
		if id == "op-c" {
			branch = "feature/other"
		}
		l, err := Open(f.common, id, branch, "main", f.clock)
		if err != nil {
			t.Fatal(err)
		}
		_ = l.Close()
		mt := time.Now().Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(l.Path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := FindByBranch(f.common, "feature/x"); err != nil || got != "op-b" {
		t.Errorf("found %q, %v", got, err)
	}
	if got, _ := FindByBranch(f.common, "nope"); got != "" {
		t.Errorf("found %q", got)
	}
}

func TestEveryEventParsesWithCommonFieldsAndOwnFields(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.ev.OpStart(ctx, map[string]any{"ticket": "X-1"}, []locks.WorktreeEntry{{Admin: "_main", Path: "/r", Roles: []string{"destination"}, Start: locks.Start{Branch: "main"}}}, "work", "placeholder", locks.Keep{SourceBranch: true}, "digest"))
	must(f.ev.Stage(ctx, "rebasing", "locked", 1500*time.Millisecond))
	must(f.ev.LockWait(ctx, locks.LockWait{Lock: "refs/l", Holder: holder("h-op", "rebasing"), Waited: 2 * time.Second, Result: locks.WaitBusy}))
	must(f.ev.RaceRetry(ctx, 2, "aaa", "bbb", "paths", true))
	must(f.ev.Hook(ctx, hooks.CommandRecord{Point: "before", Index: 1, Argv0: "check", Code: 3, Duration: 40 * time.Millisecond, Result: "failed"}))
	must(f.ev.HolderConverted(ctx, locks.Conversion{Lock: "refs/l", PreviousOwner: locks.Owner{Host: "h", PID: 9}, Why: locks.WhyExpired}))
	must(f.ev.Conflict(ctx, []string{"a.txt"}, "union", "resolved"))
	must(f.ev.OpEnd(ctx, OpEnd{Outcome: "landed", Reason: "", Retryable: false, Stage: "done", PathTaken: "fast", Stats: map[string]any{"commits": 2}, BranchFate: "deleted", WorktreeFate: "kept"}))

	got := f.lines(f.ev.Path)
	if len(got) != 8 {
		t.Fatalf("%d lines", len(got))
	}
	common := []string{"ts", "repo", "host", "pid", "tool_version", "op_id", "operation", "branch", "target"}
	wantFields := map[string][]string{
		EvOpStart:         {"context", "worktrees", "work_branch", "placeholder_branch", "keep", "config_digest"},
		EvStage:           {"stage", "previous_stage", "duration_ms"},
		EvLockWait:        {"lock", "holder", "waited_ms", "result"},
		EvRaceRetry:       {"attempt", "old_target_tip", "new_target_tip", "revalidate", "reused"},
		EvHook:            {"point", "index", "argv0", "exit", "duration_ms", "result"},
		EvHolderConverted: {"lock", "previous_owner", "why"},
		EvConflict:        {"paths", "rule", "result"},
		EvOpEnd:           {"outcome", "reason", "retryable", "stage", "path_taken", "stats", "branch_fate", "worktree_fate", "restored", "warnings"},
	}
	seen := map[string]bool{}
	for _, m := range got {
		typ := m["event"].(string)
		seen[typ] = true
		for _, k := range common {
			if v, ok := m[k]; !ok || v == "" || v == nil {
				t.Errorf("%s lacks common field %s", typ, k)
			}
		}
		for _, k := range wantFields[typ] {
			if _, ok := m[k]; !ok {
				t.Errorf("%s lacks field %s: %v", typ, k, m)
			}
		}
		if _, err := time.Parse(time.RFC3339Nano, m["ts"].(string)); err != nil {
			t.Errorf("ts: %v", err)
		}
		if m["repo"] != f.repo.Dir || m["host"] != thisHost || m["pid"] != float64(4242) || m["tool_version"] != "26.11.10" ||
			m["op_id"] != "op-1" || m["operation"] != "land" || m["branch"] != "feature/x" || m["target"] != "main" {
			t.Errorf("%s common values = %v", typ, m)
		}
	}
	if len(seen) != 8 {
		t.Errorf("event types seen = %v", seen)
	}
	if h := got[2]["holder"].(map[string]any); h["op_id"] != "h-op" || h["stage"] != "rebasing" || got[2]["waited_ms"] != float64(2000) || got[2]["result"] != "busy" {
		t.Errorf("lock_wait = %v", got[2])
	}
	if got[1]["duration_ms"] != float64(1500) || got[4]["exit"] != float64(3) || got[5]["why"] != "expired" {
		t.Errorf("values: %v %v %v", got[1], got[4], got[5])
	}
}

func holder(op, stage string) outcome.Holder { return outcome.Holder{OpID: op, Stage: stage} }

func TestEventsFileAndStateDirModes(t *testing.T) {
	f := newFx(t)
	if err := f.ev.Stage(t.Context(), "a", "", 0); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{StateDir(f.common): 0o700, f.ev.Path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", p, info.Mode().Perm(), want)
		}
	}
}

func TestRotationUnderAdminLockWithRacingWriterAppendingToArchive(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	adminRef := locks.AdminRef(locks.AdminEvents)
	heldDuringRename := false
	f.ev.beforeRename = func() {
		st, err := f.layer.Read(ctx, adminRef)
		heldDuringRename = err == nil && !st.Absent && st.Rec != nil && st.Rec.Role == locks.RoleAdmin &&
			st.Rec.State == locks.StateRunning && st.Rec.Owner.PID == 4242
	}
	if err := f.ev.Stage(ctx, "s", "p", 0); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.ev.Path)
	if err != nil {
		t.Fatal(err)
	}
	f.ev.RotateAt = 3*info.Size() - 1 // the check precedes each write: the 4th write rotates
	for i := 0; i < 2; i++ {
		if err := f.ev.Stage(ctx, "s", "p", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(f.ev.Path + ".1"); err == nil {
		t.Fatal("rotated before the threshold")
	}
	// a writer that opened the file before the rename and writes after it
	racer, err := os.OpenFile(f.ev.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer racer.Close()
	for i := 0; i < 3; i++ {
		if err := f.ev.Stage(ctx, "s", "p", 0); err != nil {
			t.Fatal(err)
		}
	}
	if !heldDuringRename {
		t.Error("the admin lock was not held (running, admin role, this owner) at the rename")
	}
	if st, _ := f.layer.Read(ctx, adminRef); !st.Absent {
		t.Error("admin lock not released after rotation")
	}
	archived := f.lines(f.ev.Path + ".1")
	if len(archived) < 3 {
		t.Fatalf("archive has %d lines", len(archived))
	}
	if _, err := racer.WriteString(`{"event":"raced"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	last := f.lines(f.ev.Path + ".1")
	if last[len(last)-1]["event"] != "raced" {
		t.Errorf("the racing writer's line is not in the archive: %v", last[len(last)-1])
	}
	cur := f.lines(f.ev.Path)
	if len(cur) != 3 || len(archived) != 3 {
		t.Errorf("current has %d lines, archive %d", len(cur), len(archived))
	}
	// one archive only: a second rotation replaces it
	for i := 0; i < 8; i++ {
		_ = f.ev.Stage(ctx, "s", "p", 0)
	}
	ents, _ := os.ReadDir(StateDir(f.common))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "events.jsonl,events.jsonl.1" {
		t.Errorf("state dir = %v", names)
	}
}

func TestRotationSkippedWhileAnotherHoldsTheAdminLock(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	f.ev.RotateAt = 300
	other := locks.Record{Owner: locks.Owner{Host: thisHost, PID: 77, PIDStart: 5, Token: "other"}}
	f.live.SetAlive(77, "5")
	set, err := f.layer.Acquire(ctx, locks.Spec{Ref: locks.AdminRef(locks.AdminEvents), Role: locks.RoleAdmin, Rec: other})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Release(ctx)
	for i := 0; i < 6; i++ {
		if err := f.ev.Stage(ctx, "s", "p", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(f.ev.Path + ".1"); err == nil {
		t.Error("rotated although another process holds the admin lock")
	}
	if n := len(f.lines(f.ev.Path)); n != 6 {
		t.Errorf("%d lines kept, want all 6", n)
	}
}

func TestRotationAtDefaultThresholdIs20MB(t *testing.T) {
	if RotateBytes != 20*1024*1024 {
		t.Errorf("RotateBytes = %d", RotateBytes)
	}
	f := newFx(t)
	if err := os.MkdirAll(StateDir(f.common), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.ev.Path, bytes.Repeat([]byte("x\n"), int(RotateBytes/2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ev.Stage(t.Context(), "s", "p", 0); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(f.ev.Path + ".1"); err != nil || info.Size() < RotateBytes {
		t.Fatalf("20 MB file not rotated: %v", err)
	}
	if n := len(f.lines(f.ev.Path)); n != 1 {
		t.Errorf("current file has %d lines", n)
	}
}

func TestHookEnvValuesNeverReachLogOrEvents(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	const secret = "s3cr3t-value-9f2a"
	script := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hook-output-marker\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoDir, _ := filepath.EvalSymlinks(f.repo.Dir)
	r := &hooks.Runner{Git: f.layer.Git, AutofixAttempts: 1, RunGitHooks: true}
	res, err := r.RunPoint(ctx, hooks.PointInput{
		Point: hooks.PointBefore,
		Hooks: []config.Hook{{Run: []string{script}, Timeout: time.Minute, Cwd: "repo", Env: map[string]string{"MY_API_TOKEN": secret}}},
		Facts: hooks.Facts{Operation: "land", OpID: "op-1", Repo: repoDir, GitCommonDir: f.common, WorkWorktree: repoDir, SourceWorktree: repoDir, Branch: "feature/x", Target: "main"},
	})
	if err != nil || len(res.Records) != 1 {
		t.Fatalf("run: %v %+v", err, res)
	}
	l, err := Open(f.common, "op-1", "feature/x", "main", f.clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range res.Records {
		l.Command("hook", rec)
		if err := f.ev.Hook(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()
	logText, _ := os.ReadFile(l.Path)
	evText, _ := os.ReadFile(f.ev.Path)
	if !strings.Contains(string(logText), "MY_API_TOKEN") || !strings.Contains(string(logText), "    | hook-output-marker") {
		t.Errorf("log lacks the variable name or the output:\n%s", logText)
	}
	for name, b := range map[string][]byte{"log": logText, "events": evText} {
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("%s contains the hook env value", name)
		}
	}
	if bytes.Contains(evText, []byte("hook-output-marker")) || bytes.Contains(evText, []byte("MY_API_TOKEN")) {
		t.Errorf("hook event carries output or env: %s", evText)
	}
}

func TestHookOutputNeverGoesIntoALockRecord(t *testing.T) {
	// The writers take no lock handle; a lock record is only written through
	// the lock layer, whose record type has no field for output. Check the
	// stop object and the whole record shape for an output-carrying field.
	b, err := json.Marshal(locks.Record{Role: locks.RoleBranch, Stop: &locks.Stop{Reason: "hook-failed"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"stdout", "stderr", "output", "log_tail"} {
		if bytes.Contains(b, []byte(`"`+bad+`"`)) {
			t.Errorf("lock record has a %q field: %s", bad, b)
		}
	}
}

func TestPruneByAgeSizeAndNamedLogs(t *testing.T) {
	f := newFx(t)
	now := time.Now()
	mk := func(id string, size int, age time.Duration) {
		t.Helper()
		l, err := Open(f.common, id, "b-"+id, "main", f.clock)
		if err != nil {
			t.Fatal(err)
		}
		_ = l.Close()
		if err := os.WriteFile(l.Path, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(l.Path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(id string) bool {
		_, err := os.Stat(LogPath(f.common, id))
		return err == nil
	}

	mk("old", 10, 15*24*time.Hour)
	mk("old-named", 10, 30*24*time.Hour)
	mk("fresh", 10, 13*24*time.Hour)
	removed, err := Prune(f.common, now, map[string]bool{"old-named": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "old" || exists("old") || !exists("old-named") || !exists("fresh") {
		t.Errorf("age: removed %v, old=%v named=%v fresh=%v", removed, exists("old"), exists("old-named"), exists("fresh"))
	}

	// size cap: oldest first, a named log kept although it is the oldest
	mk("s1", 400, 5*time.Hour)
	mk("s2", 400, 4*time.Hour)
	mk("s3", 400, 3*time.Hour)
	removed, err = prune(f.common, now, map[string]bool{"old-named": true, "s1": true}, RetentionAge, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// total: old-named 10 + fresh 10 + 1200 = 1220 > 1000; oldest unprotected first: fresh(13d)... order by mtime
	if !exists("s1") || !exists("old-named") {
		t.Error("a named log was pruned")
	}
	if len(removed) == 0 || removed[0] != "fresh" {
		t.Errorf("not oldest first: %v", removed)
	}
	var total int64
	ents, _ := os.ReadDir(LogsDir(f.common))
	for _, e := range ents {
		info, _ := e.Info()
		total += info.Size()
	}
	if total > 1000 {
		t.Errorf("total %d still over the cap (removed %v)", total, removed)
	}
}

func TestRetentionConstants(t *testing.T) {
	if RetentionAge != 14*24*time.Hour || RetentionBytes != 200*1024*1024 {
		t.Errorf("retention = %v %d", RetentionAge, RetentionBytes)
	}
	dir := t.TempDir()
	if got, err := Prune(dir, time.Now(), nil); err != nil || got != nil {
		t.Errorf("prune without a logs dir: %v %v", got, err)
	}
}

func TestPruneNamedKeepsLogsThatLockRecordsName(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	f.live.SetAlive(4242, "7")
	rec := branchRec("feature/x", "named-op", 4242)
	rec.Owner.PIDStart = 7
	set, err := f.layer.Acquire(ctx, locks.Spec{Ref: locks.BranchRef("feature/x"), Role: locks.RoleBranch, Rec: rec})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Release(ctx)
	for _, id := range []string{"named-op", "unnamed-op"} {
		l, err := Open(f.common, id, "feature/x", "main", f.clock)
		if err != nil {
			t.Fatal(err)
		}
		_ = l.Close()
		old := time.Now().Add(-20 * 24 * time.Hour)
		_ = os.Chtimes(l.Path, old, old)
	}
	removed, err := PruneNamed(ctx, f.layer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "unnamed-op" {
		t.Errorf("removed %v", removed)
	}
	if _, err := os.Stat(LogPath(f.common, "named-op")); err != nil {
		t.Error("the log a lock record names was pruned")
	}
}

func TestAttachEmitsLockWaitAndHolderConverted(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	l, err := Open(f.common, "op-1", "feature/x", "main", f.clock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	Attach(f.layer, l, f.ev)

	// a live holder: waiting ends busy
	f.live.SetAlive(77, "5")
	held, err := f.layer.Acquire(ctx, locks.Spec{Ref: locks.BranchRef("main"), Role: locks.RoleTarget, Rec: branchRec("feature/y", "holder-op", 77).Short(locks.RoleTarget)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.layer.Acquire(ctx, locks.Spec{Ref: locks.BranchRef("main"), Role: locks.RoleTarget, Rec: branchRec("feature/x", "op-1", 4242).Short(locks.RoleTarget)})
	var busy *locks.BusyError
	if !asBusy(err, &busy) {
		t.Fatalf("err = %v", err)
	}
	_ = held

	// the holder dies: the TARGET lock is taken over
	f.live.SetDead(77)
	set, err := f.layer.Acquire(ctx, locks.Spec{Ref: locks.BranchRef("main"), Role: locks.RoleTarget, Rec: branchRec("feature/x", "op-1", 4242).Short(locks.RoleTarget)})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Release(ctx)

	got := f.lines(f.ev.Path)
	var types []string
	for _, m := range got {
		types = append(types, m["event"].(string))
	}
	if strings.Join(types, ",") != "lock_wait,holder_converted" {
		t.Fatalf("events = %v", types)
	}
	if got[0]["result"] != "busy" || got[0]["lock"] != locks.BranchRef("main") || got[0]["holder"].(map[string]any)["op_id"] != "holder-op" || got[0]["waited_ms"] != float64(2000) {
		t.Errorf("lock_wait = %v", got[0])
	}
	if got[1]["why"] != "dead" || got[1]["lock"] != locks.BranchRef("main") || got[1]["previous_owner"].(map[string]any)["pid"] != float64(77) {
		t.Errorf("holder_converted = %v", got[1])
	}
	b, _ := os.ReadFile(l.Path)
	if !strings.Contains(string(b), "LOCK wait") || !strings.Contains(string(b), "LOCK converted") {
		t.Errorf("log lacks lock entries:\n%s", b)
	}
}

func asBusy(err error, target **locks.BusyError) bool {
	b, ok := err.(*locks.BusyError)
	if ok {
		*target = b
	}
	return ok
}
