package ingest

import (
	"database/sql"
	"time"
)

// QuietPeriod is how long a session may write no event before a call of it
// without tracker.call_end counts as unresolved (a crashed session never sends
// tracker.session_end). It is measured against the ingest run's wall clock.
const QuietPeriod = 2 * time.Hour

// Settlement outcome words.
const (
	settledCeta       = "ceta"
	settledPerson     = "person"
	settledMode       = "mode"
	settledRule       = "rule"
	settledClassifier = "classifier"

	approved   = "approved"
	refused    = "refused"
	pending    = "pending"
	unresolved = "unresolved"
)

// settleInput is what the derivation reads for one call.
type settleInput struct {
	checkDecision string // tracker.check decision: allow, ask, deny; "" when absent
	engine        string // ceta.verdict verdict; "" when absent
	delivered     string // ceta.verdict delivered; "" when absent
	mode          string // live permission mode; "" when absent
	outcome       string // tracker.call_end outcome; "" when no call_end
	sessionOver   bool   // the session has a session_end or has been quiet too long
}

// settleResult is the derived row.
type settleResult struct {
	settledBy    string // "" means NULL
	settlement   string // "" means NULL
	convertedAsk bool
	failed       sql.NullBool // marked failed for timing: ran and errored
}

// derive applies the settlement table. It is pure so every row is testable.
func derive(in settleInput) settleResult {
	var r settleResult
	// The verdict the call was settled under, in tracker.check words.
	verdict := in.checkDecision
	cetaDecided := false
	switch in.delivered {
	case "approve":
		verdict, cetaDecided = "allow", true
	case "reject":
		verdict, cetaDecided = "deny", true
	case "ask":
		verdict, cetaDecided = "ask", true
	}
	if in.engine == "ask" && in.delivered == "reject" {
		r.convertedAsk = true
	}

	ran := in.outcome == "ok" || in.outcome == "error"
	byOutcome := refused
	if ran {
		byOutcome = approved
	}

	switch {
	case cetaDecided && verdict != "ask":
		r.settledBy = settledCeta
		r.settlement = approved
		if verdict == "deny" {
			r.settlement = refused
		}
	case cetaDecided: // CETA asked: a person decides
		r.settledBy, r.settlement = settledPerson, byOutcome
	case verdict == "allow":
		r.settlement = approved
		if in.mode == "bypassPermissions" {
			r.settledBy = settledMode
		} else {
			r.settledBy = settledRule
		}
	case verdict == "deny":
		r.settledBy, r.settlement = settledRule, refused
	case verdict == "ask":
		r.settlement = byOutcome
		switch in.mode {
		case "auto":
			r.settledBy = settledClassifier
		case "dontAsk":
			r.settledBy, r.settlement = settledMode, refused
		default:
			r.settledBy = settledPerson
		}
	default:
		r.settlement = byOutcome // no verdict event at all: settled_by stays NULL
	}

	if in.outcome == "" && !r.convertedAsk {
		// No call_end yet: pending while the session may still run.
		r.settlement = pending
		if in.sessionOver {
			r.settlement = unresolved
		}
	}
	if r.convertedAsk {
		// An unattended session delivered CETA's ask as a reject.
		r.settledBy, r.settlement = settledCeta, refused
	}
	if in.outcome != "" {
		r.failed = sql.NullBool{Bool: in.outcome == "error", Valid: true}
	}
	return r
}

const settleSelect = `SELECT c.call_id, c.session_id,
 COALESCE((SELECT json_extract(e.data,'$.decision') FROM events e WHERE e.session_id=c.session_id AND e.tool_use_id=c.tool_use_id AND e.kind='tracker.check' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), ''),
 COALESCE((SELECT json_extract(e.data,'$.verdict') FROM events e WHERE e.session_id=c.session_id AND e.tool_use_id=c.tool_use_id AND e.kind='ceta.verdict' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), ''),
 COALESCE((SELECT json_extract(e.data,'$.delivered') FROM events e WHERE e.session_id=c.session_id AND e.tool_use_id=c.tool_use_id AND e.kind='ceta.verdict' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), ''),
 COALESCE((SELECT json_extract(e.data,'$.permission_mode') FROM events e WHERE e.session_id=c.session_id AND e.tool_use_id=c.tool_use_id AND e.kind='tracker.pre_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1),
          (SELECT json_extract(e.data,'$.permission_mode') FROM events e WHERE e.session_id=c.session_id AND e.tool_use_id=c.tool_use_id AND e.kind='tracker.post_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), ''),
 COALESCE((SELECT json_extract(e.data,'$.outcome') FROM events e WHERE e.session_id=c.session_id AND e.tool_use_id=c.tool_use_id AND e.kind='tracker.call_end' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), ''),
 EXISTS (SELECT 1 FROM events e WHERE e.session_id=c.session_id AND e.kind='tracker.session_end'),
 COALESCE((SELECT MAX(e.ts) FROM events e WHERE e.session_id=c.session_id), 0)
FROM calls c WHERE c.call_id = ?`

// settleCalls derives and stores settled_by, settlement and converted_ask for
// every call in touched and every call still pending (or never derived), so a
// later session_end or the passing of the quiet period is picked up.
func settleCalls(db *sql.DB, touched map[int64]bool, now time.Time) error {
	rows, err := db.Query(`SELECT c.call_id FROM calls c LEFT JOIN call_settlement s ON s.call_id = c.call_id
		WHERE s.call_id IS NULL OR s.settlement IS NULL OR s.settlement = 'pending'`)
	if err != nil {
		return err
	}
	ids := map[int64]bool{}
	for id := range touched {
		ids[id] = true
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for id := range ids {
		var in settleInput
		var callID int64
		var session string
		var ended bool
		var lastTS int64
		if err := tx.QueryRow(settleSelect, id).Scan(&callID, &session, &in.checkDecision, &in.engine,
			&in.delivered, &in.mode, &in.outcome, &ended, &lastTS); err != nil {
			return err
		}
		in.sessionOver = ended || now.UnixMilli()-lastTS > QuietPeriod.Milliseconds()
		r := derive(in)
		var by, st, failed any
		if r.settledBy != "" {
			by = r.settledBy
		}
		if r.settlement != "" {
			st = r.settlement
		}
		if r.failed.Valid {
			failed = r.failed.Bool
		}
		if _, err := tx.Exec(`INSERT INTO call_settlement(call_id, settled_by, settlement, converted_ask, failed_timing)
			VALUES (?,?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET settled_by=excluded.settled_by,
			settlement=excluded.settlement, converted_ask=excluded.converted_ask, failed_timing=excluded.failed_timing`,
			callID, by, st, r.convertedAsk, failed); err != nil {
			return err
		}
	}
	return tx.Commit()
}
