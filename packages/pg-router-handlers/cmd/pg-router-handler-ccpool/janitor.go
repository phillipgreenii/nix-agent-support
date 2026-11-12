package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/dispatchlib"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/handlerstate"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
	"github.com/phillipgreenii/pg-router/conformance"
)

// The janitor (bead pg2-fx5x0l.4, INV-CCH-41, ADR 0117): the scheduled home of
// the housekeeping passes a dispatch used to run inline. One run is
// single-flight (a non-blocking flock), bounded by a wall-clock deadline that is
// shared out per role, and best effort per pass. It reuses the passes of
// sweeps.go unchanged (their per-session and per-bead flocks keep a run safe
// beside live dispatches); it adds only where and when they run.
//
// Design patterns: Scheduled Reaper with a Single-Flight lock, Bulkhead (the
// per-role sub-budget), Round-Robin start (the rotated first role).

const (
	// janitorLockKey names the single-flight lock file (<lock dir>/janitor.lock).
	janitorLockKey = "janitor"
	// janitorRotationFile holds the run counter that rotates the first role,
	// inside the handler state directory.
	janitorRotationFile = "janitor-rotation"
	// defaultJanitorDeadline is the wall-clock bound of one run when --deadline
	// is not given (the HM option's default, below its 10m interval).
	defaultJanitorDeadline = 4 * time.Minute
	// janitorRunEvent is the events.jsonl kind of one run's record.
	janitorRunEvent = "janitor_run"
)

// Outcomes of a run, carried by its janitor_run event row.
const (
	janitorCompleted = "completed"
	janitorSkipped   = "skipped"
	janitorDeadline  = "deadline"
)

// Pass names, shared with the sweep_pass rows of sweeps.go.
const (
	passClosedBead  = "closed_bead_reconcile"
	passDefaultPool = "default_pool"
	passOrphan      = "orphan_reconcile"
	passWorktree    = "worktree_sweep"
	passLockGC      = "lock_gc"
)

// repeatedFlag is a repeatable string flag (--role-file).
type repeatedFlag []string

func (r *repeatedFlag) String() string     { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error { *r = append(*r, v); return nil }

// janitorPasses are the production passes, injectable so a test exercises the
// scheduling (dedupe, deadline, best effort, rotation) without ccpool or bd.
type janitorPasses struct {
	closedBead  func(ctx context.Context, role roles.Role, cfg config.Config, deps executor.Deps) int
	defaultPool func(ctx context.Context, cfg config.Config) int
	orphan      func(ctx context.Context, role roles.Role, cfg config.Config, deps executor.Deps) int
	worktree    func(ctx context.Context, role roles.Role, cfg config.Config, deps executor.Deps) int
	lockGC      func(deps executor.Deps) int
	// emit publishes the run's heartbeat gauges; nil (a test) emits nothing.
	emit gaugeEmitFn
	// flush, when set, publishes what emit buffered (one call per run).
	flush func(context.Context) error
	// ccpoolVersion reads `ccpool version` for the build_info gauge; nil
	// reports unknown.
	ccpoolVersion func(ctx context.Context) (string, error)
	// now is the clock the census budget is measured on; nil is time.Now.
	now func() time.Time
	// censusMaxBudget and censusMaxEntries override the census bounds (a test);
	// zero selects the defaults.
	censusMaxBudget  time.Duration
	censusMaxEntries int
}

func (p janitorPasses) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// prodJanitorPasses wires the passes of sweeps.go with the same arguments
// runSweeps passes them.
func prodJanitorPasses() janitorPasses {
	gb := newGaugeBatch()
	return janitorPasses{
		closedBead: func(ctx context.Context, _ roles.Role, cfg config.Config, deps executor.Deps) int {
			return reconcileClosedBeadSessions(ctx, deps.CC, worktree.GitOpener, deps.BD, cfg.SessionPrefix, cfg.RepoRoot, cfg.WorktreeDir, newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil))
		},
		defaultPool: func(ctx context.Context, cfg config.Config) int { return reconcileDefaultPool(ctx, cfg) },
		orphan: func(ctx context.Context, role roles.Role, cfg config.Config, deps executor.Deps) int {
			reclaimed, stopped := reconcileOrphanSessions(ctx, role, deps, orphanEnv{
				open:    worktree.GitOpener,
				quiet:   newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil),
				lockDir: deps.LockDir,
			})
			return reclaimed + stopped
		},
		worktree: func(ctx context.Context, role roles.Role, _ config.Config, deps executor.Deps) int {
			return sweepLeakedWorktrees(ctx, role, deps, orphanEnv{open: worktree.GitOpener, cwds: processCWDs, lockDir: deps.LockDir})
		},
		lockGC: sweepStaleLocks,
		emit:   gb.Emit,
		flush:  gb.Flush,
		ccpoolVersion: func(ctx context.Context) (string, error) {
			return ccpool.NewCLIRunner(config.Default()).Version(ctx)
		},
	}
}

// janitorOptions are the parsed flags.
type janitorOptions struct {
	cfgPath   string
	roleFiles []string
	deadline  time.Duration
	dryRun    bool
	// listDiag switches the bounded slow-list diagnostic (janitor_listdiag.go);
	// listDiagAfter is its threshold and listDiagUntil its expiry (zero selects
	// the built-in expiry).
	listDiag      bool
	listDiagAfter time.Duration
	listDiagUntil string
}

// runJanitor implements the `janitor` subcommand: not part of the wire
// contract, run by a scheduler (the darwin LaunchAgent) or by hand. Exit codes:
// 0 for a completed run, a skipped run (another is active) and a run that hit
// its deadline (the janitor_run row carries which), 2 for a usage error or an
// unreadable input; 1 only when the lock itself cannot be taken.
func runJanitor(args []string) int {
	fs := flag.NewFlagSet("janitor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := janitorOptions{}
	var files repeatedFlag
	fs.StringVar(&opts.cfgPath, "config", os.Getenv(rolefile.EnvConfig), "path to the handler launch config JSON (or "+rolefile.EnvConfig+")")
	fs.Var(&files, "role-file", "a ccpool role's rendered role file ({\"role\":{\"name\",\"config\"}}); repeatable, one per ccpool role")
	fs.DurationVar(&opts.deadline, "deadline", defaultJanitorDeadline, "wall-clock bound of the whole run, shared out per role")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "list what each pass would reclaim; reclaim nothing, take no lock, record no event")
	fs.BoolVar(&opts.listDiag, "list-diag", true, "sample the process tree when a ccpool list call is slow (bounded; stops at its expiry)")
	fs.DurationVar(&opts.listDiagAfter, "list-diag-after", defaultListDiagThreshold, "how long a ccpool list call runs before it is sampled")
	fs.StringVar(&opts.listDiagUntil, "list-diag-until", "", "RFC 3339 time after which the list diagnostic stops (default "+defaultListDiagExpiry.Format(time.RFC3339)+")")
	if exit, done := parseFlagsNoStdin("janitor", fs, args); done {
		return exit
	}
	opts.roleFiles = files
	if opts.listDiagUntil != "" {
		if _, err := time.Parse(time.RFC3339, opts.listDiagUntil); err != nil {
			fmt.Fprintln(os.Stderr, "janitor: --list-diag-until must be an RFC 3339 time:", err)
			return conformance.ExitUsage
		}
	}
	if opts.cfgPath == "" {
		fmt.Fprintln(os.Stderr, "janitor: --config (or "+rolefile.EnvConfig+") is required")
		return conformance.ExitUsage
	}
	if opts.deadline <= 0 {
		fmt.Fprintln(os.Stderr, "janitor: --deadline must be positive")
		return conformance.ExitUsage
	}
	return serveJanitor(context.Background(), os.Stdout, opts, dispatchlib.BuildDeps, prodJanitorPasses())
}

// parseFlagsNoStdin is parseFlags with a message that does not claim the
// subcommand reads stdin.
func parseFlagsNoStdin(name string, fs *flag.FlagSet, args []string) (exit int, done bool) {
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK, true
	case err != nil:
		fmt.Fprintln(os.Stderr, name+":", err)
		return conformance.ExitUsage, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, name+": unexpected argument:", fs.Arg(0))
		return conformance.ExitUsage, true
	}
	return conformance.ExitOK, false
}

// janitorRole is one ccpool role loaded from a role file.
type janitorRole struct {
	role roles.Role
	cfg  config.Config
	deps executor.Deps
	pool string // the pool key: the role's own PoolDir, else the inherited CCPOOL_POOL
}

// loadJanitorRoles decodes every role file strictly (the same decode a dispatch
// does), sorted by role name so the rotation is deterministic. A role file that
// is unreadable or invalid is an error: a role silently dropped would never be
// swept.
func loadJanitorRoles(opts janitorOptions, mkDeps func(config.Config, roles.Role) executor.Deps) ([]janitorRole, error) {
	var out []janitorRole
	seen := map[string]bool{}
	for _, f := range opts.roleFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read role file %s: %w", f, err)
		}
		ro, err := parseRole(raw)
		if err != nil {
			return nil, fmt.Errorf("role file %s: %w", f, err)
		}
		if seen[ro.Name] {
			return nil, fmt.Errorf("role file %s: role %q given twice", f, ro.Name)
		}
		seen[ro.Name] = true
		role, cfg, err := loadRoleAndConfig(ro, opts.cfgPath)
		if err != nil {
			return nil, fmt.Errorf("role file %s: %w", f, err)
		}
		pool := role.CCPool.PoolDir
		if pool == "" {
			pool = os.Getenv("CCPOOL_POOL")
		}
		out = append(out, janitorRole{role: role, cfg: cfg, deps: mkDeps(cfg, role), pool: pool})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].role.Name < out[j].role.Name })
	return out, nil
}

// passStat accumulates one pass across the run.
type passStat struct {
	Runs      int   `json:"runs"`
	Reclaimed int   `json:"reclaimed"`
	Errors    int   `json:"errors"`
	ElapsedMS int64 `json:"elapsed_ms"`
}

// janitorRun is the state of one run, and the source of its record.
type janitorRun struct {
	passes janitorPasses
	stats  map[string]*passStat
	cutOff bool
	// cutRoles names the roles whose own share of the deadline expired during
	// a pass (bead pg2-tjxs0y). The run as a whole may still be well inside its
	// deadline, so this separates a role cut at its share from a run that
	// really reached its deadline.
	cutRoles []string
	skipped  []string
	// counts is each role-level pass's progress counters, in run order
	// (INV-CCH-41, design D2).
	counts []*passCounts
	// pool is the pool key of the role whose passes are running, stamped on
	// their counters so the list numbers can be read per pool.
	pool string
}

// listStats are the list-call numbers of a run or of one pool.
type listStats struct {
	Calls    int   `json:"calls"`
	Timeouts int   `json:"timeouts"`
	WallMS   int64 `json:"wall_ms"`
}

// listTotals sums the counters' list numbers over the run and per pool.
func listTotals(counts []*passCounts) (total listStats, byPool map[string]*listStats) {
	byPool = map[string]*listStats{}
	for _, c := range counts {
		total.Calls += c.ListCalls
		total.Timeouts += c.ListTimeouts
		total.WallMS += c.ListWallMS
		st := byPool[c.Pool]
		if st == nil {
			st = &listStats{}
			byPool[c.Pool] = st
		}
		st.Calls += c.ListCalls
		st.Timeouts += c.ListTimeouts
		st.WallMS += c.ListWallMS
	}
	return total, byPool
}

// runPass runs one pass under ctx, timing it and recording its count and error
// (a panic counts as an error: a pass that blows up must not stop the others).
func (j *janitorRun) runPass(ctx context.Context, name, role string, deps executor.Deps, fn func(ctx context.Context) int) {
	st := j.stats[name]
	if st == nil {
		st = &passStat{}
		j.stats[name] = st
	}
	counts := &passCounts{Pass: name, Role: role, Pool: j.pool}
	start := time.Now()
	n, err := func() (n int, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return fn(withCounts(ctx, counts)), nil
	}()
	elapsed := time.Since(start)
	counts.finish(n)
	if name != passLockGC {
		j.counts = append(j.counts, counts)
	}
	st.Runs++
	st.Reclaimed += n
	st.ElapsedMS += elapsed.Milliseconds()
	if err != nil {
		st.Errors++
		slog.Warn("janitor: pass failed", "pass", name, "role", role, "err", err)
	}
	if ctx.Err() != nil {
		j.cutOff = true
		if n := len(j.cutRoles); n == 0 || j.cutRoles[n-1] != role {
			j.cutRoles = append(j.cutRoles, role)
		}
	}
	recordSweepPass("janitor", role, name, n, elapsed, deps)
	if n > 0 {
		slog.Info("janitor: reclaimed", "pass", name, "role", role, "reclaimed", n)
	}
}

// serveJanitor is runJanitor's testable core. mkDeps builds a role's executor
// seams (a test keeps bd and ccpool off the machine) and passes are the sweep
// passes. It returns the process exit code.
func serveJanitor(ctx context.Context, stdout io.Writer, opts janitorOptions, mkDeps func(config.Config, roles.Role) executor.Deps, passes janitorPasses) int {
	cfg, err := rolefile.LoadConfig(opts.cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "janitor:", err)
		return conformance.ExitUsage
	}
	useRemoveTimeout(cfg)
	jroles, err := loadJanitorRoles(opts, mkDeps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "janitor:", err)
		return conformance.ExitUsage
	}
	if opts.dryRun {
		return janitorDryRun(ctx, stdout, jroles, cfg)
	}

	stateDir := handlerstate.Dir(cfg)
	lockDir := sessionlock.Dir(stateDir)
	log := handlerstate.NewEventLog(cfg)
	started := time.Now()
	clockStart := passes.clock()

	lock, err := sessionlock.TryLock(lockDir, janitorLockKey)
	if err != nil {
		if errors.Is(err, sessionlock.ErrHeld) {
			fmt.Fprintln(stdout, "janitor: already running; skipping this run")
			_ = log.Emit("info", janitorRunEvent, "janitor run skipped: another run is active", map[string]any{
				"outcome":     janitorSkipped,
				"deadline_ms": opts.deadline.Milliseconds(),
			})
			return conformance.ExitOK
		}
		fmt.Fprintln(os.Stderr, "janitor: cannot take the single-flight lock:", err)
		return conformance.ExitError
	}
	defer lock.Unlock()

	// Count every session list of the role passes and, while the bounded
	// diagnostic is on, sample a slow one.
	diag := newListDiag(opts, stateDir, log)
	for i := range jroles {
		if jroles[i].deps.CC != nil {
			jroles[i].deps.CC = &listObserver{Runner: jroles[i].deps.CC, diag: diag}
		}
	}

	startIdx := nextRotation(stateDir, len(jroles))
	run := &janitorRun{passes: passes, stats: map[string]*passStat{}}
	runCtx, cancel := context.WithTimeout(ctx, opts.deadline)
	defer cancel()

	poolDone := map[string]bool{}
	defaultPoolDone := false
	var order []string
	for i := range jroles {
		jr := jroles[(startIdx+i)%len(jroles)]
		order = append(order, jr.role.Name)
		if runCtx.Err() != nil {
			run.cutOff = true
			run.skipped = append(run.skipped, jr.role.Name)
			continue
		}
		// The role's share: what is left of the run, divided over the roles still
		// to go, so time a quick role leaves over is not lost.
		share := time.Until(deadlineOf(runCtx)) / time.Duration(len(jroles)-i)
		rctx, rcancel := context.WithTimeout(runCtx, share)
		name := jr.role.Name
		run.pool = jr.pool
		// Pool-level passes: once per distinct pool however many roles share it.
		if !poolDone[jr.pool] {
			poolDone[jr.pool] = true
			run.runPass(rctx, passClosedBead, name, jr.deps, func(ctx context.Context) int { return passes.closedBead(ctx, jr.role, jr.cfg, jr.deps) })
		}
		// Default-pool pass: the same condition runSweeps keys on, once per run.
		if !defaultPoolDone && (jr.role.CCPool.PoolDir != "" || os.Getenv("CCPOOL_POOL") != "") {
			defaultPoolDone = true
			run.runPass(rctx, passDefaultPool, name, jr.deps, func(ctx context.Context) int { return passes.defaultPool(ctx, jr.cfg) })
		}
		run.runPass(rctx, passOrphan, name, jr.deps, func(ctx context.Context) int { return passes.orphan(ctx, jr.role, jr.cfg, jr.deps) })
		run.runPass(rctx, passWorktree, name, jr.deps, func(ctx context.Context) int { return passes.worktree(ctx, jr.role, jr.cfg, jr.deps) })
		rcancel()
	}
	// Lock-file collection: handler-wide and cheap, once per run, after every
	// role's pass so the locks they just used count as freshly used.
	lockDeps := executor.Deps{LockDir: lockDir}
	if len(jroles) > 0 {
		lockDeps = jroles[0].deps
	}
	// Lock GC is deliberately detached from the run deadline (a cheap, handler-wide pass that always runs).
	run.runPass(context.WithoutCancel(runCtx), passLockGC, "", lockDeps, func(context.Context) int { return passes.lockGC(lockDeps) })

	outcome := janitorCompleted
	if run.cutOff || runCtx.Err() != nil {
		outcome = janitorDeadline
	}
	rolesDone := finishedRoles(order, run.cutRoles, run.skipped)
	elapsed := time.Since(started)
	reclaimed := 0
	for _, st := range run.stats {
		reclaimed += st.Reclaimed
	}
	listTotal, listByPool := listTotals(run.counts)
	fields := map[string]any{
		"list_calls":    listTotal.Calls,
		"list_timeouts": listTotal.Timeouts,
		"list_wall_ms":  listTotal.WallMS,
		"list_by_pool":  listByPool,
		"outcome":       outcome,
		"elapsed_ms":    elapsed.Milliseconds(),
		"deadline_ms":   opts.deadline.Milliseconds(),
		"roles":         order,
		"pools":         len(poolDone),
		"passes":        run.stats,
		"reclaimed":     reclaimed,
		"roles_skipped": run.skipped,
		"roles_cut":     run.cutRoles,
		"roles_done":    rolesDone,
		"counters":      run.counts,
	}
	_ = log.Emit("info", janitorRunEvent, "janitor run "+outcome, fields)
	emitJanitorGauges(ctx, passes.emit, time.Now(), outcome, elapsed, run.stats, rolesDone)
	emitJanitorProgressGauges(ctx, passes.emit, run.counts)
	fmt.Fprintf(stdout, "janitor: outcome=%s elapsed=%s roles=%d pools=%d reclaimed=%d\n", outcome, elapsed.Round(time.Millisecond), len(jroles), len(poolDone), reclaimed)
	if passes.emit != nil {
		emitBuildInfo(ctx, passes)
		emitCensus(ctx, passes, jroles, passes.clock().Sub(clockStart))
	}
	flushGauges(ctx, passes.flush)
	return conformance.ExitOK
}

// deadlineOf returns ctx's deadline (a janitor run context always has one).
func deadlineOf(ctx context.Context) time.Time {
	d, _ := ctx.Deadline()
	return d
}

// nextRotation returns this run's start index into n roles and advances the
// persisted counter, so the role a run starts on rotates between runs. Called
// under the single-flight lock. Best effort: an unreadable or unwritable counter
// starts at 0.
func nextRotation(stateDir string, n int) int {
	if n <= 0 {
		return 0
	}
	path := filepath.Join(stateDir, janitorRotationFile)
	count := 0
	if b, err := os.ReadFile(path); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && v >= 0 {
			count = v
		}
	}
	if err := os.MkdirAll(stateDir, 0o755); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, []byte(strconv.Itoa(count+1)+"\n"), 0o644) == nil {
			_ = os.Rename(tmp, path)
		}
	}
	return count % n
}

// finishedRoles are the roles of order that were neither cut at their share nor
// skipped: they ran every pass to the end.
func finishedRoles(order, cut, skipped []string) []string {
	bad := map[string]bool{}
	for _, r := range cut {
		bad[r] = true
	}
	for _, r := range skipped {
		bad[r] = true
	}
	var out []string
	for _, r := range order {
		if !bad[r] {
			out = append(out, r)
		}
	}
	return out
}
