package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// `git rebase --exec <cmd>` / `-x <cmd>` run a shell command after each rebased
// commit, so the flag must be screened like the transport exec sinks in
// execsinkflags_test.go. These tests first prove against REAL git (hermetic temp
// repo, marker file) that every spelling below actually runs the command, then
// pin the rule's verdict for the same spellings.

// rebaseExecSpellings are argv fragments (before the upstream) that git accepts for
// the exec option; %M is replaced by the marker command.
var rebaseExecSpellings = [][]string{
	{"--exec", "%M"},
	{"--exec=%M"},
	{"-x", "%M"},
	{"-x%M"},        // glued short value
	{"--exe", "%M"}, // abbreviation
	{"--ex=%M"},     // abbreviation, glued
	{"-ix", "%M"},   // cluster with -i (needs a sequence editor)
}

func TestGit_RebaseExec_RealGitRunsTheCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, sp := range rebaseExecSpellings {
		t.Run(sp[0], func(t *testing.T) {
			// Hermetic x/gittest fixture; the sequence editor is a no-op so the
			// -i cluster spelling does not wait on an interactive editor.
			repo := gittest.New(t, gitfixture.RepoOptions{})
			for _, kv := range [][]string{{"sequence.editor", ":"}, {"core.editor", ":"}} {
				if out, err := repo.Client.Run(t.Context(), "config", kv[0], kv[1]); err != nil {
					t.Fatalf("git config %s: %v\n%s", kv[0], err, out)
				}
			}
			for _, msg := range []string{"one", "two"} {
				if _, err := repo.Commit(t.Context(), msg, nil); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(t.TempDir(), "marker")
			cmdStr := "touch " + marker
			args := []string{"rebase"}
			for _, a := range sp {
				switch a {
				case "%M":
					a = cmdStr
				case "--exec=%M":
					a = "--exec=" + cmdStr
				case "-x%M":
					a = "-x" + cmdStr
				case "--ex=%M":
					a = "--ex=" + cmdStr
				}
				args = append(args, a)
			}
			args = append(args, "HEAD~1")
			if out, err := repo.Client.Run(t.Context(), args...); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("git %v did not run the exec command: %v", args, err)
			}
		})
	}
}

func TestGit_RebaseExec_NotApproved(t *testing.T) {
	for _, cmd := range []string{
		"git rebase --exec x main",
		"git rebase --exec=x main",
		"git rebase -x x main",
		"git rebase -xtouch main",
		"git rebase -ix x main",
		"git rebase -qx x main",
		"git rebase --exe x main",
		"git rebase --ex=x main",
		"git rebase --e=x main",
		"git rebase main --exec x",
		"GIT_SEQUENCE_EDITOR=: git rebase -i -x x main",
	} {
		if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — the flag runs a shell command per commit", cmd, got.Reason)
		}
	}
}

// The verdict is the same refusal (NoOpinion/Ask, never a Reject) the transport exec
// sinks get, so the rebase spelling cannot disagree with them.
func TestGit_RebaseExec_SameVerdictAsTransportSinks(t *testing.T) {
	want := evalCmd(t, "git push --exec=x ../repo main")
	got := evalCmd(t, "git rebase --exec x main")
	if got.Decision != want.Decision {
		t.Errorf("rebase --exec: %s (%s); push --exec: %s — one exec-sink class, one decision", got.Decision, got.Reason, want.Decision)
	}
}

func TestGit_RebaseExec_NegativeControlsStayApproved(t *testing.T) {
	for _, cmd := range []string{
		"git rebase main",
		"git rebase --continue",
		"git rebase --abort",
		"git rebase --skip",
		"git rebase -q main",
		"git rebase --onto a b c",
		"git rebase -Xours main",
		"git rebase -s recursive -Xsubtree=lex main", // x inside a glued -X value
		"git rebase --empty=drop main",
		"git rebase --edit-todo",
		"git rebase --keep-base main",
		"GIT_SEQUENCE_EDITOR=: git rebase -i main",
		"git log --exec x", // not a rebase: unaffected by this screen
		"git rebase -- -x", // after --, an operand
	} {
		got := evalCmd(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want APPROVE — plain rebase must stay usable", cmd, got.Decision, got.Reason)
		}
	}
}
