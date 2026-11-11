package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// Level-source pull loop, start pass and wake detection (the router's side of
// sensors). Level-source pulls are exempt from load backoff, router pause, the
// log soft-halt and log_full: the loop consults none of them.

type loopCmd struct {
	calls atomic.Int64
	hang  bool
}

func (c *loopCmd) Run(ctx context.Context, argv []string) ([]byte, error) {
	c.calls.Add(1)
	if c.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte(`{"schemaVersion":"1","resource":"cpu","level":"OK","observed_at":"2026-11-11T00:00:00Z","valid_for_s":180,"owner":"host"}` + "\n"), nil
}

type countIngest struct {
	mu sync.Mutex
	n  int
}

func (c *countIngest) IngestJSON([]byte) (core.LevelOutcome, string, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return core.LevelAccepted, "", nil
}

func (c *countIngest) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func loopPuller(cmd query.Commander, ing discover.LevelIngester, deadline time.Duration) *discover.LevelPuller {
	src := query.Source{
		Name: "probe", LevelSource: "host", Deadline: deadline,
		Query: query.CommandQuery{Meta: query.Meta{Trig: query.PeriodTrigger{Every: time.Millisecond}}, Argv: []string{"p"}, Format: query.FormatJSONL},
	}
	return discover.NewLevelPuller(discover.LevelPullerConfig{Env: query.Env{Cmd: cmd}, Sources: query.SourceSet{src}, Ingest: ing, PollInterval: time.Millisecond})
}

func waitLevelCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Above the load threshold the producer pass is skipped, yet the level pull
// loop keeps pulling: the sensors are what report that load. (Hand-applied
// negative control: adding the loadBackoff check to the loop makes this fail.)
func TestLevelPullLoop_exemptFromLoadBackoff(t *testing.T) {
	pinLoad1(t, 94, true)
	cfg := config.Config{LoadBackoffThreshold: 50}
	if _, over := loadBackoff(cfg); !over {
		t.Fatal("setup: load backoff is not in force")
	}
	cmd := &loopCmd{}
	ing := &countIngest{}
	p := loopPuller(cmd, ing, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runLevelPullLoop(ctx, cfg, p, 5*time.Millisecond, func(time.Time) bool { return false })
	waitLevelCond(t, "a pull under load backoff", func() bool { return ing.count() >= 2 })
}

// A wake from sleep pulls every level source at once, though none is due.
func TestLevelPullLoop_wakePullsEverySourceAtOnce(t *testing.T) {
	cmd := &loopCmd{}
	ing := &countIngest{}
	src := query.Source{
		Name: "probe", LevelSource: "host", Deadline: time.Second,
		Query: query.CommandQuery{Meta: query.Meta{Trig: query.PeriodTrigger{Every: time.Hour}}, Argv: []string{"p"}, Format: query.FormatJSONL},
	}
	p := discover.NewLevelPuller(discover.LevelPullerConfig{Env: query.Env{Cmd: cmd}, Sources: query.SourceSet{src}, Ingest: ing, PollInterval: time.Hour})
	p.StartPass(context.Background(), time.Second) // one pull; the next is an hour away
	p.Wait()
	if ing.count() != 1 {
		t.Fatalf("start pass ingested %d", ing.count())
	}
	var ticks atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runLevelPullLoop(ctx, config.Config{}, p, 5*time.Millisecond, func(time.Time) bool { return ticks.Add(1) == 3 })
	waitLevelCond(t, "the wake pull", func() bool { return ing.count() == 2 })
	time.Sleep(50 * time.Millisecond)
	if ing.count() != 2 {
		t.Fatalf("ingested %d; only the wake tick may pull a source that is not due", ing.count())
	}
}

func TestWakeBetween(t *testing.T) {
	th := 30 * time.Second
	for name, tc := range map[string]struct {
		wall, mono time.Duration
		want       bool
	}{
		"steady":          {5 * time.Second, 5 * time.Second, false},
		"small jump":      {20 * time.Second, 5 * time.Second, false},
		"exactly at":      {35 * time.Second, 5 * time.Second, false},
		"just over":       {36 * time.Second, 5 * time.Second, true},
		"slept an hour":   {time.Hour, 5 * time.Second, true},
		"wall went back":  {0, 5 * time.Second, false},
		"mono ahead only": {5 * time.Second, time.Hour, false},
	} {
		if got := wakeBetween(tc.wall, tc.mono, th); got != tc.want {
			t.Errorf("%s: wakeBetween(%s, %s) = %v, want %v", name, tc.wall, tc.mono, got, tc.want)
		}
	}
	// A real clock reading does not look like a wake.
	w := wakeDetector{threshold: th}
	w.Observe(time.Now())
	if w.Observe(time.Now().Add(time.Second)) {
		t.Error("an ordinary tick was taken for a wake")
	}
}

// The start pass waits for every source and returns; with a hung source it
// returns at the start wait and the loop still starts.
func TestStartLevelPulls_boundedWait(t *testing.T) {
	cfg := config.Config{Levels: config.LevelsConfig{StartWait: 100 * time.Millisecond, WakeThreshold: time.Minute}}
	prev := levelPullTick
	levelPullTick = time.Hour
	t.Cleanup(func() { levelPullTick = prev })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ing := &countIngest{}
	begin := time.Now()
	startLevelPulls(ctx, cfg, loopPuller(&loopCmd{}, ing, time.Second))
	if ing.count() != 1 || time.Since(begin) > time.Second {
		t.Fatalf("start pass: ingested %d in %s", ing.count(), time.Since(begin))
	}

	begin = time.Now()
	startLevelPulls(ctx, cfg, loopPuller(&loopCmd{hang: true}, &countIngest{}, 3*time.Second))
	if el := time.Since(begin); el < 90*time.Millisecond || el > time.Second {
		t.Fatalf("start pass with a hung source took %s, want about the 100ms wait", el)
	}
}

func TestStartLevelPulls_noLevelSourcesIsNoop(t *testing.T) {
	p := discover.NewLevelPuller(discover.LevelPullerConfig{Env: query.Env{Cmd: &loopCmd{}}})
	startLevelPulls(context.Background(), config.Config{}, p) // must neither block nor panic
	if got := strings.Join(p.Tick(context.Background()), ","); got != "" {
		t.Fatalf("started %q", got)
	}
}

type outCmd struct{ out string }

func (c outCmd) Run(context.Context, []string) ([]byte, error) { return []byte(c.out), nil }

// run-query verifies a level-source query's output against the shared answer
// schema, without ingesting or enqueuing anything.
func TestRunQuery_levelSourceVerifiesAnswers(t *testing.T) {
	src := query.Source{
		Name: "probe", LevelSource: "host", Deadline: time.Second,
		Query: query.CommandQuery{Argv: []string{"p"}, Format: query.FormatJSONL},
	}
	valid := `{"schemaVersion":"1","resource":"cpu","level":"OK","observed_at":"2026-11-11T00:00:00Z","valid_for_s":180,"owner":"host"}`
	for name, tc := range map[string]struct {
		out      string
		wantCode int
		wantOut  string
	}{
		"valid":   {valid + "\n", exitOK, "# 1 valid, 0 invalid"},
		"invalid": {valid + "\n" + `{"resource":"cpu"}` + "\n", exitGeneric, "# 1 valid, 1 invalid"},
		"empty":   {"", exitGeneric, "# 0 valid, 0 invalid"},
	} {
		var stdout, stderr strings.Builder
		got := runRunQueryLevelSource(context.Background(), query.Env{Cmd: outCmd{tc.out}}, src, &stdout, &stderr)
		if got != tc.wantCode || !strings.Contains(stdout.String(), tc.wantOut) {
			t.Errorf("%s: code=%d stdout=%q, want code %d containing %q", name, got, stdout.String(), tc.wantCode, tc.wantOut)
		}
	}
}
