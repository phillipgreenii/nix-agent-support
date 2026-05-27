// Package httpapi exposes pg-pr daemon HTTP handlers beyond /metrics.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// DashboardHandler serves the current snapshot as JSON.
// Returns 503 until the daemon has populated a first snapshot.
func DashboardHandler(store *snapshot.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap, ok := store.Get()
		if !ok {
			http.Error(w, `{"error":"snapshot not yet populated"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
}
