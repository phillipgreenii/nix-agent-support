// status.go: reads the backlog scalar straight from `pg-router status
// --json` for --backlog-from-status (pg2-5g9e0).
package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// parseBacklog sums queues[].depth from a cli.status-reply body -- the same
// definition as pg-router's MetricBacklog (sum of DepthByType()).
func parseBacklog(raw []byte) (int, error) {
	var reply struct {
		Queues *[]struct {
			Depth int `json:"depth"`
		} `json:"queues"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return 0, fmt.Errorf("parse status json: %w", err)
	}
	if reply.Queues == nil {
		return 0, fmt.Errorf("status json has no queues array")
	}
	total := 0
	for _, q := range *reply.Queues {
		total += q.Depth
	}
	return total, nil
}

// fetchBacklogFromStatus runs `<pgRouterPath> status --json` under its own
// explicit timeout and returns the summed queue depth.
func fetchBacklogFromStatus(ctx context.Context, opts runOptions) (int, error) {
	sctx, cancel := context.WithTimeout(ctx, opts.statusTimeout)
	defer cancel()
	out, err := execCmdFactory(sctx, opts.pgRouterPath, "status", "--json").Output()
	if err != nil {
		// A child killed by our own deadline surfaces as a bare "signal:
		// killed" ExitError; attach the context error so the caller can tell
		// a timeout (retried once, pg2-zzf54) from a real failure.
		if cerr := sctx.Err(); cerr != nil {
			err = fmt.Errorf("%w: %w", err, cerr)
		}
		return 0, fmt.Errorf("pg-router status --json: %w", err)
	}
	return parseBacklog(out)
}
