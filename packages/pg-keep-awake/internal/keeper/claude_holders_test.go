package keeper

import (
	"slices"
	"strconv"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// These tests drive the recognition rule and the keeper's use of it over a
// fake process table: nothing here signals a real process. The hermetic
// real-process proof is claude_holders_darwin_test.go.

var claudeArgv = []string{"caffeinate", "-i", "-t", "300"}

// claudeFixture is a fake table holding a Claude Code process and its child
// caffeinate, as Claude Code starts it.
type claudeFixture struct {
	fp     *fakeProcs
	parent int
	holder int
}

func newClaudeFixture(t *testing.T) claudeFixture {
	t.Helper()
	fp := newFakeProcs(t)
	// `exec -a claude .claude-wrapped`: argv[0] is "claude", the executable is
	// the real binary.
	parent := fp.seedChild(50, "/nix/store/x-claude-code-2.1.303/bin/.claude-wrapped", []string{"claude", "--flag"})
	holder := fp.seedChild(parent, ClaudeCaffeinateExe, claudeArgv)
	return claudeFixture{fp: fp, parent: parent, holder: holder}
}

func (c claudeFixture) ext() []lease.StatusExternal {
	return []lease.StatusExternal{{PID: c.holder, Process: "caffeinate", Type: "PreventUserIdleSystemSleep", AgeS: 12}}
}

func TestClaudeHolders_RecognisesOnlyClaudeCodesInhibitor(t *testing.T) {
	cases := []struct {
		name string
		mut  func(f claudeFixture)
		want bool
	}{
		{"claude code's own inhibitor", func(claudeFixture) {}, true},
		{"larger t value", func(f claudeFixture) { f.fp.procs[f.holder].argv = []string{"caffeinate", "-i", "-t", "1"} }, true},
		{"parent given as a path to claude", func(f claudeFixture) {
			f.fp.procs[f.parent].argv = []string{"/Users/x/.local/bin/claude"}
		}, true},
		{"parent executable itself named claude", func(f claudeFixture) {
			f.fp.procs[f.parent].argv = []string{"node"}
			f.fp.procs[f.parent].exe = "/opt/claude"
		}, true},

		{"another binary named caffeinate", func(f claudeFixture) { f.fp.procs[f.holder].exe = "/tmp/evil/caffeinate" }, false},
		{"a wait flag", func(f claudeFixture) { f.fp.procs[f.holder].argv = []string{"caffeinate", "-i", "-w", "99"} }, false},
		{"the keeper's own shape", func(f claudeFixture) {
			f.fp.procs[f.holder].argv = []string{"/usr/bin/caffeinate", "-i", "-w", "99"}
		}, false},
		{"a system-sleep flag", func(f claudeFixture) { f.fp.procs[f.holder].argv = []string{"caffeinate", "-s", "-i", "-t", "300"} }, false},
		{"no timeout", func(f claudeFixture) { f.fp.procs[f.holder].argv = []string{"caffeinate", "-i"} }, false},
		{"non-numeric timeout", func(f claudeFixture) { f.fp.procs[f.holder].argv = []string{"caffeinate", "-i", "-t", "soon"} }, false},
		{"zero timeout", func(f claudeFixture) { f.fp.procs[f.holder].argv = []string{"caffeinate", "-i", "-t", "0"} }, false},
		{"started from a shell", func(f claudeFixture) { f.fp.procs[f.parent].argv = []string{"-zsh"} }, false},
		{"started from a claude lookalike prefix", func(f claudeFixture) { f.fp.procs[f.parent].argv = []string{"claude-code-helper"} }, false},
		{"parent is init", func(f claudeFixture) { f.fp.procs[f.holder].ppid = 1 }, false},
		{"parent unknown", func(f claudeFixture) { f.fp.procs[f.holder].ppid = 0 }, false},
		{"parent gone", func(f claudeFixture) { f.fp.kill(f.parent) }, false},
		{"holder gone", func(f claudeFixture) { f.fp.kill(f.holder) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFixture(t)
			tc.mut(f)
			ch := ClaudeHolders{Procs: f.fp}
			if _, got := ch.identify(f.holder); got != tc.want {
				t.Fatalf("identify = %v, want %v", got, tc.want)
			}
			marked := ch.Mark(f.ext())[0].ManagedBy
			if (marked == lease.ManagedByClaude) != tc.want {
				t.Fatalf("Mark managed_by = %q, want managed=%v", marked, tc.want)
			}
		})
	}
}

func TestClaudeHolders_NeverTouchesPidOneOrLess(t *testing.T) {
	f := newClaudeFixture(t)
	ch := ClaudeHolders{Procs: f.fp}
	for _, pid := range []int{-5, 0, 1} {
		if _, ok := ch.identify(pid); ok {
			t.Fatalf("identify(%d) = true", pid)
		}
	}
}

func TestClaudeHolders_MarkLeavesOtherHoldersAndTheInputAlone(t *testing.T) {
	f := newClaudeFixture(t)
	in := append(f.ext(), lease.StatusExternal{PID: 419, Process: "powerd", Type: "PreventUserIdleSystemSleep"})
	out := ClaudeHolders{Procs: f.fp}.Mark(in)
	if out[0].ManagedBy != lease.ManagedByClaude || out[1].ManagedBy != "" {
		t.Fatalf("managed_by = %q, %q", out[0].ManagedBy, out[1].ManagedBy)
	}
	if in[0].ManagedBy != "" {
		t.Fatal("Mark modified its input")
	}
}

func TestClaudeHolders_ReapTermsAndStopsThere(t *testing.T) {
	f := newClaudeFixture(t)
	res := ClaudeHolders{Procs: f.fp}.Reap(f.ext())
	if !slices.Equal(res.Reaped, []int{f.holder}) || len(res.Errs) != 0 {
		t.Fatalf("Reap = %+v", res)
	}
	equalCalls(t, f.fp.log(), "TERM caffeinate#"+strconv.Itoa(f.holder), "wait caffeinate#"+strconv.Itoa(f.holder))
	if f.fp.Probe(f.parent).Exists != true {
		t.Fatal("the claude process itself must survive")
	}
}

func TestClaudeHolders_ReapEscalatesToKill(t *testing.T) {
	f := newClaudeFixture(t)
	f.fp.ignoreTerm[f.holder] = true
	res := ClaudeHolders{Procs: f.fp}.Reap(f.ext())
	if !slices.Equal(res.Reaped, []int{f.holder}) {
		t.Fatalf("Reap = %+v", res)
	}
	h := "caffeinate#" + strconv.Itoa(f.holder)
	equalCalls(t, f.fp.log(), "TERM "+h, "wait "+h, "KILL "+h, "wait "+h)
}

func TestClaudeHolders_ReapSkipsWhatIsNotClaudes(t *testing.T) {
	f := newClaudeFixture(t)
	f.fp.procs[f.parent].argv = []string{"-zsh"}
	res := ClaudeHolders{Procs: f.fp}.Reap(f.ext())
	if len(res.Reaped) != 0 || len(f.fp.signalled) != 0 {
		t.Fatalf("a non-claude holder was touched: %+v signalled=%v", res, f.fp.signalled)
	}
	// An ext entry that is not named caffeinate is never examined.
	f2 := newClaudeFixture(t)
	e := f2.ext()
	e[0].Process = "Safari"
	if res := (ClaudeHolders{Procs: f2.fp}).Reap(e); len(res.Reaped) != 0 || len(f2.fp.signalled) != 0 {
		t.Fatalf("a non-caffeinate holder was touched: %+v", res)
	}
}

// recycled answers the first Probe of the holder with its real start time and
// every later one with a different start time, as a recycled pid would.
type recycled struct {
	*fakeProcs
	holder int
	probes int
}

func (r *recycled) Probe(pid int) lease.ProcState {
	st := r.fakeProcs.Probe(pid)
	if pid == r.holder {
		r.probes++
		if r.probes > 1 {
			st.StartUS++
		}
	}
	return st
}

func TestClaudeHolders_ReapNeverSignalsARecycledPid(t *testing.T) {
	f := newClaudeFixture(t)
	r := &recycled{fakeProcs: f.fp, holder: f.holder}
	res := ClaudeHolders{Procs: r}.Reap(f.ext())
	if len(f.fp.signalled) != 0 {
		t.Fatalf("signalled %v although the start time changed", f.fp.signalled)
	}
	if len(res.Errs) != 0 {
		t.Fatalf("a recycled pid is the holder being gone, not an error: %v", res.Errs)
	}
}

// ---- the keeper's tick ----

func releasedBattery(g *krig) { g.reader.Set(policy.Reading{OnBattery: true, Percent: 15}) }

func newClaudeKRig(t *testing.T, f claudeFixture) *krig {
	t.Helper()
	return newKRig(t, func(o *KeeperOptions) {
		o.Procs = f.fp
		o.External = func(int) []lease.StatusExternal { return f.ext() }
	})
}

func TestTick_ReleasedGateStopsClaudeCodesInhibitor(t *testing.T) {
	f := newClaudeFixture(t)
	g := newClaudeKRig(t, f)
	releasedBattery(g)
	g.tick()
	if f.fp.Probe(f.holder).Exists {
		t.Fatalf("the claude-parented caffeinate survived a released gate; calls: %v", f.fp.log())
	}
	if !f.fp.Probe(f.parent).Exists {
		t.Fatal("the claude process was signalled")
	}
	if got := g.status().External; len(got) != 0 {
		t.Fatalf("a reaped holder is still listed: %+v", got)
	}
}

func TestTick_OtherGatesOnlyMarkTheInhibitor(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    policy.Reading
	}{
		{"ac", policy.Reading{OnBattery: false, Percent: 15}},
		{"battery above hold", policy.Reading{OnBattery: true, Percent: 64}},
		{"no_spawn", policy.Reading{OnBattery: true, Percent: 25}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFixture(t)
			g := newClaudeKRig(t, f)
			g.reader.Set(tc.r)
			g.tick()
			if !f.fp.Probe(f.holder).Exists || len(f.fp.signalled) != 0 {
				t.Fatalf("the inhibitor was stopped outside a released gate: %v", f.fp.log())
			}
			ext := g.status().External
			if len(ext) != 1 || ext[0].PID != f.holder || ext[0].ManagedBy != lease.ManagedByClaude {
				t.Fatalf("status external = %+v, want the holder marked managed", ext)
			}
		})
	}
}

func TestTick_ReleasedGateLeavesForeignCaffeinateAlone(t *testing.T) {
	f := newClaudeFixture(t)
	f.fp.procs[f.parent].argv = []string{"-zsh"} // a caffeinate started from a shell
	g := newClaudeKRig(t, f)
	releasedBattery(g)
	g.tick()
	if !f.fp.Probe(f.holder).Exists || len(f.fp.signalled) != 0 {
		t.Fatalf("a non-claude caffeinate was stopped: %v", f.fp.log())
	}
	if ext := g.status().External; len(ext) != 1 || ext[0].ManagedBy != "" {
		t.Fatalf("status external = %+v", ext)
	}
}

func TestTick_BlindRuleStopsTheInhibitorToo(t *testing.T) {
	f := newClaudeFixture(t)
	g := newClaudeKRig(t, f)
	g.reader.Set(policy.Reading{OnBattery: true, Percent: 25}) // last good reading at or below hold
	g.tick()
	g.reader.SetErr(errTest)
	for i := 0; i < 3; i++ {
		g.advance(g.k.Interval())
		g.tick()
	}
	if f.fp.Probe(f.holder).Exists {
		t.Fatalf("the blind rule did not stop the inhibitor; calls %v", f.fp.log())
	}
}

func TestTick_AHolderThatIgnoresSIGTERMIsKilled(t *testing.T) {
	f := newClaudeFixture(t)
	f.fp.ignoreTerm[f.holder] = true
	g := newClaudeKRig(t, f)
	releasedBattery(g)
	g.tick()
	if f.fp.Probe(f.holder).Exists {
		t.Fatal("SIGKILL escalation should have ended the holder")
	}
	if !hasPrefixCall(f.fp.log(), "KILL caffeinate") {
		t.Fatalf("expected escalation: %v", f.fp.log())
	}
}
