package discover

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// Level-source pulls: a sensor is a level source, an ordinary command query
// marked level_source whose JSONL answers are DIVERTED to level ingest in
// runAndEnqueue before Enqueue, so they never reach the queue (no dedupe,
// retention or unconsumed-expiry accounting applies). Level-source pulls are
// exempt from every level, from router pause, from the log soft-halt, from
// log_full / log_unwritable and from the load backoff: produce does not poll
// them at all, and the LevelPuller below has no gate to consult. They run
// concurrently, each under its own deadline, in a pass of their own, so one
// hung probe delays neither another sensor nor dispatch.
//
// Exported names: LevelIngester, LevelPullObserver, the PullFail* reason
// constants, LevelPullResult, RunLevelQueryOnce, LevelPuller (NewLevelPuller,
// Tick, PullAll, StartPass, Wait, RunOnce, LastTick, Failures, Sources).
//
// Strawman defaults (the design strawmen, all configuration): pull period 60s,
// hard deadline 20s (query.DefaultDeadline), start wait 10s, wake threshold 30s.

// LevelIngester is the level projection's ingest entry (core.LevelProjection
// implements it): it validates one JSON level answer against the shared schema
// and applies the ingest rules.
type LevelIngester interface {
	IngestJSON(raw []byte) (core.LevelOutcome, string, error)
}

// The reason label of a pull failure (SEN-5).
const (
	PullFailDeadline  = "deadline"
	PullFailExit      = "exit"
	PullFailMalformed = "malformed"
	PullFailRejected  = "rejected"
)

// LevelPullObserver is notified of pull failures per sensor and reason, the
// feed of the per-sensor failure counters.
type LevelPullObserver interface {
	OnLevelPullFailure(sensor, reason string)
}

// LevelPullTimingObserver is an optional extension of LevelPullObserver: an
// observer that also implements it is told each time a pull of a sensor ends,
// with how long it took, whatever its outcome (the feed of the per-sensor pull
// counter and duration gauge). Exported name: OnLevelPull.
type LevelPullTimingObserver interface {
	OnLevelPull(sensor string, elapsed time.Duration)
}

// LevelPullFailure is one failure of a pull.
type LevelPullFailure struct {
	Reason string
	Err    error
}

// LevelPullResult is the outcome of one pull: how many answers were applied
// (accepted or idempotent) and every failure. A pull with failures keeps the
// last level of whatever it did not answer.
type LevelPullResult struct {
	Applied  int
	Failures []LevelPullFailure
}

// levelPath is the diversion target runAndEnqueue is given for a level source.
type levelPath struct {
	ingest   LevelIngester
	extraEnv []string
	// onFailure, when set, is called once per failure (sensor, reason).
	onFailure func(sensor, reason string)
	result    *LevelPullResult
}

// RunLevelQueryOnce runs a level-source query once under its own deadline
// (s.Deadline, query.DefaultDeadline when unset), with extraEnv (NAME=value
// entries, for example PG_ROUTER_LEVEL_REFRESH=1 from a level refresh) added to
// the process environment for this run only: extraEnv bypasses the query's
// env_allow list, which narrows what the router passes through and does not
// restrict what the router itself sets. Every JSONL line of the output is
// ingested. When the deadline passes the query's whole process group is killed
// and any output already read is discarded. It never touches the queue. The
// returned error is non-nil only when ctx itself was cancelled.
func RunLevelQueryOnce(ctx context.Context, env query.Env, s query.Source, ingest LevelIngester, extraEnv []string) (LevelPullResult, error) {
	var res LevelPullResult
	lv := &levelPath{ingest: ingest, extraEnv: extraEnv, result: &res}
	err := pullLevelSource(ctx, env, s, lv)
	return res, err
}

func pullLevelSource(ctx context.Context, env query.Env, s query.Source, lv *levelPath) error {
	fail := func(reason string, err error) {
		lv.result.Failures = append(lv.result.Failures, LevelPullFailure{Reason: reason, Err: err})
		if lv.onFailure != nil {
			lv.onFailure(s.LevelSource, reason)
		}
	}
	deadline := s.Deadline
	if deadline <= 0 {
		deadline = query.DefaultDeadline
	}
	pctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	out, err := query.RunRaw(pctx, env, s.Query, lv.extraEnv)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(pctx.Err(), context.DeadlineExceeded) {
			fail(PullFailDeadline, fmt.Errorf("pull exceeded its %s deadline: %w", deadline, err))
		} else {
			fail(PullFailExit, err)
		}
		return nil
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		outcome, reason, ierr := lv.ingest.IngestJSON(line)
		switch {
		case ierr != nil:
			fail(PullFailRejected, fmt.Errorf("level answer %q not recorded: %w", line, ierr))
		case outcome == core.LevelRejected && reason == core.ReasonMalformed:
			fail(PullFailMalformed, fmt.Errorf("malformed level answer %q", line))
		case outcome == core.LevelRejected:
			fail(PullFailRejected, fmt.Errorf("level answer %q rejected: %s", line, reason))
		default:
			lv.result.Applied++
		}
	}
	if serr := sc.Err(); serr != nil {
		fail(PullFailMalformed, fmt.Errorf("read level answers: %w", serr))
	}
	// A successful pull that printed no line is an empty answer: the sensor
	// has no reading for any resource. It is not a failure; the last levels
	// stay and age under the staleness rule.
	return nil
}

// runLevelSource is runAndEnqueue's diversion for a level source: the answer
// goes to level ingest and nothing is enqueued.
func runLevelSource(ctx context.Context, env query.Env, s query.Source, lv *levelPath, obs SourceFailureObserver, activityObs SourceActivityObserver, rpt *ProduceReport, now func() time.Time) error {
	rpt.LastTick[s.Name] = now()
	if lv == nil || lv.ingest == nil {
		rpt.SourceErrors[s.Name] = fmt.Errorf("produce %s: a level source needs level ingest", s.Name)
		return nil
	}
	run := *lv
	if run.result == nil {
		run.result = &LevelPullResult{}
	}
	res := run.result
	if activityObs != nil {
		activityObs.OnSourceFetchStart(s.Name)
	}
	if obs != nil {
		obs.OnSourceAttemptStart(s.Name)
	}
	start := now()
	err := pullLevelSource(ctx, env, s, &run)
	elapsed := now().Sub(start)
	if obs != nil {
		obs.OnSourceAttemptEnd(s.Name, elapsed, err != nil)
	}
	if activityObs != nil {
		activityObs.OnSourceFetchEnd(s.Name)
	}
	if err != nil {
		return fmt.Errorf("produce %s: %w", s.Name, err)
	}
	rpt.Emitted[s.Name] = res.Applied
	if len(res.Failures) > 0 {
		first := res.Failures[0].Err
		rpt.SourceErrors[s.Name] = fmt.Errorf("produce %s: %w", s.Name, first)
		rpt.Failure[s.Name] = FailureInfo{Count: len(res.Failures), NextEligible: now(), Elapsed: elapsed, Argv: query.ArgvOf(s.Query)}
		if activityObs != nil {
			activityObs.OnSourceGaveUp(s.Name)
		}
		if obs != nil {
			obs.OnSourceFailure(s.Name, first)
		}
		return nil
	}
	if activityObs != nil {
		activityObs.OnSourceProduced(s.Name, res.Applied, 0)
	}
	if obs != nil {
		obs.OnSourceSucceeded(s.Name)
	}
	return nil
}

// LevelPullerConfig configures NewLevelPuller. Sources is the whole configured
// query set (the puller picks the level sources out of it); PollInterval is the
// fallback period for a source whose own period is unset.
type LevelPullerConfig struct {
	Env              query.Env
	Sources          query.SourceSet
	Ingest           LevelIngester
	Observer         LevelPullObserver
	FailureObserver  SourceFailureObserver
	ActivityObserver SourceActivityObserver
	PollInterval     time.Duration
	// Now is the clock seam (nil means time.Now).
	Now func() time.Time
}

// LevelPuller is the pass of its own that pulls every level source: each pull
// runs on its own goroutine under its own deadline, a source whose previous
// pull is still running is not started again, and nothing here waits on
// dispatch or on another sensor. Safe for concurrent use.
type LevelPuller struct {
	cfg      LevelPullerConfig
	mu       sync.Mutex
	inflight map[string]bool
	lastTick map[string]time.Time
	failures map[string]map[string]int
	wg       sync.WaitGroup
}

// NewLevelPuller returns a puller over the level sources of cfg.Sources.
func NewLevelPuller(cfg LevelPullerConfig) *LevelPuller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LevelPuller{cfg: cfg, inflight: map[string]bool{}, lastTick: map[string]time.Time{}, failures: map[string]map[string]int{}}
}

// Sources returns the level sources, in configuration order.
func (p *LevelPuller) Sources() query.SourceSet {
	var out query.SourceSet
	for _, s := range p.cfg.Sources {
		if s.LevelSource != "" && s.Query != nil {
			out = append(out, s)
		}
	}
	return out
}

// Tick starts every level source whose period has elapsed and whose previous
// pull is not still running, and returns at once with the names it started.
func (p *LevelPuller) Tick(ctx context.Context) []string {
	return p.start(ctx, true)
}

// PullAll starts every level source not already running, regardless of its
// period (the pull after a wake from sleep), and returns the names started.
func (p *LevelPuller) PullAll(ctx context.Context) []string {
	return p.start(ctx, false)
}

// StartPass is the initial pull pass: it starts every level source at once and
// ends the wait as soon as every one of them has finished, or after wait,
// whichever is first. A pull still running when the wait ends carries on in the
// background under its own deadline; a resource that has not answered reads
// unknown. It reports whether every started pull finished in time.
func (p *LevelPuller) StartPass(ctx context.Context, wait time.Duration) bool {
	p.start(ctx, false)
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Wait blocks until every pull started so far has finished.
func (p *LevelPuller) Wait() { p.wg.Wait() }

// RunOnce runs the named level source's query once, now, synchronously (level
// refresh): extraEnv is added for this run only and bypasses the env_allow list.
func (p *LevelPuller) RunOnce(ctx context.Context, name string, extraEnv []string) (LevelPullResult, error) {
	for _, s := range p.Sources() {
		if s.Name == name {
			return p.pull(ctx, s, extraEnv)
		}
	}
	return LevelPullResult{}, fmt.Errorf("no level source %q", name)
}

// LastTick returns when each level source was last started.
func (p *LevelPuller) LastTick() map[string]time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]time.Time, len(p.lastTick))
	for k, v := range p.lastTick {
		out[k] = v
	}
	return out
}

// Failures returns the pull failure counters: sensor, then reason, then count.
func (p *LevelPuller) Failures() map[string]map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]map[string]int, len(p.failures))
	for sensor, byReason := range p.failures {
		c := make(map[string]int, len(byReason))
		for r, n := range byReason {
			c[r] = n
		}
		out[sensor] = c
	}
	return out
}

func (p *LevelPuller) start(ctx context.Context, onlyDue bool) []string {
	now := p.cfg.Now()
	var started []string
	for _, s := range p.Sources() {
		p.mu.Lock()
		busy := p.inflight[s.Name]
		due := !onlyDue || cadenceDue(now, s.Name, s.Query.Trigger(), Cadence{LastTick: p.lastTick, PollInterval: p.cfg.PollInterval})
		if busy || !due {
			p.mu.Unlock()
			continue
		}
		p.inflight[s.Name] = true
		p.lastTick[s.Name] = now
		p.wg.Add(1)
		p.mu.Unlock()
		started = append(started, s.Name)
		go func(s query.Source) {
			defer p.wg.Done()
			defer func() {
				p.mu.Lock()
				delete(p.inflight, s.Name)
				p.mu.Unlock()
			}()
			_, _ = p.pull(ctx, s, nil)
		}(s)
	}
	return started
}

// pull runs one source through runAndEnqueue's level diversion path.
func (p *LevelPuller) pull(ctx context.Context, s query.Source, extraEnv []string) (LevelPullResult, error) {
	var res LevelPullResult
	lv := &levelPath{ingest: p.cfg.Ingest, extraEnv: extraEnv, result: &res, onFailure: p.countFailure}
	rpt := newProduceReport()
	start := p.cfg.Now()
	err := runAndEnqueue(ctx, p.cfg.Env, s, nil, core.Bindings{}, nil, p.cfg.FailureObserver, p.cfg.ActivityObserver, &rpt, p.cfg.Now, nil, lv)
	if to, ok := p.cfg.Observer.(LevelPullTimingObserver); ok && err == nil {
		to.OnLevelPull(s.LevelSource, p.cfg.Now().Sub(start))
	}
	return res, err
}

func (p *LevelPuller) countFailure(sensor, reason string) {
	p.mu.Lock()
	if p.failures[sensor] == nil {
		p.failures[sensor] = map[string]int{}
	}
	p.failures[sensor][reason]++
	p.mu.Unlock()
	if p.cfg.Observer != nil {
		p.cfg.Observer.OnLevelPullFailure(sensor, reason)
	}
}
