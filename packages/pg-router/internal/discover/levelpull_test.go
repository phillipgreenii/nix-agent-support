package discover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// Tests for the level-source pull path: diversion before Enqueue, the exemption
// from every gate, concurrency, failure classification, the start pass and the
// run-once function. The projection is replaced by a fake ingester; the real
// projection's rules are tested in internal/core.

// levelCmd is a commander whose per-argv[0] behaviour the test scripts. It
// records the environment of every RunEnv call.
type levelCmd struct {
	mu   sync.Mutex
	fn   map[string]func(ctx context.Context, env []string) ([]byte, error)
	envs map[string][][]string
}

func (c *levelCmd) Run(ctx context.Context, argv []string) ([]byte, error) {
	return c.RunEnv(ctx, argv, nil)
}

func (c *levelCmd) RunEnv(ctx context.Context, argv []string, env []string) ([]byte, error) {
	c.mu.Lock()
	if c.envs == nil {
		c.envs = map[string][][]string{}
	}
	c.envs[argv[0]] = append(c.envs[argv[0]], env)
	fn := c.fn[argv[0]]
	c.mu.Unlock()
	return fn(ctx, env)
}

func answer(resource string) []byte {
	return []byte(`{"schemaVersion":"1","resource":"` + resource + `","level":"OK","observed_at":"2026-11-11T00:00:00Z","valid_for_s":180,"owner":"host"}` + "\n")
}

// fakeIngest accepts every line except one containing "bad" (malformed) or
// "nope" (rejected for another reason).
type fakeIngest struct {
	mu    sync.Mutex
	lines []string
}

func (f *fakeIngest) IngestJSON(raw []byte) (core.LevelOutcome, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, string(raw))
	switch {
	case strings.Contains(string(raw), "bad"):
		return core.LevelRejected, core.ReasonMalformed, nil
	case strings.Contains(string(raw), "nope"):
		return core.LevelRejected, core.ReasonNotRegisteredSensor, nil
	}
	return core.LevelAccepted, "", nil
}

func (f *fakeIngest) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

type failObs struct {
	mu  sync.Mutex
	got []string
}

func (o *failObs) OnLevelPullFailure(sensor, reason string) {
	o.mu.Lock()
	o.got = append(o.got, sensor+"/"+reason)
	o.mu.Unlock()
}

func levelSrc(name, sensor, argv0 string, deadline time.Duration) query.Source {
	return query.Source{
		Name: name, LevelSource: sensor, Deadline: deadline,
		Query: query.CommandQuery{Meta: query.Meta{Trig: query.PeriodTrigger{Every: time.Hour}}, Argv: []string{argv0}, Format: query.FormatJSONL},
	}
}

func newPuller(cmd *levelCmd, ing LevelIngester, obs LevelPullObserver, srcs ...query.Source) *LevelPuller {
	return NewLevelPuller(LevelPullerConfig{Env: query.Env{Cmd: cmd}, Sources: srcs, Ingest: ing, Observer: obs, PollInterval: time.Hour})
}

// A level answer is diverted to ingest in runAndEnqueue and never reaches the
// queue, even when its type would be declared.
func TestRunAndEnqueue_levelAnswerNeverReachesEnqueue(t *testing.T) {
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"p": func(context.Context, []string) ([]byte, error) { return answer("cpu"), nil },
	}}
	ing := &fakeIngest{}
	s := levelSrc("probe", "host", "p", time.Second)
	rpt := newProduceReport()
	lv := &levelPath{ingest: ing, result: &LevelPullResult{}}
	if err := runAndEnqueue(context.Background(), query.Env{Cmd: cmd}, s, q, core.NewBindings("e"), realSleep, nil, nil, &rpt, time.Now, nil, lv); err != nil {
		t.Fatal(err)
	}
	if len(ing.got()) != 1 {
		t.Fatalf("ingested %v, want the one answer", ing.got())
	}
	if d := q.DepthByType(); len(d) != 0 {
		t.Fatalf("queue depth = %v; a level answer must never reach Enqueue", d)
	}
	if rpt.Emitted["probe"] != 1 || len(rpt.Rejected) != 0 {
		t.Fatalf("report = %+v", rpt)
	}
}

// Level sources are not run by the gated producer pass at all: under a soft
// halt and an EXHAUSTED requirement block the pass neither polls them nor names
// them as halted or blocked. (Hand-applied negative control: removing the
// level-source skip in produce makes this fail.)
func TestProduce_levelSourceExemptFromGatesAndHalt(t *testing.T) {
	mem := eventqueue.NewMemStore()
	q, err := eventqueue.New(mem, eventqueue.WithLogLimits(900, 1000), eventqueue.WithRegistry(diskRegistry()),
		eventqueue.WithLevelSource(func() levels.Snapshot {
			return levels.Snapshot{Now: time.Now(), Levels: map[string]levels.Level{"disk": {Value: "LOW", State: levels.StateHeld, Source: "sensor", Owner: "s"}}}
		}))
	if err != nil {
		t.Fatal(err)
	}
	q.RegisterEmitter("probe", []levels.Consumer{{Req: diskOKReq(), Enabled: true}})
	mem.SetLogSize(950)
	q.EnforceLogLimits()
	if !q.EmittersHalted() {
		t.Fatal("setup: not halted")
	}
	var calls atomic.Int64
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"p": func(context.Context, []string) ([]byte, error) { calls.Add(1); return answer("cpu"), nil },
	}}
	rpt, err := Produce(context.Background(), query.Env{Cmd: cmd}, query.SourceSet{levelSrc("probe", "host", "p", time.Second)}, q, core.NewBindings())
	if err != nil {
		t.Fatal(err)
	}
	if len(rpt.Halted) != 0 || len(rpt.Blocked) != 0 || len(rpt.SourceErrors) != 0 {
		t.Fatalf("the produce pass touched a level source: halted=%v blocked=%v errors=%v", rpt.Halted, rpt.Blocked, rpt.SourceErrors)
	}
	if _, ok := rpt.LastTick["probe"]; ok || calls.Load() != 0 {
		t.Fatalf("the produce pass polled a level source (calls=%d)", calls.Load())
	}

	// The level pass runs it under the very same halt and block.
	ing := &fakeIngest{}
	p := newPuller(cmd, ing, nil, levelSrc("probe", "host", "p", time.Second))
	p.PullAll(context.Background())
	p.Wait()
	if calls.Load() != 1 || len(ing.got()) != 1 {
		t.Fatalf("the level pass did not pull under halt and block: calls=%d ingested=%v", calls.Load(), ing.got())
	}
	if d := q.DepthByType(); len(d) != 0 {
		t.Fatalf("queue depth = %v", d)
	}
}

// One hung probe delays neither another sensor nor a later pass; it is not
// started again while running, ends at its own deadline, and is counted.
func TestLevelPuller_hungProbeDelaysNoOtherSensor(t *testing.T) {
	release := make(chan struct{})
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"hung": func(ctx context.Context, _ []string) ([]byte, error) {
			select {
			case <-ctx.Done():
				return []byte("partial output that must be discarded\n"), ctx.Err()
			case <-release:
				return nil, nil
			}
		},
		"fast": func(context.Context, []string) ([]byte, error) { return answer("ram"), nil },
	}}
	ing := &fakeIngest{}
	obs := &failObs{}
	p := newPuller(cmd, ing, obs, levelSrc("a", "slow", "hung", 300*time.Millisecond), levelSrc("b", "quick", "fast", time.Second))
	start := time.Now()
	started := p.PullAll(context.Background())
	if len(started) != 2 {
		t.Fatalf("started %v, want both", started)
	}
	deadline := time.After(2 * time.Second)
	for len(ing.got()) == 0 {
		select {
		case <-deadline:
			t.Fatal("the quick sensor was never ingested")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if time.Since(start) >= 250*time.Millisecond {
		t.Fatalf("the quick sensor took %s: it waited on the hung one", time.Since(start))
	}
	if again := p.PullAll(context.Background()); len(again) != 1 || again[0] != "b" {
		t.Fatalf("second pull started %v; the still-running probe must not be started twice", again)
	}
	p.Wait()
	if f := p.Failures(); f["slow"][PullFailDeadline] != 1 || len(f["quick"]) != 0 {
		t.Fatalf("failures = %v, want one deadline for slow", f)
	}
	for _, l := range ing.got() {
		if strings.Contains(l, "partial") {
			t.Fatal("output read before the deadline must be discarded")
		}
	}
	if len(obs.got) != 1 || obs.got[0] != "slow/deadline" {
		t.Fatalf("observer = %v", obs.got)
	}
}

// A non-zero exit, a malformed answer and a rejected one each count under their
// own reason and do not stop another sensor.
func TestLevelPuller_failureReasons(t *testing.T) {
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"exit":      func(context.Context, []string) ([]byte, error) { return nil, errors.New("exit status 1") },
		"malformed": func(context.Context, []string) ([]byte, error) { return []byte("bad line\n"), nil },
		"empty":     func(context.Context, []string) ([]byte, error) { return nil, nil },
		"rejected":  func(context.Context, []string) ([]byte, error) { return []byte("nope\n"), nil },
		"good":      func(context.Context, []string) ([]byte, error) { return answer("cpu"), nil },
	}}
	ing := &fakeIngest{}
	p := newPuller(cmd, ing, nil,
		levelSrc("e", "s-exit", "exit", time.Second), levelSrc("m", "s-mal", "malformed", time.Second),
		levelSrc("n", "s-empty", "empty", time.Second), levelSrc("r", "s-rej", "rejected", time.Second),
		levelSrc("g", "s-good", "good", time.Second))
	p.PullAll(context.Background())
	p.Wait()
	f := p.Failures()
	want := map[string]string{"s-exit": PullFailExit, "s-mal": PullFailMalformed, "s-empty": PullFailMalformed, "s-rej": PullFailRejected}
	for sensor, reason := range want {
		if f[sensor][reason] != 1 || len(f[sensor]) != 1 {
			t.Errorf("failures[%s] = %v, want one %s", sensor, f[sensor], reason)
		}
	}
	if len(f["s-good"]) != 0 {
		t.Errorf("the good sensor failed: %v", f["s-good"])
	}
	if n := len(ing.got()); n != 3 { // good + malformed line + rejected line; exit and empty never reach ingest
		t.Errorf("ingested %d lines: %v", n, ing.got())
	}
}

// The start pass ends as soon as every source finished, or at the wait.
func TestLevelPuller_startPassBoundedWait(t *testing.T) {
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"fast": func(context.Context, []string) ([]byte, error) { return answer("cpu"), nil },
		"hung": func(ctx context.Context, _ []string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() },
	}}
	ing := &fakeIngest{}
	p := newPuller(cmd, ing, nil, levelSrc("a", "x", "fast", time.Second))
	begin := time.Now()
	if !p.StartPass(context.Background(), 5*time.Second) {
		t.Fatal("StartPass reported a timeout although the only source answered")
	}
	if time.Since(begin) > time.Second {
		t.Fatalf("StartPass waited %s for a source that had answered", time.Since(begin))
	}

	p2 := newPuller(cmd, ing, nil, levelSrc("a", "x", "fast", time.Second), levelSrc("h", "y", "hung", 2*time.Second))
	begin = time.Now()
	if p2.StartPass(context.Background(), 100*time.Millisecond) {
		t.Fatal("StartPass reported every source answered with one hung")
	}
	if el := time.Since(begin); el < 90*time.Millisecond || el > time.Second {
		t.Fatalf("StartPass returned after %s, want about the 100ms wait", el)
	}
	p2.Wait()
}

// Tick starts only sources whose period elapsed.
func TestLevelPuller_tickHonorsPeriod(t *testing.T) {
	var calls atomic.Int64
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"p": func(context.Context, []string) ([]byte, error) { calls.Add(1); return answer("cpu"), nil },
	}}
	var now atomic.Pointer[time.Time]
	set := func(t time.Time) { now.Store(&t) }
	t0 := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	set(t0)
	s := levelSrc("p", "host", "p", time.Second)
	s.Query = query.CommandQuery{Meta: query.Meta{Trig: query.PeriodTrigger{Every: time.Minute}}, Argv: []string{"p"}, Format: query.FormatJSONL}
	p := NewLevelPuller(LevelPullerConfig{Env: query.Env{Cmd: cmd}, Sources: query.SourceSet{s}, Ingest: &fakeIngest{}, Now: func() time.Time { return *now.Load() }})
	if got := p.Tick(context.Background()); len(got) != 1 {
		t.Fatalf("first tick started %v", got)
	}
	p.Wait()
	set(t0.Add(30 * time.Second))
	if got := p.Tick(context.Background()); len(got) != 0 {
		t.Fatalf("tick before the period started %v", got)
	}
	set(t0.Add(61 * time.Second))
	if got := p.Tick(context.Background()); len(got) != 1 {
		t.Fatalf("tick after the period started %v", got)
	}
	p.Wait()
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if lt := p.LastTick()["p"]; !lt.Equal(t0.Add(61 * time.Second)) {
		t.Fatalf("lastTick = %v", lt)
	}
}

// The run-once function adds its extra variables for that run only; and they
// bypass the env_allow list while a variable that is not listed stays out.
func TestRunLevelQueryOnce_extraEnvBypassesAllowlistForExtraOnly(t *testing.T) {
	t.Setenv("PGR_LISTED", "yes")
	t.Setenv("PGR_UNLISTED", "no")
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"p": func(context.Context, []string) ([]byte, error) { return answer("cpu"), nil },
	}}
	s := levelSrc("probe", "host", "p", time.Second)
	cq := s.Query.(query.CommandQuery)
	cq.EnvAllow = []string{"PGR_LISTED"}
	s.Query = cq
	ing := &fakeIngest{}
	res, err := RunLevelQueryOnce(context.Background(), query.Env{Cmd: cmd}, s, ing, []string{"PG_ROUTER_LEVEL_REFRESH=1"})
	if err != nil || res.Applied != 1 || len(res.Failures) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	env := strings.Join(cmd.envs["p"][0], "\n")
	for _, want := range []string{"PGR_LISTED=yes", "PG_ROUTER_LEVEL_REFRESH=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}
	if strings.Contains(env, "PGR_UNLISTED") {
		t.Errorf("a variable not in env_allow was passed:\n%s", env)
	}

	// The next run has no extra variable.
	if _, err := RunLevelQueryOnce(context.Background(), query.Env{Cmd: cmd}, s, ing, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.envs["p"][1], "\n"), "PG_ROUTER_LEVEL_REFRESH") {
		t.Error("the refresh variable leaked into a later run")
	}
}

// The deadline kills the query's whole process group and discards what it
// printed; the failure is a deadline.
func TestRunLevelQueryOnce_deadlineKillsProcessGroupAndDiscardsOutput(t *testing.T) {
	s := levelSrc("probe", "host", "sh", 200*time.Millisecond)
	s.Query = query.CommandQuery{
		Meta: query.Meta{Trig: query.PeriodTrigger{Every: time.Hour}}, Format: query.FormatJSONL,
		Argv: []string{"sh", "-c", `printf '%s\n' '{"schemaVersion":"1"}'; sleep 60`},
	}
	ing := &fakeIngest{}
	begin := time.Now()
	res, err := RunLevelQueryOnce(context.Background(), query.Env{}, s, ing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(begin); el > 10*time.Second {
		t.Fatalf("took %s: the process group was not killed at the deadline", el)
	}
	if len(res.Failures) != 1 || res.Failures[0].Reason != PullFailDeadline {
		t.Fatalf("failures = %+v, want one deadline", res.Failures)
	}
	if len(ing.got()) != 0 {
		t.Fatalf("output read before the deadline was ingested: %v", ing.got())
	}
}

// A cancelled parent context is a shutdown, not a pull failure.
func TestRunLevelQueryOnce_shutdownIsNotAFailure(t *testing.T) {
	cmd := &levelCmd{fn: map[string]func(context.Context, []string) ([]byte, error){
		"p": func(ctx context.Context, _ []string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	res, err := RunLevelQueryOnce(ctx, query.Env{Cmd: cmd}, levelSrc("probe", "host", "p", 5*time.Second), &fakeIngest{}, nil)
	if err == nil || len(res.Failures) != 0 {
		t.Fatalf("res=%+v err=%v; shutdown must propagate and not count", res, err)
	}
}
