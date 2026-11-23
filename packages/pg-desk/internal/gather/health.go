package gather

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
)

// BaselineOpenAges runs `pg-connector <type> health --backend <backend>
// --output json` and returns, keyed by query name, the age in seconds of every
// query of the backend whose baseline is NOT stored yet
// (queries[].baseline_open_age_seconds present and baseline_at absent). A
// query whose baseline is stored has no entry. The backend's own status
// document is the only source: the feed wire does not carry the baseline time.
//
// Exit 0 and 5 (degraded) are answers; exit 3 and any other exit are errors, and so is an
// answer whose backends[] holds no status document for the backend (the
// backend's source row is degraded or disabled), because "no entry" would
// then read as "baseline stored". The wire error unavailable wraps
// ErrUnavailable.
func (g *Gatherer) BaselineOpenAges(ctx context.Context, entityType, backend string) (map[string]int64, error) {
	if entityType == "" || backend == "" {
		return nil, fmt.Errorf("gather: baseline open ages: type and backend are required")
	}
	args := []string{entityType, "health", "--backend", backend, "--output", "json"}
	stdout, err := g.fanOutCall(ctx, args, g.feedEnv(entityType))
	if err != nil {
		return nil, err
	}
	if wireErrorCode(stdout) == "unavailable" {
		return nil, exitError(args, client.ExitOK, stdout)
	}
	var wire struct {
		client.FanOutOutcome
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
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(stdout, &wire); err != nil {
		return nil, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	if hasWireError(wire.Error) {
		return nil, exitError(args, client.ExitOK, stdout)
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
