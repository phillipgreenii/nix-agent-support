// Package collector runs the shadow tick loop: aligned slots, one change-feed
// poll per watched query and daemon instance per slot (the daemon's own client
// mode, under the sandbox), bracketed rows, the live-side reader and the kill
// criteria.
package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/budget"
	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
	"github.com/phillipgreenii/pg-desk-shadow/internal/shim"
	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
	"github.com/phillipgreenii/pg-desk-shadow/internal/tail"
)

// Errors the loop ends with.
var (
	// ErrKill: a kill criterion tripped (exit 4).
	ErrKill = errors.New("kill criterion")
	// ErrAlreadyRunning: another collector holds the scratch lock.
	ErrAlreadyRunning = errors.New("another collector holds the scratch lock")
)

// Exec runs a sandboxed child (satisfied by *runner.Runner). RunStdin feeds the
// request on stdin and appends extraEnv to the child environment.
type Exec interface {
	Run(ctx context.Context, timeout time.Duration, argv ...string) (runner.Result, error)
	RunStdin(ctx context.Context, timeout time.Duration, stdin string, extraEnv []string, argv ...string) (runner.Result, error)
}

// Daemon is the shadow daemon child the run supervises. cmdRun starts it
// before Collector.Run and stops it after; Sample (called once per tick, so
// once per slot with a tick row) restarts a child whose process died and
// returns the health reading the row carries. A nil Config.Daemon means the
// run has no daemon and no row carries a sample.
type Daemon interface {
	Start(ctx context.Context) error
	Sample(ctx context.Context) schema.DaemonSample
	Stop() error
}

// Config configures a collector.
type Config struct {
	Layout   scratch.Layout
	Manifest scratch.Manifest
	Exec     Exec
	// Daemon is the supervised shadow daemon (nil: none, rows carry no sample).
	Daemon Daemon

	// ClientBin is the pinned pg-connector-github (the daemon's own client
	// modes pr and ci are exec'd from it) and ClientConfig its --config file.
	// ClientEnv is appended to the child environment of every client call (empty
	// in the real run; the tests point their stand-in at its files with it).
	ClientBin, ClientConfig string
	ClientEnv               []string
	// Queries are the watched query names, each polled on both instances, and
	// Consumer the change-feed consumer name; both default to the manifest's.
	Queries  []string
	Consumer string
	// AfterRowWrite is a test-only hook called after a tick row is written (and
	// the highest-seq map saved) and before any acknowledgement; an error from
	// it ends the tick with no acknowledgement sent, as a crash would.
	AfterRowWrite func() error

	Period            time.Duration
	TickTimeout       time.Duration
	KillPointsPerHour int
	MaxFailedTicks    int
	FloorMargin       int
	// MaxTicks stops the loop after that many executed (not skipped) ticks;
	// 0 means run until stopped.
	MaxTicks int
	// WarmupExclusion enables the warm-up window (phase A). Phase B turns it
	// off: its first ticks ARE the measurement.
	WarmupExclusion bool
	// SandboxDenialScan reads the system log for denials after each tick; an
	// unreadable log is recorded once, not counted as zero.
	SandboxDenialScan bool
	Procs             []string // process names whose denials count

	Now   func() time.Time
	Sleep func(ctx context.Context, until time.Time) error
	Log   io.Writer
}

// State is persisted in collector/state.json.
type State struct {
	LastSlot     string                `json:"last_slot,omitempty"`
	LastDuration int64                 `json:"last_duration_ms,omitempty"`
	WarmupDone   bool                  `json:"warmup_done"`
	ZeroStreak   int                   `json:"zero_streak"`
	FailedStreak int                   `json:"failed_streak"`
	Executed     int                   `json:"executed"`
	Tails        map[string]tail.State `json:"tails"`
	ShimOffsets  map[string]int64      `json:"shim_offsets"`
	Spend        []SpendPoint          `json:"spend"`
	LogScanSince string                `json:"log_scan_since,omitempty"`
	// CostWindowEnd is the end of the last tick row's cost window (RFC 3339,
	// nanoseconds): the next row's window starts just after it.
	CostWindowEnd string `json:"cost_window_end,omitempty"`
	// HighestSeq maps "pr:<query>" and "ci:<query>" to the highest feed seq a
	// written tick row already holds, so a redelivered row is not written twice.
	HighestSeq map[string]int64 `json:"highest_seq,omitempty"`
}

// SpendPoint is one tick's GraphQL spend (lower bound).
type SpendPoint struct {
	At   string `json:"at"`
	Cost int    `json:"cost"`
}

// Collector is one collector instance.
type Collector struct {
	cfg Config
	st  State
	db  sqlite.DB
	pol safety.Policy
	tl  map[string]*tail.Tailer
	// logUnreadable is set when the sandbox denial scan failed once.
	logUnreadable bool
	gapWritten    bool
	// runStart is the start of the current Run (zero outside one): the first
	// cost window of a run begins there.
	runStart time.Time
}

// New builds a collector and loads its state.
func New(cfg Config) (*Collector, error) {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepUntil(cfg.Now)
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	if cfg.Period <= 0 {
		cfg.Period = time.Minute
	}
	if cfg.Consumer == "" {
		cfg.Consumer = cfg.Manifest.Consumer
	}
	if len(cfg.Queries) == 0 {
		cfg.Queries = cfg.Manifest.Queries
	}
	if cfg.ClientBin == "" || len(cfg.Queries) == 0 || cfg.Consumer == "" {
		return nil, errors.New("collector: the client binary, the watched queries and the consumer are required")
	}
	c := &Collector{cfg: cfg, db: sqlite.DB{Path: cfg.Layout.StoreDB()}}
	c.pol = cfg.Layout.Policy(cfg.Manifest)
	c.tl = map[string]*tail.Tailer{
		"router-events": {Src: cfg.Manifest.Live.RouterEvents, Dst: filepath.Join(cfg.Layout.LiveDir(), "router-events.jsonl"), StartAtEnd: true},
		"router-queue":  {Src: cfg.Manifest.Live.RouterQueue, Dst: filepath.Join(cfg.Layout.LiveDir(), "router-queue.jsonl"), StartAtEnd: true},
		"run-record":    {Src: cfg.Manifest.Live.RunRecord, Dst: filepath.Join(cfg.Layout.LiveDir(), "run-record.log")},
	}
	for _, t := range c.tl {
		t.Now = cfg.Now
	}
	if err := c.loadState(); err != nil {
		return nil, err
	}
	return c, nil
}

func sleepUntil(now func() time.Time) func(context.Context, time.Time) error {
	return func(ctx context.Context, until time.Time) error {
		d := until.Sub(now())
		if d <= 0 {
			return ctx.Err()
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
}

func (c *Collector) loadState() error {
	c.st = State{Tails: map[string]tail.State{}, ShimOffsets: map[string]int64{}, HighestSeq: map[string]int64{}}
	b, err := os.ReadFile(c.cfg.Layout.StateFile())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &c.st); err != nil {
		return fmt.Errorf("collector state: %w", err)
	}
	if c.st.Tails == nil {
		c.st.Tails = map[string]tail.State{}
	}
	if c.st.ShimOffsets == nil {
		c.st.ShimOffsets = map[string]int64{}
	}
	if c.st.HighestSeq == nil {
		c.st.HighestSeq = map[string]int64{}
	}
	return nil
}

func (c *Collector) saveState() error {
	b, err := json.MarshalIndent(c.st, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(c.cfg.Layout.StateFile(), b)
}

func atomicWrite(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Collector) logf(format string, a ...any) {
	_, _ = fmt.Fprintf(c.cfg.Log, "%s %s\n", c.cfg.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

func (c *Collector) row(r schema.Row) error {
	r.Phase = c.cfg.Manifest.Phase
	return schema.Append(c.cfg.Layout.TicksFile(), r)
}

// Lock takes the exclusive scratch lock; the returned func releases it.
func Lock(l scratch.Layout) (func(), error) {
	f, err := os.OpenFile(l.LockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrAlreadyRunning
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// SelfCheck proves the wiring before the first tick: the sandbox denies an
// outside write, and the real shims in the scratch bin reject write verbs and
// pass a read. A failure refuses the run.
func (c *Collector) SelfCheck(ctx context.Context) error {
	if err := safety.SelfTest(ctx, c.cfg.Manifest.SandboxExec, c.pol.Scratch, c.cfg.Layout.TmpDir()); err != nil {
		return err
	}
	probe := func(want int, argv ...string) error {
		res, err := c.cfg.Exec.Run(ctx, 30*time.Second, argv...)
		if err != nil {
			return fmt.Errorf("shim self-check %v: %w", argv[:2], err)
		}
		if res.Exit != want {
			return fmt.Errorf("shim self-check %v: exit %d, want %d (%s)", argv[:2], res.Exit, want, strings.TrimSpace(res.Stderr))
		}
		return nil
	}
	bin := c.cfg.Layout.BinDir()
	if err := probe(shim.ExitRejected, filepath.Join(bin, "gh"), "api", "-X", "POST", "repos/x/y/issues"); err != nil {
		return err
	}
	if err := probe(shim.ExitRejected, filepath.Join(bin, "gh"), "pr", "merge", "1"); err != nil {
		return err
	}
	if err := probe(shim.ExitRejected, filepath.Join(bin, "bd"), "create", "x"); err != nil {
		return err
	}
	// The probes above were deliberate: forget their log rows so the run's own
	// write-reject counter starts clean.
	for _, tool := range []string{"gh", "bd"} {
		shim.MarkSelfCheck(c.cfg.Layout.ShimLog(tool))
	}
	return c.syncShimOffsets()
}

func (c *Collector) syncShimOffsets() error {
	for _, tool := range []string{"gh", "bd"} {
		_, _, off, err := shim.CountRejects(c.cfg.Layout.ShimLog(tool), c.st.ShimOffsets[tool])
		if err != nil {
			return err
		}
		c.st.ShimOffsets[tool] = off
	}
	return nil
}

// Recover turns a trailing tick_start without a tick row into a recovered tick
// row built from the scratch change_log, and records the gap.
func (c *Collector) Recover(ctx context.Context) error {
	rows, err := schema.ReadAll(c.cfg.Layout.TicksFile())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	last := rows[len(rows)-1]
	now := c.cfg.Now()
	resumed := true
	if last.Kind == schema.KindTickStart {
		cur, err := c.db.Int(ctx, CursorSQL(c.cfg.Manifest.Consumer))
		if err != nil {
			return fmt.Errorf("recover: %w", err)
		}
		items, err := RecoverItems(ctx, c.db, last.CursorFrom, cur)
		if err != nil {
			return fmt.Errorf("recover: %w", err)
		}
		if err := c.row(schema.Row{
			Kind: schema.KindTick, Slot: last.Slot, TickStart: last.TickStart, Status: schema.StatusRecovered,
			Recovered: true, Items: items, CursorFrom: last.CursorFrom, CursorTo: cur, Warmup: last.Warmup, Builds: last.Builds,
		}); err != nil {
			return err
		}
		c.logf("recovered unfinished tick %s with %d items", last.Slot, len(items))
		if err := c.row(schema.Row{Kind: schema.KindGap, Reason: schema.GapUnfinish, GapFrom: last.TickStart, GapTo: schema.Format(now), At: schema.Format(now)}); err != nil {
			return err
		}
	}
	lastAt := lastTime(rows)
	if !lastAt.IsZero() {
		if err := c.row(schema.Row{Kind: schema.KindGap, Reason: schema.GapRestart, GapFrom: schema.Format(lastAt), GapTo: schema.Format(now), At: schema.Format(now)}); err != nil {
			return err
		}
	}
	_ = resumed
	return nil
}

func lastTime(rows []schema.Row) time.Time {
	var best time.Time
	for _, r := range rows {
		for _, s := range []string{r.TickEnd, r.TickStart, r.At} {
			if t, ok := schema.Time(s); ok && t.After(best) {
				best = t
			}
		}
	}
	return best
}

// NextBoundary returns the first slot boundary strictly after t.
func NextBoundary(t time.Time, period time.Duration) time.Time {
	p := period.Nanoseconds()
	n := t.UnixNano()
	return time.Unix(0, (n/p+1)*p).UTC()
}

// Run is the loop. It returns nil when it stopped on request (MaxTicks,
// context), ErrKill (wrapped) when a kill criterion tripped.
func (c *Collector) Run(ctx context.Context, resumed bool) error {
	start := c.cfg.Now()
	c.runStart = start
	if err := c.row(schema.Row{Kind: schema.KindRunStart, At: schema.Format(start), Resumed: resumed, Params: map[string]any{
		"period_s": c.cfg.Period.Seconds(), "kill_points_per_hour": c.cfg.KillPointsPerHour,
		"max_failed_ticks": c.cfg.MaxFailedTicks, "warmup_exclusion": c.cfg.WarmupExclusion,
	}, Builds: c.cfg.Manifest.Builds}); err != nil {
		return err
	}
	defer func() {
		_ = c.row(schema.Row{Kind: schema.KindRunEnd, At: schema.Format(c.cfg.Now())})
		_ = c.saveState()
	}()
	var prevSlot time.Time
	if t, ok := schema.Time(c.st.LastSlot); ok {
		prevSlot = t
	}
	executed := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		slot := NextBoundary(c.cfg.Now(), c.cfg.Period)
		if err := c.cfg.Sleep(ctx, slot); err != nil {
			return nil
		}
		now := c.cfg.Now()
		if now.Sub(slot) > c.cfg.Period/2 {
			// Woke well after the slot (sleep, lid close, a stalled machine):
			// record the gap and realign instead of running a stale slot.
			if err := c.row(schema.Row{Kind: schema.KindGap, Reason: schema.GapSleep, GapFrom: schema.Format(slot), GapTo: schema.Format(now), At: schema.Format(now)}); err != nil {
				return err
			}
			c.gapWritten = true
			c.logf("gap: woke %s late; realigning", now.Sub(slot).Round(time.Second))
			continue
		}
		if !prevSlot.IsZero() && slot.Sub(prevSlot) > c.cfg.Period && !c.gapWritten {
			reason := schema.GapSleep
			if time.Duration(c.st.LastDuration)*time.Millisecond >= c.cfg.Period {
				reason = schema.GapOverrun
			}
			if err := c.row(schema.Row{Kind: schema.KindGap, Reason: reason, GapFrom: schema.Format(prevSlot.Add(c.cfg.Period)), GapTo: schema.Format(slot), At: schema.Format(now)}); err != nil {
				return err
			}
		}
		c.gapWritten = false
		ran, err := c.Tick(ctx, slot)
		prevSlot = slot
		c.st.LastSlot = schema.Format(slot)
		if serr := c.saveState(); serr != nil {
			return serr
		}
		if err != nil {
			return err
		}
		if ran {
			executed++
			if c.cfg.MaxTicks > 0 && executed >= c.cfg.MaxTicks {
				return nil
			}
		}
	}
}

// Tick runs one slot. ran is false for a budget-skipped slot.
func (c *Collector) Tick(ctx context.Context, slot time.Time) (ran bool, err error) {
	m := c.cfg.Manifest
	started := c.cfg.Now()

	// One daemon sample per slot with a tick row, executed or skipped. It is
	// taken before the budget guard so a skipped slot still carries it; a slot
	// with no tick row at all (collector down) has none, and the report counts
	// that as not up.
	dsample := c.sampleDaemon(ctx)

	// Budget guard.
	readings, gerr := budget.Gather(started, budget.DefaultWindow, m.Live.ConnectorEvents, c.cfg.Layout.ConnectorLog())
	if gerr != nil {
		c.logf("budget: %v", Scrub(gerr.Error(), m.Scrub))
	}
	floor := budget.Floor(m.MaxPerPoll, c.cfg.FloorMargin)
	dec := budget.Decide(started, floor, readings)
	bud := &schema.Budget{Remaining: dec.Remaining, Source: dec.Source, Floor: floor, Known: dec.Known}
	if dec.Known {
		bud.ResetAt = schema.Format(dec.ResetAt)
	}
	if dec.Skip {
		c.logf("slot %s skipped_budget remaining=%d floor=%d source=%s", schema.Format(slot), dec.Remaining, floor, dec.Source)
		// The daemon spends in background work tied to no call, so a skipped slot
		// still carries the cost of its window and still feeds the kill criterion.
		ended := c.cfg.Now()
		cost := c.costWindow(started, ended)
		if err := c.row(schema.Row{
			Kind: schema.KindTick, Slot: schema.Format(slot), TickStart: schema.Format(started), TickEnd: schema.Format(ended),
			Status: schema.StatusSkippedBudget, SkippedBudget: true, Budget: bud, Warmup: c.inWarmup(), Daemon: dsample,
			GraphQLCostSum: cost,
		}); err != nil {
			return false, err
		}
		c.advanceCostWindow(ended)
		return false, c.spendKill(ended, cost)
	}

	builds := c.builds(m)
	if err := c.row(schema.Row{Kind: schema.KindTickStart, Slot: schema.Format(slot), TickStart: schema.Format(started), CursorFrom: 0, Builds: builds, Budget: bud, Warmup: c.inWarmup()}); err != nil {
		return false, err
	}

	// The change feed: the daemon's own client mode, once per watched query and
	// instance. Nothing here reads GitHub; the daemon spends, never the harness.
	polls, perr := c.pollFeed(ctx, started)
	ended := c.cfg.Now()
	if perr != nil {
		return false, fmt.Errorf("%w: %v", ErrKill, perr)
	}

	row := schema.Row{
		Kind: schema.KindTick, Slot: schema.Format(slot), TickStart: schema.Format(started), TickEnd: schema.Format(ended),
		DurationMS: ended.Sub(started).Milliseconds(), Builds: builds, Budget: bud, Warmup: c.inWarmup(), Daemon: dsample,
	}
	denials := 0
	failedPolls := 0
	var firstFailure string
	var cursorSeen []int64
	for _, p := range polls {
		row.Items = append(row.Items, p.items...)
		row.Sources = append(row.Sources, p.src)
		cursorSeen = append(cursorSeen, p.nextSeqs...)
		if p.denied {
			denials++
		}
		if p.failed {
			failedPolls++
			if firstFailure == "" {
				firstFailure = p.src.Reason
			}
		}
	}
	if len(cursorSeen) > 0 {
		row.CursorFrom, row.CursorTo = cursorSeen[0], cursorSeen[len(cursorSeen)-1]
	}
	exit := 0
	switch {
	case failedPolls == 0:
	case failedPolls == len(polls):
		exit = 3
	default:
		exit = 2
	}
	row.ExitCode = schema.ExitPtr(exit)
	if failedPolls > 0 {
		row.Error = fmt.Sprintf("%d of %d feed polls failed: %s", failedPolls, len(polls), firstFailure)
	}
	row.GraphQLCostSum = c.costWindow(started, ended)

	// Safety counters.
	if c.cfg.SandboxDenialScan && !c.logUnreadable {
		n, serr := safety.ScanSystemLog(ctx, started.Add(-2*time.Second), c.cfg.Procs)
		if serr != nil {
			c.logUnreadable = true
			c.logf("sandbox log scan unavailable (recorded as unverified): %v", Scrub(serr.Error(), m.Scrub))
		}
		denials += n
	}
	row.SandboxDenials = denials
	writes, unknown, rejects := 0, 0, 0
	for _, tool := range []string{"gh", "bd"} {
		w, u, off, cerr := shim.CountRejects(c.cfg.Layout.ShimLog(tool), c.st.ShimOffsets[tool])
		if cerr == nil {
			c.st.ShimOffsets[tool] = off
		}
		writes += w
		unknown += u
	}
	rejects = writes + unknown
	row.ShimRejects = rejects
	if unknown > 0 && row.Error == "" {
		row.Error = fmt.Sprintf("%d shim-rejected call(s) of an unknown verb", unknown)
	}

	// Status and the warm-up window. Status: ok when every query answered,
	// partial when some failed, failed when all did; any failed query also
	// counts as a failed tick for --max-failed-ticks.
	switch {
	case (failedPolls > 0 && failedPolls == len(polls)) || unknown > 0:
		row.Status = schema.StatusFailed
	case failedPolls > 0:
		row.Status = schema.StatusPartial
	default:
		row.Status = schema.StatusOK
	}
	if failedPolls > 0 || unknown > 0 {
		c.st.FailedStreak++
	} else {
		c.st.FailedStreak = 0
	}
	if row.Warmup && row.Status != schema.StatusFailed {
		if row.Hydrations == 0 {
			c.st.ZeroStreak++
		} else {
			c.st.ZeroStreak = 0
		}
		if c.st.ZeroStreak >= 2 {
			c.st.WarmupDone = true
		}
	}
	c.st.LastDuration = row.DurationMS
	c.st.Executed++

	if werr := c.row(row); werr != nil {
		return true, werr
	}
	// The row is on disk (schema.Append syncs): remember how far each key has
	// been written, so a redelivery after a crash writes nothing twice, then
	// let the test hook stand in for a crash before the acknowledgements.
	c.advanceCostWindow(ended)
	for _, p := range polls {
		if p.maxSeq > c.st.HighestSeq[p.key] {
			c.st.HighestSeq[p.key] = p.maxSeq
		}
		if p.replayed && p.canAck {
			// A reset starts a new position: the key's rows up to the
			// acknowledged one are consumed, whatever the old numbering was.
			c.st.HighestSeq[p.key] = p.ackSeq
		}
	}
	if serr := c.saveState(); serr != nil {
		return true, serr
	}
	if c.cfg.AfterRowWrite != nil {
		if herr := c.cfg.AfterRowWrite(); herr != nil {
			return true, herr
		}
	}
	ackDenied, aerr := c.ackFeed(ctx, polls)
	if aerr != nil {
		return true, fmt.Errorf("%w: %v", ErrKill, aerr)
	}
	denials += ackDenied

	for name, t := range c.tl {
		ts := c.st.Tails[name]
		if _, serr := t.Sync(&ts); serr != nil {
			c.logf("live tail %s: %v", name, Scrub(serr.Error(), m.Scrub))
		}
		c.st.Tails[name] = ts
	}
	c.logf("slot %s status=%s exit=%d items=%d polls=%d failed=%d cost=%d dur=%dms warmup=%v", schema.Format(slot), row.Status, exit, len(row.Items), len(polls), failedPolls, row.GraphQLCostSum, row.DurationMS, row.Warmup)

	// Stop conditions.
	if denials > 0 {
		return true, fmt.Errorf("%w: %d sandbox denial(s): a tool tried to write outside the scratch directory", ErrKill, denials)
	}
	if writes > 0 {
		return true, fmt.Errorf("%w: %d write verb(s) rejected by the shims", ErrKill, writes)
	}
	if err := c.spendKill(ended, row.GraphQLCostSum); err != nil {
		return true, err
	}
	if c.cfg.MaxFailedTicks > 0 && c.st.FailedStreak > c.cfg.MaxFailedTicks {
		return true, fmt.Errorf("%w: %d consecutive failed ticks", ErrKill, c.st.FailedStreak)
	}
	return true, nil
}

// costWindow is the daemon's GraphQL spend (kind=origin rows of the scratch
// event log) from just after the previous row's window to ended. The windows of
// consecutive rows share no instant and leave none out, so no origin row is
// counted twice or dropped; the first row of a run starts at the run start (a
// bare Tick, with no run, at the tick start).
func (c *Collector) costWindow(started, ended time.Time) int {
	from := started
	if !c.runStart.IsZero() {
		from = c.runStart
	}
	if prev, ok := schema.Time(c.st.CostWindowEnd); ok {
		if next := prev.Add(time.Nanosecond); next.After(from) || c.runStart.IsZero() {
			from = next
		}
	}
	return CostInWindow(c.cfg.Layout.ConnectorLog(), from, ended)
}

// advanceCostWindow records ended as the end of the window just accounted for.
func (c *Collector) advanceCostWindow(ended time.Time) {
	if prev, ok := schema.Time(c.st.CostWindowEnd); ok && !ended.After(prev) {
		return
	}
	c.st.CostWindowEnd = ended.UTC().Format(time.RFC3339Nano)
}

// spendKill records one row's spend and applies the rolling-hour kill
// criterion; the skipped-budget row goes through it like an executed one.
func (c *Collector) spendKill(ended time.Time, cost int) error {
	c.st.Spend = append(c.st.Spend, SpendPoint{At: schema.Format(ended), Cost: cost})
	if over := c.spendOver(ended); over > c.cfg.KillPointsPerHour && c.cfg.KillPointsPerHour > 0 {
		return fmt.Errorf("%w: shadow spend %d points in the last hour exceeds %d", ErrKill, over, c.cfg.KillPointsPerHour)
	}
	return nil
}

// sampleDaemon reads the daemon's health for the slot; nil when the run has no
// daemon.
func (c *Collector) sampleDaemon(ctx context.Context) *schema.DaemonSample {
	if c.cfg.Daemon == nil {
		return nil
	}
	smp := c.cfg.Daemon.Sample(ctx)
	if smp.State == "" {
		// A sampler that names no state is not evidence of health.
		smp.State = schema.DaemonDown
	}
	return &smp
}

func (c *Collector) inWarmup() bool { return c.cfg.WarmupExclusion && !c.st.WarmupDone }

func (c *Collector) builds(m scratch.Manifest) map[string]string {
	out := map[string]string{}
	for k, v := range m.Builds {
		out[k] = v
	}
	return out
}

// spendOver sums the spend of the last hour and trims older points.
func (c *Collector) spendOver(now time.Time) int {
	var keep []SpendPoint
	sum := 0
	for _, p := range c.st.Spend {
		t, ok := schema.Time(p.At)
		if !ok || now.Sub(t) > time.Hour {
			continue
		}
		keep = append(keep, p)
		sum += p.Cost
	}
	c.st.Spend = keep
	return sum
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// CostInWindow sums graphql_cost of the daemon event log's kind=origin rows
// whose time falls in [from, to]. A row of any other kind (a kind=request row,
// or a row with no kind) adds nothing, so a request is never double-counted.
func CostInWindow(path string, from, to time.Time) int {
	b, err := budget.ReadTail(path, 4<<20)
	if err != nil || len(b) == 0 {
		return 0
	}
	sum := 0
	for _, line := range bytes.Split(b, []byte("\n")) {
		var r struct {
			Time string `json:"time"`
			Kind string `json:"kind"`
			Cost *int   `json:"graphql_cost"`
		}
		if json.Unmarshal(line, &r) != nil || r.Kind != "origin" || r.Cost == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, r.Time)
		if err != nil {
			continue
		}
		if !t.Before(from) && !t.After(to) {
			sum += *r.Cost
		}
	}
	return sum
}

// SortedKeys is a small helper for deterministic logs.
func SortedKeys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
