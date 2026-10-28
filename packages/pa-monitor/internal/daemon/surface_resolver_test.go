package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// hostSignaler counts Detect calls; classifier mimics CmuxSignaler.
type hostSignaler struct {
	name       string
	detect     bool
	classifier bool
	detects    int
}

func (h *hostSignaler) Name() string { return h.name }
func (h *hostSignaler) Detect(int) bool {
	h.detects++
	return h.detect
}
func (h *hostSignaler) Send(int, string) error { return nil }

type classifierSignaler struct{ hostSignaler }

func (c *classifierSignaler) DetectIsHostClassification() bool { return true }

func hostTree(svs ...*aggregate.SessionView) *aggregate.Tree { return treeWithSessions(svs...) }

func sessionWithHost(sid string, pid int, host string, alive bool) *aggregate.SessionView {
	return &aggregate.SessionView{Session: &session.Session{SessionID: sid, PID: pid, TerminalHost: host, PidAlive: alive}}
}

func TestSurfaceResolver_CmuxHostWalksNothing(t *testing.T) {
	tmux := &hostSignaler{name: "tmux"}
	cmux := &classifierSignaler{hostSignaler{name: "cmux", detect: true}}
	r := newSurfaceResolver([]signal.Signaler{tmux, cmux}, hostTree(sessionWithHost("a", 10, "cmux (no bridge)", true)))
	if !r.HasSurface(10) {
		t.Fatal("cmux-hosted pid must have a surface")
	}
	if !r.HasUnsubmittedInput(10) { // cmux has no inspector: fail closed
		t.Fatal("cmux has no PaneInputInspector: must fail closed")
	}
	if tmux.detects != 0 || cmux.detects != 0 {
		t.Fatalf("detects tmux=%d cmux=%d, want 0/0", tmux.detects, cmux.detects)
	}
}

func TestSurfaceResolver_UnknownPIDsAndHostsUseTheFullWalk(t *testing.T) {
	cases := map[string]*aggregate.Tree{
		"pid not in tree":          hostTree(),
		"unknown host":             hostTree(sessionWithHost("a", 10, "unknown", true)),
		"empty host":               hostTree(sessionWithHost("a", 10, "", true)),
		"dead pid (reuse hazard)":  hostTree(sessionWithHost("a", 10, "cmux", false)),
		"nil tree":                 nil,
		"two live sessions, 1 pid": hostTree(sessionWithHost("a", 10, "cmux", true), sessionWithHost("b", 10, "tmux", true)),
	}
	for name, tree := range cases {
		t.Run(name, func(t *testing.T) {
			tmux := &hostSignaler{name: "tmux"}
			cmux := &classifierSignaler{hostSignaler{name: "cmux", detect: true}}
			r := newSurfaceResolver([]signal.Signaler{tmux, cmux}, tree)
			if !r.HasSurface(10) {
				t.Fatal("legacy walk must still find cmux")
			}
			if tmux.detects != 1 || cmux.detects != 1 {
				t.Fatalf("detects tmux=%d cmux=%d, want the legacy full walk 1/1", tmux.detects, cmux.detects)
			}
		})
	}
}

// D5 (hasUnattemptedNudgeableDisrupt) resolves through the session's cached
// host: a cmux-hosted session with a terminal retryable error holds the Mac
// awake without ever consulting the tmux signaler.
func TestHasUnattemptedNudgeableDisrupt_UsesCachedHost(t *testing.T) {
	wm := newWMForTest(t)
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: time.Now().Add(-time.Minute)}
	sv := svWithError("sid-1", 100, le)
	sv.TerminalHost = "cmux"
	tmux := &hostSignaler{name: "tmux"}
	cmux := &classifierSignaler{hostSignaler{name: "cmux", detect: true}}
	if !hasUnattemptedNudgeableDisrupt(treeWithSessions(sv), wm, []signal.Signaler{tmux, cmux}) {
		t.Fatal("want keep-awake true for a cmux-hosted terminal retryable error")
	}
	if tmux.detects != 0 {
		t.Fatalf("tmux Detect called %d times for a cmux-hosted session, want 0", tmux.detects)
	}
}
