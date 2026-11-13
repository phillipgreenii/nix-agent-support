package outcome

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fieldNames are the top-level fields of design section 14.1.
var fieldNames = []string{
	"schema", "outcome", "exit", "op_id", "operation", "branch", "target", "onto",
	"reason", "detail", "retryable", "stage", "next", "hint", "holder", "blocking_op",
	"paths", "source_worktree", "destination_worktree", "work_worktree", "role", "path",
	"config_error", "work_branch", "placeholder_branch", "branch_fate", "worktree_fate",
	"old_branch_tip", "restored", "tips", "landed_commit", "path_taken", "stats",
	"dropped_commits", "resolved", "autofix", "skipped_hooks", "warnings",
	"moved_already", "config_changed", "log", "log_tail",
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	goldenFile(t, name, got, func(b []byte) string { return string(b) })
}

func goldenFile(t *testing.T, name string, got []byte, norm func([]byte) string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden %s (run with -update): %v", p, err)
	}
	if norm(want) != norm(got) {
		t.Errorf("%s mismatch\nwant:\n%s\ngot:\n%s", p, want, got)
	}
}

func goldenJSON(t *testing.T, name string, o *Outcome) {
	t.Helper()
	raw, err := o.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range fieldNames {
		if _, ok := m[f]; !ok {
			t.Errorf("%s: field %q missing", name, f)
		}
	}
	if string(m["schema"]) != "1" {
		t.Errorf("%s: schema = %s", name, m["schema"])
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	buf.WriteByte('\n')
	goldenFile(t, name+".json", buf.Bytes(), func(b []byte) string {
		// The repo formatter may reflow the golden files, so compare compacted JSON.
		var c bytes.Buffer
		if err := json.Compact(&c, b); err != nil {
			t.Fatal(err)
		}
		return c.String()
	})
}

func TestExitCodes(t *testing.T) {
	want := map[Word]int{
		Landed: 0, Rebased: 0, UpToDate: 0, NothingToLand: 0, Aborted: 0, Quit: 0, Status: 0, Log: 0, ConfigOK: 0,
		InternalError: 1, UsageError: 2, ConfigError: 7, Busy: 9, Stopped: 21, Refused: 22,
	}
	if len(want) != len(AllWords) {
		t.Fatalf("test covers %d words, AllWords has %d", len(want), len(AllWords))
	}
	for w, code := range want {
		if got := ExitCode(w); got != code {
			t.Errorf("ExitCode(%s) = %d, want %d", w, got, code)
		}
		if got := New(w).Exit; got != code {
			t.Errorf("New(%s).Exit = %d, want %d", w, got, code)
		}
	}
	if ExitCode("bogus") != ExitInternal {
		t.Error("unknown word must map to exit 1")
	}
}

func TestReasonTablesComplete(t *testing.T) {
	if len(AllRefusals) != 22 || len(Refusals) != 22 {
		t.Errorf("refusals: %d listed, %d in table, want 22", len(AllRefusals), len(Refusals))
	}
	if len(AllStops) != 16 || len(Stops) != 16 {
		t.Errorf("stops: %d listed, %d in table, want 16", len(AllStops), len(Stops))
	}
	for _, r := range AllRefusals {
		if _, ok := Refusals[r]; !ok {
			t.Errorf("refusal %s missing from table", r)
		}
	}
	for _, r := range AllStops {
		if _, ok := Stops[r]; !ok {
			t.Errorf("stop %s missing from table", r)
		}
	}
}

func TestStopTable(t *testing.T) {
	cases := []struct {
		r         StopReason
		retryable bool
		next      []string
	}{
		{StopConflict, false, []string{"--continue", "--skip", "--abort", "--quit"}},
		{StopResolveFailed, false, []string{"--continue", "--skip", "--abort", "--quit"}},
		{StopMaxConflictRounds, false, []string{"--abort", "--quit"}},
		{StopHookFailed, false, []string{"--continue", "--abort", "--quit"}},
		{StopCommitRejected, false, []string{"--continue", "--abort", "--quit"}},
		{StopTransient, true, []string{"--continue", "--abort"}},
		{StopTimeout, true, []string{"--continue", "--abort"}},
		{StopInterrupted, true, []string{"--continue", "--abort"}},
		{StopRace, true, []string{"--continue", "--abort"}},
		{StopBranchMoved, false, []string{"--abort"}},
		{StopCheckoutChanged, true, []string{"--continue", "--abort"}},
		{StopMoveUnverified, false, []string{"--abort"}},
		{StopLockLost, true, []string{"--continue", "--abort"}},
		{StopHolderDied, true, []string{"--continue", "--abort"}},
		{StopGitError, false, []string{"--abort", "--quit"}},
		{StopInternalError, false, []string{"--abort"}},
	}
	for _, c := range cases {
		i := Stops[c.r]
		if i.Retryable != c.retryable || !reflect.DeepEqual(i.Next, c.next) {
			t.Errorf("%s: got retryable=%v next=%v", c.r, i.Retryable, i.Next)
		}
		if len(i.Stages) == 0 {
			t.Errorf("%s: no stages", c.r)
		}
	}
}

func TestGoldenWords(t *testing.T) {
	for _, w := range AllWords {
		o := New(w)
		o.Operation, o.Branch = "land", "feature/x"
		goldenJSON(t, "word-"+string(w), o)
	}
}

func TestGoldenRefusals(t *testing.T) {
	for _, r := range AllRefusals {
		goldenJSON(t, "refusal-"+string(r), NewRefused(r, "land", "feature/x", ""))
	}
}

func TestGoldenStops(t *testing.T) {
	for _, r := range AllStops {
		goldenJSON(t, "stop-"+string(r), NewStopped(r, "land", "feature/x", Stops[r].Stages[0], ""))
	}
}

func TestRepoInNext(t *testing.T) {
	o := NewStopped(StopBranchMoved, "rebase", "b", StageMoving, "/r")
	want := []string{"pg-git-lander rebase b --abort --repo /r"}
	if !reflect.DeepEqual(o.Next, want) {
		t.Errorf("next = %v", o.Next)
	}
}

func TestJSONSingleLineObject(t *testing.T) {
	raw, _ := New(Landed).JSON()
	if bytes.ContainsRune(raw, '\n') || raw[0] != '{' {
		t.Errorf("not one JSON object line: %s", raw)
	}
}

func landedOutcome() *Outcome {
	o := New(Landed)
	o.Operation, o.Branch, o.Target = "land", "feature/x", "main"
	o.LandedCommit = "9b3f2c1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	o.Stats = &Stats{DurationMS: 6200, RebasedCommits: 3, AutofixCommits: 1}
	return o
}

func busyOutcome() *Outcome {
	return NewBusy("land", "feature/x", "main", Holder{
		OpID: "op1", Operation: "land", Branch: "feature/y", Stage: "before_move", PID: 41730, Host: "h", AgeS: 130,
	})
}

func stoppedOutcome() *Outcome {
	o := NewStopped(StopConflict, "land", "feature/x", StageRebasing, "")
	o.Message = "conflict in docs/guide.md, no conflict rule matches"
	o.WorkWorktree = &Worktree{Name: "feature-x", Path: "/src/example/.worktrees/feature-x"}
	o.WorkBranch = "pg-git-lander/20261107T101500Z-3f9c2a"
	return o
}

func TestHumanExamples(t *testing.T) {
	cases := []struct {
		name string
		o    *Outcome
		want string
	}{
		{"landed", landedOutcome(), "landed feature/x into main at 9b3f2c1 (rebased 3 commits, 1 autofix commit, 6.2 s)"},
		{"busy", busyOutcome(), "busy: main is held by land feature/y (before_move, pid 41730, 2m10s); nothing changed"},
		{"stopped", stoppedOutcome(), strings.Join([]string{
			"stopped: land feature/x: conflict in docs/guide.md, no conflict rule matches",
			"  work worktree: /src/example/.worktrees/feature-x   (work branch pg-git-lander/20261107T101500Z-3f9c2a)",
			"  resolve and `git add` the files there, then:  pg-git-lander land feature/x --continue",
			"  drop the replayed commit instead:             pg-git-lander land feature/x --skip",
			"  give up:                                      pg-git-lander land feature/x --abort",
			"  keep the worktree and work branch, stop tracking: pg-git-lander land feature/x --quit",
		}, "\n")},
	}
	for _, c := range cases {
		got := c.o.Human()
		if got != c.want {
			t.Errorf("%s:\nwant:\n%s\ngot:\n%s", c.name, c.want, got)
		}
		golden(t, "human-"+c.name+".txt", []byte(got+"\n"))
	}
}

func TestHumanRefusedNamesNextCommand(t *testing.T) {
	o := NewRefused(RefStoppedPending, "land", "feature/x", "")
	o.Message = "feature/x has a stopped operation"
	got := o.Human()
	for _, s := range []string{"refused: land feature/x:", "pg-git-lander land feature/x --continue", "--quit"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in\n%s", s, got)
		}
	}
}

func TestHumanWarnings(t *testing.T) {
	o := landedOutcome()
	o.Warnings = []WarningEntry{{Warning: WarnBranchNotDeleted, Reason: NotDeletedMoved}}
	if got := o.Human(); !strings.Contains(got, "warning: branch-not-deleted (moved)") {
		t.Errorf("got %s", got)
	}
}

func TestHumanSingularPlural(t *testing.T) {
	o := landedOutcome()
	o.Stats = &Stats{DurationMS: 1000, RebasedCommits: 1, AutofixCommits: 2}
	if got := o.Human(); !strings.Contains(got, "rebased 1 commit, 2 autofix commits, 1.0 s") {
		t.Errorf("got %s", got)
	}
}
