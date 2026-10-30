package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

// The selection announcement (bead pg2-e5g9gx). A PR the plan newly selects
// reaches the decider only through a `pr.changed` event, and the PR change
// feed belongs to the connector daemon: selecting writes no event there, so a
// PR that never changes again stayed unminted. After a lock commits, select
// (and pull) therefore inject one `pr.changed` event per newly inserted PR row
// into the live router with its own operator command (`pg-router push-inject`),
// shaped like the item the PR source emits, so the `pr-change` role runs for
// the PR at selection time. The run is idempotent for a minted PR (the decider
// finds the bead), and a re-select inserts nothing new so it emits nothing.
//
// Issue rows need no event here: the focus_selected annotation appends an
// annotation_changed change_log row that the issue feed delivers.
//
// A failed injection (no router on PATH, no running core) is a one-line stderr
// warning; the lock's outcome and exit code are never affected.

// envPGRouterBin names the router binary the announcement execs; unset means
// `pg-router` from PATH.
const envPGRouterBin = "PG_DESK_PG_ROUTER_BIN"

// focusAnnounceTTL is how long an injected event stays retryable.
const focusAnnounceTTL = time.Hour

// focusInject hands one event JSON to the router. Tests replace it.
var focusInject = func(ctx context.Context, eventJSON string) error {
	bin := os.Getenv(envPGRouterBin)
	if bin == "" {
		bin = "pg-router"
	}
	out, err := exec.CommandContext(ctx, bin, "push-inject", eventJSON).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s push-inject: %w: %s", bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// focusAnnouncer returns the LockDeps.Emit of a run.
func focusAnnouncer(period string, now func() time.Time) func(context.Context, []focus.Key) error {
	return func(ctx context.Context, keys []focus.Key) error {
		var failed []string
		for _, k := range keys {
			if k.Type != "pr" {
				continue
			}
			ev, err := selectionEvent(k, period, now().UTC())
			if err == nil {
				err = focusInject(ctx, ev)
			}
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v); run pg-pr-change apply %s", k.ID, err, k.ID))
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("%s", strings.Join(failed, "; "))
		}
		return nil
	}
}

// selectionEvent builds the pr.changed event of one newly selected PR. The
// payload mirrors the item pg-router-source-pg-connector emits: id
// "<entity_id>@<12 hex>" (the suffix is the delivery key of the composed
// command), type pr and metadata.entity_id. The event id carries the period,
// so the router's queue folds a repeated announcement of one selection.
func selectionEvent(k focus.Key, period string, now time.Time) (string, error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(k.ID + "|" + period))
	suffix := fmt.Sprintf("%012x", h.Sum64()&0xffffffffffff)
	if suffix == "000000000000" {
		suffix = "000000000001"
	}
	ev := map[string]any{
		"id":        "focus-select:" + k.ID + "@" + period,
		"type":      "pr.changed",
		"at":        now.Format(time.RFC3339),
		"expiresAt": now.Add(focusAnnounceTTL).Format(time.RFC3339),
		"payload": map[string]any{
			"id":    k.ID + "@" + suffix,
			"type":  "pr",
			"title": k.ID,
			"metadata": map[string]any{
				"entity_type": "pr",
				"entity_id":   k.ID,
				"kinds":       []string{"focus_selected"},
				"origin":      "pg-desk",
			},
		},
	}
	b, err := json.Marshal(ev)
	return string(b), err
}
