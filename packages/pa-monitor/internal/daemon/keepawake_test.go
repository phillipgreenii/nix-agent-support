package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// detectSignaler is a fake signal.Signaler whose Detect verdict and Send
// behavior are configurable. Used to exercise the keep-awake signaler gate.
type detectSignaler struct {
	detect bool
}

func (d detectSignaler) Name() string           { return "fake" }
func (d detectSignaler) Detect(int) bool        { return d.detect }
func (d detectSignaler) Send(int, string) error { return nil }

func svWithError(sid string, pid int, le *transcript.ErrorRecord) *aggregate.SessionView {
	return &aggregate.SessionView{
		Session:           &session.Session{SessionID: sid, PID: pid},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: le},
	}
}

func treeWithSessions(svs ...*aggregate.SessionView) *aggregate.Tree {
	return &aggregate.Tree{Dirs: []*aggregate.Directory{{Path: "/p", Sessions: svs}}}
}

func newWMForTest(t *testing.T) *WatermarkStore {
	t.Helper()
	w, err := NewWatermarkStore(filepath.Join(t.TempDir(), "runtime.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestHasUnattemptedNudgeableDisrupt_TerminalRetryableNoAttempt(t *testing.T) {
	wm := newWMForTest(t)
	now := time.Now()
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: now.Add(-time.Minute)}
	tree := treeWithSessions(svWithError("sid-1", 100, le))
	sigs := []signal.Signaler{detectSignaler{detect: true}}
	if !hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake true for terminal+retryable+no-attempt+resolvable signaler")
	}
}

func TestHasUnattemptedNudgeableDisrupt_AfterAttemptReleases(t *testing.T) {
	wm := newWMForTest(t)
	now := time.Now()
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: now.Add(-time.Minute)}
	// Record an attempt AFTER the error timestamp → no longer holds awake.
	wm.RecordDisruptAttempt("sid-1", now)
	tree := treeWithSessions(svWithError("sid-1", 100, le))
	sigs := []signal.Signaler{detectSignaler{detect: true}}
	if hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake false after an attempt was recorded post-error")
	}
}

func TestHasUnattemptedNudgeableDisrupt_StaleAttemptStillHolds(t *testing.T) {
	wm := newWMForTest(t)
	now := time.Now()
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: now}
	// An attempt from a PREVIOUS error cycle (before this error) does not count.
	wm.RecordDisruptAttempt("sid-1", now.Add(-time.Hour))
	tree := treeWithSessions(svWithError("sid-1", 100, le))
	sigs := []signal.Signaler{detectSignaler{detect: true}}
	if !hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake true: prior-cycle attempt predates this error")
	}
}

func TestHasUnattemptedNudgeableDisrupt_NonRetryableSkipped(t *testing.T) {
	wm := newWMForTest(t)
	le := &transcript.ErrorRecord{Kind: transcript.ErrAuthFailed, IsTerminal: true, At: time.Now()}
	tree := treeWithSessions(svWithError("sid-1", 100, le))
	sigs := []signal.Signaler{detectSignaler{detect: true}}
	if hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake false for a non-retryable (terminal) error")
	}
}

func TestHasUnattemptedNudgeableDisrupt_SubagentSkipped(t *testing.T) {
	wm := newWMForTest(t)
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: time.Now(), FromSubagent: true}
	tree := treeWithSessions(svWithError("sid-1", 100, le))
	sigs := []signal.Signaler{detectSignaler{detect: true}}
	if hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake false for a subagent-only error (excluded, §11 gap)")
	}
}

func TestHasUnattemptedNudgeableDisrupt_NoSignalerSkipped(t *testing.T) {
	wm := newWMForTest(t)
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: time.Now()}
	tree := treeWithSessions(svWithError("sid-1", 100, le))
	sigs := []signal.Signaler{detectSignaler{detect: false}}
	if hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake false when no signaler resolves for the pid")
	}
}

func TestHasUnattemptedNudgeableDisrupt_NonTerminalSkipped(t *testing.T) {
	wm := newWMForTest(t)
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: false, At: time.Now()}
	tree := treeWithSessions(svWithError("sid-1", 100, le))
	sigs := []signal.Signaler{detectSignaler{detect: true}}
	if hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake false for a non-terminal error")
	}
}

func TestHasUnattemptedNudgeableDisrupt_WaitingForHumanSkipped(t *testing.T) {
	wm := newWMForTest(t)
	// Terminal + retryable + no attempt would normally hold the Mac awake, but
	// a WaitingForHuman session suppresses both nudges and caffeinate (§6/D3).
	// Since no nudge is ever attempted for it, it must NOT keep the Mac awake.
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: time.Now()}
	sv := svWithError("sid-1", 100, le)
	sv.Status = session.Blocked
	sv.Blocker = session.HumanInput
	tree := treeWithSessions(sv)
	sigs := []signal.Signaler{detectSignaler{detect: true}}
	if hasUnattemptedNudgeableDisrupt(tree, wm, sigs) {
		t.Error("want keep-awake false for a waiting-for-human session (D3 suppresses caffeinate)")
	}
}
