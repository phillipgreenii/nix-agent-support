package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/provider"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// recordingPS is a recording `ps` shim over a fixed fake process table. It is
// the RunCmd seam of the REAL TmuxSignaler and CmuxSignaler, so every ancestry
// walk the production signalers perform is counted as one exec, exactly as
// SentinelOne would count it (pg2-lcohce, spike pg2-57blt). Hermetic: it never
// execs a real ps.
type recordingPS struct {
	mu     sync.Mutex
	parent map[int]int
	comm   map[int]string
	args   map[int]string
	panes  string // `tmux list-panes` output
	walks  int    // `ps -o ppid=[,comm=] -p N` execs (one per ancestor hop)
	tables int    // `ps -A ...` whole-table enumerations
	other  int    // anything else
}

func (r *recordingPS) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch name {
	case "ps":
		if len(args) >= 4 && args[0] == "-o" && args[2] == "-p" {
			r.walks++
			pid, _ := strconv.Atoi(args[3])
			ppid, ok := r.parent[pid]
			if !ok {
				return []byte(""), nil
			}
			if args[1] == "ppid=,comm=" {
				return []byte(fmt.Sprintf("%d %s\n", ppid, r.comm[pid])), nil
			}
			return []byte(fmt.Sprintf("%d\n", ppid)), nil
		}
		if len(args) >= 3 && args[0] == "-A" {
			r.tables++
			var sb strings.Builder
			for pid, c := range r.comm {
				if args[2] == "pid,comm,args" {
					fmt.Fprintf(&sb, "%d %s %s\n", pid, c, r.args[pid])
				} else {
					fmt.Fprintf(&sb, "%d %s\n", pid, c)
				}
			}
			return []byte(sb.String()), nil
		}
	case "tmux":
		if len(args) >= 3 && args[2] == "list-panes" {
			return []byte(r.panes), nil
		}
	}
	r.other++
	return nil, fmt.Errorf("recordingPS: unexpected %s %v", name, args)
}

func (r *recordingPS) snapshot() (walks int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.walks
}

const (
	cmuxServerPID = 1409
	nCmuxSessions = 21
	tmuxSessPID   = 5000
	tmuxPanePID   = 5001
)

// newShimWorld builds the spike's 22-session population: 21 cmux sessions
// (claude -> zsh -> login -> cmux server -> launchd) and 1 tmux session
// (claude -> pane shell, the pane pid listed by tmux).
func newShimWorld() (*recordingPS, []*session.Session) {
	r := &recordingPS{
		parent: map[int]int{1: 0, cmuxServerPID: 1, 900: 1, tmuxPanePID: 900, tmuxSessPID: tmuxPanePID},
		comm:   map[int]string{1: "launchd", cmuxServerPID: "cmux", 900: "tmux", tmuxPanePID: "zsh", tmuxSessPID: "claude"},
		args:   map[int]string{900: "tmux -L sock"},
		panes:  fmt.Sprintf("%d main:0.0\n", tmuxPanePID),
	}
	var sessions []*session.Session
	for i := range nCmuxSessions {
		pid, sh, lg := 2000+i, 3000+i, 4000+i
		r.parent[pid], r.parent[sh], r.parent[lg] = sh, lg, cmuxServerPID
		r.comm[pid], r.comm[sh], r.comm[lg] = "claude", "zsh", "login"
		sessions = append(sessions, &session.Session{PID: pid, SessionID: fmt.Sprintf("cmux-%d", i), PidAlive: true})
	}
	sessions = append(sessions, &session.Session{PID: tmuxSessPID, SessionID: "tmux-0", PidAlive: true})
	return r, sessions
}

type tickCounts struct{ refine, surface, unsubmitted int }

// runTicks simulates n daemon ticks over the 22 sessions with the production
// signalers and a real provider.Cache. Per tick it mirrors, in order: the
// poller's cached host lookup + cmux refine walk (refineCmuxTerminalHost calls
// FindCmuxServerAncestor), then the nudger's HasSurface and HasUnsubmittedInput
// for each session through resolve. resolve is the code under test.
func runTicks(n int, resolve func(signalers []signal.Signaler, tree *aggregate.Tree) (hasSurface, hasUnsubmitted func(int) bool)) []tickCounts {
	r, sessions := newShimWorld()
	cmux := &signal.CmuxSignaler{RunCmd: r.run}
	tmux := &signal.TmuxSignaler{RunCmd: r.run}
	signalers := []signal.Signaler{tmux, cmux} // production order: tmux first

	clock := time.Unix(1_000_000, 0)
	cache := provider.New(func() time.Time { return clock })
	cache.FetchTerminalHost = func(pid int) string { return signal.DetectHost(signalers, pid) }

	var out []tickCounts
	for range n {
		var tc tickCounts
		cache.BeginScan()
		dir := &aggregate.Directory{Path: "/p"}
		start := r.snapshot()
		for _, s := range sessions {
			s.TerminalHost = cache.TerminalHost(s.SessionID, s.PID)
			if s.TerminalHost == "cmux" {
				cmux.FindCmuxServerAncestor(s.PID) // the refine walk (unchanged)
			}
			dir.Sessions = append(dir.Sessions, &aggregate.SessionView{Session: s})
		}
		cache.Reconcile(sessions)
		tc.refine = r.snapshot() - start

		hasSurface, hasUnsub := resolve(signalers, &aggregate.Tree{Dirs: []*aggregate.Directory{dir}})
		start = r.snapshot()
		for _, s := range sessions {
			hasSurface(s.PID)
		}
		tc.surface = r.snapshot() - start
		start = r.snapshot()
		for _, s := range sessions {
			hasUnsub(s.PID)
		}
		tc.unsubmitted = r.snapshot() - start
		out = append(out, tc)
		clock = clock.Add(5 * time.Second)
	}
	return out
}

func legacyResolve(signalers []signal.Signaler, _ *aggregate.Tree) (func(int) bool, func(int) bool) {
	return func(pid int) bool { return signal.ResolveSignaler(signalers, pid) != nil },
		func(pid int) bool { return signal.HasUnsubmittedInput(signalers, pid) }
}

func productionResolve(signalers []signal.Signaler, tree *aggregate.Tree) (func(int) bool, func(int) bool) {
	r := newSurfaceResolver(signalers, tree)
	return r.HasSurface, r.HasUnsubmittedInput
}

// TestSurfaceExecCount_PerTick_22Sessions proves the per-tick ps exec count
// with a NON-VACUOUS lower bound: the unchanged refine walk MUST still cost
// 3 execs per cmux session (21 x 3 = 63), so a shim that records nothing
// fails; and the legacy resolution MUST reproduce the spike's baseline
// (HasSurface = 63 cmux + 105 tmux-walk execs), so the "before" is measured by
// the same shim, not asserted from the spike's prose.
func TestSurfaceExecCount_PerTick_22Sessions(t *testing.T) {
	const ticks = 12
	before := runTicks(ticks, legacyResolve)
	after := runTicks(ticks, productionResolve)

	for i := 1; i < ticks; i++ { // tick 0 is cold
		b, a := before[i], after[i]
		t.Logf("tick %d: refine=%d | HasSurface before=%d after=%d | HasUnsubmittedInput before=%d after=%d | per-tick total before=%d after=%d",
			i, a.refine, b.surface, a.surface, b.unsubmitted, a.unsubmitted,
			b.refine+b.surface, a.refine+a.surface)

		// Lower bound: the refine walk is unchanged and not vacuous.
		if a.refine < 3*nCmuxSessions {
			t.Fatalf("tick %d: refine walk recorded %d execs, want >= %d (shim vacuous or refine walk lost)", i, a.refine, 3*nCmuxSessions)
		}
		if b.refine != a.refine {
			t.Errorf("tick %d: refine walk changed: before=%d after=%d (must be untouched)", i, b.refine, a.refine)
		}
		// Baseline reproduces the spike: 63 cmux + 105 tmux walks for 21 cmux
		// sessions (plus the tmux session's own cheap hit).
		if b.surface < 168 {
			t.Fatalf("tick %d: legacy HasSurface recorded %d execs, want >= 168 (baseline not reproduced)", i, b.surface)
		}
		// After: a cmux session costs 0 execs (cached host proves Detect); only
		// the one tmux session walks, and only its own tmux signaler (<= 2 hops).
		if a.surface > 2 {
			t.Errorf("tick %d: HasSurface after = %d execs, want <= 2 (cmux-only sessions must not walk)", i, a.surface)
		}
		if a.unsubmitted > 6 {
			t.Errorf("tick %d: HasUnsubmittedInput after = %d execs, want <= 6", i, a.unsubmitted)
		}
		if total := a.refine + a.surface; total > 3*nCmuxSessions+2 {
			t.Errorf("tick %d: per-tick total after = %d, want about 63 (<= 65)", i, total)
		}
	}
}
