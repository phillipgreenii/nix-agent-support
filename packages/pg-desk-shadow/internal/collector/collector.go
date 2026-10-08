// Package collector runs the shadow tick loop: aligned slots, one pg-desk call
// per slot under the sandbox, bracketed rows, the live-side reader and the kill
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

// Exec runs a sandboxed child (satisfied by *runner.Runner).
type Exec interface {
	Run(ctx context.Context, timeout time.Duration, argv ...string) (runner.Result, error)
}

// Config configures a collector.
type Config struct {
	Layout   scratch.Layout
	Manifest scratch.Manifest
	Exec     Exec

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
}

// SpendPoint is one tick's GraphQL spend (lower bound).
type SpendPoint struct {
	At   string `json:"at"`
	Cost int    `json:"cost"`
}

// Collector is one collector instance.
type Collector struct {
	cfg  Config
	st   State
	prev Projection
	db   sqlite.DB
	pol  safety.Policy
	tl   map[string]*tail.Tailer
	// logUnreadable is set when the sandbox denial scan failed once.
	logUnreadable bool
	gapWritten    bool
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
	c.st = State{Tails: map[string]tail.State{}, ShimOffsets: map[string]int64{}}
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
	if b, err := os.ReadFile(c.cfg.Layout.ProjectionFile()); err == nil {
		var p Projection
		if json.Unmarshal(b, &p) == nil {
			c.prev = p
		}
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

func (c *Collector) saveProjection() error {
	b, err := json.Marshal(c.prev)
	if err != nil {
		return err
	}
	return atomicWrite(c.cfg.Layout.ProjectionFile(), b)
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
		return false, c.row(schema.Row{
			Kind: schema.KindTick, Slot: schema.Format(slot), TickStart: schema.Format(started), TickEnd: schema.Format(c.cfg.Now()),
			Status: schema.StatusSkippedBudget, SkippedBudget: true, Budget: bud, Warmup: c.inWarmup(),
		})
	}

	cursorFrom, err := c.db.Int(ctx, CursorSQL(m.Consumer))
	if err != nil {
		return false, fmt.Errorf("tick: read cursor: %w", err)
	}
	hydBefore, err := c.db.Int(ctx, HydrationsSQL)
	if err != nil {
		return false, fmt.Errorf("tick: read hydrations: %w", err)
	}
	builds := c.builds(m)
	if err := c.row(schema.Row{Kind: schema.KindTickStart, Slot: schema.Format(slot), TickStart: schema.Format(started), CursorFrom: cursorFrom, Builds: builds, Budget: bud, Warmup: c.inWarmup()}); err != nil {
		return false, err
	}

	argv := []string{filepath.Join(c.cfg.Layout.BinDir(), "pg-desk"), "pr", "changes", "--consumer", m.Consumer, "--json"}
	res, rerr := c.cfg.Exec.Run(ctx, c.cfg.TickTimeout, argv...)
	ended := c.cfg.Now()
	if errors.Is(rerr, runner.ErrSafety) {
		return false, fmt.Errorf("%w: %v", ErrKill, rerr)
	}

	row := schema.Row{
		Kind: schema.KindTick, Slot: schema.Format(slot), TickStart: schema.Format(started), TickEnd: schema.Format(ended),
		DurationMS: ended.Sub(started).Milliseconds(), Builds: builds, Budget: bud, Warmup: c.inWarmup(), CursorFrom: cursorFrom,
	}
	exit := res.Exit
	failed := false
	switch {
	case rerr != nil:
		failed = true
		exit = -1
		row.Error = Scrub(rerr.Error(), m.Scrub)
	case exit == 1:
		failed = true
		row.Error = Scrub(firstLine(res.Stderr), m.Scrub)
	}
	row.ExitCode = schema.ExitPtr(exit)

	var env Envelope
	if rerr == nil && (exit == 0 || exit == 2 || exit == 3) {
		var perr error
		env, perr = ParseEnvelope(res.Stdout)
		if perr != nil {
			failed = true
			row.Error = Scrub(perr.Error(), m.Scrub)
		}
	}
	if exit == 3 {
		failed = true
	}
	row.CursorTo = env.Cursor.To
	// A source reason can quote the failing search string (a repo slug, a
	// login): scrub it like any other text that could leave the scratch tree.
	for _, src := range env.Sources {
		src.Reason = Scrub(src.Reason, m.Scrub)
		row.Sources = append(row.Sources, src)
	}

	// Projection and its diff.
	cur, perr := ReadProjection(ctx, c.db)
	var diff map[string][]string
	if perr != nil {
		c.logf("projection: %v", Scrub(perr.Error(), m.Scrub))
	} else {
		diff = Diff(c.prev, cur)
	}
	for _, r := range env.Records {
		it := schema.Item{EntityID: r.ID, Kinds: r.Kinds, Seq: r.Seq, Origin: r.Origin, At: r.At}
		if it.Kinds == nil {
			it.Kinds = []string{}
		}
		it.Fields = diff[r.ID]
		if p, ok := cur[r.ID]; ok {
			it.UpdatedAt = p["updated_at"]
		}
		row.Items = append(row.Items, it)
	}

	hydAfter, herr := c.db.Int(ctx, HydrationsSQL)
	if herr == nil {
		row.Hydrations = hydAfter - hydBefore
	}
	row.GraphQLCostSum = CostInWindow(c.cfg.Layout.ConnectorLog(), started, ended)

	// Safety counters.
	denials := 0
	if res.Denied {
		denials++
	}
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

	// Status and the warm-up window.
	switch {
	case failed || unknown > 0:
		row.Status = schema.StatusFailed
		c.st.FailedStreak++
	case exit == 2:
		row.Status = schema.StatusPartial
		c.st.FailedStreak = 0
	default:
		row.Status = schema.StatusOK
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
	if perr == nil {
		c.prev = cur
		if err := c.saveProjection(); err != nil {
			return true, err
		}
	}
	for name, t := range c.tl {
		ts := c.st.Tails[name]
		if _, serr := t.Sync(&ts); serr != nil {
			c.logf("live tail %s: %v", name, Scrub(serr.Error(), m.Scrub))
		}
		c.st.Tails[name] = ts
	}
	c.logf("slot %s status=%s exit=%d items=%d hydrations=%d cost=%d dur=%dms warmup=%v", schema.Format(slot), row.Status, exit, len(row.Items), row.Hydrations, row.GraphQLCostSum, row.DurationMS, row.Warmup)

	// Stop conditions.
	if denials > 0 {
		return true, fmt.Errorf("%w: %d sandbox denial(s): a tool tried to write outside the scratch directory", ErrKill, denials)
	}
	if writes > 0 {
		return true, fmt.Errorf("%w: %d write verb(s) rejected by the shims", ErrKill, writes)
	}
	c.st.Spend = append(c.st.Spend, SpendPoint{At: schema.Format(ended), Cost: row.GraphQLCostSum})
	if over := c.spendOver(ended); over > c.cfg.KillPointsPerHour && c.cfg.KillPointsPerHour > 0 {
		return true, fmt.Errorf("%w: shadow spend %d points in the last hour exceeds %d", ErrKill, over, c.cfg.KillPointsPerHour)
	}
	if c.cfg.MaxFailedTicks > 0 && c.st.FailedStreak > c.cfg.MaxFailedTicks {
		return true, fmt.Errorf("%w: %d consecutive failed ticks", ErrKill, c.st.FailedStreak)
	}
	return true, nil
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

// CostInWindow sums graphql_cost of connector log rows whose time falls in
// [from, to]. It is a LOWER BOUND: older rows lack the field and a show row
// excludes its metadata read.
func CostInWindow(path string, from, to time.Time) int {
	b, err := budget.ReadTail(path, 4<<20)
	if err != nil || len(b) == 0 {
		return 0
	}
	sum := 0
	for _, line := range bytes.Split(b, []byte("\n")) {
		var r struct {
			Time string `json:"time"`
			Cost *int   `json:"graphql_cost"`
		}
		if json.Unmarshal(line, &r) != nil || r.Cost == nil {
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
