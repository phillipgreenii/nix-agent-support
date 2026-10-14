package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/parknote"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// The needs_input stall bound (bead pg2-zybju, INV-CCH-29). These tests drive the
// real waitDone loop (the supervising dispatch's wait) on an injected clock with
// a fake ccpool pool runner and a fake bd runner: nothing sleeps, nothing shells
// out, and the ambient pool environment is scrubbed in every test.

var stallT0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

const (
	stallActor     = relActor // the drain role's own claim identity
	strikeLabelBd  = "update zr-d --add-label drain-needs-input-stall"
	releaseCallBd  = "update zr-d --status=open --assignee="
	releaseHumanBd = "update zr-d --add-label human --status=open --assignee="
)

// hermeticPoolEnv removes every CCPOOL_* and BEADS_* variable from the test's
// environment (restored afterwards): a pool session exports both, and a test that
// let them through would be testing the ambient pool instead of its fixture.
func hermeticPoolEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CCPOOL_") || strings.HasPrefix(k, "BEADS_") {
			t.Setenv(k, "") // registers the restore
			_ = os.Unsetenv(k)
		}
	}
}

// stallCall is one recorded ccpool call and the injected-clock time it was made at.
type stallCall struct {
	name string
	at   time.Time
}

// stallCC is a ccpool.Runner over ONE session row. script moves the row as a
// function of the injected clock (it runs before every List), so a timeline is
// written in wall-clock terms. Cancel and Close behave like ccpool's: a confirmed
// cancel resets the row to ready, a close stamps the handler reason and leaves the
// row's state alone (ADR 0015).
type stallCC struct {
	mu            sync.Mutex
	clk           *dtest.ManualClock
	row           ccpool.Session
	script        func(now time.Time, row *ccpool.Session)
	calls         []stallCall
	cancelToReady bool
	cancelErr     error
	closeErr      error
}

func (c *stallCC) record(name string) {
	c.calls = append(c.calls, stallCall{name, c.clk.Now()})
}

func (c *stallCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return nil
}

func (c *stallCC) Send(context.Context, string, string, ccpool.SendMode) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record("send")
	return nil
}

func (c *stallCC) Cancel(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record("cancel")
	if c.cancelErr == nil && c.cancelToReady {
		c.row.State = ccpool.StateReady
	}
	return c.cancelErr
}

func (c *stallCC) Close(_ context.Context, _ string, purge bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if purge {
		c.record("close-purge")
	} else {
		c.record("close")
	}
	if c.closeErr != nil {
		return c.closeErr
	}
	c.row.Live, c.row.CloseReason = false, "handler"
	return nil
}

func (c *stallCC) List(context.Context) ([]ccpool.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.script != nil && c.row.CloseReason == "" {
		c.script(c.clk.Now(), &c.row)
	}
	return []ccpool.Session{c.row}, nil
}

func (c *stallCC) Capacity(context.Context) (ccpool.Capacity, error) {
	return ccpool.Capacity{MaxSessions: 1, Free: 1}, nil
}

func (c *stallCC) SetMeta(context.Context, string, string, string) error { return nil }

// callNames lists the recorded call names in order.
func (c *stallCC) callNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, k := range c.calls {
		out = append(out, k.name)
	}
	return out
}

// firstCall returns the first recorded call of that name.
func (c *stallCC) firstCall(name string) (stallCall, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.firstCallLocked(name)
}

// firstCallLocked is firstCall for a caller already holding c.mu (a script runs
// inside List, under it).
func (c *stallCC) firstCallLocked(name string) (stallCall, bool) {
	for _, k := range c.calls {
		if k.name == name {
			return k, true
		}
	}
	return stallCall{}, false
}

// stallBD is a beads.Runner over ONE bead: a show reads its current state and an
// update applies the flags the handler uses, so what the handler wrote is
// observable as the bead's state AND as the exact calls it made.
type stallBD struct {
	mu       sync.Mutex
	status   string
	assignee string
	labels   []string
	showErr  error
	calls    []string // every Run, joined
}

func (b *stallBD) Run(_ context.Context, args ...string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, strings.Join(args, " "))
	switch args[0] {
	case "show":
		if b.showErr != nil {
			return "", b.showErr
		}
		labels, _ := json.Marshal(b.labels)
		return fmt.Sprintf(`{"id":%q,"status":%q,"assignee":%q,"labels":%s}`, args[1], b.status, b.assignee, labels), nil
	case "update":
		for i := 2; i < len(args); i++ {
			switch a := args[i]; a {
			case "--status=open":
				b.status = "open"
			case "--assignee=":
				b.assignee = ""
			case "--add-label":
				i++
				b.labels = append(b.labels, args[i])
			case "--remove-label":
				i++
				b.labels = slices.DeleteFunc(b.labels, func(l string) bool { return l == args[i] })
			}
		}
	}
	return "", nil
}

// withPrefix returns every recorded call that starts with prefix.
func (b *stallBD) withPrefix(prefix string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, c := range b.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// writes returns every recorded call that is not a read.
func (b *stallBD) writes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, c := range b.calls {
		if !strings.HasPrefix(c, "show ") {
			out = append(out, c)
		}
	}
	return out
}

func (b *stallBD) setShowErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.showErr = err
}

// stallRig wires one waitDone run: the injected clock, the two fakes, the event
// log and the fake transcript (lastWrite is the newest write time the stall bound
// can see).
type stallRig struct {
	t         *testing.T
	e         *ccpoolRun
	cc        *stallCC
	bd        *stallBD
	clk       *dtest.ManualClock
	d         DispatchContext
	logPath   string
	lastWrite time.Time
	onTick    func(now time.Time)
}

func newStallRig(t *testing.T, role roles.Role, mutate func(*config.Config)) *stallRig {
	t.Helper()
	hermeticPoolEnv(t)
	cfg := config.Default()
	cfg.PollInterval = 10 * time.Second
	cfg.MaxWait = 3 * time.Hour
	cfg.WorktreeQuietWindow = 2 * time.Minute
	cfg.Autonomous = true
	if mutate != nil {
		mutate(&cfg)
	}
	clk := &dtest.ManualClock{T: stallT0}
	cc := &stallCC{clk: clk, cancelToReady: true, row: ccpool.Session{
		ExternalID: relSession, Live: true, State: ccpool.StateWorking, TranscriptPath: "/fixture/transcript.jsonl",
	}}
	bd := &stallBD{status: "in_progress", assignee: stallActor}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lw.Now = clk.Now
	rig := &stallRig{
		t: t, cc: cc, bd: bd, clk: clk, logPath: logPath,
		d: DispatchContext{Role: role, Item: item.Item{ID: relBead}},
	}
	rig.e = &ccpoolRun{deps: Deps{
		CC: cc, BD: bd, Cfg: cfg, Log: lw, Now: clk.Now,
		LatestActivity: func(string) (time.Time, bool) { return rig.lastWrite, !rig.lastWrite.IsZero() },
	}}
	return rig
}

// run drives waitDone until it returns or the injected clock reaches stopAt (the
// context is then cancelled and waitDone returns context.Canceled).
func (r *stallRig) run(stopAt time.Time) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adv := r.clk.TickAdvancing()
	r.e.deps.Tick = func(ctx context.Context, d time.Duration) error {
		if err := adv(ctx, d); err != nil {
			return err
		}
		if r.onTick != nil {
			r.onTick(r.clk.Now())
		}
		if !r.clk.Now().Before(stopAt) {
			cancel()
		}
		return nil
	}
	return r.e.waitDone(ctx, nil, r.d, relSession)
}

func (r *stallRig) events() []map[string]any { return readEventLog(r.t, r.logPath) }

// enterNeedsInput is the ccpool hook edge: the row becomes needs_input and its
// activity stamp moves to that instant.
func enterNeedsInput(now time.Time, row *ccpool.Session) {
	row.State = ccpool.StateNeedsInput
	row.LastActivityAt = now.Unix()
}

// timeline: working until in, needs_input from in until out (zero = forever).
func needsInputBetween(in, out time.Time) func(time.Time, *ccpool.Session) {
	return func(now time.Time, row *ccpool.Session) {
		inside := !now.Before(in) && (out.IsZero() || now.Before(out))
		switch {
		case inside && row.State != ccpool.StateNeedsInput && row.State != ccpool.StateReady:
			enterNeedsInput(now, row)
		case !inside && row.State == ccpool.StateNeedsInput:
			row.State = ccpool.StateWorking
		}
	}
}

func mustStalled(t *testing.T, err error) *needsInputStalled {
	t.Helper()
	var ns *needsInputStalled
	if !errors.Is(err, ErrNeedsInputStalled) || !errors.As(err, &ns) {
		t.Fatalf("want ErrNeedsInputStalled, got %v", err)
	}
	return ns
}

// The full timeline: nothing before the bound, Escape at it, nothing during the
// grace, then the close and ONE release call, with the bead's own claim.
func TestStall_fullTimeline_escapeThenCloseAndSingleCallRelease(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	in := stallT0.Add(time.Minute) // working until 12:01:00, then the dialog opens
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})

	err := rig.run(stallT0.Add(3 * time.Hour))
	ns := mustStalled(t, err)
	if !ns.released || ns.escalated {
		t.Fatalf("first stall must release without escalating: %+v", ns)
	}

	if got, want := rig.cc.callNames(), []string{"cancel", "close"}; !slices.Equal(got, want) {
		t.Fatalf("ccpool calls = %v, want exactly Escape then close %v", got, want)
	}
	esc, _ := rig.cc.firstCall("cancel")
	cls, _ := rig.cc.firstCall("close")
	// Nothing before N = 15m of needs_input (12:01:00 + 15m); the Escape is on the
	// first poll that reaches it.
	if want := in.Add(15 * time.Minute); !esc.at.Equal(want) {
		t.Errorf("Escape at %s, want %s (needs_input for exactly 15m)", esc.at.Format(time.TimeOnly), want.Format(time.TimeOnly))
	}
	// The bounded gap: the close is the first poll at or after Escape + the 2m grace.
	if gap := cls.at.Sub(esc.at); gap < 2*time.Minute || gap > 2*time.Minute+10*time.Second {
		t.Errorf("Escape to close gap = %v, want the 2m grace (+ at most one 10s poll)", gap)
	}

	// ONE bd call releases the claim (status and assignee together); the strike
	// label and the comment are separate, after it.
	if rel := rig.bd.withPrefix("update zr-d --status"); len(rel) != 1 || rel[0] != releaseCallBd {
		t.Fatalf("release calls = %v, want exactly [%q]", rel, releaseCallBd)
	}
	if w := rig.bd.writes(); len(w) != 3 || w[0] != releaseCallBd || !strings.HasPrefix(w[1], "comment zr-d ") || w[2] != strikeLabelBd {
		t.Fatalf("bd writes = %q, want release, comment, strike label in that order", w)
	}
	if rig.bd.status != "open" || rig.bd.assignee != "" {
		t.Errorf("bead after the clear = %s/%q, want open and unassigned", rig.bd.status, rig.bd.assignee)
	}
	if v := verbOf(rig.e.waitFailureResult(rig.d.Role.CCPool, relBead, err)); v != report.Unclaimed {
		t.Errorf("reported verb = %q, want %q", v, report.Unclaimed)
	}

	// Surfaced as two structured events, each once.
	ev := rig.events()
	if n := countKind(ev, needsInputEscapeKind); n != 1 {
		t.Fatalf("%s events = %d, want 1: %v", needsInputEscapeKind, n, ev)
	}
	var cleared map[string]any
	for _, e := range ev {
		if e["kind"] == needsInputClearedKind {
			if cleared != nil {
				t.Fatalf("more than one %s event: %v", needsInputClearedKind, ev)
			}
			cleared = e
		}
	}
	if cleared == nil {
		t.Fatalf("no %s event: %v", needsInputClearedKind, ev)
	}
	if cleared["session"] != relSession || cleared["bead"] != relBead || cleared["role"] != "drain" ||
		cleared["released"] != true || cleared["escalated"] != false || cleared["bound_s"] != float64(15*60) {
		t.Errorf("cleared event = %v", cleared)
	}
}

// A session that leaves needs_input resets the timer: the bound counts from the
// RE-entry, not from the first entry.
//
// Two variants: the working phase writes to the transcript (the liveness guard
// would restart the clock by itself), and it does not (only the state edge can
// reset it), so the state reset is exercised on its own.
func TestStall_recoveryInBetweenResetsTheTimer(t *testing.T) {
	for _, writes := range []bool{true, false} {
		name := "working phase writes"
		if !writes {
			name = "working phase silent"
		}
		t.Run(name, func(t *testing.T) {
			rig := newStallRig(t, releaseRole(config.Default()), nil)
			first := stallT0.Add(time.Minute)
			back := stallT0.Add(10 * time.Minute) // answered and working again at 12:10:00
			again := stallT0.Add(12 * time.Minute)
			lastBeforeDialog := first.Add(-5 * time.Second)
			rig.cc.script = func(now time.Time, row *ccpool.Session) {
				switch {
				case now.Before(first):
				case now.Before(back):
					if row.State != ccpool.StateNeedsInput {
						enterNeedsInput(now, row)
					}
					rig.lastWrite = lastBeforeDialog
				case now.Before(again):
					row.State = ccpool.StateWorking
					if writes {
						rig.lastWrite = now // it is working: it writes
					}
				default:
					if row.State != ccpool.StateNeedsInput && row.State != ccpool.StateReady {
						enterNeedsInput(now, row)
						if writes {
							rig.lastWrite = now.Add(-5 * time.Second)
						}
					}
				}
			}
			// 12:16:00 would be 15m after the FIRST entry: nothing may happen then.
			rig.onTick = func(now time.Time) {
				if now.Before(again.Add(15*time.Minute)) && len(rig.cc.callNames()) != 0 {
					t.Fatalf("acted at %s, before 15m of needs_input since the re-entry at %s: %v",
						now.Format(time.TimeOnly), again.Format(time.TimeOnly), rig.cc.callNames())
				}
			}
			err := rig.run(stallT0.Add(3 * time.Hour))
			mustStalled(t, err)
			esc, _ := rig.cc.firstCall("cancel")
			if want := again.Add(15 * time.Minute); !esc.at.Equal(want) {
				t.Errorf("Escape at %s, want %s (15m after the re-entry)", esc.at.Format(time.TimeOnly), want.Format(time.TimeOnly))
			}
		})
	}
}

// The liveness guard: ccpool never leaves needs_input when a dialog is answered,
// so a healthy session that keeps writing must never be Escaped or closed.
func TestStall_staleNeedsInputWhileSessionStillWritesIsLeftAlone(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	in := stallT0.Add(time.Minute)
	rig.cc.script = needsInputBetween(in, time.Time{})
	rig.onTick = func(now time.Time) {
		if now.Minute()%5 == 0 && now.Second() == 0 { // a write every five minutes
			rig.lastWrite = now
		}
	}
	err := rig.run(stallT0.Add(90 * time.Minute))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the run to end by the test's own cancel, got %v", err)
	}
	if got := rig.cc.callNames(); len(got) != 0 {
		t.Fatalf("a session that is still writing was acted on: %v", got)
	}
	if w := rig.bd.writes(); len(w) != 0 {
		t.Fatalf("a session that is still writing got bead writes: %q", w)
	}
}

// The Escape worked: the session wrote after it, so it is neither closed nor
// released, and it can finish normally.
func TestStall_escapeThatRevivesTheSessionIsNotClosed(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	base := needsInputBetween(in, time.Time{})
	rig.cc.script = func(now time.Time, row *ccpool.Session) {
		base(now, row)
		if esc, ok := rig.cc.firstCallLocked("cancel"); ok && now.After(esc.at.Add(20*time.Second)) {
			row.State = ccpool.StateWorking // it carried on after the dialog was dismissed
			rig.lastWrite = now
		}
	}
	done := stallT0.Add(45 * time.Minute)
	rig.onTick = func(now time.Time) {
		if !now.Before(done) {
			rig.bd.mu.Lock()
			rig.bd.status = "closed"
			rig.bd.mu.Unlock()
		}
	}
	if err := rig.run(stallT0.Add(3 * time.Hour)); err != nil {
		t.Fatalf("a revived session that completes its bead is a success, got %v", err)
	}
	if got := rig.cc.callNames(); !slices.Equal(got, []string{"cancel"}) {
		t.Fatalf("ccpool calls = %v, want only the Escape", got)
	}
	if w := rig.bd.writes(); len(w) != 0 {
		t.Fatalf("a revived session got bead writes: %q", w)
	}
	if n := countKind(rig.events(), needsInputClearedKind); n != 0 {
		t.Fatalf("a revived session logged %d %s events", n, needsInputClearedKind)
	}
}

// An Escape that could not be confirmed (the row stays needs_input) is followed by
// the close after the same grace.
func TestStall_unconfirmedEscapeStillEndsInClose(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	rig.cc.cancelErr = errors.New("ccpool cancel: exit 6")
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})
	err := rig.run(stallT0.Add(3 * time.Hour))
	mustStalled(t, err)
	if got := rig.cc.callNames(); !slices.Equal(got, []string{"cancel", "close"}) {
		t.Fatalf("ccpool calls = %v", got)
	}
	var esc map[string]any
	for _, e := range rig.events() {
		if e["kind"] == needsInputEscapeKind {
			esc = e
		}
	}
	if esc == nil || esc["escape_ok"] != false || esc["escape_error"] == nil {
		t.Errorf("the escape event must record the failed Escape: %v", esc)
	}
}

// The claim guard: only a bead still in_progress under the role's own actor is
// released. Anything else is never written to; the stalled session is still closed.
func TestStall_claimGuard(t *testing.T) {
	cases := []struct {
		name         string
		status       string
		assignee     string
		wantWrites   int
		wantPeerHeld bool
		wantVerb     report.Verb
	}{
		{"held by a peer", "in_progress", "someone-else", 0, true, ""},
		{"already reassigned and open", "open", "someone-else", 0, true, ""},
		{"open and unassigned", "open", "", 0, false, ""},
		{"deferred and unassigned", "deferred", "", 0, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newStallRig(t, releaseRole(config.Default()), nil)
			rig.bd.status, rig.bd.assignee = tc.status, tc.assignee
			in := stallT0.Add(time.Minute)
			rig.lastWrite = in.Add(-5 * time.Second)
			rig.cc.script = needsInputBetween(in, time.Time{})
			err := rig.run(stallT0.Add(3 * time.Hour))
			if tc.wantPeerHeld {
				if !errors.Is(err, ErrPeerHeld) {
					t.Fatalf("want ErrPeerHeld, got %v", err)
				}
			} else {
				ns := mustStalled(t, err)
				if ns.released || ns.escalated {
					t.Fatalf("nothing to release, got %+v", ns)
				}
			}
			if got := rig.cc.callNames(); !slices.Equal(got, []string{"cancel", "close"}) {
				t.Fatalf("the stalled session must still be cleared: %v", got)
			}
			if w := rig.bd.writes(); len(w) != tc.wantWrites {
				t.Fatalf("the bead must not be written to, got %q", w)
			}
			if v := verbOf(rig.e.waitFailureResult(rig.d.Role.CCPool, relBead, err)); v != tc.wantVerb {
				t.Errorf("verb = %q, want none", v)
			}
		})
	}
}

// A bead that closed while the row still read needs_input is left to the
// completion check: the stall bound does not close the finishing session.
func TestStall_beadClosedMeanwhileIsNotCleared(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})
	rig.onTick = func(now time.Time) {
		if !now.Before(in.Add(17*time.Minute)) && !now.After(in.Add(17*time.Minute+time.Second)) {
			rig.bd.mu.Lock()
			rig.bd.status = "closed"
			rig.bd.mu.Unlock()
		}
	}
	if err := rig.run(stallT0.Add(3 * time.Hour)); err != nil {
		t.Fatalf("a closed bead is done: %v", err)
	}
	if got := rig.cc.callNames(); slices.Contains(got, "close") {
		t.Fatalf("a finishing session was closed: %v", got)
	}
}

// The second consecutive stall of one bead escalates in the SAME call as the
// release, so a bead that wedges every worker stops being re-offered.
func TestStall_secondConsecutiveStallAddsHumanInOneCall(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	rig.bd.labels = []string{drainStallLabel}
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})
	err := rig.run(stallT0.Add(3 * time.Hour))
	ns := mustStalled(t, err)
	if !ns.released || !ns.escalated {
		t.Fatalf("second strike must release and escalate: %+v", ns)
	}
	w := rig.bd.writes()
	if len(w) != 2 || w[1] != releaseHumanBd || !strings.HasPrefix(w[0], "comment zr-d "+parknote.Marker) {
		t.Fatalf("bd writes = %q, want the park note, then ONE human+release call", w)
	}
	for _, want := range []string{"needs_input stall bound", "No person-question was identified", "Session: ", "role drain"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("the park note must contain %q; got %q", want, w[0])
		}
	}
	if v := verbOf(rig.e.waitFailureResult(rig.d.Role.CCPool, relBead, err)); v != report.Escalated {
		t.Errorf("verb = %q, want %q", v, report.Escalated)
	}
}

// The bead cannot be read at the moment of the clear: nothing is closed (a closed
// session with an unreadable claim would strand it); the next poll retries.
func TestStall_unreadableBeadDefersTheCloseToTheNextPoll(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})
	clearAt := in.Add(15*time.Minute + 2*time.Minute)
	rig.onTick = func(now time.Time) {
		switch {
		case now.Equal(clearAt.Add(-10 * time.Second)):
			rig.bd.setShowErr(errors.New("bd: transient"))
		case now.Equal(clearAt.Add(10 * time.Second)):
			rig.bd.setShowErr(nil)
		}
	}
	err := rig.run(stallT0.Add(3 * time.Hour))
	mustStalled(t, err)
	cls, _ := rig.cc.firstCall("close")
	if want := clearAt.Add(10 * time.Second); !cls.at.Equal(want) {
		t.Errorf("closed at %s, want %s: the first poll at which the bead was readable again (the unreadable polls close nothing)",
			cls.at.Format(time.TimeOnly), want.Format(time.TimeOnly))
	}
}

// A close that fails leaves the claim alone and ends the dispatch with an error:
// releasing under a live session would hand the bead to a second worker.
func TestStall_failedCloseLeavesTheClaim(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	rig.cc.closeErr = errors.New("ccpool close: boom")
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})
	err := rig.run(stallT0.Add(3 * time.Hour))
	if err == nil || errors.Is(err, ErrNeedsInputStalled) {
		t.Fatalf("a failed close must surface as its own error, got %v", err)
	}
	if w := rig.bd.writes(); len(w) != 0 {
		t.Fatalf("the claim was written to under a live session: %q", w)
	}
}

// The control: a role that is not a drain role keeps ADR 0037's rule. The same
// needs_input timeline, run for four times the bound, makes no ccpool call and
// no bead write.
func TestStall_nonDrainRoleIsUntouched(t *testing.T) {
	for _, mk := range []func(config.Config) roles.Role{workerRole, reviewRole, feedbackRole} {
		role := mk(config.Default())
		t.Run(role.Name, func(t *testing.T) {
			rig := newStallRig(t, role, nil)
			rig.bd.assignee = role.CCPool.Actor
			in := stallT0.Add(time.Minute)
			rig.lastWrite = in.Add(-5 * time.Second)
			rig.cc.script = needsInputBetween(in, time.Time{})
			err := rig.run(stallT0.Add(60 * time.Minute))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("want the run to end by the test's own cancel, got %v", err)
			}
			if got := rig.cc.callNames(); len(got) != 0 {
				t.Fatalf("a non-drain session was acted on: %v", got)
			}
			if w := rig.bd.writes(); len(w) != 0 {
				t.Fatalf("a non-drain bead was written to: %q", w)
			}
			if n := countKind(rig.events(), needsInputEscapeKind) + countKind(rig.events(), needsInputClearedKind); n != 0 {
				t.Fatalf("a non-drain role logged %d stall events", n)
			}
		})
	}
}

// Configuration: the handler-wide value 0 is the kill switch, and a role's own
// override moves the bound for that role only.
func TestStall_configurableBound(t *testing.T) {
	t.Run("handler-wide 0 disables", func(t *testing.T) {
		rig := newStallRig(t, releaseRole(config.Default()), func(c *config.Config) { c.NeedsInputStall = 0 })
		in := stallT0.Add(time.Minute)
		rig.lastWrite = in.Add(-5 * time.Second)
		rig.cc.script = needsInputBetween(in, time.Time{})
		if err := rig.run(stallT0.Add(60 * time.Minute)); !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancel, got %v", err)
		}
		if got := rig.cc.callNames(); len(got) != 0 {
			t.Fatalf("a disabled bound acted: %v", got)
		}
	})
	t.Run("handler-wide value", func(t *testing.T) {
		rig := newStallRig(t, releaseRole(config.Default()), func(c *config.Config) { c.NeedsInputStall = 5 * time.Minute })
		in := stallT0.Add(time.Minute)
		rig.lastWrite = in.Add(-5 * time.Second)
		rig.cc.script = needsInputBetween(in, time.Time{})
		mustStalled(t, rig.run(stallT0.Add(3*time.Hour)))
		if esc, _ := rig.cc.firstCall("cancel"); !esc.at.Equal(in.Add(5 * time.Minute)) {
			t.Errorf("Escape at %s, want 5m after entry", esc.at.Format(time.TimeOnly))
		}
	})
	t.Run("role override", func(t *testing.T) {
		role := releaseRole(config.Default())
		role.CCPool.NeedsInputStall = 8 * time.Minute
		rig := newStallRig(t, role, nil)
		in := stallT0.Add(time.Minute)
		rig.lastWrite = in.Add(-5 * time.Second)
		rig.cc.script = needsInputBetween(in, time.Time{})
		mustStalled(t, rig.run(stallT0.Add(3*time.Hour)))
		if esc, _ := rig.cc.firstCall("cancel"); !esc.at.Equal(in.Add(8 * time.Minute)) {
			t.Errorf("Escape at %s, want 8m after entry", esc.at.Format(time.TimeOnly))
		}
	})
}

// A handler restart (ADR 0085's absorb) must not grant a fresh bound: the row's
// last_activity_at and the quiet transcript show the session has been blocked
// since before this dispatch started watching it.
func TestStall_absorbedSessionCountsFromItsLastActivity(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	blocked := stallT0.Add(-20 * time.Minute)
	rig.lastWrite = blocked.Add(-5 * time.Second)
	rig.cc.row.State = ccpool.StateNeedsInput
	rig.cc.row.LastActivityAt = blocked.Unix()
	mustStalled(t, rig.run(stallT0.Add(3*time.Hour)))
	if esc, _ := rig.cc.firstCall("cancel"); !esc.at.Equal(stallT0) {
		t.Errorf("Escape at %s, want on the first poll (already blocked for 20m)", esc.at.Format(time.TimeOnly))
	}
}

// A transcript that cannot be observed is no evidence of life: the bound still
// runs (from the first observation, never from an unverified activity stamp).
func TestStall_unobservableTranscriptDoesNotBlockTheBound(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), nil)
	rig.cc.row.State = ccpool.StateNeedsInput
	rig.cc.row.LastActivityAt = stallT0.Add(-3 * time.Hour).Unix() // stale and unverifiable
	mustStalled(t, rig.run(stallT0.Add(3*time.Hour)))
	if esc, _ := rig.cc.firstCall("cancel"); !esc.at.Equal(stallT0.Add(15 * time.Minute)) {
		t.Errorf("Escape at %s, want 15m after the first observation", esc.at.Format(time.TimeOnly))
	}
}

func TestNeedsInputBound_appliesToDrainRolesOnly(t *testing.T) {
	cfg := config.Default()
	r := &ccpoolRun{deps: Deps{Cfg: cfg}}
	if got := r.needsInputBound(releaseRole(cfg).CCPool); got != 15*time.Minute {
		t.Errorf("drain role bound = %v, want the 15m default", got)
	}
	for _, mk := range []func(config.Config) roles.Role{workerRole, reviewRole, feedbackRole, triagerRole} {
		role := mk(cfg)
		if got := r.needsInputBound(role.CCPool); got != 0 {
			t.Errorf("role %s (completion %s) bound = %v, want none", role.Name, role.CCPool.Completion, got)
		}
	}
	if got := r.needsInputBound(nil); got != 0 {
		t.Errorf("nil role config bound = %v", got)
	}
}

// waitFailureResult: the three shapes of a stall clear.
func TestWaitFailureResult_needsInputStalled(t *testing.T) {
	r := &ccpoolRun{}
	cc := releaseRole(config.Default()).CCPool
	cases := []struct {
		err  *needsInputStalled
		want report.Verb
	}{
		{&needsInputStalled{released: true}, report.Unclaimed},
		{&needsInputStalled{released: true, escalated: true}, report.Escalated},
		{&needsInputStalled{}, ""},
	}
	for _, c := range cases {
		if got := verbOf(r.waitFailureResult(cc, relBead, fmt.Errorf("%s: %w", relBead, c.err))); got != c.want {
			t.Errorf("%+v: verb = %q, want %q", c.err, got, c.want)
		}
	}
	if !errors.Is(&needsInputStalled{}, ErrNeedsInputStalled) {
		t.Error("needsInputStalled must match ErrNeedsInputStalled")
	}
}
