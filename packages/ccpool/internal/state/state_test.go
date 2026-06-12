package state

import (
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// --- pure Classify: the six precedence branches ---------------------------

func TestClassify(t *testing.T) {
	const counter = "✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)"
	const proseA = "Here is the first chunk of a long essay about Unix pipes."
	const proseB = "Here is the first chunk of a long essay about Unix pipes and more."
	const staticPane = "❯ ready for input\n  -- INSERT --"

	cases := []struct {
		name    string
		in      Inputs
		want    State
		wantSub SubState
		wantLK  store.State // LastKnown
	}{
		{
			// precedence 1: not live -> not-live, carry row state
			name:   "not_live_reports_last_known",
			in:     Inputs{Name: "a", Live: false, Row: store.Session{State: store.Done}},
			want:   NotLive,
			wantLK: store.Done,
		},
		{
			// precedence 2: in-flight thinking (counter in frame1, fast path)
			name:    "inflight_thinking_fast_path",
			in:      Inputs{Name: "a", Live: true, Frame1: counter, NumFrames: 1, Row: store.Session{State: store.Working}},
			want:    Working,
			wantSub: SubThinking,
			wantLK:  store.Working,
		},
		{
			// precedence 2: in-flight streaming (counter-less frames differ)
			name:    "inflight_streaming_via_diff",
			in:      Inputs{Name: "a", Live: true, Frame1: proseA, Frame2: proseB, Frame3: proseB, NumFrames: 3, Row: store.Session{State: store.Working}},
			want:    Working,
			wantSub: SubStreaming,
			wantLK:  store.Working,
		},
		{
			// precedence 2: counter appears only in frame3 -> thinking
			name:    "inflight_thinking_counter_in_f3",
			in:      Inputs{Name: "a", Live: true, Frame1: staticPane, Frame2: staticPane, Frame3: counter, NumFrames: 3, Row: store.Session{State: store.Working}},
			want:    Working,
			wantSub: SubThinking,
			wantLK:  store.Working,
		},
		{
			// precedence 3: settled + awaiting -> waiting-for-human
			name:   "settled_waiting_for_human",
			in:     Inputs{Name: "a", Live: true, Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3, Awaiting: true, Row: store.Session{State: store.Working}},
			want:   WaitingForHuman,
			wantLK: store.Working,
		},
		{
			// precedence 3 before 4: waiting wins over a Failed row
			name:   "waiting_precedes_error",
			in:     Inputs{Name: "a", Live: true, Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3, Awaiting: true, Row: store.Session{State: store.Failed}},
			want:   WaitingForHuman,
			wantLK: store.Failed,
		},
		{
			// precedence 4: settled + Failed row -> error
			name:   "settled_error_from_failed_row",
			in:     Inputs{Name: "a", Live: true, Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3, Row: store.Session{State: store.Failed}},
			want:   Error,
			wantLK: store.Failed,
		},
		{
			// precedence 5: settled + Starting row (launching) -> working/thinking
			name:    "settled_starting_launching",
			in:      Inputs{Name: "a", Live: true, Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3, Row: store.Session{State: store.Starting}},
			want:    Working,
			wantSub: SubThinking,
			wantLK:  store.Starting,
		},
		{
			// precedence 6: settled, completed turn -> idle
			name:   "settled_idle_completed_turn",
			in:     Inputs{Name: "a", Live: true, Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3, Row: store.Session{State: store.Done}},
			want:   Idle,
			wantLK: store.Done,
		},
		{
			// precedence 6: settled, discarded/rewound (static pane, Ready row) -> idle
			name:   "settled_idle_discarded_thinking",
			in:     Inputs{Name: "a", Live: true, Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3, Row: store.Session{State: store.Ready}},
			want:   Idle,
			wantLK: store.Ready,
		},
		{
			// GATE: counter-less animation on a NON-working row is NOT a streaming
			// turn — a freshly-launched session still DRAWING its TUI churns the pane
			// at startup, which the pane-diff cannot distinguish from prose streaming.
			// So the streaming-via-diff branch is gated on the row corroborating a
			// turn (Working/Starting); a Ready row settles to idle.
			name:    "animating_but_ready_row_is_idle_not_streaming",
			in:      Inputs{Name: "a", Live: true, Frame1: proseA, Frame2: proseB, Frame3: proseB, NumFrames: 3, Row: store.Session{State: store.Ready}},
			want:    Idle,
			wantSub: SubNone,
			wantLK:  store.Ready,
		},
		{
			// The thinking COUNTER is a reliable turn signal (startup draw never
			// renders it), so the counter path is NOT gated: a counter on a Ready row
			// still classifies working/thinking.
			name:    "counter_on_ready_row_still_working",
			in:      Inputs{Name: "a", Live: true, Frame1: counter, NumFrames: 1, Row: store.Session{State: store.Ready}},
			want:    Working,
			wantSub: SubThinking,
			wantLK:  store.Ready,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in)
			if got.State != tc.want {
				t.Errorf("State = %q, want %q", got.State, tc.want)
			}
			if got.SubState != tc.wantSub {
				t.Errorf("SubState = %q, want %q", got.SubState, tc.wantSub)
			}
			if got.LastKnown != tc.wantLK {
				t.Errorf("LastKnown = %q, want %q", got.LastKnown, tc.wantLK)
			}
			if got.Live != tc.in.Live {
				t.Errorf("Live = %v, want %v", got.Live, tc.in.Live)
			}
			if got.Name != tc.in.Name {
				t.Errorf("Name = %q, want %q", got.Name, tc.in.Name)
			}
		})
	}
}

// --- pure ClassifyFrame: the R2 3-frame logic -----------------------------

func TestClassifyFrame(t *testing.T) {
	const counter = "✽ Working… (8s · ↓ 42 tokens · thinking with xhigh effort)"
	const a = "frame-A"
	const b = "frame-B"

	cases := []struct {
		name       string
		f1         string
		f2         string
		f3         string
		nFrames    int
		wantFlight bool
		wantSub    SubState
	}{
		{"counter_in_f1_fast_path", counter, "", "", 1, true, SubThinking},
		{"counter_in_f3", a, a, counter, 3, true, SubThinking},
		{"counter_in_f2", a, counter, counter, 3, true, SubThinking},
		{"diff_without_counter_streaming", a, b, b, 3, true, SubStreaming},
		{"diff_only_f2_f3_streaming", a, a, b, 3, true, SubStreaming},
		{"identical_without_counter_settled", a, a, a, 3, false, SubNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := ClassifyFrame(tc.f1, tc.f2, tc.f3, tc.nFrames)
			if v.InFlight != tc.wantFlight {
				t.Errorf("InFlight = %v, want %v", v.InFlight, tc.wantFlight)
			}
			if v.Sub != tc.wantSub {
				t.Errorf("Sub = %q, want %q", v.Sub, tc.wantSub)
			}
		})
	}
}

// --- Gather: scripted Paner fake (sticky-last, mirrors closeTmux.panes) ----

// fakePaner scripts a CapturePane sequence and a fixed HasSession answer.
type fakePaner struct {
	live     bool
	panes    []string // scripted CapturePane sequence; last element is sticky
	capCalls int
	hasCalls int
}

func (p *fakePaner) HasSession(string) bool { p.hasCalls++; return p.live }
func (p *fakePaner) CapturePane(string) (string, error) {
	if len(p.panes) == 0 {
		return "", nil
	}
	i := p.capCalls
	if i >= len(p.panes) {
		i = len(p.panes) - 1
	}
	p.capCalls++
	return p.panes[i], nil
}

// recordingSleep counts sleep calls so the fast path (no sleep) is assertable.
type recordingSleep struct{ calls int }

func (s *recordingSleep) Sleep(time.Duration) { s.calls++ }

func staticAwaiting(b bool) func() (bool, error) {
	return func() (bool, error) { return b, nil }
}

func TestGather_fastPathSkipsSecondCapture(t *testing.T) {
	const counter = "✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)"
	p := &fakePaner{live: true, panes: []string{counter}}
	sl := &recordingSleep{}
	row := store.Session{Name: "a", State: store.Working}

	res, err := Gather(p, sl.Sleep, staticAwaiting(false), "cc-a", "a", row)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if res.State != Working || res.SubState != SubThinking {
		t.Errorf("got %s/%s, want working/thinking", res.State, res.SubState)
	}
	if p.capCalls != 1 {
		t.Errorf("capCalls = %d, want 1 (fast path: no second capture)", p.capCalls)
	}
	if sl.calls != 0 {
		t.Errorf("sleep calls = %d, want 0 (fast path: no sleep)", sl.calls)
	}
}

func TestGather_streamingViaThreeFrames(t *testing.T) {
	// Three distinct counter-less frames -> animating prose -> streaming.
	p := &fakePaner{live: true, panes: []string{"prose 1", "prose 1 2", "prose 1 2 3"}}
	sl := &recordingSleep{}
	row := store.Session{Name: "s", State: store.Working}

	res, err := Gather(p, sl.Sleep, staticAwaiting(false), "cc-s", "s", row)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if res.State != Working || res.SubState != SubStreaming {
		t.Errorf("got %s/%s, want working/streaming", res.State, res.SubState)
	}
	if p.capCalls != 3 {
		t.Errorf("capCalls = %d, want 3 (counter-less needs 3 frames)", p.capCalls)
	}
	if sl.calls != 2 {
		t.Errorf("sleep calls = %d, want 2 (between f1-f2 and f2-f3)", sl.calls)
	}
}

func TestGather_settledIdle(t *testing.T) {
	// Identical counter-less frames + not awaiting + non-failed row -> idle.
	const staticPane = "❯ ready\n  -- INSERT --"
	p := &fakePaner{live: true, panes: []string{staticPane}} // sticky -> all identical
	sl := &recordingSleep{}
	row := store.Session{Name: "i", State: store.Ready}

	res, err := Gather(p, sl.Sleep, staticAwaiting(false), "cc-i", "i", row)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if res.State != Idle {
		t.Errorf("State = %s, want idle", res.State)
	}
	if res.SubState != SubNone {
		t.Errorf("SubState = %q, want empty", res.SubState)
	}
}

func TestGather_awaitingWaitsForHuman(t *testing.T) {
	const staticPane = "[picker] Alpha / Bravo"
	p := &fakePaner{live: true, panes: []string{staticPane}}
	sl := &recordingSleep{}
	row := store.Session{Name: "q", State: store.Working}

	res, err := Gather(p, sl.Sleep, staticAwaiting(true), "cc-q", "q", row)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if res.State != WaitingForHuman {
		t.Errorf("State = %s, want waiting-for-human", res.State)
	}
}

func TestGather_notLiveSkipsCapture(t *testing.T) {
	p := &fakePaner{live: false, panes: []string{"unused"}}
	sl := &recordingSleep{}
	row := store.Session{Name: "d", State: store.Done}

	res, err := Gather(p, sl.Sleep, staticAwaiting(false), "cc-d", "d", row)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if res.State != NotLive {
		t.Errorf("State = %s, want not-live", res.State)
	}
	if res.LastKnown != store.Done {
		t.Errorf("LastKnown = %s, want done", res.LastKnown)
	}
	if res.Live {
		t.Error("Live = true, want false")
	}
	if p.capCalls != 0 {
		t.Errorf("capCalls = %d, want 0 (no pane reads when not live)", p.capCalls)
	}
}

func TestGather_awaitingErrorTolerated(t *testing.T) {
	// A transcript read error in the settled branch must not crash; it falls
	// through to the Failed/idle checks (awaiting treated as false).
	const staticPane = "❯ ready"
	p := &fakePaner{live: true, panes: []string{staticPane}}
	sl := &recordingSleep{}
	row := store.Session{Name: "u", State: store.Ready}
	boom := func() (bool, error) { return false, errors.New("transcript unreadable") }

	res, err := Gather(p, sl.Sleep, boom, "cc-u", "u", row)
	if err != nil {
		t.Fatalf("Gather should tolerate a transcript read error, got: %v", err)
	}
	if res.State != Idle {
		t.Errorf("State = %s, want idle (read error tolerated)", res.State)
	}
}
