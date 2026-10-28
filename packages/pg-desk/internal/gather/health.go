package gather

import (
	"context"
	"encoding/json"
	"fmt"
)

// BaselineOpenAges runs `pg-connector <type> health --backend <backend>
// --output json` and returns, keyed by query name, the age in seconds of every
// query of the backend whose baseline is NOT stored yet
// (queries[].baseline_open_age_seconds present and baseline_at absent). A
// query whose baseline is stored has no entry. The backend's own status
// document is the only source: the feed wire does not carry the baseline time.
//
// Exit 0 and 2 are answers; exit 3 and any other exit are errors, and so is an
// answer whose backends[] holds no status document for the backend (the
// backend's source row is degraded or disabled), because "no entry" would
// then read as "baseline stored". The wire error unavailable wraps
// ErrUnavailable.
func (g *Gatherer) BaselineOpenAges(ctx context.Context, entityType, backend string) (map[string]int64, error) {
	if entityType == "" || backend == "" {
		return nil, fmt.Errorf("gather: baseline open ages: type and backend are required")
	}
	args := []string{entityType, "health", "--backend", backend, "--output", "json"}
	res, err := g.run(ctx, args, g.feedEnv(entityType))
	if err != nil {
		return nil, err
	}
	switch res.exitCode {
	case 0, 2:
	default:
		return nil, exitError(args, res.exitCode, res.stdout)
	}
	if wireErrorCode(res.stdout) == "unavailable" {
		return nil, exitError(args, res.exitCode, res.stdout)
	}
	var wire struct {
		Sources []struct {
			Source string `json:"source"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"sources"`
		Backends []struct {
			Backend string `json:"backend"`
			Status  struct {
				Queries []struct {
					Name                   string `json:"name"`
					BaselineAt             string `json:"baseline_at"`
					BaselineOpenAgeSeconds *int64 `json:"baseline_open_age_seconds"`
				} `json:"queries"`
			} `json:"status"`
		} `json:"backends"`
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal(res.stdout, &wire); err != nil {
		return nil, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	if wire.Error != nil {
		return nil, exitError(args, res.exitCode, res.stdout)
	}
	for _, b := range wire.Backends {
		if b.Backend != backend {
			continue
		}
		out := map[string]int64{}
		for _, q := range b.Status.Queries {
			if q.BaselineAt == "" && q.BaselineOpenAgeSeconds != nil {
				out[q.Name] = *q.BaselineOpenAgeSeconds
			}
		}
		return out, nil
	}
	reason := "no status document"
	for _, s := range wire.Sources {
		if s.Source == backend && s.Reason != "" {
			reason = s.Reason
		}
	}
	return nil, fmt.Errorf("pg-connector %v: backend %s gave no status document: %s", args, backend, reason)
}
