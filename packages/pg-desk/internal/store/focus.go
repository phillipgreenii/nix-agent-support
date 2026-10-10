package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The focus access layer (schema version 2 only): a Go API over the four
// focus tables of the cutover block (focus_period, focus_selection,
// focus_draft, focus_run; docket pg2-2j5ac.44, spec section 5). This layer
// owns SQL; the focus verbs own policy. It stores and retrieves: cap rules,
// first-lock rules and draft staleness decisions beyond the two transactional
// re-checks below belong to the verbs.
//
// Transaction shape. The store is a single connection, so every transaction
// here runs on a dedicated connection opened with BEGIN IMMEDIATE: the write
// lock is taken up front, which makes a re-check made inside the transaction
// ("no plan rows for the period", "rows of every other period") hold at
// commit and makes two concurrent locks serialize. Code inside a FocusLockTx
// callback MUST use only the FocusTx it is given: any other Store call blocks
// on the single connection until the callback returns.
//
// Telemetry: SQLite rows only. This layer emits no OpenTelemetry or
// Prometheus signal and logs nothing; the focus run record (focus_run) is the
// durable copy of the verbs' structured stderr line (spec section 8.2).

// Focus period types the schema admits (CHECK constraint).
const (
	FocusPeriodDay    = "day"
	FocusPeriodWeek   = "week"
	FocusPeriodSprint = "sprint"
)

// FocusAnnotationNone is the focus_selected value of an entity with no row
// in any period.
const FocusAnnotationNone = "none"

// focusDayLayout is the period-key layout of a day period. It is parsed with
// the lenient layout "2006-1-2" (so 2026-9-3 is accepted) and always written
// back in this canonical form.
const focusDayLayout = "2006-01-02"

// FocusPeriod is one focus_period row. A NULL cap is Cap == nil; a NULL
// closed_at / close_note is the empty string.
type FocusPeriod struct {
	ID                    int64
	PeriodType, PeriodKey string
	Cap                   *int
	ClosedAt, CloseNote   string
}

// FocusSelection is one focus_selection row: one entity of the plan of one
// period. A NULL tier (a hand-add) is Tier == nil.
type FocusSelection struct {
	ID, FocusPeriodID int64
	Repo, EntityType  string
	EntityID          string
	SelectedAt        string
	RankPosition      int
	Tier              *string
}

// FocusDraft is the single focus_draft row (id is always 1).
type FocusDraft struct {
	PeriodType, PeriodKey string
	Cap                   int
	MadeAt, BodyJSON      string
}

// FocusRun is one focus_run row. A NULL exit_code is ExitCode == nil; a NULL
// focus_period_id is FocusPeriodID == nil.
type FocusRun struct {
	ID            int64
	RunID, Verb   string
	FocusPeriodID *int64
	StartedAt     string
	Actor         string
	ExitCode      *int
	CountsJSON    string
}

// NormalizeFocusPeriodKey validates periodType (day, week or sprint) and
// returns the canonical form of key: a day key is parsed (2026-9-3 and
// 2026-09-03 are the same day) and re-formatted as 2006-01-02; a week or
// sprint key (not produced this phase) must be non-empty and is kept as
// written. An unknown period type or an unparseable key is an error.
func NormalizeFocusPeriodKey(periodType, key string) (string, error) {
	switch periodType {
	case FocusPeriodDay:
		t, err := time.Parse("2006-1-2", key)
		if err != nil {
			return "", fmt.Errorf("store: focus period key %q is not a date: %w", key, err)
		}
		return t.Format(focusDayLayout), nil
	case FocusPeriodWeek, FocusPeriodSprint:
		if strings.TrimSpace(key) == "" {
			return "", errors.New("store: focus period key is empty")
		}
		return key, nil
	default:
		return "", fmt.Errorf("store: focus period type %q is not one of day, week, sprint", periodType)
	}
}

// ---------------------------------------------------------------- plumbing

// focusExec is the statement surface shared by the dedicated-connection
// transactions below.
type focusExec struct {
	ctx  context.Context
	conn *sql.Conn
}

func (x focusExec) exec(q string, args ...any) (sql.Result, error) {
	return x.conn.ExecContext(x.ctx, q, args...)
}

func (x focusExec) query(q string, args ...any) (*sql.Rows, error) {
	return x.conn.QueryContext(x.ctx, q, args...)
}

func (x focusExec) row(q string, args ...any) *sql.Row {
	return x.conn.QueryRowContext(x.ctx, q, args...)
}

// focusImmediate runs fn inside BEGIN IMMEDIATE ... COMMIT on a dedicated
// connection. An error return or a panic from fn rolls everything back.
func (s *Store) focusImmediate(what string, fn func(x focusExec) error) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := s.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: %s: acquire connection: %w", what, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("store: %s: begin: %w", what, err)
	}
	committed := false
	defer func() {
		if !committed {
			// A failed statement may already have ended the transaction; the
			// resulting "no transaction is active" error is harmless.
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if err := fn(focusExec{ctx: ctx, conn: conn}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("store: %s: commit: %w", what, err)
	}
	committed = true
	return nil
}

const focusPeriodCols = `id, period_type, period_key, cap, closed_at, close_note`

func scanFocusPeriod(sc interface{ Scan(...any) error }) (FocusPeriod, error) {
	var p FocusPeriod
	var cap sql.NullInt64
	var closedAt, closeNote sql.NullString
	if err := sc.Scan(&p.ID, &p.PeriodType, &p.PeriodKey, &cap, &closedAt, &closeNote); err != nil {
		return FocusPeriod{}, err
	}
	if cap.Valid {
		c := int(cap.Int64)
		p.Cap = &c
	}
	p.ClosedAt, p.CloseNote = closedAt.String, closeNote.String
	return p, nil
}

const focusSelectionCols = `id, focus_period_id, repo, entity_type, entity_id, selected_at, rank_position, tier`

func scanFocusSelection(sc interface{ Scan(...any) error }) (FocusSelection, error) {
	var r FocusSelection
	var tier sql.NullString
	if err := sc.Scan(&r.ID, &r.FocusPeriodID, &r.Repo, &r.EntityType, &r.EntityID, &r.SelectedAt, &r.RankPosition, &tier); err != nil {
		return FocusSelection{}, err
	}
	if tier.Valid {
		t := tier.String
		r.Tier = &t
	}
	return r, nil
}

const focusRunCols = `id, run_id, verb, focus_period_id, started_at, actor, exit_code, counts_json`

func scanFocusRun(sc interface{ Scan(...any) error }) (FocusRun, error) {
	var r FocusRun
	var periodID, exit sql.NullInt64
	if err := sc.Scan(&r.ID, &r.RunID, &r.Verb, &periodID, &r.StartedAt, &r.Actor, &exit, &r.CountsJSON); err != nil {
		return FocusRun{}, err
	}
	if periodID.Valid {
		v := periodID.Int64
		r.FocusPeriodID = &v
	}
	if exit.Valid {
		v := int(exit.Int64)
		r.ExitCode = &v
	}
	return r, nil
}

func nullableInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableStringPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// ---------------------------------------------------------------- periods

// getOrCreatePeriod returns the period (periodType, periodKey), creating it
// (cap, closed_at and close_note NULL) when absent. The key is normalized
// first, so 2026-9-3 and 2026-09-03 are one period; an invalid key or period
// type is an error and writes nothing.
func getOrCreatePeriod(x focusExec, periodType, periodKey string) (FocusPeriod, error) {
	key, err := NormalizeFocusPeriodKey(periodType, periodKey)
	if err != nil {
		return FocusPeriod{}, err
	}
	if _, err := x.exec(
		`INSERT INTO focus_period (period_type, period_key) VALUES (?, ?)
		 ON CONFLICT (period_type, period_key) DO NOTHING`, periodType, key,
	); err != nil {
		return FocusPeriod{}, fmt.Errorf("store: focus period (%s,%s): %w", periodType, key, err)
	}
	p, err := scanFocusPeriod(x.row(`SELECT `+focusPeriodCols+` FROM focus_period WHERE period_type = ? AND period_key = ?`, periodType, key))
	if err != nil {
		return FocusPeriod{}, fmt.Errorf("store: focus period (%s,%s): %w", periodType, key, err)
	}
	return p, nil
}

// GetOrCreateFocusPeriod returns the focus_period row for (periodType,
// periodKey), creating it when absent with closed_at and close_note NULL.
// The key is parsed and re-formatted on write, so 2026-9-3 cannot make a
// second period. An invalid key, or a period type outside day|week|sprint,
// is an error and writes nothing.
func (s *Store) GetOrCreateFocusPeriod(periodType, periodKey string) (FocusPeriod, error) {
	var p FocusPeriod
	err := s.focusImmediate("focus period", func(x focusExec) error {
		var err error
		p, err = getOrCreatePeriod(x, periodType, periodKey)
		return err
	})
	return p, err
}

// FocusPeriodGet returns the focus_period row for (periodType, periodKey)
// and found=true, or found=false when none exists. It never creates a row.
// The key is normalized like GetOrCreateFocusPeriod's.
func (s *Store) FocusPeriodGet(periodType, periodKey string) (FocusPeriod, bool, error) {
	if err := s.RequireNewSchema(); err != nil {
		return FocusPeriod{}, false, err
	}
	key, err := NormalizeFocusPeriodKey(periodType, periodKey)
	if err != nil {
		return FocusPeriod{}, false, err
	}
	p, err := scanFocusPeriod(s.sql.QueryRow(`SELECT `+focusPeriodCols+` FROM focus_period WHERE period_type = ? AND period_key = ?`, periodType, key))
	if errors.Is(err, sql.ErrNoRows) {
		return FocusPeriod{}, false, nil
	}
	if err != nil {
		return FocusPeriod{}, false, fmt.Errorf("store: focus period (%s,%s): %w", periodType, key, err)
	}
	return p, true, nil
}

// FocusPeriods returns every focus_period row, ordered by (period_type,
// period_key).
func (s *Store) FocusPeriods() ([]FocusPeriod, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(`SELECT ` + focusPeriodCols + ` FROM focus_period ORDER BY period_type, period_key`)
	if err != nil {
		return nil, fmt.Errorf("store: list focus periods: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []FocusPeriod
	for rows.Next() {
		p, err := scanFocusPeriod(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan focus period: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list focus periods: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------- plan reads

func (s *Store) focusSelections(what, where string, args ...any) ([]FocusSelection, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(`SELECT `+focusSelectionCols+` FROM focus_selection `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("store: focus rows of %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []FocusSelection
	for rows.Next() {
		r, err := scanFocusSelection(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan focus row of %s: %w", what, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: focus rows of %s: %w", what, err)
	}
	return out, nil
}

// FocusPlanRows returns the plan of one period: its focus_selection rows
// ordered by rank_position (then id).
func (s *Store) FocusPlanRows(periodID int64) ([]FocusSelection, error) {
	return s.focusSelections(fmt.Sprintf("period %d", periodID),
		`WHERE focus_period_id = ? ORDER BY rank_position, id`, periodID)
}

// FocusRowsAllPeriods returns every focus_selection row, ordered by
// (focus_period_id, rank_position, id).
func (s *Store) FocusRowsAllPeriods() ([]FocusSelection, error) {
	return s.focusSelections("every period", `ORDER BY focus_period_id, rank_position, id`)
}

// FocusRowsOfEntity returns the rows of one entity in every period, ordered
// by focus_period_id.
func (s *Store) FocusRowsOfEntity(repo, entityType, entityID string) ([]FocusSelection, error) {
	return s.focusSelections(fmt.Sprintf("(%s,%s,%s)", repo, entityType, entityID),
		`WHERE repo = ? AND entity_type = ? AND entity_id = ? ORDER BY focus_period_id, id`,
		repo, entityType, entityID)
}

// focusAnnotationValue is the focus_selected annotation function: the
// maximum period_key over the entity's rows in every period, else "none".
// Period keys of a day period are ISO dates, which order lexically.
func focusAnnotationValue(q func(string, ...any) *sql.Row, repo, entityType, entityID string) (string, error) {
	var v sql.NullString
	err := q(
		`SELECT MAX(p.period_key) FROM focus_selection s
		   JOIN focus_period p ON p.id = s.focus_period_id
		  WHERE s.repo = ? AND s.entity_type = ? AND s.entity_id = ?`,
		repo, entityType, entityID,
	).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("store: focus annotation value (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	if !v.Valid {
		return FocusAnnotationNone, nil
	}
	return v.String, nil
}

// FocusAnnotationValue returns the value the focus_selected annotation of an
// entity MUST hold: the maximum period_key over the entity's rows in every
// period, else "none" (D-F18). It is a function of the committed table; it
// never means "selected today".
func (s *Store) FocusAnnotationValue(repo, entityType, entityID string) (string, error) {
	if err := s.RequireNewSchema(); err != nil {
		return "", err
	}
	return focusAnnotationValue(s.sql.QueryRow, repo, entityType, entityID)
}

// ---------------------------------------------------------------- the draft

const focusDraftCols = `period_type, period_key, cap, made_at, body_json`

func scanFocusDraft(sc interface{ Scan(...any) error }) (FocusDraft, error) {
	var d FocusDraft
	err := sc.Scan(&d.PeriodType, &d.PeriodKey, &d.Cap, &d.MadeAt, &d.BodyJSON)
	return d, err
}

// FocusDraftGet returns the single stored draft, or found=false when the
// row is free.
func (s *Store) FocusDraftGet() (FocusDraft, bool, error) {
	if err := s.RequireNewSchema(); err != nil {
		return FocusDraft{}, false, err
	}
	d, err := scanFocusDraft(s.sql.QueryRow(`SELECT ` + focusDraftCols + ` FROM focus_draft WHERE id = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return FocusDraft{}, false, nil
	}
	if err != nil {
		return FocusDraft{}, false, fmt.Errorf("store: get focus draft: %w", err)
	}
	return d, true, nil
}

// periodHoldsRows reports whether the period (periodType, key) has any
// focus_selection row. A period with no focus_period row holds none.
func periodHoldsRows(x focusExec, periodType, key string) (bool, error) {
	var n int
	err := x.row(
		`SELECT COUNT(*) FROM focus_selection s JOIN focus_period p ON p.id = s.focus_period_id
		  WHERE p.period_type = ? AND p.period_key = ?`, periodType, key,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: focus plan rows of (%s,%s): %w", periodType, key, err)
	}
	return n > 0, nil
}

// FocusDraftStore stores d as the single draft, in one transaction that
// re-checks the addressed period has no plan rows and then runs INSERT ...
// ON CONFLICT DO UPDATE taken only when the stored draft's period key is
// LESS than addressedKey, else DO NOTHING (RV-A). It returns stored=false
// when a plan appeared for the period (a show that computed before a lock
// committed stores nothing) or when a draft for the same or a later period
// holds the row. Both keys are normalized first. A same-period draft that
// could not be decoded is replaced through FocusDraftReplace, not here.
func (s *Store) FocusDraftStore(d FocusDraft, addressedKey string) (stored bool, err error) {
	dkey, err := NormalizeFocusPeriodKey(d.PeriodType, d.PeriodKey)
	if err != nil {
		return false, err
	}
	akey, err := NormalizeFocusPeriodKey(d.PeriodType, addressedKey)
	if err != nil {
		return false, err
	}
	d.PeriodKey = dkey
	err = s.focusImmediate("store focus draft", func(x focusExec) error {
		held, err := periodHoldsRows(x, d.PeriodType, akey)
		if err != nil {
			return err
		}
		if held {
			return nil
		}
		res, err := x.exec(
			`INSERT INTO focus_draft (id, period_type, period_key, cap, made_at, body_json)
			 VALUES (1, ?, ?, ?, ?, ?)
			 ON CONFLICT (id) DO UPDATE SET
			   period_type = excluded.period_type,
			   period_key  = excluded.period_key,
			   cap         = excluded.cap,
			   made_at     = excluded.made_at,
			   body_json   = excluded.body_json
			 WHERE focus_draft.period_key < ?`,
			d.PeriodType, d.PeriodKey, d.Cap, d.MadeAt, d.BodyJSON, akey,
		)
		if err != nil {
			return fmt.Errorf("store: store focus draft: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: store focus draft: %w", err)
		}
		stored = n > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

// FocusDraftReplace replaces the single draft with d (an explicit
// recompute), in one transaction that re-checks the plan rows of the draft's
// period and writes the row only when none exist. planNowExists=true means a
// plan appeared and nothing was written.
func (s *Store) FocusDraftReplace(d FocusDraft) (planNowExists bool, err error) {
	key, err := NormalizeFocusPeriodKey(d.PeriodType, d.PeriodKey)
	if err != nil {
		return false, err
	}
	d.PeriodKey = key
	err = s.focusImmediate("replace focus draft", func(x focusExec) error {
		held, err := periodHoldsRows(x, d.PeriodType, key)
		if err != nil {
			return err
		}
		if held {
			planNowExists = true
			return nil
		}
		if _, err := x.exec(
			`INSERT INTO focus_draft (id, period_type, period_key, cap, made_at, body_json)
			 VALUES (1, ?, ?, ?, ?, ?)
			 ON CONFLICT (id) DO UPDATE SET
			   period_type = excluded.period_type,
			   period_key  = excluded.period_key,
			   cap         = excluded.cap,
			   made_at     = excluded.made_at,
			   body_json   = excluded.body_json`,
			d.PeriodType, d.PeriodKey, d.Cap, d.MadeAt, d.BodyJSON,
		); err != nil {
			return fmt.Errorf("store: replace focus draft: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return planNowExists, nil
}

// ---------------------------------------------------------------- runs

// FocusRunInsert inserts a focus_run row outside a lock transaction (for
// verbs that fail before locking) and returns its id. ExitCode nil leaves
// exit_code NULL until FocusRunFinalize.
func (s *Store) FocusRunInsert(r FocusRun) (int64, error) {
	var id int64
	err := s.focusImmediate("insert focus run", func(x focusExec) error {
		var err error
		id, err = insertRun(x, r)
		return err
	})
	return id, err
}

// FocusRunFinalize sets the exit_code and counts_json of the run runID. It
// errors when no such run exists.
func (s *Store) FocusRunFinalize(runID string, exitCode int, countsJSON string) error {
	return s.focusImmediate("finalize focus run", func(x focusExec) error {
		return finalizeRun(x, runID, exitCode, countsJSON)
	})
}

func insertRun(x focusExec, r FocusRun) (int64, error) {
	counts := r.CountsJSON
	if counts == "" {
		counts = "{}"
	}
	res, err := x.exec(
		`INSERT INTO focus_run (run_id, verb, focus_period_id, started_at, actor, exit_code, counts_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.Verb, nullableInt64(r.FocusPeriodID), r.StartedAt, r.Actor, nullableInt(r.ExitCode), counts,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert focus run %q: %w", r.RunID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert focus run %q: %w", r.RunID, err)
	}
	return id, nil
}

func finalizeRun(x focusExec, runID string, exitCode int, countsJSON string) error {
	res, err := x.exec(`UPDATE focus_run SET exit_code = ?, counts_json = ? WHERE run_id = ?`, exitCode, countsJSON, runID)
	if err != nil {
		return fmt.Errorf("store: finalize focus run %q: %w", runID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: finalize focus run %q: %w", runID, err)
	} else if n == 0 {
		return fmt.Errorf("store: finalize focus run %q: no such run", runID)
	}
	return nil
}

// FocusRunStats is the run-statistics read of the metrics and doctor
// packets. ByVerbOutcome counts focus_run rows by {verb, outcome}; outcome is
// one of ok, empty, partial, total, period_closed, usage. LastSelectAt is the
// started_at of the newest select row ("" when none). The two last-lock
// values come from that row's counts_json members draft_age_seconds and
// drift_rows (nil when absent).
type FocusRunStats struct {
	ByVerbOutcome           map[[2]string]int
	LastSelectAt            string
	LastLockDraftAgeSeconds *int64
	LastLockDriftRows       *int
}

// FocusRunStats reads the run statistics (spec section 8.2). The outcome
// word is derived from each focus_run row: exit_code 0 is ok, except a select
// whose counts_json.selected is 0 and whose counts_json.removed counts are all
// zero, which is empty; 2 is partial; 3 or NULL (a crash left the run
// unfinalised) is total; 6 is period_closed; 1 is usage. Any other code is
// counted as total (fail closed). A row is written only by a non-dry-run, so
// every row counts.
func (s *Store) FocusRunStats() (FocusRunStats, error) {
	out := FocusRunStats{ByVerbOutcome: map[[2]string]int{}}
	if err := s.RequireNewSchema(); err != nil {
		return out, err
	}
	rows, err := s.sql.Query(`SELECT ` + focusRunCols + ` FROM focus_run ORDER BY id`)
	if err != nil {
		return out, fmt.Errorf("store: focus run stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var newest *FocusRun
	for rows.Next() {
		r, err := scanFocusRun(rows)
		if err != nil {
			return out, fmt.Errorf("store: scan focus run: %w", err)
		}
		out.ByVerbOutcome[[2]string{r.Verb, focusRunOutcome(r)}]++
		if r.Verb == "select" && (newest == nil || r.StartedAt >= newest.StartedAt) {
			cp := r
			newest = &cp
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("store: focus run stats: %w", err)
	}
	if newest != nil {
		out.LastSelectAt = newest.StartedAt
		var counts map[string]json.RawMessage
		if json.Unmarshal([]byte(newest.CountsJSON), &counts) == nil {
			var age int64
			if json.Unmarshal(counts["draft_age_seconds"], &age) == nil {
				out.LastLockDraftAgeSeconds = &age
			}
			var drift int
			if json.Unmarshal(counts["drift_rows"], &drift) == nil {
				out.LastLockDriftRows = &drift
			}
		}
	}
	return out, nil
}

// focusRunOutcome derives the outcome word of one run row.
func focusRunOutcome(r FocusRun) string {
	if r.ExitCode == nil {
		return "total"
	}
	switch *r.ExitCode {
	case 0:
		if r.Verb == "select" && focusRunIsEmpty(r.CountsJSON) {
			return "empty"
		}
		return "ok"
	case 1:
		return "usage"
	case 2:
		return "partial"
	case 6:
		return "period_closed"
	default: // 3 and any code outside the focus scheme
		return "total"
	}
}

// focusRunIsEmpty reports counts_json.selected == 0 and every
// counts_json.removed count zero. An undecodable document, or one without
// the members, is not empty.
func focusRunIsEmpty(countsJSON string) bool {
	var counts map[string]json.RawMessage
	if json.Unmarshal([]byte(countsJSON), &counts) != nil {
		return false
	}
	var selected int
	if raw, ok := counts["selected"]; !ok || json.Unmarshal(raw, &selected) != nil || selected != 0 {
		return false
	}
	raw, ok := counts["removed"]
	if !ok {
		return false
	}
	var total int
	if json.Unmarshal(raw, &total) == nil { // a bare number
		return total == 0
	}
	var byCause map[string]int
	if json.Unmarshal(raw, &byCause) != nil {
		return false
	}
	for _, n := range byCause {
		if n != 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- lock tx

// FocusTx is the ONE transaction object the lock (and pull and close) use:
// the table writes, the draft deletion, the focus_period row and the
// focus_run row commit together or not at all. It is valid only inside the
// FocusLockTx callback that received it.
type FocusTx struct {
	x focusExec
}

// FocusLockTx runs fn in one write transaction (BEGIN IMMEDIATE). It commits
// when fn returns nil and rolls everything back when fn returns an error or
// panics. fn MUST use only the FocusTx it is given.
func (s *Store) FocusLockTx(fn func(*FocusTx) error) error {
	return s.focusImmediate("focus lock", func(x focusExec) error {
		return fn(&FocusTx{x: x})
	})
}

// GetOrCreatePeriod is GetOrCreateFocusPeriod inside the transaction.
func (t *FocusTx) GetOrCreatePeriod(periodType, periodKey string) (FocusPeriod, error) {
	return getOrCreatePeriod(t.x, periodType, periodKey)
}

// SetPeriodCap persists the cap in force for the period.
func (t *FocusTx) SetPeriodCap(periodID int64, cap int) error {
	return t.mustUpdate(fmt.Sprintf("set focus period %d cap", periodID),
		`UPDATE focus_period SET cap = ? WHERE id = ?`, cap, periodID)
}

// ClosePeriod sets closed_at and close_note on the period.
func (t *FocusTx) ClosePeriod(periodID int64, closedAt, closeNote string) error {
	return t.mustUpdate(fmt.Sprintf("close focus period %d", periodID),
		`UPDATE focus_period SET closed_at = ?, close_note = ? WHERE id = ?`, closedAt, closeNote, periodID)
}

func (t *FocusTx) mustUpdate(what, q string, args ...any) error {
	res, err := t.x.exec(q, args...)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	} else if n == 0 {
		return fmt.Errorf("store: %s: no such row", what)
	}
	return nil
}

// InsertSelection inserts a plan row and returns its id. The (period,
// repo, type, id) unique constraint and the entity foreign key apply: a
// duplicate or an unknown entity is an error.
func (t *FocusTx) InsertSelection(r FocusSelection) (int64, error) {
	res, err := t.x.exec(
		`INSERT INTO focus_selection (focus_period_id, repo, entity_type, entity_id, selected_at, rank_position, tier)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.FocusPeriodID, r.Repo, r.EntityType, r.EntityID, r.SelectedAt, r.RankPosition, nullableStringPtr(r.Tier),
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert focus row (%d,%s,%s,%s): %w", r.FocusPeriodID, r.Repo, r.EntityType, r.EntityID, err)
	}
	return res.LastInsertId()
}

// DeleteSelection deletes the entity's row in one period and reports whether
// a row existed.
func (t *FocusTx) DeleteSelection(periodID int64, repo, entityType, entityID string) (bool, error) {
	return t.deleted(`DELETE FROM focus_selection WHERE focus_period_id = ? AND repo = ? AND entity_type = ? AND entity_id = ?`,
		periodID, repo, entityType, entityID)
}

// DeleteEntityRows deletes every row of the entity in every period (a
// strike) and returns how many rows went.
func (t *FocusTx) DeleteEntityRows(repo, entityType, entityID string) (int, error) {
	return t.deletedCount(`DELETE FROM focus_selection WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID)
}

// DeleteRowsOfOtherPeriods deletes every row of every period other than
// periodID (the first lock of a new period) and returns how many rows went.
// The focus_period rows, closed_at and close_note stay.
func (t *FocusTx) DeleteRowsOfOtherPeriods(periodID int64) (int, error) {
	return t.deletedCount(`DELETE FROM focus_selection WHERE focus_period_id <> ?`, periodID)
}

func (t *FocusTx) deleted(q string, args ...any) (bool, error) {
	n, err := t.deletedCount(q, args...)
	return n > 0, err
}

func (t *FocusTx) deletedCount(q string, args ...any) (int, error) {
	res, err := t.x.exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("store: delete focus rows: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete focus rows: %w", err)
	}
	return int(n), nil
}

// PlanRows returns the rows of one period ordered by rank_position (then
// id), read inside the transaction.
func (t *FocusTx) PlanRows(periodID int64) ([]FocusSelection, error) {
	rows, err := t.x.query(`SELECT `+focusSelectionCols+` FROM focus_selection WHERE focus_period_id = ? ORDER BY rank_position, id`, periodID)
	if err != nil {
		return nil, fmt.Errorf("store: focus rows of period %d: %w", periodID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []FocusSelection
	for rows.Next() {
		r, err := scanFocusSelection(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan focus row of period %d: %w", periodID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: focus rows of period %d: %w", periodID, err)
	}
	return out, nil
}

// CountRows counts the focus_selection rows of one period. A focus_period
// row with no selection row counts zero (the first-lock test counts rows,
// not the period row).
func (t *FocusTx) CountRows(periodID int64) (int, error) {
	var n int
	if err := t.x.row(`SELECT COUNT(*) FROM focus_selection WHERE focus_period_id = ?`, periodID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count focus rows of period %d: %w", periodID, err)
	}
	return n, nil
}

// OtherPeriodsHoldRows reports whether any period other than periodID holds
// a focus_selection row.
func (t *FocusTx) OtherPeriodsHoldRows(periodID int64) (bool, error) {
	var n int
	if err := t.x.row(`SELECT COUNT(*) FROM focus_selection WHERE focus_period_id <> ?`, periodID).Scan(&n); err != nil {
		return false, fmt.Errorf("store: other focus periods rows: %w", err)
	}
	return n > 0, nil
}

// MaxOtherPeriodKey returns the greatest period_key among the periods other
// than periodID that hold rows, empty when none does.
func (t *FocusTx) MaxOtherPeriodKey(periodID int64) (string, error) {
	var v sql.NullString
	err := t.x.row(
		`SELECT MAX(p.period_key) FROM focus_period p
		  WHERE p.id <> ? AND EXISTS (SELECT 1 FROM focus_selection s WHERE s.focus_period_id = p.id)`,
		periodID,
	).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("store: max other focus period key: %w", err)
	}
	return v.String, nil
}

// MaxRankPosition returns the greatest rank_position of the period's rows,
// 0 when it holds none.
func (t *FocusTx) MaxRankPosition(periodID int64) (int, error) {
	var v sql.NullInt64
	if err := t.x.row(`SELECT MAX(rank_position) FROM focus_selection WHERE focus_period_id = ?`, periodID).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: max focus rank position of period %d: %w", periodID, err)
	}
	return int(v.Int64), nil
}

// AnnotationValue is FocusAnnotationValue read inside the transaction (the
// committed table plus this transaction's own writes).
func (t *FocusTx) AnnotationValue(repo, entityType, entityID string) (string, error) {
	return focusAnnotationValue(t.x.row, repo, entityType, entityID)
}

// ReadDraft returns the stored draft read inside the transaction, or
// found=false.
func (t *FocusTx) ReadDraft() (FocusDraft, bool, error) {
	d, err := scanFocusDraft(t.x.row(`SELECT ` + focusDraftCols + ` FROM focus_draft WHERE id = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return FocusDraft{}, false, nil
	}
	if err != nil {
		return FocusDraft{}, false, fmt.Errorf("store: read focus draft: %w", err)
	}
	return d, true, nil
}

// DeleteDraftAtOrBelow deletes the stored draft when its period type is
// periodType and its period key is at or below key (the applicable draft and
// any stale earlier one), reporting whether a draft was deleted.
func (t *FocusTx) DeleteDraftAtOrBelow(periodType, key string) (bool, error) {
	k, err := NormalizeFocusPeriodKey(periodType, key)
	if err != nil {
		return false, err
	}
	return t.deleted(`DELETE FROM focus_draft WHERE period_type = ? AND period_key <= ?`, periodType, k)
}

// DeleteDraftOfPeriod deletes exactly that period's draft (for close),
// reporting whether one was deleted.
func (t *FocusTx) DeleteDraftOfPeriod(periodType, periodKey string) error {
	k, err := NormalizeFocusPeriodKey(periodType, periodKey)
	if err != nil {
		return err
	}
	_, err = t.deleted(`DELETE FROM focus_draft WHERE period_type = ? AND period_key = ?`, periodType, k)
	return err
}

// InsertRun inserts a focus_run row inside the transaction and returns its id.
func (t *FocusTx) InsertRun(r FocusRun) (int64, error) {
	return insertRun(t.x, r)
}

// FinalizeRun sets the exit_code and counts_json of the run runID inside the
// transaction.
func (t *FocusTx) FinalizeRun(runID string, exitCode int, countsJSON string) error {
	return finalizeRun(t.x, runID, exitCode, countsJSON)
}
