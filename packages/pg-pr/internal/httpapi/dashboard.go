// Package httpapi exposes pg-pr daemon HTTP handlers beyond /metrics.
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// nowUTC is the serve-time clock used to stamp the freshness verdict.
// Overridable in tests.
var nowUTC = func() time.Time { return time.Now().UTC() }

// DashboardHandler serves the current snapshot as JSON.
// Returns 503 until the daemon has populated a first snapshot.
//
// The held snapshot AGES between daemon ticks (and indefinitely if a tick wedges),
// so the payload's freshness verdict is stamped HERE, per request, via
// Snapshot.WithFreshness — the served document always carries an age and a stale
// flag computed at the moment the consumer read it, never at the moment the
// daemon built it (pr-pool INV-FRESH-1).
func DashboardHandler(store *snapshot.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap, ok := store.Get()
		if !ok {
			http.Error(w, `{"error":"snapshot not yet populated"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap.WithFreshness(nowUTC()))
	})
}
