package killprobe

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// TestKillProbe_SafeForms_Approve covers every null-signal spelling ADR 0074
// recognizes: the glued numeric `-0`, `-s 0`, `--signal 0`, and `--signal=0`
// — with single and multiple pid operands, and in the exact Monitor
// until-loop idiom pg2-z3u4f's measurement attributes the 81 misses to.
func TestKillProbe_SafeForms_Approve(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"glued -0 single pid", "kill -0 12345"},
		{"glued -0 multiple pids", "kill -0 111 222"},
		{"-s 0", "kill -s 0 12345"},
		{"--signal 0", "kill --signal 0 12345"},
		{"--signal=0", "kill --signal=0 12345"},
		{"the measured Monitor idiom", "kill -0 12345 && echo RUNNING && echo DONE"},
		{"until-loop negated form", "until ! kill -0 12345 2>/dev/null; do sleep 2; done"},
		{"variable pid", `kill -0 "$PID"`},
		{"redundant repeated -0", "kill -0 -0 12345"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.Approve {
				t.Errorf("%q => %v, want Approve", tt.command, got)
			}
		})
	}
}

// TestKillProbe_UnsafeForms_Defer covers every shape that must NOT be
// approved by this rule: the default signal, an explicit non-zero or named
// signal, signal-listing flags, and a signal-0 flag combined with a second,
// disqualifying signal flag. Each must come back NoOpinion (unclaimed,
// deferred to whatever the rest of the chain already does — unchanged from
// before this rule existed).
func TestKillProbe_UnsafeForms_Defer(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"bare kill (default SIGTERM)", "kill 12345"},
		{"glued -9", "kill -9 12345"},
		{"named -TERM", "kill -TERM 12345"},
		{"named -KILL", "kill -KILL 12345"},
		{"-s with non-zero signal", "kill -s TERM 12345"},
		{"-s with non-zero number", "kill -s 9 12345"},
		{"--signal non-zero", "kill --signal 9 12345"},
		{"list signals -l", "kill -l"},
		{"list signals -L", "kill -L"},
		{"two conflicting signals, -0 then -9", "kill -0 -9 12345"},
		{"-s0 glued (unrecognized form)", "kill -s0 12345"},
		// A KNOWN, DELIBERATE limitation (isSignalZeroProbe's doc): a
		// negative-process-group pid operand is lexically indistinguishable
		// from a glued numeric signal flag, so it is misread as a second
		// signal spec and disqualifies the match. Safe direction per the
		// under-matching bias documented on isSignalZeroProbe (a miss costs a
		// prompt/abstain, never a wrong Approve) — not something a future
		// change should "fix" by widening the glued-flag heuristic.
		{"negative pid misread as a signal flag (documented limitation)", "kill -0 -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.NoOpinion {
				t.Errorf("%q => %v, want NoOpinion (deferred, unchanged scrutiny)", tt.command, got)
			}
		})
	}
}

// TestKillProbe_NonKillCommands_NotApplicable pins that this rule stays out
// of the way of every other Bash command, and of the non-Bash KillShell tool
// (owned by the unrelated internal/rules/killshell package).
func TestKillProbe_NonKillCommands_NotApplicable(t *testing.T) {
	r := New()
	t.Run("unrelated bash command", func(t *testing.T) {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("echo hi")}
		got, err := r.Evaluate(input)
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("got result=%v err=%v, want ErrNotApplicable", got, err)
		}
	})
	t.Run("killall is a different command", func(t *testing.T) {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("killall -0 someproc")}
		got, err := r.Evaluate(input)
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("got result=%v err=%v, want ErrNotApplicable", got, err)
		}
	})
	t.Run("KillShell tool is a different tool", func(t *testing.T) {
		input := &hookio.HookInput{ToolName: "KillShell", ToolInput: json.RawMessage(`{"shell_id":"shell-1"}`)}
		got, err := r.Evaluate(input)
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("got result=%v err=%v, want ErrNotApplicable", got, err)
		}
	})
}
