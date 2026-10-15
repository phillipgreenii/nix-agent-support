package keeper

import (
	"errors"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// anchorArgs are the parameters read back from a running anchor's argv.
type anchorArgs struct {
	releasePct, blindPct   int
	watchdogS, intervalS   int
	heartbeat, stateDir    string
	haveRelease, haveBlind bool
}

// parseAnchorArgv reads an anchor's argv (argv[0] is the executable, argv[1]
// the subcommand). It reports ok=false when the subcommand is not anchor or
// the --state-dir is missing or malformed. Both "--flag value" and
// "--flag=value" are accepted. The executable path is never compared: store
// paths change at every apply.
func parseAnchorArgv(argv []string) (anchorArgs, bool) {
	var a anchorArgs
	if len(argv) < 2 || argv[1] != "anchor" {
		return a, false
	}
	rest := argv[2:]
	for i := 0; i < len(rest); i++ {
		if !strings.HasPrefix(rest[i], "-") {
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(rest[i], "-"), "=")
		if !hasVal {
			if i+1 >= len(rest) {
				continue
			}
			i++
			val = rest[i]
		}
		switch name {
		case "release-pct":
			a.releasePct, a.haveRelease = atoiOK(val)
		case "blind-pct":
			a.blindPct, a.haveBlind = atoiOK(val)
		case "watchdog":
			a.watchdogS = durSeconds(val)
		case "interval":
			a.intervalS = durSeconds(val)
		case "heartbeat":
			a.heartbeat = val
		case "state-dir":
			a.stateDir = val
		}
	}
	return a, a.stateDir != ""
}

func atoiOK(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func durSeconds(s string) int {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return ceilSeconds(d)
}

// matches reports whether the anchor argv carries exactly the parameters cfg
// would start it with.
func (a anchorArgs) matches(cfg AssertionConfig) bool {
	p := cfg.Params()
	return a.haveRelease && a.haveBlind &&
		a.releasePct == p.ReleasePct && a.blindPct == p.BlindPct &&
		a.watchdogS == p.WatchdogSeconds && a.intervalS == p.HeartbeatSeconds &&
		a.stateDir == p.StateDir && a.heartbeat == cfg.HeartbeatPath()
}

// caffeinateWatch reads a caffeinate argv: the pid after -w and the flags
// (d i m s order) among its options. ok=false when there is no -w <pid>.
func caffeinateWatch(argv []string) (watch int, flags []string, ok bool) {
	seen := map[string]bool{}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-w" && i+1 < len(argv):
			if n, err := strconv.Atoi(argv[i+1]); err == nil {
				watch, ok = n, true
			}
			i++
		case strings.HasPrefix(arg, "-w") && len(arg) > 2:
			if n, err := strconv.Atoi(arg[2:]); err == nil {
				watch, ok = n, true
			}
		case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--"):
			for _, c := range arg[1:] {
				if strings.ContainsRune(flagOrder, c) {
					seen[string(c)] = true
				}
			}
		}
	}
	for _, f := range strings.Split(flagOrder, "") {
		if seen[f] {
			flags = append(flags, f)
		}
	}
	return watch, flags, ok
}

// Adopt takes over an existing anchor and caffeinate pair at keeper start. It
// is adoption steps 2 to 4 (step 1, creating keeper.heartbeat, is the caller's):
//
//  1. Read assertion.json and adopt the pair iff each pid and start_us match,
//     the anchor's argv has subcommand anchor, caffeinate's argv has
//     -w <anchor pid>, and the parameters and state_dir equal the current
//     ones. Argv is compared by subcommand and parameters, never by binary
//     path.
//  2. If the file is missing or wrong, scan for pg-keep-awake anchor processes
//     whose --state-dir equals this keeper's, adopt the newest valid one
//     (alive, parameters equal, with a caffeinate partner) and stop the
//     others. A process with a different state_dir is never touched.
//  3. A pair that differs from the desired state is replaced make-before-break
//     by the next Reconcile.
//
// Reconcile calls Adopt itself on its first run, so calling it earlier is only
// needed to read its result separately. Adopt runs at most once.
func (r *Reconciler) Adopt(in ReconcileInputs) ReconcileResult {
	res := ReconcileResult{}
	if !r.adopted {
		r.adopt(in, &res)
	}
	r.fill(&res)
	return res
}

func (r *Reconciler) adopt(in ReconcileInputs, res *ReconcileResult) {
	r.adopted = true
	if r.adoptFromFile(in, res) {
		return
	}
	r.adoptByScan(in, res)
}

// adoptFromFile adopts the pair assertion.json names.
func (r *Reconciler) adoptFromFile(in ReconcileInputs, res *ReconcileResult) bool {
	cfg := in.Config
	a, err := r.dir.ReadAssertion()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			res.Errs = append(res.Errs, err)
		}
		return false
	}
	if a.Params != cfg.Params() || !r.alive(a.Anchor) || !r.alive(a.Caffeinate) {
		return false
	}
	ai, err := r.procs.Info(a.Anchor.PID)
	if err != nil {
		return false
	}
	if args, ok := parseAnchorArgv(ai.Argv); !ok || !args.matches(cfg) {
		return false
	}
	ci, err := r.procs.Info(a.Caffeinate.PID)
	if err != nil {
		return false
	}
	if watch, _, ok := caffeinateWatch(ci.Argv); !ok || watch != a.Anchor.PID {
		return false
	}
	_, flags, _ := caffeinateWatch(ci.Argv)
	if len(flags) == 0 {
		flags = slices.Clone(a.Flags)
	}
	anchor, caff := a.Anchor, a.Caffeinate
	anchor.Argv, caff.Argv = ai.Argv, ci.Argv
	r.cur = &pair{anchor: anchor, caff: caff, flags: flags, params: a.Params, startedAt: a.StartedAt}
	res.Adopted = true
	return true
}

// scanned is an anchor found by the process scan.
type scanned struct {
	desc  lease.ProcDesc
	valid bool
	caff  *lease.ProcDesc
}

// adoptByScan finds this state_dir's anchors, adopts the newest valid pair and
// stops the others.
func (r *Reconciler) adoptByScan(in ReconcileInputs, res *ReconcileResult) {
	cfg := in.Config
	pids, err := r.procs.PIDs()
	if err != nil {
		res.Errs = append(res.Errs, err)
		return
	}
	var anchors []*scanned
	caffs := map[int][]lease.ProcDesc{} // anchor pid -> caffeinates watching it
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		info, err := r.procs.Info(pid)
		if err != nil {
			continue
		}
		st := r.procs.Probe(pid)
		if !st.Exists || st.Zombie {
			continue
		}
		desc := lease.ProcDesc{PID: pid, StartUS: st.StartUS, Argv: info.Argv}
		if info.Name == "caffeinate" {
			if watch, _, ok := caffeinateWatch(info.Argv); ok {
				caffs[watch] = append(caffs[watch], desc)
			}
			continue
		}
		if args, ok := parseAnchorArgv(info.Argv); ok && args.stateDir == cfg.StateDir {
			anchors = append(anchors, &scanned{desc: desc, valid: args.matches(cfg)})
		}
	}

	var best *scanned
	for _, s := range anchors {
		if cs := caffs[s.desc.PID]; len(cs) > 0 {
			c := slices.MaxFunc(cs, func(x, y lease.ProcDesc) int { return int(x.StartUS - y.StartUS) })
			s.caff = &c
		}
		if s.valid && s.caff != nil && (best == nil || s.desc.StartUS > best.desc.StartUS) {
			best = s
		}
	}
	if best != nil {
		_, flags, _ := caffeinateWatch(best.caff.Argv)
		r.cur = &pair{anchor: best.desc, caff: *best.caff, flags: flags, params: cfg.Params(), startedAt: in.Now}
		r.writeAssertion(r.cur, cfg, res)
		res.Adopted = true
	}
	for _, s := range anchors {
		if s == best {
			continue
		}
		// Stop the stray anchor and every caffeinate following it. Each pid is
		// re-verified before it is signalled.
		for _, c := range caffs[s.desc.PID] {
			if best != nil && c.PID == best.caff.PID {
				continue
			}
			r.terminate(c, res)
			res.StoppedOthers = append(res.StoppedOthers, c.PID)
		}
		r.terminate(s.desc, res)
		res.StoppedOthers = append(res.StoppedOthers, s.desc.PID)
	}
}
