package bgkill

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// fakeStore is an in-memory JobStore for the decision table.
type fakeStore map[string]Job

func (f fakeStore) Job(name string) (Job, bool) {
	j, ok := f[name]
	return j, ok
}

const session = "sess-A"

func bashInput(cmd, sessionID string) *hookio.HookInput {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return &hookio.HookInput{ToolName: "Bash", ToolInput: b, SessionID: sessionID}
}

func liveJob(sess string) Job { return Job{Session: sess, LauncherAlive: true} }

func TestBgkill_Approves_OnlyOwnLiveJob(t *testing.T) {
	store := fakeStore{
		"mine":      liveJob(session),
		"theirs":    liveJob("sess-B"),
		"unowned":   liveJob(""),
		"finished":  {Session: session, Finished: true, LauncherAlive: true},
		"dead":      {Session: session},
		"flake.2-x": liveJob(session),
	}
	tests := []struct {
		name      string
		command   string
		sessionID string
		store     JobStore
		want      hookio.Decision
	}{
		{"own live job", "bgkill mine", session, store, hookio.Approve},
		{"name with dots and dashes", "bgkill flake.2-x", session, store, hookio.Approve},
		{"absolute path to the tool", "/etc/profiles/per-user/u/bin/bgkill mine", session, store, hookio.Approve},
		{"in an && chain leaf", "bgkill mine && bgcheck mine", session, store, hookio.Approve},
		{"stderr to /dev/null", "bgkill mine 2>/dev/null", session, store, hookio.Approve},
		{"stderr dup to stdout", "bgkill mine 2>&1", session, store, hookio.Approve},
		{"another session's job", "bgkill theirs", session, store, hookio.NoOpinion},
		{"job recorded without a session", "bgkill unowned", session, store, hookio.NoOpinion},
		{"finished job", "bgkill finished", session, store, hookio.NoOpinion},
		{"launcher no longer alive or not bgrun", "bgkill dead", session, store, hookio.NoOpinion},
		{"unrecorded job", "bgkill ghost", session, store, hookio.NoOpinion},
		{"empty hook session id", "bgkill mine", "", store, hookio.NoOpinion},
		{"nil store (replay)", "bgkill mine", session, nil, hookio.NoOpinion},
		{"no name", "bgkill", session, store, hookio.NoOpinion},
		{"two names", "bgkill mine theirs", session, store, hookio.NoOpinion},
		{"--dir flag", "bgkill --dir /tmp/x mine", session, store, hookio.NoOpinion},
		{"--grace flag", "bgkill --grace 1 mine", session, store, hookio.NoOpinion},
		{"help flag", "bgkill --help", session, store, hookio.NoOpinion},
		{"BG_DIR env prefix", "BG_DIR=/tmp/x bgkill mine", session, store, hookio.NoOpinion},
		{"path-hostile name", "bgkill ../mine", session, store, hookio.NoOpinion},
		{"variable name", `bgkill "$JOB"`, session, store, hookio.NoOpinion},
		{"command substitution name", "bgkill $(echo mine)", session, store, hookio.NoOpinion},
		{"redirect to a file", "bgkill mine > /tmp/out", session, store, hookio.NoOpinion},
		{"process substitution", "bgkill mine <(echo x)", session, store, hookio.NoOpinion},
		{"raw kill of a pid is not this rule's", "kill 12345", session, store, hookio.NoOpinion},
		{"pkill is not this rule's", "pkill -f mine", session, store, hookio.NoOpinion},
		{"non-Bash tool", "", session, store, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := bashInput(tt.command, tt.sessionID)
			if tt.name == "non-Bash tool" {
				input = &hookio.HookInput{ToolName: "Read", SessionID: tt.sessionID}
			}
			res, err := New(tt.store).Evaluate(input)
			if err != nil && !errors.Is(err, hookio.ErrNotApplicable) {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := hookio.Verdict(res, err).Decision; got != tt.want {
				t.Errorf("%q => %v (%s), want %v", tt.command, got, res.Reason, tt.want)
			}
		})
	}
}

// TestBgkill_ApproveOnlyNeverReject pins the rule's under-matching bias: a
// non-matching shape is "not applicable", so the chain continues unchanged; the
// rule can never turn a command that was merely prompted into a Reject.
func TestBgkill_ApproveOnlyNeverReject(t *testing.T) {
	_, err := New(fakeStore{}).Evaluate(bashInput("bgkill ghost", session))
	if !errors.Is(err, hookio.ErrNotApplicable) {
		t.Errorf("err = %v, want ErrNotApplicable (the chain continues unchanged)", err)
	}
}
