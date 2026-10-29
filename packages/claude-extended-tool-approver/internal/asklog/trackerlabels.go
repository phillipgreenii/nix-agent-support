package asklog

import (
	"fmt"
	"strings"
)

// CallKey is the lasting identity of a call the claude-tool-tracker recorded:
// the session and the tool_use_id. It is the key of tracker_labels because the
// tracker's integer call_id belongs to one database file, while this pair
// survives any re-ingest.
type CallKey struct {
	SessionID string
	ToolUseID string
}

// TrackerLabel is the operator's offline-validation data for one tracker call.
// A nil CorrectDec means no correct-decision label; Excluded false means not
// excluded.
type TrackerLabel struct {
	CorrectDec            *string
	CorrectDecExplanation string
	Excluded              bool
	ExcludedReason        string
}

// SetTrackerCorrectDecision records the ground-truth decision for each call
// (the --source tracker twin of SetCorrectDecision). It keeps any existing
// exclusion on the same call.
func (s *Store) SetTrackerCorrectDecision(keys []CallKey, decision, explanation string) error {
	for _, k := range keys {
		_, err := s.db.Exec(`INSERT INTO tracker_labels
			(session_id, tool_use_id, correct_hook_decision, correct_hook_decision_explanation)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(session_id, tool_use_id) DO UPDATE SET
				correct_hook_decision = excluded.correct_hook_decision,
				correct_hook_decision_explanation = excluded.correct_hook_decision_explanation`,
			k.SessionID, k.ToolUseID, decision, explanation)
		if err != nil {
			return fmt.Errorf("set-correct-decision %s/%s: %w", k.SessionID, k.ToolUseID, err)
		}
	}
	return nil
}

// MarkTrackerExcluded excludes each call from evaluation (the --source tracker
// twin of MarkExcluded). It keeps any existing correct-decision label.
func (s *Store) MarkTrackerExcluded(keys []CallKey, reason string) error {
	for _, k := range keys {
		_, err := s.db.Exec(`INSERT INTO tracker_labels
			(session_id, tool_use_id, excluded, excluded_reason)
			VALUES (?, ?, 1, ?)
			ON CONFLICT(session_id, tool_use_id) DO UPDATE SET
				excluded = 1, excluded_reason = excluded.excluded_reason`,
			k.SessionID, k.ToolUseID, reason)
		if err != nil {
			return fmt.Errorf("mark-excluded %s/%s: %w", k.SessionID, k.ToolUseID, err)
		}
	}
	return nil
}

// TrackerLabels returns every tracker label. A database that predates the
// table (a read-only store cannot migrate it) has no labels, which is not an
// error.
func (s *Store) TrackerLabels() (map[CallKey]TrackerLabel, error) {
	out := map[CallKey]TrackerLabel{}
	err := s.withTornReadRetry(func() error {
		clear(out)
		rows, err := s.db.Query(`SELECT session_id, tool_use_id, correct_hook_decision,
			COALESCE(correct_hook_decision_explanation, ''), excluded, COALESCE(excluded_reason, '')
			FROM tracker_labels`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k CallKey
			var l TrackerLabel
			var ex int
			if err := rows.Scan(&k.SessionID, &k.ToolUseID, &l.CorrectDec, &l.CorrectDecExplanation, &ex, &l.ExcludedReason); err != nil {
				return err
			}
			l.Excluded = ex != 0
			out[k] = l
		}
		return rows.Err()
	})
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return map[CallKey]TrackerLabel{}, nil
		}
		return nil, err
	}
	return out, nil
}
