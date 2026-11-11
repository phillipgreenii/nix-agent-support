package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// envCmd answers a sensor query and records the environment of each run.
type envCmd struct {
	mu   sync.Mutex
	envs [][]string
	out  string
	err  error
}

func (c *envCmd) Run(ctx context.Context, argv []string) ([]byte, error) {
	return c.RunEnv(ctx, argv, nil)
}

func (c *envCmd) RunEnv(_ context.Context, _ []string, env []string) ([]byte, error) {
	c.mu.Lock()
	c.envs = append(c.envs, env)
	c.mu.Unlock()
	return []byte(c.out), c.err
}

func refresherFixture(cmd *envCmd) (*levelRefresher, *core.LevelProjection) {
	proj := core.NewLevelProjection(core.LevelProjectionConfig{
		Registry:  levels.Registry{"cpu": {Levels: []string{"LOW", "HIGH"}, Unknown: "LOW"}, "disk": {Levels: []string{"OK", "LOW"}, Unknown: "OK"}},
		Resources: map[string]core.LevelResource{"cpu": {Sensor: "host-sensor", GraceFactor: 2}, "disk": {Sensor: "host-sensor", GraceFactor: 2}},
		Skew:      time.Minute,
		Now:       time.Now,
	})
	src := query.Source{
		Name: "host-probe", LevelSource: "host-sensor", Deadline: time.Second,
		Query: query.CommandQuery{Meta: query.Meta{Trig: query.PeriodTrigger{Every: time.Hour}}, Argv: []string{"p"}, Format: query.FormatJSONL},
	}
	puller := discover.NewLevelPuller(discover.LevelPullerConfig{Env: query.Env{Cmd: cmd}, Sources: query.SourceSet{src}, Ingest: proj, PollInterval: time.Hour})
	rs := config.ResourceSet{
		{Name: "cpu", Sensor: "host-sensor"}, {Name: "disk", Sensor: "host-sensor"}, {Name: "orphan", Sensor: "ghost-sensor"}, {Name: "bare"},
	}
	return &levelRefresher{resources: rs, puller: func() *discover.LevelPuller { return puller }, proj: proj}, proj
}

func answerLine(resource, level string) string {
	return `{"schemaVersion":"1","resource":"` + resource + `","level":"` + level + `","observed_at":"` +
		time.Now().UTC().Format(time.RFC3339) + `","valid_for_s":180,"owner":"host-sensor"}` + "\n"
}

func TestLevelRefresher_appliesAnswerWithRefreshEnvAndOnePullPerSensor(t *testing.T) {
	cmd := &envCmd{out: answerLine("cpu", "HIGH") + answerLine("disk", "OK")}
	r, _ := refresherFixture(cmd)
	res := r.RefreshLevels(context.Background(), []string{"cpu", "disk", "orphan", "bare", "unlisted"})
	got := map[string]core.LevelRefreshResult{}
	for _, x := range res {
		got[x.Resource] = x
	}
	if got["cpu"].Outcome != "applied" || got["cpu"].Level != "HIGH" || got["disk"].Outcome != "applied" || got["disk"].Level != "OK" {
		t.Fatalf("results: %+v", res)
	}
	for _, n := range []string{"orphan", "bare", "unlisted"} {
		if got[n].Outcome != "no_source" {
			t.Errorf("%s: %+v, want no_source", n, got[n])
		}
	}
	if len(cmd.envs) != 1 {
		t.Fatalf("pulled %d times, want one pull for the shared sensor", len(cmd.envs))
	}
	if !slices.Contains(cmd.envs[0], "PG_ROUTER_LEVEL_REFRESH=1") {
		t.Fatalf("refresh run env lacks PG_ROUTER_LEVEL_REFRESH=1: %v", cmd.envs[0])
	}
}

func TestLevelRefresher_failuresAndEmptyMeansEverySource(t *testing.T) {
	cmd := &envCmd{err: errors.New("boom")}
	r, _ := refresherFixture(cmd)
	res := r.RefreshLevels(context.Background(), nil)
	if len(res) != 2 {
		t.Fatalf("empty request should cover every resource with a source: %+v", res)
	}
	for _, x := range res {
		if x.Outcome != "failed" || x.Reason != discover.PullFailExit {
			t.Errorf("%+v, want failed/exit", x)
		}
	}
	cmd2 := &envCmd{out: answerLine("cpu", "HIGH")}
	r2, _ := refresherFixture(cmd2)
	res = r2.RefreshLevels(context.Background(), []string{"disk"})
	if res[0].Outcome != "failed" || !strings.Contains(res[0].Reason, "no_answer") {
		t.Errorf("a resource the sensor did not answer: %+v", res)
	}
}
