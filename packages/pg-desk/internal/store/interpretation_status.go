package store

import (
	"encoding/json"
	"fmt"
)

// MetaKeyReinterpretLastRun is the meta key that holds the counts of the last
// interpretation catch-up pass (docs/behavior/pg-desk/reinterpret.md,
// "Observability"). It is one JSON document so a reader sees one pass's
// counts together, never a mix of two passes.
const MetaKeyReinterpretLastRun = "reinterpret.last_run"

// ReinterpretRun is what the last catch-up pass did: the heartbeat's bounded
// pass or `pg-desk pr reinterpret --open`.
type ReinterpretRun struct {
	// At is the RFC 3339 UTC time the pass finished.
	At string `json:"at"`
	// Source is "heartbeat" or "pr reinterpret --open".
	Source      string `json:"source"`
	Selected    int    `json:"selected"`
	Changed     int    `json:"changed"`
	Unchanged   int    `json:"unchanged"`
	StampedOnly int    `json:"stamped_only"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
	// Remaining is the number still behind when the pass stopped at a limit
	// (0 for a pass that finished).
	Remaining int `json:"remaining"`
}

// RecordReinterpretRun persists the counts of a pass. A pass that selected no
// row records nothing (an idle heartbeat writes no meta row), so the last-run
// line keeps describing the last pass that did work. It reports whether a row
// was written.
func (s *Store) RecordReinterpretRun(r ReinterpretRun) (bool, error) {
	if r.Selected <= 0 {
		return false, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return false, fmt.Errorf("store: encode reinterpret last run: %w", err)
	}
	if err := s.SetMeta(MetaKeyReinterpretLastRun, string(b)); err != nil {
		return false, err
	}
	return true, nil
}

// LastReinterpretRun returns the persisted counts of the last pass, with
// found=false when no pass has recorded any (including on a store that lacks
// the key). An unreadable value is an error.
func (s *Store) LastReinterpretRun() (r ReinterpretRun, found bool, err error) {
	v, found, err := s.GetMeta(MetaKeyReinterpretLastRun)
	if err != nil || !found {
		return ReinterpretRun{}, false, err
	}
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return ReinterpretRun{}, false, fmt.Errorf("store: decode meta %q: %w", MetaKeyReinterpretLastRun, err)
	}
	return r, true, nil
}

// InterpretationsFailed counts the `pr` interpretation rows of repo marked
// failed under the handle's stamp (stamp + FailedStampSuffix). known is false,
// with no error, on a store that cannot answer (no stamp
// column yet): "unknown" is never "none". The handle must carry a stamp, else
// ErrNoInterpStamp.
func (s *Store) InterpretationsFailed(repo string) (n int, known bool, err error) {
	stamp := s.InterpStamp()
	if stamp == "" {
		return 0, false, ErrNoInterpStamp
	}
	haveStamp, err := s.hasInterpStampColumn()
	if err != nil {
		return 0, false, fmt.Errorf("store: interpretations failed: %w", err)
	}
	if !haveStamp {
		return 0, false, nil
	}
	err = s.sql.QueryRow(
		`SELECT COUNT(*) FROM interpretation WHERE repo = ? AND entity_type = 'pr' AND `+interpStampColumn+` = ?`,
		repo, stamp+FailedStampSuffix,
	).Scan(&n)
	if err != nil {
		return 0, false, fmt.Errorf("store: interpretations failed: %w", err)
	}
	return n, true, nil
}
