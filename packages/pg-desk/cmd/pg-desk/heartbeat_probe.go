package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

// heartbeatProbeConsumer is the consumer name the freshness probe polls under.
// The probe is read-only: --cached never runs a query, --no-ack never moves a
// cursor, so the name only keys the ledger's last_seen stamp.
const heartbeatProbeConsumer = "pg-desk-freshness"

// heartbeatProbeTimeout bounds the whole probe (capabilities plus one cached
// poll per query), so a wedged or absent daemon (whose client retries for
// about 30 seconds per call) can never hold the heartbeat open for long.
const heartbeatProbeTimeout = 45 * time.Second

// heartbeatRunConnector execs `pg-connector <args>` and returns its stdout:
// the same D10-permitted literal as heartbeatRunLedgerShow. A package-level var
// so tests can stub it.
var heartbeatRunConnector = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "pg-connector", args...).Output()
}

// prCapabilities is the part of `pg-connector pr capabilities` the probe
// reads: each backend, whether it owns its changes, and its configured queries.
type prCapabilities struct {
	Backends []struct {
		Backend    string `json:"backend"`
		Vocabulary struct {
			OwnsChanges bool `json:"owns_changes"`
		} `json:"vocabulary"`
		Queries map[string]json.RawMessage `json:"queries"`
	} `json:"backends"`
}

// probeOwningSources makes the connector stamp its ledger for every query of
// every pr backend that owns its changes (the daemon-backed GitHub backend), so
// the `ledger show` read that follows carries the daemon's own last whole-query
// origin answer (INV-FRESH-7).
//
// Why a probe: the connector stamps a (type, backend, query) ledger key only
// as a side effect of a forwarded `changes` call that names a query. With the
// daemon as the PR change feed nothing polls per query any more (the umbrella
// PR refresher is gone), so no pr key existed and the pr-github source never
// refreshed. `pr changes --cached --no-ack --query Q` is local (the daemon
// answers from its store, no origin call), moves no cursor, and returns the
// backend's own last_success_at verbatim, so a healthy daemon reads fresh and a
// down daemon or failing origin stops advancing it (INV-FRESH-2, INV-FRESH-3).
//
// Every failure is reported on stderr and never fails the heartbeat: the
// previous source times simply age.
func probeOwningSources(cmd *cobra.Command) {
	ctx, cancel := context.WithTimeout(cmd.Context(), heartbeatProbeTimeout)
	defer cancel()
	errOut := cmd.ErrOrStderr()

	out, err := heartbeatRunConnector(ctx, "pr", "capabilities", "--output", "json")
	if err != nil {
		fmt.Fprintf(errOut, "heartbeat: pr sources not probed: pg-connector pr capabilities: %v\n", err)
		return
	}
	var caps prCapabilities
	if err := json.Unmarshal(out, &caps); err != nil {
		fmt.Fprintf(errOut, "heartbeat: pr sources not probed: decode pr capabilities: %v\n", err)
		return
	}
	for _, b := range caps.Backends {
		if !b.Vocabulary.OwnsChanges || b.Backend == "" {
			continue
		}
		queries := make([]string, 0, len(b.Queries))
		for q := range b.Queries {
			queries = append(queries, q)
		}
		sort.Strings(queries)
		for _, q := range queries {
			if _, err := heartbeatRunConnector(ctx, "pr", "changes",
				"--consumer", heartbeatProbeConsumer, "--cached", "--no-ack",
				"--backend", b.Backend, "--query", q, "--output", "json"); err != nil {
				// An unreachable daemon fails every query alike; skip the rest
				// of this backend rather than wait out each retry.
				fmt.Fprintf(errOut, "heartbeat: %s query %q not probed: %v\n", b.Backend, q, err)
				break
			}
		}
	}
}
