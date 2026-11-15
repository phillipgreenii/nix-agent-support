package poller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
	"github.com/phillipgreenii/pa-monitor/internal/core/provider"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// hostDiscovery gives each discovered session the recorder Host fact a fixture
// names (a nil entry leaves the session without one).
type hostDiscovery struct {
	inner session.Discovery
	hosts map[string]*session.Host
}

func (d hostDiscovery) Discover() ([]*session.Session, error) {
	out, err := d.inner.Discover()
	for _, s := range out {
		s.Host = d.hosts[s.SessionID]
	}
	return out, err
}

// execRecorder is the RunCmd seam of the real tmux and cmux signalers; it counts
// every process the signal layer would start, as SentinelOne would.
type execRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *execRecorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprint(name, " ", args))
	return nil, fmt.Errorf("execRecorder: %s %v", name, args)
}

func (r *execRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestAssemble_TerminalHostFromRecorderFactRunsNoExec: one Discover/Assemble pass
// labels every session from its recorder Host fact and starts no process on the
// label path (no ps, tmux or cmux). The control runs the previous mechanism, the
// signalers' ancestry detection, over the same shim to show the counter does
// see an exec when one happens.
func TestAssemble_TerminalHostFromRecorderFactRunsNoExec(t *testing.T) {
	e := corpustest.Start(t)
	hosts := map[string]*session.Host{
		"h-cmux":    {Kind: "cmux"},
		"h-tmux":    {Kind: "tmux"},
		"h-ghostty": {Kind: "other", TermProgram: "ghostty"},
		"h-vscode":  {Kind: "other", TermProgram: "vscode"},
		"h-other":   {Kind: "other", TermProgram: "example-term"},
		"h-nil":     nil,
	}
	want := map[string]string{
		"h-cmux": "cmux", "h-tmux": "tmux", "h-ghostty": "ghostty",
		"h-vscode": "vscode", "h-other": "unknown", "h-nil": "unknown",
	}
	pids := map[string]int{"h-cmux": 801, "h-tmux": 802, "h-ghostty": 803, "h-vscode": 804, "h-other": 805, "h-nil": 806}
	for sid, pid := range pids {
		e.Register(pid, sid, "/w/"+sid, "", true)
	}

	rec := &execRecorder{}
	sigs := []signal.Signaler{&signal.TmuxSignaler{RunCmd: rec.run}, &signal.CmuxSignaler{RunCmd: rec.run}}
	now := time.Unix(1_776_000_300, 0)
	p := &Poller{
		IdleThreshold: time.Hour,
		Now:           func() time.Time { return now },
		Monitor:       corpus.New(e.Facts, hostDiscovery{inner: e.Discovery(), hosts: hosts}),
		Providers:     provider.New(func() time.Time { return now }),
		Signalers:     sigs,
	}
	tree, _, err := assembleAndSnapshot(t, p, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			got[s.Session.SessionID] = s.Session.TerminalHost
		}
	}
	for sid, w := range want {
		if got[sid] != w {
			t.Errorf("%s: terminal host = %q, want %q", sid, got[sid], w)
		}
	}
	if n := rec.count(); n != 0 {
		t.Errorf("Discover/Assemble started %d processes, want 0: %v", n, rec.calls)
	}

	// Negative control: the detection the label path used before (ancestry walk
	// through the signalers) does run processes against the same shim.
	for _, pid := range pids {
		signal.ResolveSignaler(sigs, pid)
	}
	if rec.count() == 0 {
		t.Fatal("control: the previous detection started no process, so the counter proves nothing")
	}
}
