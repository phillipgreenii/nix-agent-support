package main

import (
	"context"
	"sync"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
)

// level refresh: the router side of `pg-router level refresh [resource...]`.
//
// Exported names: none (package main). Names: levelRefreshEnv, levelRefresher.
//
// A refresh runs the sensor's pull query once, now, through the run-query path
// (discover.LevelPuller.RunOnce) with PG_ROUTER_LEVEL_REFRESH=1 in addition to
// the query's allowlisted environment. A sensor that sees the variable skips the
// rise dwell for a credential resource. It is a probe, not an override: nothing
// is pushed as an operator level.

// levelRefreshEnv is the extra environment of a refresh run (it bypasses the
// query's env_allow list).
const levelRefreshEnv = "PG_ROUTER_LEVEL_REFRESH=1"

// levelRefresher implements core.LevelRefresher over the level puller.
type levelRefresher struct {
	resources config.ResourceSet
	puller    func() *discover.LevelPuller
	proj      *core.LevelProjection
}

// RefreshLevels implements core.LevelRefresher. Resources of one sensor share one
// pull; distinct sensors are pulled concurrently. A resource with no sensor, or
// whose sensor has no level-source query, is no_source.
func (r *levelRefresher) RefreshLevels(ctx context.Context, resources []string) []core.LevelRefreshResult {
	puller := r.puller()
	srcBySensor := map[string]string{} // sensor -> query name
	for _, s := range puller.Sources() {
		srcBySensor[s.LevelSource] = s.Name
	}
	names := resources
	if len(names) == 0 {
		for _, res := range r.resources {
			if _, ok := srcBySensor[res.Sensor]; ok {
				names = append(names, res.Name)
			}
		}
	}
	type pull struct {
		res discover.LevelPullResult
		err error
	}
	pulls := map[string]*pull{} // sensor -> outcome
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, n := range names {
		res, ok := r.resources.Find(n)
		if !ok || res.Sensor == "" {
			continue
		}
		q, ok := srcBySensor[res.Sensor]
		if !ok {
			continue
		}
		mu.Lock()
		_, seen := pulls[res.Sensor]
		if !seen {
			pulls[res.Sensor] = &pull{}
		}
		p := pulls[res.Sensor]
		mu.Unlock()
		if seen {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.res, p.err = puller.RunOnce(ctx, q, []string{levelRefreshEnv})
		}()
	}
	wg.Wait()

	current := map[string]string{}
	for _, d := range r.proj.Details() {
		if d.HasLevel {
			current[d.Resource] = d.Level
		}
	}
	out := make([]core.LevelRefreshResult, 0, len(names))
	for _, n := range names {
		row := core.LevelRefreshResult{Resource: n, Outcome: "no_source"}
		res, ok := r.resources.Find(n)
		if ok {
			if p := pulls[res.Sensor]; p != nil {
				switch {
				case p.err != nil:
					row.Outcome, row.Reason = "failed", p.err.Error()
				case len(p.res.Failures) > 0:
					row.Outcome, row.Reason = "failed", p.res.Failures[0].Reason
				case current[n] == "":
					row.Outcome, row.Reason = "failed", "no_answer"
				default:
					row.Outcome, row.Level = "applied", current[n]
				}
			}
		}
		out = append(out, row)
	}
	return out
}
