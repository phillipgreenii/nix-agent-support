package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
)

// A budget message is a pasted prompt followed by Enter; into a session parked on
// a permission dialog that Enter accepts the dialog (bead pg2-zybju, INV-CCH-29).
// These tests pin that the watchdog never pastes into a needs_input session.

// hermeticPoolEnv removes every CCPOOL_* and BEADS_* variable for the test (a
// pool session exports both); the guard under test reads only the fake pool.
func hermeticPoolEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CCPOOL_") || strings.HasPrefix(k, "BEADS_") {
			t.Setenv(k, "")
			_ = os.Unsetenv(k)
		}
	}
}

// stateCC is a fakeCC whose one session row reads the state the test sets (or
// fails the list outright).
type stateCC struct {
	fakeCC
	state   ccpool.SessionState
	listErr error
}

func (c *stateCC) List(context.Context) ([]ccpool.Session, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo", State: c.state}}, nil
}

// pollReader serves one snapshot per poll and tells the test which poll this is
// BEFORE the watchdog acts on it, so a state change lands between two polls.
type pollReader struct {
	seq    []usage.Snapshot
	i      int
	onPoll func(i int)
}

func (r *pollReader) Read(context.Context, string) (usage.Snapshot, error) {
	if r.onPoll != nil {
		r.onPoll(r.i)
	}
	s := r.seq[min(r.i, len(r.seq)-1)]
	r.i++
	return s, nil
}

func eventKinds(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRun_reminderIsHeldWhileTheSessionNeedsInput(t *testing.T) {
	hermeticPoolEnv(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cc := &stateCC{state: ccpool.StateWorking}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sentAtPoll [8]int
	r := &pollReader{
		// 70% (nothing), then 73% (the reminder level) for the rest.
		seq: []usage.Snapshot{{OutputTokens: 700}, {OutputTokens: 730}},
		onPoll: func(i int) {
			if i < len(sentAtPoll) {
				sentAtPoll[i] = len(cc.sent)
			}
			switch {
			case i == 2 || i == 3 || i == 4: // the dialog is open through polls 2-4
				cc.state = ccpool.StateNeedsInput
			case i == 1:
				cc.state = ccpool.StateNeedsInput // the level is first crossed with the dialog already open
			default:
				cc.state = ccpool.StateWorking
			}
			if i == 7 {
				cancel()
			}
		},
	}
	wd := newWD(r, cc, &recBD{}, tokBudget(1000))
	wd.Log = lw
	if err := wd.Run(ctx, "s", "zr-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the run to end by the test's own cancel, got %v", err)
	}
	for i := 1; i <= 5; i++ { // polls 1-4 saw the level with the dialog open; the send follows poll 5's state change
		if sentAtPoll[i] != 0 {
			t.Errorf("a prompt was pasted by poll %d, while the session was in needs_input (sent=%v)", i, cc.sent)
		}
	}
	if len(cc.sent) != 1 || !strings.Contains(cc.sent[0], "near limit") {
		t.Fatalf("the reminder must be sent exactly once after the dialog is gone, got %v", cc.sent)
	}
	var held, reminded int
	for _, e := range eventKinds(t, logPath) {
		switch {
		case e["kind"] == "reminder-suppressed" && e["reason"] == "needs_input":
			held++
		case e["kind"] == "reminder":
			reminded++
		}
	}
	if held != 1 || reminded != 1 {
		t.Errorf("want one held-back event (not one per poll) and one reminder, got held=%d reminded=%d", held, reminded)
	}
}

// A list that cannot be read is treated as needs_input: delaying a reminder costs
// nothing, pasting into an unknown state can approve a dialog.
func TestRun_reminderIsHeldWhenTheStateCannotBeRead(t *testing.T) {
	hermeticPoolEnv(t)
	cc := &stateCC{listErr: errors.New("ccpool list: boom")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &pollReader{seq: []usage.Snapshot{{OutputTokens: 730}}, onPoll: func(i int) {
		if i == 5 {
			cancel()
		}
	}}
	wd := newWD(r, cc, &recBD{}, tokBudget(1000))
	_ = wd.Run(ctx, "s", "zr-1")
	if len(cc.sent) != 0 {
		t.Fatalf("a reminder was pasted into a session whose state is unknown: %v", cc.sent)
	}
}

// The 90% step is an Escape burst (Cancel) and so safe to send in needs_input; the
// wrap-up message that follows is a paste, and goes only if Cancel took the
// session out of needs_input.
func TestRun_wrapUpIsHeldWhenCancelLeftTheSessionInNeedsInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    ccpool.SessionState
		wantSend bool
	}{
		{"still needs_input after the Escape", ccpool.StateNeedsInput, false},
		{"the Escape cleared it", ccpool.StateReady, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hermeticPoolEnv(t)
			cc := &stateCC{state: tc.state}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &pollReader{seq: []usage.Snapshot{{OutputTokens: 950}}, onPoll: func(i int) {
				if i == 3 {
					cancel()
				}
			}}
			wd := newWD(r, cc, &recBD{}, tokBudget(1000))
			_ = wd.Run(ctx, "s", "zr-1")
			if cc.cancels != 1 {
				t.Errorf("the 90%% step must still send its Escape, cancels=%d", cc.cancels)
			}
			if got := len(cc.sent) == 1; got != tc.wantSend {
				t.Errorf("wrap-up sent = %v, want %v (sent=%v)", got, tc.wantSend, cc.sent)
			}
		})
	}
}
