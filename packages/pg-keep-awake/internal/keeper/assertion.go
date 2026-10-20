package keeper

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// Assertion constants.
const (
	// CaffeinatePath is the real tool, called by absolute path so the shim
	// (which stands in for caffeinate on PATH) is never re-entered.
	CaffeinatePath = "/usr/bin/caffeinate"

	// StopGrace is how long a SIGTERMed process gets before SIGKILL.
	StopGrace = 5 * time.Second
	// killGrace is the wait after SIGKILL.
	killGrace = 2 * time.Second

	// ChurnWindow: an anchor that exits sooner than this after being spawned
	// is "churning".
	ChurnWindow = 30 * time.Second
	// ChurnBackoff is how long, from the churning anchor's exit, no new pair is
	// started.
	ChurnBackoff = 60 * time.Second
)

// flagOrder is the order of the caffeinate flags: d i m s.
const flagOrder = "dims"

// Spawn-blocked reasons (ReconcileResult.BlockedBy).
const (
	BlockNoSpawn    = "no_spawn"
	BlockLowBattery = "low_battery"
	BlockChurn      = "churn_backoff"
)

// AssertionConfig is the keeper's configuration as the Reconciler needs it.
// The keeper (14-keeper) passes it with every tick, so a changed value takes
// effect as a make-before-break replacement.
type AssertionConfig struct {
	// HoldPct is the battery hold level; 0 (or less) means the rule is off.
	HoldPct int
	// ReleasePct is the configured release level; it is capped at HoldPct.
	ReleasePct int
	// Watchdog is the anchor's heartbeat watchdog.
	Watchdog time.Duration
	// Heartbeat is the keeper's heartbeat_interval, which is also the anchor's
	// polling interval.
	Heartbeat time.Duration
	// Linger is how long the assertion outlives the last live lease.
	Linger time.Duration
	// StateDir is the state directory (absolute path); it scopes the process
	// scan and names the heartbeat file.
	StateDir string
	// KeeperVersion is recorded in assertion.json.
	KeeperVersion string
}

// EffectiveRelease is min(release, hold), or 0 when the rule is off or the
// release is not positive.
func (c AssertionConfig) EffectiveRelease() int {
	if c.HoldPct <= 0 {
		return 0
	}
	return max(0, min(c.ReleasePct, c.HoldPct))
}

// BlindLevel is the anchor's --blind-pct: the hold level, 0 when the rule is
// off.
func (c AssertionConfig) BlindLevel() int { return max(0, c.HoldPct) }

// HeartbeatPath is the keeper's heartbeat file under StateDir.
func (c AssertionConfig) HeartbeatPath() string { return filepath.Join(c.StateDir, FileHeartbeat) }

// Params is the parameter set recorded in assertion.json and compared on
// adoption.
func (c AssertionConfig) Params() lease.AssertionParams {
	return lease.AssertionParams{
		ReleasePct:       c.EffectiveRelease(),
		BlindPct:         c.BlindLevel(),
		WatchdogSeconds:  seconds(c.Watchdog),
		HeartbeatSeconds: seconds(c.Heartbeat),
		StateDir:         c.StateDir,
	}
}

// seconds is d in fractional seconds, 0 for a non-positive d. Nothing is rounded:
// the anchor takes the configured duration as it is, so heartbeat_interval =
// "200ms" polls every 200ms and is recorded as 0.2.
func seconds(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return d.Seconds()
}

// formatDuration renders d for an anchor flag: whole seconds as "Ns" (the
// long-standing form), otherwise milliseconds or, below that, the Go form. It
// round-trips through time.ParseDuration exactly; a non-positive d is "0s".
func formatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d%time.Second == 0:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	case d%time.Millisecond == 0:
		return strconv.FormatInt(int64(d/time.Millisecond), 10) + "ms"
	}
	return d.String()
}

// ceilSeconds rounds a positive duration up to whole seconds (at least 1). It
// is for the status.json staleness threshold only (rounding up is the safe
// side there), never for anchor parameters.
func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return max(1, int((d+time.Second-1)/time.Second))
}

// AnchorArgv builds the anchor's argv after the executable:
// anchor --release-pct R --blind-pct B --watchdog D --heartbeat PATH
// --interval I --state-dir DIR. R is min(release, hold), B is the hold level,
// both 0 when the rule is off. The hidden battery source needs no element: it
// reaches the anchor by environment inheritance.
func (c AssertionConfig) AnchorArgv(exe string) []string {
	p := c.Params()
	return []string{
		exe, "anchor",
		"--release-pct", strconv.Itoa(p.ReleasePct),
		"--blind-pct", strconv.Itoa(p.BlindPct),
		"--watchdog", formatDuration(c.Watchdog),
		"--heartbeat", c.HeartbeatPath(),
		"--interval", formatDuration(c.Heartbeat),
		"--state-dir", c.StateDir,
	}
}

// ReconcileInputs are one tick's inputs to Reconcile.
type ReconcileInputs struct {
	// Now is the tick's time.
	Now time.Time
	// Live are the live leases (the sweep result).
	Live []LiveLease
	// Pause is the pause state, or nil. A past Until is no pause.
	Pause *lease.PauseFile
	// Gate is the battery rule's verdict for the tick (policy.Evaluator).
	Gate policy.Status
	// Config holds the keeper's configuration values.
	Config AssertionConfig
}

// ReconcileResult is what one Reconcile did.
type ReconcileResult struct {
	// Held is true when the keeper holds an assertion after this tick.
	Held bool
	// Flags are the flags of the held assertion (d i m s order).
	Flags []string
	// AnchorPID and CaffeinatePID are the held pair's pids (0 when not held).
	AnchorPID, CaffeinatePID int
	// StartedAt is when the held pair was started (zero when not held).
	StartedAt time.Time

	// Started: a pair (or a lone caffeinate) was started this tick.
	Started bool
	// Replaced: the pair was replaced make-before-break this tick.
	Replaced bool
	// Adopted: an existing pair was adopted this tick.
	Adopted bool
	// Stopped: the keeper stopped the pair on purpose this tick.
	Stopped bool
	// StoppedOthers are the pids of stray anchors and caffeinates the adoption
	// scan stopped.
	StoppedOthers []int
	// PairLost: a previously held pair was found gone (it exited by itself).
	PairLost bool
	// SpawnBlocked: a start the leases asked for was withheld, see BlockedBy.
	SpawnBlocked bool
	// BlockedBy is no_spawn, low_battery or churn_backoff when SpawnBlocked.
	BlockedBy string
	// Release is the last_release reason for a stop the keeper performed
	// itself: release_threshold, blind_threshold, no_leases or paused. Reading
	// anchor-exit.json, the watchdog and unknown reasons and keeper_stop belong
	// to the tick loop.
	Release string
	// LingerActive: no lease is live and the assertion is being kept for the
	// linger window.
	LingerActive bool
	// LingerUntil is the end of the linger window while LingerActive.
	LingerUntil *time.Time
	// WantHold: the desired state is to hold an assertion (a live lease with
	// neither a pause nor a release gate, or the linger window).
	WantHold bool
	// Errs are the non-fatal errors of the tick (a failed start, signal or
	// file write); the tick still counts as completed.
	Errs []error
}

// pair is the anchor and caffeinate the keeper holds.
type pair struct {
	anchor, caff lease.ProcDesc
	flags        []string
	params       lease.AssertionParams
	startedAt    time.Time
	// spawnedAt is when this keeper spawned the anchor; zero for an adopted
	// pair, which the churn guard does not judge.
	spawnedAt time.Time
	// stale marks an adopted pair whose anchor was started with other
	// parameters than the current ones: converge replaces it make-before-break
	// even when the recorded params happen to compare equal.
	stale bool
}

// AssertionOptions are the Reconciler's construction parameters.
type AssertionOptions struct {
	// Exe is the pg-keep-awake executable the anchor is started from (default
	// os.Executable()).
	Exe string
	// Caffeinate is the caffeinate path (default CaffeinatePath).
	Caffeinate string
}

// Reconciler converges the keeper's processes (one anchor and its caffeinate)
// on the desired state, once per tick. It is not safe for concurrent use; the
// tick loop owns it. Only the holder of keeper.lock may use it.
type Reconciler struct {
	dir   *lease.Dir
	procs ProcControl
	opts  AssertionOptions

	cur         *pair
	adopted     bool
	lingerStart *time.Time
	// blockedUntil: no pair is started before this time (the churn guard).
	blockedUntil time.Time
}

// NewReconciler builds a Reconciler over a checked state directory and a
// process-control seam (NewOSProcs in production).
func NewReconciler(dir *lease.Dir, procs ProcControl, opts AssertionOptions) *Reconciler {
	if opts.Exe == "" {
		opts.Exe, _ = os.Executable()
	}
	if opts.Caffeinate == "" {
		opts.Caffeinate = CaffeinatePath
	}
	return &Reconciler{dir: dir, procs: procs, opts: opts}
}

// unionFlags is the union of the live leases' flags in d i m s order; an empty
// union is "i".
func unionFlags(live []LiveLease) []string {
	var out []string
	for _, f := range strings.Split(flagOrder, "") {
		if slices.ContainsFunc(live, func(l LiveLease) bool { return slices.Contains(l.Flags, f) }) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return []string{"i"}
	}
	return out
}

// caffeinateArgv is /usr/bin/caffeinate <flags> -w <anchor pid>.
func (r *Reconciler) caffeinateArgv(flags []string, anchorPID int) []string {
	argv := []string{r.opts.Caffeinate}
	for _, f := range flags {
		argv = append(argv, "-"+f)
	}
	return append(argv, "-w", strconv.Itoa(anchorPID))
}

// Reconcile runs one tick's convergence: adopt on the first call, observe the
// actual state, compute the desired state from the live leases, the pause, the
// gate and the linger window, and then start, replace, adopt or stop.
func (r *Reconciler) Reconcile(in ReconcileInputs) ReconcileResult {
	res := ReconcileResult{}
	if !r.adopted {
		r.adopt(in, &res)
	}
	r.observe(in, &res)

	cfg, now := in.Config, in.Now
	paused := in.Pause != nil && !in.Pause.Expired(now)
	hasLease := len(in.Live) > 0
	gate := in.Gate.Gate

	if hasLease {
		r.lingerStart = nil
	} else if r.lingerStart == nil {
		t := now
		r.lingerStart = &t
	}

	// wantHold: the desired state includes an assertion; stopReason: why the
	// keeper drops one when it does not.
	var wantHold bool
	var stopReason string
	switch {
	case paused:
		stopReason = lease.ReasonPaused
	case gate == policy.GateReleased:
		stopReason = lease.ReasonReleaseThreshold
		if in.Gate.Blind {
			stopReason = lease.ReasonBlindThreshold
		}
	case !hasLease:
		deadline := r.lingerStart.Add(cfg.Linger)
		if r.held() && now.Before(deadline) {
			res.LingerActive, res.WantHold = true, true
			res.LingerUntil = &deadline
		} else {
			stopReason = lease.ReasonNoLeases
		}
	default:
		wantHold = true
	}
	res.WantHold = res.WantHold || wantHold

	switch {
	case !wantHold && !res.LingerActive:
		if r.cur != nil {
			complete := r.held()
			r.stopPair(r.cur, &res)
			r.cur = nil
			r.removeAssertion(&res)
			if complete {
				res.Stopped, res.Release = true, stopReason
			}
		}
	case wantHold:
		r.converge(in, &res)
	}

	r.fill(&res)
	return res
}

// converge handles a tick whose desired state is to hold for live leases.
func (r *Reconciler) converge(in ReconcileInputs, res *ReconcileResult) {
	cfg, now := in.Config, in.Now
	flags := unionFlags(in.Live)
	params := cfg.Params()
	gate := in.Gate.Gate

	block := ""
	switch {
	case gate == policy.GateNoSpawn:
		block = BlockNoSpawn
	case lowBattery(in.Gate.Reading, cfg):
		block = BlockLowBattery
	case now.Before(r.blockedUntil):
		block = BlockChurn
	}

	// A lone anchor (its caffeinate was killed): restart the caffeinate under
	// the same anchor when starting is allowed and nothing else changed.
	if r.cur != nil && r.cur.caff.PID == 0 {
		if block == "" && slices.Equal(r.cur.flags, flags) && r.cur.params == params && !r.cur.stale {
			if err := r.restartCaffeinate(r.cur); err != nil {
				res.Errs = append(res.Errs, err)
				r.stopPair(r.cur, res)
				r.cur = nil
				r.removeAssertion(res)
			} else {
				res.Started = true
				r.writeAssertion(r.cur, cfg, res)
				return
			}
		} else {
			r.stopPair(r.cur, res)
			r.cur = nil
			r.removeAssertion(res)
		}
	}

	if r.cur != nil {
		// A pair is held.
		if slices.Equal(r.cur.flags, flags) && r.cur.params == params && !r.cur.stale {
			return
		}
		if block != "" {
			res.SpawnBlocked, res.BlockedBy = true, block
			return
		}
		next, err := r.startPair(flags, cfg, now)
		if err != nil {
			res.Errs = append(res.Errs, err)
			return
		}
		old := r.cur
		r.cur = next
		r.writeAssertion(next, cfg, res)
		r.stopPair(old, res) // make before break: the old pair goes last
		res.Started, res.Replaced = true, true
		return
	}

	if block != "" {
		res.SpawnBlocked, res.BlockedBy = true, block
		return
	}
	next, err := r.startPair(flags, cfg, now)
	if err != nil {
		res.Errs = append(res.Errs, err)
		return
	}
	r.cur = next
	r.writeAssertion(next, cfg, res)
	res.Started = true
}

// lowBattery is the churn guard's first rule: the keeper's own last reading is
// on battery at or below the effective release level. It does not apply when
// that level is 0.
func lowBattery(rd policy.Reading, cfg AssertionConfig) bool {
	rel := cfg.EffectiveRelease()
	return rel > 0 && rd.OnBattery && rd.Percent != policy.NoPercent && rd.Percent <= rel
}

// held reports whether a complete pair (anchor and caffeinate) is held.
func (r *Reconciler) held() bool { return r.cur != nil && r.cur.caff.PID != 0 }

// fill copies the held pair into the result.
func (r *Reconciler) fill(res *ReconcileResult) {
	if !r.held() {
		return
	}
	res.Held = true
	res.Flags = slices.Clone(r.cur.flags)
	res.AnchorPID, res.CaffeinatePID = r.cur.anchor.PID, r.cur.caff.PID
	res.StartedAt = r.cur.startedAt
}

// alive reports whether d is a live, non-zombie process with d's start time.
func (r *Reconciler) alive(d lease.ProcDesc) bool {
	if d.PID <= 1 {
		return false
	}
	st := r.procs.Probe(d.PID)
	return st.Exists && !st.Zombie && st.StartUS == d.StartUS
}

// observe checks the held pair against the process table. A vanished anchor is
// the churn guard's input; a vanished caffeinate under a live anchor leaves a
// lone anchor (caff.PID 0) that converge restarts.
func (r *Reconciler) observe(in ReconcileInputs, res *ReconcileResult) {
	p := r.cur
	if p == nil {
		return
	}
	anchorUp, caffUp := r.alive(p.anchor), p.caff.PID != 0 && r.alive(p.caff)
	switch {
	case anchorUp && caffUp:
		return
	case anchorUp:
		p.caff = lease.ProcDesc{}
		res.PairLost = true
		return
	}
	// The anchor is gone: caffeinate -w follows it. Stop a leftover
	// caffeinate and judge the churn.
	if !p.spawnedAt.IsZero() && in.Now.Sub(p.spawnedAt) < ChurnWindow {
		r.blockedUntil = in.Now.Add(ChurnBackoff)
	}
	if caffUp {
		r.terminate(p.caff, res)
	}
	r.cur = nil
	res.PairLost = true
	r.removeAssertion(res)
}

// startPair starts the anchor first and then caffeinate -w <anchor pid>. On a
// failure of the second start the anchor is stopped again.
func (r *Reconciler) startPair(flags []string, cfg AssertionConfig, now time.Time) (*pair, error) {
	anchorArgv := cfg.AnchorArgv(r.opts.Exe)
	apid, err := r.procs.Start(anchorArgv)
	if err != nil {
		return nil, err
	}
	p := &pair{
		anchor:    lease.ProcDesc{PID: apid, StartUS: r.procs.Probe(apid).StartUS, Argv: anchorArgv},
		flags:     slices.Clone(flags),
		params:    cfg.Params(),
		startedAt: now,
		spawnedAt: now,
	}
	if err := r.restartCaffeinate(p); err != nil {
		r.terminate(p.anchor, nil)
		return nil, err
	}
	return p, nil
}

// restartCaffeinate starts /usr/bin/caffeinate <flags> -w <anchor pid> and
// records it in p.
func (r *Reconciler) restartCaffeinate(p *pair) error {
	argv := r.caffeinateArgv(p.flags, p.anchor.PID)
	cpid, err := r.procs.Start(argv)
	if err != nil {
		return err
	}
	p.caff = lease.ProcDesc{PID: cpid, StartUS: r.procs.Probe(cpid).StartUS, Argv: argv}
	return nil
}

// stopPair stops caffeinate and then the anchor of p.
func (r *Reconciler) stopPair(p *pair, res *ReconcileResult) {
	if p.caff.PID != 0 {
		r.terminate(p.caff, res)
	}
	r.terminate(p.anchor, res)
}

// Stop stops the held pair, if any, and removes assertion.json. It returns
// whether a pair was stopped.
func (r *Reconciler) Stop() (bool, []error) {
	res := ReconcileResult{}
	if r.cur == nil {
		return false, nil
	}
	r.stopPair(r.cur, &res)
	r.cur = nil
	r.removeAssertion(&res)
	return true, res.Errs
}

// terminate stops one process: SIGTERM, a wait of StopGrace, then SIGKILL. The
// pid and start_us are re-verified first, so a pid adopted from an earlier
// keeper (or recycled) is never signalled by mistake; a process that is
// already gone is left alone.
func (r *Reconciler) terminate(d lease.ProcDesc, res *ReconcileResult) {
	if !r.alive(d) {
		return
	}
	if err := r.procs.Signal(d.PID, syscall.SIGTERM); err != nil {
		r.noteErr(res, err)
		return
	}
	if r.procs.WaitExit(d.PID, StopGrace) {
		return
	}
	if r.alive(d) {
		if err := r.procs.Signal(d.PID, syscall.SIGKILL); err != nil {
			r.noteErr(res, err)
			return
		}
		r.procs.WaitExit(d.PID, killGrace)
	}
}

func (r *Reconciler) noteErr(res *ReconcileResult, err error) {
	if res != nil {
		res.Errs = append(res.Errs, err)
	}
}

// writeAssertion records the held pair in assertion.json.
func (r *Reconciler) writeAssertion(p *pair, cfg AssertionConfig, res *ReconcileResult) {
	a := &lease.AssertionFile{
		Anchor:        p.anchor,
		Caffeinate:    p.caff,
		Flags:         slices.Clone(p.flags),
		Params:        p.params,
		StartedAt:     p.startedAt.UTC(),
		KeeperVersion: cfg.KeeperVersion,
	}
	if err := r.dir.WriteAssertion(a); err != nil {
		res.Errs = append(res.Errs, err)
	}
}

// removeAssertion removes assertion.json (no assertion is held).
func (r *Reconciler) removeAssertion(res *ReconcileResult) {
	if err := r.dir.Remove(lease.FileAssertion); err != nil {
		res.Errs = append(res.Errs, err)
	}
}
