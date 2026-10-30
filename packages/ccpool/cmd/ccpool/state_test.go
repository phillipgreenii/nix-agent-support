package main

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/state"
	"github.com/phillipgreenii/ccpool/internal/store"
)

func TestRenderState_human(t *testing.T) {
	cases := []struct {
		name string
		res  state.Result
		want string
	}{
		{
			"working_thinking",
			state.Result{Name: "alpha", State: state.Working, SubState: state.SubThinking, Live: true, LastKnown: store.Working},
			"name=alpha state=working sub=thinking live=true",
		},
		{
			"working_streaming",
			state.Result{Name: "s", State: state.Working, SubState: state.SubStreaming, Live: true, LastKnown: store.Working},
			"name=s state=working sub=streaming live=true",
		},
		{
			"idle_no_sub",
			state.Result{Name: "alpha", State: state.Idle, Live: true, LastKnown: store.Idle},
			"name=alpha state=idle live=true",
		},
		{
			"waiting_for_human",
			state.Result{Name: "a", State: state.WaitingForHuman, Live: true, LastKnown: store.Working},
			"name=a state=waiting-for-human live=true",
		},
		{
			// waiting-for-human with a question -> question token (collapsed to first line).
			"waiting_for_human_with_question",
			state.Result{Name: "a", State: state.WaitingForHuman, Live: true, LastKnown: store.NeedsInput, Question: "Which path? Alpha or Bravo"},
			"name=a state=waiting-for-human question=Which path? Alpha or Bravo live=true",
		},
		{
			// a multi-line question collapses to its first line.
			"waiting_question_collapsed_to_first_line",
			state.Result{Name: "a", State: state.WaitingForHuman, Live: true, LastKnown: store.NeedsInput, Question: "Which path?\nAlpha or Bravo"},
			"name=a state=waiting-for-human question=Which path? live=true",
		},
		{
			"not_live_with_last_known",
			state.Result{Name: "d", State: state.NotLive, Live: false, LastKnown: store.Idle},
			"name=d state=not-live last_known=idle live=false",
		},
		{
			// idle with a last reply -> last_reply token.
			"idle_with_last_reply",
			state.Result{Name: "a", State: state.Idle, Live: true, LastKnown: store.Idle, LastText: "done here"},
			"name=a state=idle last_reply=done here live=true",
		},
		{
			// error with last text -> last_error token.
			"error_with_last_error",
			state.Result{Name: "a", State: state.Error, Live: true, LastKnown: store.Errored, LastText: "panic: boom"},
			"name=a state=error last_error=panic: boom live=true",
		},
		{
			// a multi-line reply is collapsed to its first line so the human line
			// stays one-line.
			"idle_reply_collapsed_to_first_line",
			state.Result{Name: "a", State: state.Idle, Live: true, LastKnown: store.Idle, LastText: "first line\nsecond line"},
			"name=a state=idle last_reply=first line live=true",
		},
		{
			// LastText set on a non-idle/non-error state is never rendered (defensive:
			// Gather only populates it for idle/error).
			"working_ignores_last_text",
			state.Result{Name: "a", State: state.Working, SubState: state.SubThinking, Live: true, LastKnown: store.Working, LastText: "leak"},
			"name=a state=working sub=thinking live=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.TrimRight(renderState(tc.res), "\n")
			if got != tc.want {
				t.Errorf("renderState =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestStateAwaitingWiring pins the cmd-layer Awaiting closure over the real
// transcriptAdapter and a real in-process recorder: a dangling AskUserQuestion
// reads true, a completed turn reads false, and an empty path short-circuits to
// false without a read.
func TestStateAwaitingWiring(t *testing.T) {
	e := useRecorder(t)
	rel := "projects/" + e.Project + "/" + testSess + ".jsonl"
	e.Write(rel, tuserLine("2026-01-01T00:00:01Z", "go")+tAskLine("2026-01-01T00:00:02Z", "toolu_1"))
	e.Settle()
	path := e.Path("projects", e.Project, testSess+".jsonl")
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"dangling_question_awaits", path, true},
		{"empty_path_not_awaiting", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			awaiting := func() (bool, error) {
				if tc.path == "" {
					return false, nil
				}
				return transcriptAdapter{}.IsAwaitingInput(tc.path)
			}
			got, err := awaiting()
			if err != nil {
				t.Fatalf("awaiting(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Errorf("awaiting(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
	e.Append(rel, `{"type":"user","uuid":"r1","sessionId":"`+testSess+`","timestamp":"2026-01-01T00:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}}`+"\n"+tReplyLine("2026-01-01T00:00:04Z", "done"))
	e.Settle()
	if got, err := (transcriptAdapter{}).IsAwaitingInput(path); err != nil || got {
		t.Errorf("completed turn: awaiting = %v, %v; want false", got, err)
	}
}

// TestStateAndResultRefuseWhenDaemonOffline: the fact-reading commands say
// "daemon offline" and fail instead of guessing from files.
func TestRequireFactsDaemonOffline(t *testing.T) {
	useDownRecorder(t)
	out, code := captureStderr(t, func() int { return requireFacts("state") })
	if code != 1 || !strings.Contains(out, "daemon offline") {
		t.Fatalf("requireFacts with the recorder down = %d, %q; want 1 and daemon offline", code, out)
	}
	useRecorder(t)
	if code := requireFacts("state"); code != 0 {
		t.Fatalf("requireFacts with the recorder up = %d", code)
	}
}

func TestRenderStateJSON_omitempty(t *testing.T) {
	cases := []struct {
		name         string
		res          state.Result
		wantSubKey   bool
		wantLKKey    bool
		wantReplyKey bool
		wantErrKey   bool
		wantQKey     bool
		wantState    string
	}{
		{
			"working_includes_sub_omits_last_known",
			state.Result{Name: "alpha", State: state.Working, SubState: state.SubThinking, Live: true, LastKnown: store.Working},
			true, false, false, false, false, "working",
		},
		{
			"idle_omits_sub_and_last_known",
			state.Result{Name: "alpha", State: state.Idle, Live: true, LastKnown: store.Idle},
			false, false, false, false, false, "idle",
		},
		{
			"not_live_includes_last_known_omits_sub",
			state.Result{Name: "d", State: state.NotLive, Live: false, LastKnown: store.Idle},
			false, true, false, false, false, "not-live",
		},
		{
			// idle + LastText -> last_reply only.
			"idle_with_reply_emits_last_reply",
			state.Result{Name: "a", State: state.Idle, Live: true, LastKnown: store.Idle, LastText: "done here"},
			false, false, true, false, false, "idle",
		},
		{
			// error + LastText -> last_error only.
			"error_with_text_emits_last_error",
			state.Result{Name: "a", State: state.Error, Live: true, LastKnown: store.Errored, LastText: "panic: boom"},
			false, false, false, true, false, "error",
		},
		{
			// LastText on a non-idle/non-error state emits neither key (defensive).
			"working_with_text_emits_neither",
			state.Result{Name: "a", State: state.Working, SubState: state.SubThinking, Live: true, LastKnown: store.Working, LastText: "leak"},
			true, false, false, false, false, "working",
		},
		{
			// waiting-for-human + Question -> question key only.
			"waiting_with_question_emits_question",
			state.Result{Name: "a", State: state.WaitingForHuman, Live: true, LastKnown: store.NeedsInput, Question: "Which path?"},
			false, false, false, false, true, "waiting-for-human",
		},
		{
			// Question on a non-waiting state emits no question key (defensive).
			"idle_with_question_emits_no_question",
			state.Result{Name: "a", State: state.Idle, Live: true, LastKnown: store.Idle, Question: "leak"},
			false, false, false, false, false, "idle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := renderStateJSON(tc.res)
			if err != nil {
				t.Fatalf("renderStateJSON: %v", err)
			}
			s := string(b)
			if !strings.Contains(s, `"state":"`+tc.wantState+`"`) {
				t.Errorf("json %s missing state=%q", s, tc.wantState)
			}
			if got := strings.Contains(s, `"sub_state"`); got != tc.wantSubKey {
				t.Errorf("json %s sub_state present=%v, want %v", s, got, tc.wantSubKey)
			}
			if got := strings.Contains(s, `"last_known"`); got != tc.wantLKKey {
				t.Errorf("json %s last_known present=%v, want %v", s, got, tc.wantLKKey)
			}
			if got := strings.Contains(s, `"last_reply"`); got != tc.wantReplyKey {
				t.Errorf("json %s last_reply present=%v, want %v", s, got, tc.wantReplyKey)
			}
			if got := strings.Contains(s, `"last_error"`); got != tc.wantErrKey {
				t.Errorf("json %s last_error present=%v, want %v", s, got, tc.wantErrKey)
			}
			if got := strings.Contains(s, `"question"`); got != tc.wantQKey {
				t.Errorf("json %s question present=%v, want %v", s, got, tc.wantQKey)
			}
		})
	}
}
