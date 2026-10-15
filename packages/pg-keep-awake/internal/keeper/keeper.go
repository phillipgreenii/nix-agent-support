package keeper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/telemetry"
)

// externalEvery is how often the external-holder probe (a `pmset -g
// assertions` run) is repeated. Between runs the last answer is reused, so a
// short heartbeat_interval does not fork pmset on every tick.
const externalEvery = 10 * time.Second

// emitterCloseTimeout bounds the final OTel flush at shutdown.
const emitterCloseTimeout = 3 * time.Second

// Tick steps, reported to the unexported step hook so tests can assert the
// order of one pass.
const (
	stepSweep     = "sweep"
	stepGate      = "gate"
	stepDesired   = "desired"
	stepObserve   = "observe"
	stepConverge  = "converge"
	stepStatus    = "status"
	stepHeartbeat = "heartbeat"
	stepStartHB   = "startup-heartbeat"
)

// KeeperOptions configures a Keeper. Only Dir and Config are required; every
// other field has the production default.
type KeeperOptions struct {
	// Dir is the checked state directory; the caller already holds
	// keeper.lock (TryLockInstance).
	Dir *lease.Dir
	// Config is the effective configuration (LoadConfig). The keeper reads
	// it at startup only.
	Config config.Config
	// ConfigError is LoadConfig's error text; non-empty is reported as
	// keeper.config_error in status.json.
	ConfigError string
	// Version is the keeper version recorded in status.json and
	// assertion.json.
	Version string

	// Emitter receives the metrics and logs (default telemetry.Noop).
	Emitter telemetry.Emitter
	// Reader reads the battery (default policy.NewReader(os.Getenv), which
	// honours the hidden PG_KEEP_AWAKE_BATTERY_SOURCE variable).
	Reader policy.Reader
	// Procs controls processes (default NewOSProcs).
	Procs ProcControl
	// Exe is the pg-keep-awake executable the anchor is started from
	// (default os.Executable()).
	Exe string
	// Sweep injects the sweeper's seams; its Now defaults to Now below.
	Sweep Options
	// External lists the non-keeper assertion holders; the argument is the
	// held caffeinate's pid. Default is ExternalProbe{}.Probe.
	External func(ownCaffeinatePID int) []lease.StatusExternal
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Log receives one line per notable event (default io.Discard).
	Log io.Writer
}

// Keeper is the tick loop that owns the keep-awake assertion: it runs the six
// steps every heartbeat_interval and touches keeper.heartbeat only after a
// completed pass. Only the holder of keeper.lock may run one. A Keeper is not
// safe for concurrent use.
type Keeper struct {
	dir  *lease.Dir
	opts KeeperOptions
	cfg  config.Config
	acfg AssertionConfig

	sw    *Sweeper
	rc    *Reconciler
	eval  *policy.Evaluator
	procs ProcControl
	em    telemetry.Emitter
	probe func(int) []lease.StatusExternal
	now   func() time.Time

	// step is called at each stage of a pass; tests record the order.
	step func(string)

	// Carried between ticks.
	prevLastRelease *lease.StatusRelease
	prevUnexplained int
	prevGate        string
	prevBlocked     bool
	prevLive        map[string]bool

	// watch is the anchor of the pair held after the previous tick, used to
	// notice an anchor that exited on its own. exitSeenFor is the watched
	// anchor's pid for which an anchor-exit.json was already consumed, so its
	// later disappearance is not also counted as unknown.
	watch       lease.ProcRef
	exitSeenFor int

	extAt   time.Time
	extPID  int
	extList []lease.StatusExternal
}

// NewKeeper builds a Keeper. It does not touch the state directory; Run (or
// Start) does.
func NewKeeper(opts KeeperOptions) *Keeper {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Emitter == nil {
		opts.Emitter = telemetry.Noop{}
	}
	if opts.Reader == nil {
		opts.Reader = policy.NewReader(os.Getenv)
	}
	if opts.Procs == nil {
		opts.Procs = NewOSProcs()
	}
	if opts.External == nil {
		opts.External = ExternalProbe{}.Probe
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Sweep.Now == nil {
		opts.Sweep.Now = opts.Now
	}
	cfg := opts.Config
	k := &Keeper{
		dir:   opts.Dir,
		opts:  opts,
		cfg:   cfg,
		procs: opts.Procs,
		em:    opts.Emitter,
		probe: opts.External,
		now:   opts.Now,
		step:  func(string) {},
		acfg: AssertionConfig{
			HoldPct:       cfg.Battery.HoldPct,
			ReleasePct:    cfg.Battery.ReleasePct,
			Watchdog:      cfg.WatchdogTimeout,
			Heartbeat:     cfg.HeartbeatInterval,
			Linger:        cfg.Linger,
			StateDir:      opts.Dir.Path(),
			KeeperVersion: opts.Version,
		},
		eval: policy.NewEvaluator(policy.Policy{
			HoldPct:       cfg.Battery.HoldPct,
			ReleasePct:    cfg.Battery.ReleasePct,
			HysteresisPct: cfg.Battery.HysteresisPct,
		}),
		prevLive: map[string]bool{},
	}
	k.sw = NewSweeper(opts.Dir, opts.Sweep)
	k.rc = NewReconciler(opts.Dir, opts.Procs, AssertionOptions{Exe: opts.Exe})
	return k
}

func (k *Keeper) logf(format string, a ...any) {
	fmt.Fprintf(k.opts.Log, "pg-keep-awake keeper: "+format+"\n", a...)
}

// Interval is the tick period (the effective heartbeat_interval).
func (k *Keeper) Interval() time.Duration { return k.cfg.HeartbeatInterval }

// Start runs the startup sequence: create keeper.heartbeat first, report a
// configuration error, run the startup sweep (which keeps the heartbeat) and
// adopt a running anchor and caffeinate pair. It runs once, before the first
// tick.
func (k *Keeper) Start() error {
	if err := TouchHeartbeat(k.dir, k.now()); err != nil {
		return fmt.Errorf("create %s: %w", FileHeartbeat, err)
	}
	k.step(stepStartHB)
	k.em.RecordKeeperUp(true)
	if k.opts.ConfigError != "" {
		k.logf("configuration error, running on the fallback: %s", k.opts.ConfigError)
		k.em.LogConfigError(errors.New(k.opts.ConfigError))
	}
	if st, err := k.dir.ReadStatus(); err == nil {
		k.prevLastRelease = st.LastRelease // last_release survives a restart
	}
	if _, err := k.sw.Startup(); err != nil {
		k.logf("startup sweep: %v", err)
	}
	ar := k.rc.Adopt(ReconcileInputs{Now: k.now(), Config: k.acfg})
	for _, err := range ar.Errs {
		k.logf("adopt: %v", err)
	}
	if ar.Adopted {
		k.logf("adopted assertion (caffeinate pid %d)", ar.CaffeinatePID)
		k.em.LogAssertionAdopted(ar.CaffeinatePID)
	}
	for _, pid := range ar.StoppedOthers {
		k.logf("stopped stray assertion process %d", pid)
	}
	k.noteWatch(ar)
	return nil
}

// Run starts the keeper, ticks every heartbeat_interval until ctx is done
// (SIGTERM in production) and then runs the graceful shutdown. A pass that
// fails is logged and the loop carries on: the heartbeat simply is not
// touched, which is how a stuck keeper becomes visible.
func (k *Keeper) Run(ctx context.Context) error {
	if err := k.Start(); err != nil {
		return err
	}
	defer k.Shutdown()
	ticker := time.NewTicker(k.Interval())
	defer ticker.Stop()
	for {
		if err := k.Tick(); err != nil {
			k.logf("tick: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Shutdown is the graceful shutdown: the shutdown sweep (removes expired and
// dead leases and keeper.heartbeat, which starts the anchor's watchdog
// countdown) and a bounded OTel flush. Valid leases, assertion.json, the
// anchor and caffeinate are left alone.
func (k *Keeper) Shutdown() {
	if _, err := k.sw.Shutdown(); err != nil {
		k.logf("shutdown sweep: %v", err)
	}
	k.em.RecordKeeperUp(false)
	ctx, cancel := context.WithTimeout(context.Background(), emitterCloseTimeout)
	defer cancel()
	if err := k.em.Close(ctx); err != nil {
		k.logf("close emitter: %v", err)
	}
}

// Tick runs one pass: sweep; read the battery once and compute the gate;
// compute the desired state; observe; converge; write status.json and then
// touch keeper.heartbeat. The heartbeat is touched only when the pass
// completed. A sweep that was skipped because leases/.lock could not be taken
// still counts as completed. A pass that cannot trust its view of the leases
// (the lease directory could not be listed) or cannot write status.json is
// not completed: it returns an error and leaves the heartbeat alone.
func (k *Keeper) Tick() error {
	started := k.now()
	now := started

	// The anchor is autonomous: notice an exit before the sweep so the
	// reason is read while its exit record is fresh.
	gone, goneRef := k.anchorGone(), k.watch

	// 1. Sweep.
	sres, serr := k.sw.Sweep()
	k.step(stepSweep)
	if serr != nil {
		k.logf("sweep: %v", serr)
	}
	if serr != nil && sres.Live == nil && !sres.LockSkipped {
		return fmt.Errorf("sweep: %w", serr)
	}

	// 2. Read the battery once; compute the gate.
	gate := k.eval.Evaluate(k.opts.Reader)
	k.step(stepGate)

	// 3. Desired state: leases, pause, gate and linger (the Reconciler
	// computes it from these inputs inside step 5; the inputs are gathered
	// here).
	pause := k.readPause(now)
	exitFile := k.consumeAnchorExit()
	// An exit with no record is unknown, unless a record for this anchor was
	// already consumed while it was still alive.
	unknownExit := gone && exitFile == nil && k.exitSeenFor != goneRef.PID
	k.step(stepDesired)

	// 4 and 5. Observe actual state and converge.
	in := ReconcileInputs{Now: now, Live: sres.Live, Pause: pause, Gate: gate, Config: k.acfg}
	k.step(stepObserve)
	rr := k.rc.Reconcile(in)
	k.step(stepConverge)
	for _, err := range rr.Errs {
		k.logf("reconcile: %v", err)
	}
	k.noteWatch(rr)

	// 6. Write status.json, then touch the heartbeat.
	ext := k.external(rr, now)
	snap := BuildSnapshot(SnapshotInputs{
		Now:                  now,
		KeeperPID:            os.Getpid(),
		KeeperVersion:        k.opts.Version,
		HeartbeatMTime:       heartbeatMTime(k.dir),
		HeartbeatInterval:    k.cfg.HeartbeatInterval,
		ConfigError:          k.opts.ConfigError,
		Pause:                pause,
		Gate:                 gate,
		Live:                 sres.Live,
		Reconcile:            rr,
		AnchorExit:           exitFile,
		PrevLastRelease:      k.prevLastRelease,
		PrevUnexplainedTicks: k.prevUnexplained,
		External:             ext,
	})
	// An anchor found gone with no anchor-exit.json is recorded as unknown
	// even when this same tick restarted the pair (BuildSnapshot records a
	// loss only when nothing is held afterwards).
	if unknownExit && !isNewRelease(snap.LastRelease, now) {
		snap.LastRelease = &lease.StatusRelease{At: now.UTC(), Reason: lease.ReasonUnknown}
	}
	k.emit(now, sres, gate, rr, snap, exitFile, unknownExit, goneRef)

	if err := WriteSnapshot(k.dir, snap); err != nil {
		return fmt.Errorf("write %s: %w", lease.FileStatus, err)
	}
	k.step(stepStatus)
	k.prevLastRelease = snap.LastRelease
	k.prevUnexplained = snap.UnexplainedTicks

	if err := TouchHeartbeat(k.dir, k.now()); err != nil {
		return fmt.Errorf("touch %s: %w", FileHeartbeat, err)
	}
	k.step(stepHeartbeat)
	k.em.RecordTick(k.now().Sub(started), now)
	return nil
}

func isNewRelease(r *lease.StatusRelease, now time.Time) bool {
	return r != nil && r.At.Equal(now.UTC())
}

// noteWatch remembers the anchor of the pair held after a tick (or after
// adoption), so the next tick can tell whether it exited by itself.
func (k *Keeper) noteWatch(rr ReconcileResult) {
	if !rr.Held || rr.AnchorPID == 0 {
		k.watch, k.exitSeenFor = lease.ProcRef{}, 0
		return
	}
	if k.watch.PID != rr.AnchorPID {
		k.exitSeenFor = 0
	}
	k.watch = lease.ProcRef{PID: rr.AnchorPID, StartUS: k.procs.Probe(rr.AnchorPID).StartUS}
}

// anchorGone reports whether the anchor watched since the previous tick has
// exited by itself (the keeper's own stops reset the watch).
func (k *Keeper) anchorGone() bool {
	if k.watch.PID == 0 {
		return false
	}
	st := k.procs.Probe(k.watch.PID)
	return !st.Exists || st.Zombie || st.StartUS != k.watch.StartUS
}

// readPause reads pause.json. A pause whose until is past is removed and
// reported as no pause; an unreadable file (a newer schema, or damaged) is
// treated as no pause and left alone.
func (k *Keeper) readPause(now time.Time) *lease.PauseFile {
	p, err := k.dir.ReadPause()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			k.logf("ignoring %s: %v", lease.FilePause, err)
		}
		return nil
	}
	if !p.Expired(now) {
		return p
	}
	// Re-read before deleting: a `pause` written in between must survive.
	if cur, rerr := k.dir.ReadPause(); rerr == nil && cur.Expired(now) {
		if err := k.dir.RemovePause(); err != nil {
			k.logf("remove expired %s: %v", lease.FilePause, err)
		}
	}
	return nil
}

// consumeAnchorExit reads anchor-exit.json, removes it and returns it (nil
// when absent). A record in a newer schema is left alone and ignored; a
// damaged one is removed and reported as an unknown exit.
func (k *Keeper) consumeAnchorExit() *lease.AnchorExitFile {
	ex, err := k.dir.ReadAnchorExit()
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, lease.ErrUnreadable):
		k.logf("ignoring %s: %v", lease.FileAnchorExit, err)
		return nil
	default:
		k.logf("damaged %s removed: %v", lease.FileAnchorExit, err)
		ex = &lease.AnchorExitFile{Reason: lease.ReasonUnknown}
	}
	if err := k.dir.RemoveAnchorExit(); err != nil {
		k.logf("remove %s: %v", lease.FileAnchorExit, err)
	}
	if k.watch.PID != 0 {
		k.exitSeenFor = k.watch.PID
	}
	return ex
}

// external returns the non-keeper assertion holders, probing at most every
// externalEvery (or when the held caffeinate changes).
func (k *Keeper) external(rr ReconcileResult, now time.Time) []lease.StatusExternal {
	if k.extList == nil || rr.CaffeinatePID != k.extPID || now.Sub(k.extAt) >= externalEvery {
		k.extList = k.probe(rr.CaffeinatePID)
		if k.extList == nil {
			k.extList = []lease.StatusExternal{}
		}
		k.extAt, k.extPID = now, rr.CaffeinatePID
	}
	return slices.Clone(k.extList)
}

// emit records the tick's metrics and logs. It never blocks: the emitter's
// export runs on its own goroutines.
func (k *Keeper) emit(now time.Time, sres *Result, gate policy.Status, rr ReconcileResult,
	snap *lease.StatusFile, exitFile *lease.AnchorExitFile, unknownExit bool, goneRef lease.ProcRef,
) {
	em := k.em
	em.RecordKeeperUp(true)

	// Leases.
	byOwner := map[string]int{}
	cur := map[string]bool{}
	for _, l := range sres.Live {
		byOwner[l.Owner]++
		cur[l.ID] = true
		if !k.prevLive[l.ID] {
			em.LogLeaseCreated(l.ID, l.Owner)
		}
	}
	k.prevLive = cur
	em.RecordLeasesLive(byOwner)
	for _, r := range sres.ReapedLeases {
		em.IncLeasesReaped(string(r.Reason))
		em.LogLeaseReaped(r.Lease.ID, r.Lease.Owner, string(r.Reason))
	}
	for _, reason := range RejectReasons {
		for range sres.Rejected[reason] {
			em.IncLeasesRejected(string(reason))
		}
	}

	// Assertion.
	em.RecordAssertion(rr.Held, rr.Flags)
	if rr.Adopted {
		em.LogAssertionAdopted(rr.CaffeinatePID)
	}
	if rr.Started && rr.Held {
		em.LogAssertionSpawned(rr.CaffeinatePID)
	}

	// Battery and gate.
	g := gate.Gate.String()
	em.RecordGateState(g)
	if k.prevGate != "" && k.prevGate != g {
		k.logf("gate %s -> %s", k.prevGate, g)
		em.LogGateTransition(k.prevGate, g)
	}
	k.prevGate = g
	if snap.Battery.ReadOK && snap.Battery.Percent >= 0 {
		em.RecordBattery(snap.Battery.Percent, snap.Battery.OnBattery)
	}
	em.RecordReleasePct(k.acfg.EffectiveRelease())
	blocked := rr.SpawnBlocked && (rr.BlockedBy == BlockNoSpawn || rr.BlockedBy == BlockLowBattery)
	if blocked && !k.prevBlocked {
		em.IncBatteryRelease("spawn_blocked")
	}
	k.prevBlocked = blocked

	// The anchor's own exit, reported after the fact.
	switch {
	case exitFile != nil:
		reason := knownReleaseReason(exitFile.Reason)
		em.IncAnchorExit(reason)
		em.LogAnchorExit(reason, exitFile.Percent, exitFile.PID, exitFile.At)
		k.logf("anchor exited: %s", reason)
	case unknownExit:
		em.IncAnchorExit(lease.ReasonUnknown)
		em.LogAnchorExit(lease.ReasonUnknown, 0, goneRef.PID, now)
		k.logf("anchor exited: %s (no exit record)", lease.ReasonUnknown)
	}

	// A release this tick: counted against the battery when the battery or
	// the watchdog caused it.
	if isNewRelease(snap.LastRelease, now) {
		r := snap.LastRelease.Reason
		em.LogAssertionReleased(r)
		k.logf("assertion released: %s", r)
		switch r {
		case lease.ReasonReleaseThreshold, lease.ReasonBlindThreshold, lease.ReasonWatchdog:
			em.IncBatteryRelease(r)
		}
	}
}
