// Package dashboard is the read client for the pg-pr sync daemon's
// /api/v1/dashboard endpoint.
//
// That endpoint is the ONLY cheap source of the review-set snapshot. The
// snapshot is never persisted: internal/sync builds it from PRInputs carrying
// live provider enrichment (Engine.buildPRInput) and hands the result to an
// in-memory snapshot.Store that internal/httpapi serves. A one-shot CLI
// therefore cannot re-derive the payload from the local store without repeating
// the daemon's per-PR provider round-trips, so it reads it back over localhost
// HTTP instead — the same seam the external Grafana panel consumes.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// Path is the daemon route that serves the snapshot.
const Path = "/api/v1/dashboard"

// DefaultTimeout bounds the localhost round-trip. The daemon serves an
// already-built in-memory snapshot, so a slow response means it is wedged
// rather than working — failing fast beats blocking an interactive command.
const DefaultTimeout = 5 * time.Second

// ErrNoSnapshot reports the daemon's 503: it is up, but has not completed a
// first sync tick, so there is nothing to show yet. It is distinguished from a
// transport failure because the remedy differs — wait for the next tick, versus
// start the daemon.
var ErrNoSnapshot = errors.New("the pg-pr sync daemon has not built its first snapshot yet")

// Fetch reads the current snapshot from the daemon listening at addr, given as
// host:port exactly as `pg-pr sync --metrics-addr` accepts it.
//
// The payload decodes into snapshot.Snapshot — the same type
// httpapi.DashboardHandler serializes — so a field rename in the producer is a
// compile error here rather than a silently empty result.
func Fetch(ctx context.Context, addr string) (*snapshot.Snapshot, error) {
	url := "http://" + addr + Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build dashboard request: %w", err)
	}

	client := &http.Client{Timeout: DefaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach the pg-pr sync daemon at %s: %w", addr, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusServiceUnavailable:
		return nil, ErrNoSnapshot
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("dashboard endpoint %s returned %s", url, resp.Status)
	}

	var snap snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, fmt.Errorf("decode dashboard payload from %s: %w", url, err)
	}
	return &snap, nil
}
