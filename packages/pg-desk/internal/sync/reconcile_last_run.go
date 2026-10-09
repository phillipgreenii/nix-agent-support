package sync

import (
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// ReconcileLastRunKey is the meta key holding the most recent
// `pg-desk reconcile` run's summary (bead pg2-q89ng).
//
// `reconcile` runs as a separate short-lived process whose stderr pg-router
// keeps only for a FAILED run, so its reconcile_open_set / reconcile_deferred
// log lines were unobservable on a healthy run. The run therefore also leaves
// this summary in the shared store, which `serve` reads at scrape time (the
// same cross-process path the sync_error gauges use).
const ReconcileLastRunKey = "reconcile.last_run"

// ReconcileLastRun is one reconcile run's summary.
type ReconcileLastRun struct {
	// At is when the run finished, RFC 3339.
	At string `json:"at"`
	// OpenIDs and SkippedOpen are the reconcile_open_set line's open_ids and
	// skipped_open. They are nil when the open set was not read this run (no
	// open anchor could use it), so a gauge reports no series rather than 0.
	OpenIDs     *int `json:"open_ids,omitempty"`
	SkippedOpen *int `json:"skipped_open,omitempty"`
	// Candidates is the number of entities the run considered for re-drive.
	Candidates int `json:"candidates"`
	// Deferred is the number of candidates deferred (transient read failure)
	// by this run: the count of reconcile_deferred lines it logged.
	Deferred int `json:"deferred"`
}

// WriteReconcileLastRun records s as the latest run summary.
func WriteReconcileLastRun(st *store.Store, s ReconcileLastRun) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetMeta(ReconcileLastRunKey, string(b))
}

// ReadReconcileLastRun returns the latest run summary and the seconds since it
// finished (never negative). found is false when no run has recorded one, or
// the stored value is unparseable, so a caller exports no series instead of a
// misleading zero.
func ReadReconcileLastRun(st *store.Store, now time.Time) (s ReconcileLastRun, ageSeconds int, found bool, err error) {
	v, ok, err := st.GetMeta(ReconcileLastRunKey)
	if err != nil || !ok {
		return ReconcileLastRun{}, 0, false, err
	}
	if json.Unmarshal([]byte(v), &s) != nil {
		return ReconcileLastRun{}, 0, false, nil
	}
	at, perr := time.Parse(time.RFC3339, s.At)
	if perr != nil {
		return ReconcileLastRun{}, 0, false, nil
	}
	age := int(now.Sub(at).Seconds())
	if age < 0 {
		age = 0
	}
	return s, age, true, nil
}
