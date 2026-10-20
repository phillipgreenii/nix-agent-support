package keeper

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// seededPair is a running anchor and caffeinate seeded into the fake table, as
// an earlier keeper would have left them.
type seededPair struct {
	anchor, caff int
}

const oldExe = "/nix/store/zzzz-old-pg-keep-awake/bin/pg-keep-awake"

// seedPair seeds a pair for cfg (anchor argv from cfg, any binary path) with
// the anchor started at startUS.
func (g *rig) seedPair(cfg AssertionConfig, startUS int64, flags ...string) seededPair {
	g.t.Helper()
	aid := g.fp.seed(cfg.AnchorArgv(oldExe), startUS)
	cargv := []string{"/usr/bin/caffeinate"}
	for _, f := range flags {
		cargv = append(cargv, "-"+f)
	}
	cargv = append(cargv, "-w", strconv.Itoa(aid))
	cid := g.fp.seed(cargv, startUS+5)
	return seededPair{anchor: aid, caff: cid}
}

// writeFile writes assertion.json for sp as the keeper would have.
func (g *rig) writeFile(sp seededPair, mod func(*lease.AssertionFile)) {
	g.t.Helper()
	a := &lease.AssertionFile{
		Anchor:     lease.ProcDesc{PID: sp.anchor, StartUS: g.fp.procs[sp.anchor].startUS, Argv: g.fp.procs[sp.anchor].argv},
		Caffeinate: lease.ProcDesc{PID: sp.caff, StartUS: g.fp.procs[sp.caff].startUS, Argv: g.fp.procs[sp.caff].argv},
		Flags:      []string{"i"},
		Params:     g.cfg.Params(),
		StartedAt:  t0.Add(-time.Hour),
	}
	if mod != nil {
		mod(a)
	}
	if err := g.dir.WriteAssertion(a); err != nil {
		g.t.Fatal(err)
	}
}

func TestAdoptByAssertionFile(t *testing.T) {
	g := newRig(t)
	g.fp.noEnum = true // the file alone must carry the adoption
	sp := g.seedPair(g.cfg, 5_000, "i")
	g.writeFile(sp, nil)

	res := g.rc.Adopt(g.inputs(nil, okGate()))
	noErrs(t, res)
	if !res.Adopted || !res.Held || res.AnchorPID != sp.anchor || res.CaffeinatePID != sp.caff || !slices.Equal(res.Flags, []string{"i"}) {
		t.Fatalf("result = %+v", res)
	}
	if !res.StartedAt.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("StartedAt = %v, want the file's", res.StartedAt)
	}
	equalCalls(t, g.fp.log())

	// A matching tick keeps the adopted pair untouched, even though its binary
	// path (a store path) differs from the keeper's.
	res = g.tick(ll("a", "i"))
	if res.Started || res.Replaced || !res.Held || res.AnchorPID != sp.anchor {
		t.Fatalf("result = %+v", res)
	}
	equalCalls(t, g.fp.log())
}

func TestAdoptByFileRejects(t *testing.T) {
	cases := map[string]func(g *rig, sp seededPair){
		"anchor start_us differs": func(g *rig, sp seededPair) {
			g.writeFile(sp, func(a *lease.AssertionFile) { a.Anchor.StartUS++ })
		},
		"caffeinate start_us differs": func(g *rig, sp seededPair) {
			g.writeFile(sp, func(a *lease.AssertionFile) { a.Caffeinate.StartUS++ })
		},
		"anchor is another subcommand": func(g *rig, sp seededPair) {
			g.fp.procs[sp.anchor].argv[1] = "run"
			g.writeFile(sp, nil)
		},
		"caffeinate does not watch the anchor": func(g *rig, sp seededPair) {
			g.fp.procs[sp.caff].argv = []string{"/usr/bin/caffeinate", "-i"}
			g.writeFile(sp, nil)
		},
		"caffeinate watches another pid": func(g *rig, sp seededPair) {
			g.fp.procs[sp.caff].argv = []string{"/usr/bin/caffeinate", "-i", "-w", "99999"}
			g.writeFile(sp, nil)
		},
		"recorded parameters differ": func(g *rig, sp seededPair) {
			g.writeFile(sp, func(a *lease.AssertionFile) { a.Params.WatchdogSeconds++ })
		},
		"state_dir differs in the file": func(g *rig, sp seededPair) {
			g.writeFile(sp, func(a *lease.AssertionFile) { a.Params.StateDir = "/elsewhere" })
		},
		"state_dir differs in the argv": func(g *rig, sp seededPair) {
			other := g.cfg
			other.StateDir = "/elsewhere"
			g.fp.procs[sp.anchor].argv = other.AnchorArgv(oldExe)
			g.writeFile(sp, nil)
		},
		"anchor argv parameters differ": func(g *rig, sp seededPair) {
			other := g.cfg
			other.Watchdog = time.Minute
			g.fp.procs[sp.anchor].argv = other.AnchorArgv(oldExe)
			g.writeFile(sp, nil)
		},
		"anchor is gone": func(g *rig, sp seededPair) {
			g.writeFile(sp, nil)
			g.fp.kill(sp.anchor)
		},
		"caffeinate is gone": func(g *rig, sp seededPair) {
			g.writeFile(sp, nil)
			g.fp.kill(sp.caff)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			g.fp.noEnum = true
			sp := g.seedPair(g.cfg, 5_000, "i")
			mutate(g, sp)
			res := g.rc.Adopt(g.inputs(nil, okGate()))
			if res.Adopted || res.Held {
				t.Fatalf("adopted a pair it must reject: %+v", res)
			}
		})
	}
}

func TestAdoptedPairWithDifferentFlagsIsReplacedMakeBeforeBreak(t *testing.T) {
	g := newRig(t)
	g.fp.noEnum = true
	sp := g.seedPair(g.cfg, 5_000, "i")
	g.writeFile(sp, nil)
	res := g.tick(ll("a", "i"), ll("b", "s"))
	noErrs(t, res)
	if !res.Adopted || !res.Replaced || !slices.Equal(res.Flags, []string{"i", "s"}) {
		t.Fatalf("result = %+v", res)
	}
	calls := g.fp.log()
	newCaff := slices.IndexFunc(calls, func(c string) bool { return len(c) > 15 && c[:15] == "start caffeinat" })
	oldCaff := indexOf(t, calls, "TERM caffeinate#"+strconv.Itoa(sp.caff))
	if newCaff < 0 || newCaff > oldCaff {
		t.Fatalf("make before break violated: %q", calls)
	}
	if g.fp.procs[sp.anchor].up || g.fp.procs[sp.caff].up {
		t.Fatal("the old adopted pair must be stopped")
	}
}

func TestAdoptByScanTakesNewestValidAndStopsTheOthers(t *testing.T) {
	g := newRig(t)
	other := g.cfg
	other.StateDir = "/some/other/state"
	stale := g.cfg
	stale.Watchdog = time.Minute

	older := g.seedPair(g.cfg, 1_000, "i")
	newest := g.seedPair(g.cfg, 2_000, "i", "s")
	// The newest anchor uses the --state-dir=DIR form.
	full := g.cfg.AnchorArgv(oldExe)
	g.fp.procs[newest.anchor].argv = append(full[:len(full)-2:len(full)-2], "--state-dir="+g.cfg.StateDir)
	foreign := g.seedPair(other, 9_000, "i")
	wrongParams := g.seedPair(stale, 3_000, "i")
	noPartner := g.fp.seed(g.cfg.AnchorArgv(oldExe), 4_000) // newest of all, but no caffeinate

	res := g.rc.Reconcile(g.inputs([]LiveLease{ll("a", "i"), ll("b", "s")}, okGate()))
	noErrs(t, res)
	if !res.Adopted || res.AnchorPID != newest.anchor || res.CaffeinatePID != newest.caff {
		t.Fatalf("result = %+v, want the pair of anchor %d", res, newest.anchor)
	}
	if res.Started || res.Replaced {
		t.Fatalf("a matching adopted pair must be kept: %+v", res)
	}
	if !slices.Equal(res.Flags, []string{"i", "s"}) {
		t.Fatalf("flags = %v, want those read from caffeinate's argv", res.Flags)
	}

	for _, pid := range []int{older.anchor, older.caff, wrongParams.anchor, wrongParams.caff, noPartner} {
		if g.fp.procs[pid].up {
			t.Errorf("process %d (%s) must have been stopped", pid, g.fp.procs[pid].role())
		}
	}
	for _, pid := range []int{newest.anchor, newest.caff, foreign.anchor, foreign.caff} {
		if !g.fp.procs[pid].up {
			t.Errorf("process %d (%s) must be untouched", pid, g.fp.procs[pid].role())
		}
	}
	for _, pid := range g.fp.signalled {
		if pid == foreign.anchor || pid == foreign.caff || pid == newest.anchor || pid == newest.caff {
			t.Errorf("signalled pid %d, which must not be touched", pid)
		}
	}
	slices.Sort(res.StoppedOthers)
	want := []int{older.anchor, older.caff, wrongParams.anchor, wrongParams.caff, noPartner}
	slices.Sort(want)
	if !slices.Equal(res.StoppedOthers, want) {
		t.Fatalf("StoppedOthers = %v, want %v", res.StoppedOthers, want)
	}

	a, err := g.dir.ReadAssertion()
	if err != nil || a.Anchor.PID != newest.anchor || a.Caffeinate.PID != newest.caff {
		t.Fatalf("assertion.json = %+v, %v", a, err)
	}
}

func TestScanNeverTouchesAnotherStateDir(t *testing.T) {
	g := newRig(t)
	other := g.cfg
	other.StateDir = "/some/other/state"
	foreign := g.seedPair(other, 9_000, "i")
	lone := g.fp.seed(other.AnchorArgv(oldExe), 9_500)

	res := g.rc.Reconcile(g.inputs(nil, okGate()))
	noErrs(t, res)
	if res.Adopted || res.Held || len(res.StoppedOthers) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(g.fp.signalled) != 0 {
		t.Fatalf("signalled %v, want nothing", g.fp.signalled)
	}
	for _, pid := range []int{foreign.anchor, foreign.caff, lone} {
		if !g.fp.procs[pid].up {
			t.Errorf("pid %d of another state_dir was stopped", pid)
		}
	}
}

func TestStopDoesNotSignalARecycledPid(t *testing.T) {
	g := newRig(t)
	g.fp.noEnum = true
	sp := g.seedPair(g.cfg, 5_000, "i")
	g.writeFile(sp, nil)
	g.rc.Adopt(g.inputs(nil, okGate()))

	g.fp.procs[sp.anchor].startUS++ // the anchor exited and its pid was recycled
	res := g.tick(ll("a", "i"))
	if !res.PairLost {
		t.Fatalf("result = %+v", res)
	}
	if slices.Contains(g.fp.signalled, sp.anchor) {
		t.Fatalf("signalled the recycled pid %d", sp.anchor)
	}
	if hasPrefixCall(g.fp.log(), "TERM anchor#"+strconv.Itoa(sp.anchor)) {
		t.Fatalf("calls: %q", g.fp.log())
	}
}

func TestParseAnchorArgv(t *testing.T) {
	good := []string{
		"/x/pg-keep-awake", "anchor", "--release-pct", "20", "--blind-pct=30", "--watchdog", "10m",
		"--heartbeat", "/s/keeper.heartbeat", "--interval=10s", "--state-dir=/s",
	}
	a, ok := parseAnchorArgv(good)
	if !ok || a.releasePct != 20 || a.blindPct != 30 || a.watchdogS != 600 || a.intervalS != 10 ||
		a.heartbeat != "/s/keeper.heartbeat" || a.stateDir != "/s" || !a.haveRelease || !a.haveBlind {
		t.Fatalf("parsed = %+v, %v", a, ok)
	}
	sub, ok := parseAnchorArgv([]string{"/x/pg-keep-awake", "anchor", "--watchdog", "1500ms", "--interval=200ms", "--state-dir=/s"})
	if !ok || sub.watchdogS != 1.5 || sub.intervalS != 0.2 {
		t.Fatalf("sub-second parse = %+v, %v", sub, ok)
	}
	for name, argv := range map[string][]string{
		"not anchor":         {"/x/pg-keep-awake", "run", "--state-dir", "/s"},
		"no subcommand":      {"/x/pg-keep-awake"},
		"missing state-dir":  {"/x/pg-keep-awake", "anchor", "--release-pct", "20"},
		"dangling state-dir": {"/x/pg-keep-awake", "anchor", "--release-pct", "20", "--state-dir"},
		"empty state-dir":    {"/x/pg-keep-awake", "anchor", "--state-dir="},
	} {
		if _, ok := parseAnchorArgv(argv); ok {
			t.Errorf("%s: parsed ok", name)
		}
	}
}

func TestCaffeinateWatch(t *testing.T) {
	cases := []struct {
		argv  []string
		watch int
		flags []string
		ok    bool
	}{
		{[]string{"caffeinate", "-d", "-i", "-w", "77"}, 77, []string{"d", "i"}, true},
		{[]string{"caffeinate", "-s", "-i", "-w77"}, 77, []string{"i", "s"}, true},
		{[]string{"caffeinate", "-is", "-w", "5"}, 5, []string{"i", "s"}, true},
		{[]string{"caffeinate", "-i"}, 0, []string{"i"}, false},
		{[]string{"caffeinate", "-w", "x"}, 0, nil, false},
	}
	for _, c := range cases {
		w, f, ok := caffeinateWatch(c.argv)
		if w != c.watch || ok != c.ok || !slices.Equal(f, c.flags) {
			t.Errorf("%q = %d %v %v, want %d %v %v", c.argv, w, f, ok, c.watch, c.flags, c.ok)
		}
	}
}

// firstTerm is the index of the first signal in calls, or len(calls).
func firstTerm(calls []string) int {
	i := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "TERM ") || strings.HasPrefix(c, "KILL ") })
	if i < 0 {
		return len(calls)
	}
	return i
}

// TestAdoptByScanStaleParamsReplacedMakeBeforeBreak: a pair whose anchor was
// started with other parameters (the scan path, as assertion.json no longer
// matches) is kept by adoption and replaced by the first tick make-before-
// break: the new pair is started, and recorded, before the old one is stopped.
func TestAdoptByScanStaleParamsReplacedMakeBeforeBreak(t *testing.T) {
	g := newRig(t)
	stale := g.cfg
	stale.Watchdog = time.Minute
	old := g.seedPair(stale, 5_000, "i")
	g.writeFile(old, func(a *lease.AssertionFile) { a.Params = stale.Params() })

	// Adoption alone must not drop the assertion: the old pair still holds.
	res := g.rc.Adopt(g.inputs(nil, okGate()))
	noErrs(t, res)
	if !res.Adopted || !res.Held || res.AnchorPID != old.anchor || res.CaffeinatePID != old.caff {
		t.Fatalf("adopt result = %+v, want the stale pair held", res)
	}
	if len(res.StoppedOthers) != 0 || len(g.fp.signalled) != 0 {
		t.Fatalf("adoption stopped %v (signalled %v), want nothing", res.StoppedOthers, g.fp.signalled)
	}

	res = g.tick(ll("a", "i"))
	noErrs(t, res)
	if !res.Replaced || !res.Held || res.AnchorPID == old.anchor || res.CaffeinatePID == old.caff {
		t.Fatalf("tick result = %+v, want a replacement", res)
	}
	calls := g.fp.log()
	startAnchor := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "start anchor#") })
	startCaff := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "start caffeinate#") })
	if startAnchor < 0 || startCaff < 0 || startCaff > firstTerm(calls) || startAnchor > firstTerm(calls) {
		t.Fatalf("make before break violated: %q", calls)
	}
	if g.fp.procs[old.anchor].up || g.fp.procs[old.caff].up {
		t.Fatal("the old pair must be stopped after the replacement")
	}
	a, err := g.dir.ReadAssertion()
	if err != nil || a.Anchor.PID != res.AnchorPID || a.Params != g.cfg.Params() {
		t.Fatalf("assertion.json = %+v, %v", a, err)
	}
}

// TestAdoptByScanStaleHeartbeatPathIsReplaced: an anchor whose only difference
// is the heartbeat path records equal params, but is still replaced.
func TestAdoptByScanStaleHeartbeatPathIsReplaced(t *testing.T) {
	g := newRig(t)
	old := g.seedPair(g.cfg, 5_000, "i")
	g.fp.procs[old.anchor].argv[9] = "/elsewhere/keeper.heartbeat"

	res := g.tick(ll("a", "i"))
	noErrs(t, res)
	if !res.Adopted || !res.Replaced || res.AnchorPID == old.anchor {
		t.Fatalf("result = %+v, want the old pair replaced", res)
	}
	calls := g.fp.log()
	if i := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "start caffeinate#") }); i < 0 || i > firstTerm(calls) {
		t.Fatalf("make before break violated: %q", calls)
	}
}

// TestAdoptByScanStaleKeptWhileSpawnBlocked: when a start is withheld, the
// stale pair keeps holding rather than being stopped into a gap.
func TestAdoptByScanStaleKeptWhileSpawnBlocked(t *testing.T) {
	g := newRig(t)
	stale := g.cfg
	stale.Watchdog = time.Minute
	old := g.seedPair(stale, 5_000, "i")

	res := g.tickG(gateOf(policy.GateNoSpawn), ll("a", "i"))
	noErrs(t, res)
	if !res.Held || res.AnchorPID != old.anchor || !res.SpawnBlocked || res.Replaced {
		t.Fatalf("result = %+v, want the stale pair kept while blocked", res)
	}
	if len(g.fp.signalled) != 0 || !g.fp.procs[old.anchor].up || !g.fp.procs[old.caff].up {
		t.Fatalf("the old pair was stopped: signalled %v", g.fp.signalled)
	}
}

// TestAdoptByScanPrefersValidOverStale: a valid pair wins and the stale one is
// stopped, as before.
func TestAdoptByScanPrefersValidOverStale(t *testing.T) {
	g := newRig(t)
	stale := g.cfg
	stale.Watchdog = time.Minute
	valid := g.seedPair(g.cfg, 1_000, "i")
	old := g.seedPair(stale, 9_000, "i") // newer, but wrong parameters

	res := g.tick(ll("a", "i"))
	noErrs(t, res)
	if !res.Adopted || res.AnchorPID != valid.anchor || res.Replaced || res.Started {
		t.Fatalf("result = %+v, want the valid pair kept", res)
	}
	if g.fp.procs[old.anchor].up || g.fp.procs[old.caff].up {
		t.Fatal("the stale pair must be stopped")
	}
}

// A pair started with sub-second parameters is adopted by a keeper with the
// same configuration (argv and assertion.json both carry the fractions), and
// is not adopted by one whose heartbeat differs by a sub-second amount.
func TestAdoptSubSecondParams(t *testing.T) {
	g := newRig(t)
	g.cfg.Heartbeat = 200 * time.Millisecond
	sp := g.seedPair(g.cfg, 5_000, "i")
	g.writeFile(sp, nil)
	res := g.rc.Adopt(g.inputs(nil, okGate()))
	noErrs(t, res)
	if !res.Adopted || res.AnchorPID != sp.anchor {
		t.Fatalf("result = %+v, want the sub-second pair adopted", res)
	}

	h := newRig(t)
	h.cfg.Heartbeat = 200 * time.Millisecond
	sp = h.seedPair(h.cfg, 5_000, "i")
	h.writeFile(sp, nil)
	h.cfg.Heartbeat = 300 * time.Millisecond
	if res := h.rc.Adopt(h.inputs(nil, okGate())); res.Adopted {
		t.Fatalf("result = %+v, a 200ms pair must not match a 300ms config", res)
	}
}
