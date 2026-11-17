package ladder

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
)

var (
	t0            = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	g             = Grace{Interrupt: 5 * time.Minute, Kill: 2 * time.Minute}
	contextPoints = config.Points{Nudge: 0.8, Interrupt: 0.95, Kill: 1.0}
	blockPoints   = config.Points{Nudge: 0.95, Interrupt: 0.98, Kill: 1.0}
)

func contextMeter(f float64, detail string) Meter {
	return Meter{Name: "context", Fraction: f, Points: contextPoints, Detail: detail}
}

func blockMeter(name string, f float64) Meter {
	return Meter{Name: name, Fraction: f, Points: blockPoints}
}

func enabled(now time.Time, meters ...Meter) Input {
	return Input{Now: now, Enabled: true, Meters: meters}
}

func disabled(now time.Time, meters ...Meter) Input {
	return Input{Now: now, Enabled: false, Meters: meters}
}

// want is one expected Step result. A zero reason means "do not check".
type want struct {
	act    Action
	rung   Rung
	kind   Kind
	at     time.Time
	reason string
}

// check runs Step and compares the result with w, returning the new state so
// a test can thread it into the next call.
func check(t *testing.T, label string, s State, in Input, w want) State {
	t.Helper()
	got, act, reason := Step(s, in, g)
	if act != w.act {
		t.Errorf("%s: action = %v, want %v (reason %q)", label, act, w.act, reason)
	}
	if got.Rung != w.rung {
		t.Errorf("%s: rung = %v, want %v", label, got.Rung, w.rung)
	}
	if got.Kind != w.kind {
		t.Errorf("%s: kind = %v, want %v", label, got.Kind, w.kind)
	}
	if !got.At.Equal(w.at) {
		t.Errorf("%s: at = %v, want %v", label, got.At, w.at)
	}
	if w.reason != "" && reason != w.reason {
		t.Errorf("%s: reason = %q, want %q", label, reason, w.reason)
	}
	if w.act == ActNone && reason != "" {
		t.Errorf("%s: reason = %q with ActNone, want empty", label, reason)
	}
	if w.act != ActNone && reason == "" {
		t.Errorf("%s: empty reason for %v", label, act)
	}
	return got
}

func TestBelowNudgeDoesNothing(t *testing.T) {
	check(t, "context 0.75", State{}, enabled(t0, contextMeter(0.75, "150000 of 200000 tokens")),
		want{act: ActNone, rung: RungNone, kind: KindMeter})
}

func TestContextNudge(t *testing.T) {
	s, act, reason := Step(State{}, enabled(t0, contextMeter(0.80, "160000 of 200000 tokens")), g)
	if act != ActNudge {
		t.Fatalf("action = %v, want %v", act, ActNudge)
	}
	if !strings.Contains(reason, "context") || !strings.Contains(reason, "80%") {
		t.Errorf("reason = %q, want it to name context and 80%%", reason)
	}
	if want := "context at 80% (160000 of 200000 tokens)"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if want := (State{Kind: KindMeter, Rung: RungNudge, At: t0}); s != want {
		t.Errorf("state = %+v, want %+v", s, want)
	}
}

func TestReasonWithoutDetail(t *testing.T) {
	_, _, reason := Step(State{}, enabled(t0, blockMeter("five_hour", 0.96)), g)
	if want := "five_hour at 96%"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestReasonPercentIsNotRoundedUp(t *testing.T) {
	// 0.999 is below the kill point; the reason must not claim 100%.
	_, _, reason := Step(State{Kind: KindMeter, Rung: RungNudge, At: t0},
		enabled(t0, contextMeter(0.999, "")), g)
	if want := "context at 99%"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestOneRungPerStep(t *testing.T) {
	in := enabled(t0, contextMeter(1.0, "200000 of 200000 tokens"))
	s := check(t, "call 1", State{}, in, want{act: ActNudge, rung: RungNudge, kind: KindMeter, at: t0})
	s = check(t, "call 2", s, in, want{act: ActInterrupt, rung: RungInterrupt, kind: KindMeter, at: t0})
	check(t, "call 3", s, in, want{act: ActKill, rung: RungKill, kind: KindMeter, at: t0})
}

func TestHighestMeterWins(t *testing.T) {
	cases := []struct {
		name   string
		meters []Meter
		names  string // the meter the reason must name
		other  string // a meter the reason must not name
	}{
		{"context first", []Meter{contextMeter(0.5, ""), blockMeter("five_hour", 0.98)}, "five_hour", "context"},
		{"block first", []Meter{blockMeter("five_hour", 0.98), contextMeter(0.5, "")}, "five_hour", "context"},
		// The higher rung wins, not the higher fraction: five_hour 0.97 is
		// only a nudge against the block points, context 0.96 an interrupt.
		{"rung not fraction", []Meter{blockMeter("five_hour", 0.97), contextMeter(0.96, "")}, "context", "five_hour"},
		// Two meters on the same rung: the reason names the first one.
		{"tie names the first", []Meter{contextMeter(0.96, ""), blockMeter("five_hour", 0.98)}, "context", "five_hour"},
		{"tie names the first, reversed", []Meter{blockMeter("five_hour", 0.98), contextMeter(0.96, "")}, "five_hour", "context"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := enabled(t0, c.meters...)
			s := State{}
			for i, w := range []want{
				{act: ActNudge, rung: RungNudge, kind: KindMeter, at: t0},
				{act: ActInterrupt, rung: RungInterrupt, kind: KindMeter, at: t0},
			} {
				next, act, reason := Step(s, in, g)
				if act != w.act || next.Rung != w.rung {
					t.Fatalf("call %d: action %v rung %v, want %v %v", i+1, act, next.Rung, w.act, w.rung)
				}
				if !strings.HasPrefix(reason, c.names+" ") || strings.Contains(reason, c.other) {
					t.Errorf("call %d: reason = %q, want it to name %s only", i+1, reason, c.names)
				}
				s = next
			}
			// The target (interrupt) is reached and no time has passed.
			check(t, "call 3", s, in, want{act: ActNone, rung: RungInterrupt, kind: KindMeter, at: t0})
		})
	}
}

func TestBlockMeterLadder(t *testing.T) {
	for _, name := range []string{"five_hour", "seven_day"} {
		t.Run(name, func(t *testing.T) {
			s := check(t, "0.95", State{}, enabled(t0, blockMeter(name, 0.95)),
				want{act: ActNudge, rung: RungNudge, kind: KindMeter, at: t0, reason: name + " at 95%"})
			s = check(t, "0.98", s, enabled(t0.Add(time.Minute), blockMeter(name, 0.98)),
				want{act: ActInterrupt, rung: RungInterrupt, kind: KindMeter, at: t0.Add(time.Minute), reason: name + " at 98%"})
			check(t, "1.0", s, enabled(t0.Add(2*time.Minute), blockMeter(name, 1.0)),
				want{act: ActKill, rung: RungKill, kind: KindMeter, at: t0.Add(2 * time.Minute), reason: name + " at 100%"})
		})
	}
}

func TestNeverMovesDown(t *testing.T) {
	s := State{Kind: KindMeter, Rung: RungInterrupt, At: t0}
	check(t, "context 0.05", s, enabled(t0.Add(time.Minute), contextMeter(0.05, "")),
		want{act: ActNone, rung: RungInterrupt, kind: KindMeter, at: t0})
}

func TestMeterNudgeThenIdleEscalates(t *testing.T) {
	s := State{Kind: KindMeter, Rung: RungNudge, At: t0}
	m := contextMeter(0.81, "162000 of 200000 tokens")
	check(t, "+4m59s", s, enabled(t0.Add(4*time.Minute+59*time.Second), m),
		want{act: ActNone, rung: RungNudge, kind: KindMeter, at: t0})
	check(t, "+5m", s, enabled(t0.Add(5*time.Minute), m),
		want{
			act: ActInterrupt, rung: RungInterrupt, kind: KindMeter, at: t0.Add(5 * time.Minute),
			reason: "no exit 5m0s after the nudge",
		})
}

func TestTimeFallbackWithoutMeters(t *testing.T) {
	s := State{Kind: KindMeter, Rung: RungNudge, At: t0}
	check(t, "no meters +5m", s, enabled(t0.Add(5*time.Minute)),
		want{act: ActInterrupt, rung: RungInterrupt, kind: KindMeter, at: t0.Add(5 * time.Minute)})
}

func TestInterruptTimeFallback(t *testing.T) {
	cases := []struct {
		name   string
		meters []Meter
	}{
		{"no meters", nil},
		{"meter at interrupt", []Meter{contextMeter(0.96, "")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := State{Kind: KindMeter, Rung: RungInterrupt, At: t0}
			check(t, "+119s", s, enabled(t0.Add(119*time.Second), c.meters...),
				want{act: ActNone, rung: RungInterrupt, kind: KindMeter, at: t0})
			check(t, "+2m", s, enabled(t0.Add(2*time.Minute), c.meters...),
				want{
					act: ActKill, rung: RungKill, kind: KindMeter, at: t0.Add(2 * time.Minute),
					reason: "no exit 2m0s after the interrupt",
				})
		})
	}
}

func TestForceKillRepeats(t *testing.T) {
	cases := []struct {
		name   string
		kind   Kind
		in     func(time.Time) Input
		reason string
	}{
		{"meter", KindMeter, func(now time.Time) Input { return enabled(now) }, "still running after the stop"},
		{"meter below its points", KindMeter, func(now time.Time) Input { return enabled(now, contextMeter(0.1, "")) }, "still running after the stop"},
		{"disabled", KindDisabled, func(now time.Time) Input { return disabled(now) }, "disabled: still running after the stop"},
		{"exit", KindExit, func(now time.Time) Input { return Input{Now: now, Enabled: true} }, "exit requested: still running after the stop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
			s := check(t, "from kill", State{Kind: c.kind, Rung: RungKill, At: t0}, c.in(t1),
				want{act: ActForceKill, rung: RungForceKill, kind: c.kind, at: t1, reason: c.reason})
			// Repeated every call; At stays when the force kill first fired.
			check(t, "from force kill", s, c.in(t2),
				want{act: ActForceKill, rung: RungForceKill, kind: c.kind, at: t1, reason: c.reason})
		})
	}
}

func TestKillAboveOne(t *testing.T) {
	pts := config.Points{Nudge: 0.8, Interrupt: 0.95, Kill: 1.05}
	s := State{Kind: KindMeter, Rung: RungInterrupt, At: t0}
	now := t0.Add(time.Minute)
	check(t, "1.025", s, enabled(now, Meter{Name: "context", Fraction: 1.025, Points: pts}),
		want{act: ActNone, rung: RungInterrupt, kind: KindMeter, at: t0})
	check(t, "1.05", s, enabled(now, Meter{Name: "context", Fraction: 1.05, Points: pts}),
		want{act: ActKill, rung: RungKill, kind: KindMeter, at: now, reason: "context at 105%"})
}

func TestDisabledLadderTiming(t *testing.T) {
	nudgeAt := t0
	interruptAt := nudgeAt.Add(5 * time.Minute)
	killAt := interruptAt.Add(2 * time.Minute)
	s := check(t, "first tick", State{}, disabled(nudgeAt),
		want{act: ActNudge, rung: RungNudge, kind: KindDisabled, at: nudgeAt, reason: "disabled"})
	s = check(t, "+4m59s", s, disabled(nudgeAt.Add(4*time.Minute+59*time.Second)),
		want{act: ActNone, rung: RungNudge, kind: KindDisabled, at: nudgeAt})
	s = check(t, "+5m", s, disabled(interruptAt),
		want{
			act: ActInterrupt, rung: RungInterrupt, kind: KindDisabled, at: interruptAt,
			reason: "disabled: no exit 5m0s after the nudge",
		})
	s = check(t, "+1m59s after interrupt", s, disabled(interruptAt.Add(119*time.Second)),
		want{act: ActNone, rung: RungInterrupt, kind: KindDisabled, at: interruptAt})
	s = check(t, "+2m after interrupt", s, disabled(killAt),
		want{
			act: ActKill, rung: RungKill, kind: KindDisabled, at: killAt,
			reason: "disabled: no exit 2m0s after the interrupt",
		})
	check(t, "next tick", s, disabled(killAt.Add(time.Minute)),
		want{act: ActForceKill, rung: RungForceKill, kind: KindDisabled, at: killAt.Add(time.Minute)})
}

func TestDisabledBeatsMeters(t *testing.T) {
	_, act, reason := Step(State{}, disabled(t0, contextMeter(0.96, "192000 of 200000 tokens")), g)
	if act != ActNudge {
		t.Fatalf("action = %v, want %v", act, ActNudge)
	}
	if !strings.Contains(reason, "disabled") || strings.Contains(reason, "context") {
		t.Errorf("reason = %q, want a disabled reason, not a context one", reason)
	}
	// Even a meter at its kill point does not speed the disabled ladder up.
	s := State{Kind: KindDisabled, Rung: RungNudge, At: t0}
	check(t, "context at kill", s, disabled(t0.Add(time.Minute), contextMeter(1.0, "")),
		want{act: ActNone, rung: RungNudge, kind: KindDisabled, at: t0})
}

func TestExitRequestWins(t *testing.T) {
	s := State{Kind: KindMeter, Rung: RungNudge, At: t0}
	in := enabled(t0.Add(time.Minute), contextMeter(0.85, ""))
	in.ExitRequested = true
	s = check(t, "request on file", s, in,
		want{act: ActGracefulStop, rung: RungKill, kind: KindExit, at: t0.Add(time.Minute), reason: "exit requested"})
	check(t, "request no longer set", s, enabled(t0.Add(2*time.Minute), contextMeter(0.5, "")),
		want{act: ActForceKill, rung: RungForceKill, kind: KindExit, at: t0.Add(2 * time.Minute)})
}

func TestExitBelowKillStartsGracefulStop(t *testing.T) {
	for _, r := range []Rung{RungNone, RungNudge, RungInterrupt} {
		t.Run(r.String(), func(t *testing.T) {
			in := enabled(t0.Add(time.Minute))
			in.ExitRequested = true
			check(t, "exit", State{Kind: KindMeter, Rung: r, At: t0}, in,
				want{act: ActGracefulStop, rung: RungKill, kind: KindExit, at: t0.Add(time.Minute)})
		})
	}
}

func TestExitAtKillForceKills(t *testing.T) {
	// A session the meter ladder already signalled is not stopped gracefully.
	in := enabled(t0.Add(time.Minute))
	in.ExitRequested = true
	check(t, "exit at kill", State{Kind: KindMeter, Rung: RungKill, At: t0}, in,
		want{act: ActForceKill, rung: RungForceKill, kind: KindExit, at: t0.Add(time.Minute)})
}

func TestExitBeatsDisabled(t *testing.T) {
	s := State{Kind: KindDisabled, Rung: RungNudge, At: t0}
	in := disabled(t0.Add(time.Minute))
	in.ExitRequested = true
	s = check(t, "disabled and exit requested", s, in,
		want{act: ActGracefulStop, rung: RungKill, kind: KindExit, at: t0.Add(time.Minute)})
	// Kind stays exit once set, even over the disabled ladder.
	check(t, "disabled, request gone", s, disabled(t0.Add(2*time.Minute)),
		want{act: ActForceKill, rung: RungForceKill, kind: KindExit, at: t0.Add(2 * time.Minute)})
}

func TestKindChangeKeepsRung(t *testing.T) {
	t.Run("meter to disabled", func(t *testing.T) {
		s := State{Kind: KindMeter, Rung: RungInterrupt, At: t0}
		s = check(t, "disabled at T+1m", s, disabled(t0.Add(time.Minute)),
			want{act: ActNone, rung: RungInterrupt, kind: KindDisabled, at: t0})
		check(t, "disabled at T+2m", s, disabled(t0.Add(2*time.Minute)),
			want{act: ActKill, rung: RungKill, kind: KindDisabled, at: t0.Add(2 * time.Minute)})
	})
	t.Run("disabled to meter", func(t *testing.T) {
		s := State{Kind: KindDisabled, Rung: RungNudge, At: t0}
		s = check(t, "enabled at T+4m", s, enabled(t0.Add(4*time.Minute)),
			want{act: ActNone, rung: RungNudge, kind: KindMeter, at: t0})
		check(t, "enabled at T+5m", s, enabled(t0.Add(5*time.Minute)),
			want{act: ActInterrupt, rung: RungInterrupt, kind: KindMeter, at: t0.Add(5 * time.Minute)})
	})
}

func TestNoMetersAtRungNone(t *testing.T) {
	s := check(t, "first tick", State{}, enabled(t0),
		want{act: ActNone, rung: RungNone, kind: KindMeter})
	// An idle session that was never nudged is left alone, however long.
	check(t, "a day later", s, enabled(t0.Add(24*time.Hour)),
		want{act: ActNone, rung: RungNone, kind: KindMeter})
}

func TestMeterWithoutPointsSetsNoTarget(t *testing.T) {
	// A zero Points would put every fraction at or above "kill"; such a meter
	// is ignored rather than killing the session.
	check(t, "zero points", State{}, enabled(t0, Meter{Name: "context", Fraction: 0.1}),
		want{act: ActNone, rung: RungNone, kind: KindMeter})
}

// NudgeIgnored is the signal behind status's "did not exit after the nudge"
// hint: it is set when the interrupt fires once grace.interrupt has passed
// since the nudge, whichever reason wins the step, and never otherwise.
func TestNudgeIgnored(t *testing.T) {
	nudged := func(k Kind) State { return State{Kind: k, Rung: RungNudge, At: t0} }
	due := t0.Add(5 * time.Minute)
	early := t0.Add(time.Minute)
	exitIn := enabled(due)
	exitIn.ExitRequested = true
	cases := []struct {
		name       string
		s          State
		in         Input
		act        Action
		reasonHas  string
		reasonNot  string
		wantIgnore bool
	}{
		{"time step, no meters", nudged(KindMeter), enabled(due), ActInterrupt, "after the nudge", "", true},
		{"time step, meter below interrupt", nudged(KindMeter), enabled(due, contextMeter(0.85, "")), ActInterrupt, "after the nudge", "", true},
		{"meter crosses before the grace", nudged(KindMeter), enabled(early, contextMeter(0.96, "")), ActInterrupt, "context at 96%", "", false},
		// Both due on the same call: the meter wins the reason, and the hint
		// still holds, because the session did not exit within the grace.
		{"meter and time step both due", nudged(KindMeter), enabled(due, contextMeter(0.96, "")), ActInterrupt, "context at 96%", "after the nudge", true},
		{"block meter and time step both due", nudged(KindMeter), enabled(due, blockMeter("five_hour", 0.98)), ActInterrupt, "five_hour at 98%", "after the nudge", true},
		// The disabled ladder's interrupt is always a time step.
		{"disabled ladder", nudged(KindDisabled), disabled(due), ActInterrupt, "after the nudge", "", true},
		{"exit request", nudged(KindMeter), exitIn, ActGracefulStop, "exit requested", "", false},
		{"before the grace, no meter", nudged(KindMeter), enabled(early), ActNone, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, act, reason := Step(c.s, c.in, g)
			if act != c.act {
				t.Fatalf("action = %v, want %v (reason %q)", act, c.act, reason)
			}
			if !strings.Contains(reason, c.reasonHas) {
				t.Errorf("reason = %q, want it to contain %q", reason, c.reasonHas)
			}
			if c.reasonNot != "" && strings.Contains(reason, c.reasonNot) {
				t.Errorf("reason = %q, want it not to contain %q", reason, c.reasonNot)
			}
			if got.NudgeIgnored != c.wantIgnore {
				t.Errorf("NudgeIgnored = %v, want %v", got.NudgeIgnored, c.wantIgnore)
			}
		})
	}
}

// Once set, NudgeIgnored stays for the session: the kill and force kill that
// follow keep it, and a change of ladder keeps it as it keeps the rung.
func TestNudgeIgnoredIsKept(t *testing.T) {
	s, _, _ := Step(State{Kind: KindMeter, Rung: RungNudge, At: t0}, enabled(t0.Add(5*time.Minute)), g)
	if !s.NudgeIgnored {
		t.Fatal("NudgeIgnored not set by the time-step interrupt")
	}
	s, act, _ := Step(s, disabled(t0.Add(7*time.Minute)), g)
	if act != ActKill || !s.NudgeIgnored {
		t.Errorf("after the kill: action %v, NudgeIgnored %v; want %v, true", act, s.NudgeIgnored, ActKill)
	}
	s, act, _ = Step(s, disabled(t0.Add(8*time.Minute)), g)
	if act != ActForceKill || !s.NudgeIgnored {
		t.Errorf("after the force kill: action %v, NudgeIgnored %v; want %v, true", act, s.NudgeIgnored, ActForceKill)
	}
	if fresh, _, _ := Step(State{}, enabled(t0, contextMeter(0.85, "")), g); fresh.NudgeIgnored {
		t.Error("a first nudge set NudgeIgnored")
	}
}

func TestNames(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{KindNone.String(), "none"},
		{KindMeter.String(), "meter"},
		{KindDisabled.String(), "disabled"},
		{KindExit.String(), "exit"},
		{Kind(9).String(), "Kind(9)"},
		{RungNone.String(), "none"},
		{RungNudge.String(), "nudge"},
		{RungInterrupt.String(), "interrupt"},
		{RungKill.String(), "kill"},
		{RungForceKill.String(), "force-kill"},
		{Rung(9).String(), "Rung(9)"},
		{ActNone.String(), "none"},
		{ActNudge.String(), "nudge"},
		{ActInterrupt.String(), "interrupt"},
		{ActKill.String(), "kill"},
		{ActForceKill.String(), "force_kill"},
		{ActGracefulStop.String(), "graceful_stop"},
		{Action(9).String(), "Action(9)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("String() = %q, want %q", c.got, c.want)
		}
	}
}

// A meter at its interrupt point wins the step over the interrupt's own time
// step on the same call, and the time step still marks the nudge ignored.
func TestMeterBeatsTimeStepOnTheSameCall(t *testing.T) {
	s := State{Kind: KindMeter, Rung: RungNudge, At: t0}
	now := t0.Add(g.Interrupt)
	got, act, reason := Step(s, enabled(now, contextMeter(0.96, "x")), g)
	if act != ActInterrupt || got.Rung != RungInterrupt {
		t.Fatalf("action %v rung %v, want interrupt", act, got.Rung)
	}
	if !strings.HasPrefix(reason, "context at 96%") {
		t.Errorf("reason = %q, want the meter's", reason)
	}
	if !got.NudgeIgnored {
		t.Error("NudgeIgnored = false, want true")
	}
}

func TestNonFiniteAndZeroPointMetersAreSkipped(t *testing.T) {
	for name, m := range map[string]Meter{
		"+Inf":        contextMeter(math.Inf(1), ""),
		"NaN":         contextMeter(math.NaN(), ""),
		"zero kill":   {Name: "context", Fraction: 0.99, Points: config.Points{Nudge: 0.8, Interrupt: 0.9}},
		"zero points": {Name: "context", Fraction: 5},
	} {
		if _, act, _ := Step(State{}, enabled(t0, m), g); act != ActNone {
			t.Errorf("%s: action %v, want none", name, act)
		}
	}
}

func TestToKill(t *testing.T) {
	got := ToKill(State{}, t0)
	if got.Kind != KindMeter || got.Rung != RungKill || !got.At.Equal(t0) {
		t.Errorf("ToKill(zero) = %+v", got)
	}
	got = ToKill(State{Kind: KindDisabled, Rung: RungNudge, NudgeIgnored: true}, t0)
	if got.Kind != KindDisabled || got.Rung != RungKill || !got.NudgeIgnored {
		t.Errorf("ToKill(disabled) = %+v", got)
	}
}

func TestOrphanReasonSaysNotInConfig(t *testing.T) {
	_, act, reason := Step(State{}, Input{Now: t0, Orphan: true}, g)
	if act != ActNudge || reason != "not in config" {
		t.Errorf("action %v reason %q, want nudge for \"not in config\"", act, reason)
	}
	_, _, reason = Step(State{}, Input{Now: t0}, g)
	if reason != "disabled" {
		t.Errorf("reason = %q, want disabled", reason)
	}
}
