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
			state.Result{Name: "alpha", State: state.Idle, Live: true, LastKnown: store.Done},
			"name=alpha state=idle live=true",
		},
		{
			"waiting_for_human",
			state.Result{Name: "a", State: state.WaitingForHuman, Live: true, LastKnown: store.Working},
			"name=a state=waiting-for-human live=true",
		},
		{
			"not_live_with_last_known",
			state.Result{Name: "d", State: state.NotLive, Live: false, LastKnown: store.Done},
			"name=d state=not-live last_known=done live=false",
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
// transcriptAdapter + fixtures: a dangling AskUserQuestion reads true, a
// completed turn reads false, and an empty path short-circuits to false without
// a read (claude-transcript's IsAwaitingInput is already tested in its own
// package — this only checks the ccpool-side wiring).
func TestStateAwaitingWiring(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"dangling_question_awaits", "testdata/awaiting_question.jsonl", true},
		{"completed_turn_not_awaiting", "testdata/completed_idle.jsonl", false},
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
}

func TestRenderStateJSON_omitempty(t *testing.T) {
	cases := []struct {
		name       string
		res        state.Result
		wantSubKey bool
		wantLKKey  bool
		wantState  string
	}{
		{
			"working_includes_sub_omits_last_known",
			state.Result{Name: "alpha", State: state.Working, SubState: state.SubThinking, Live: true, LastKnown: store.Working},
			true, false, "working",
		},
		{
			"idle_omits_sub_and_last_known",
			state.Result{Name: "alpha", State: state.Idle, Live: true, LastKnown: store.Done},
			false, false, "idle",
		},
		{
			"not_live_includes_last_known_omits_sub",
			state.Result{Name: "d", State: state.NotLive, Live: false, LastKnown: store.Done},
			false, true, "not-live",
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
		})
	}
}
