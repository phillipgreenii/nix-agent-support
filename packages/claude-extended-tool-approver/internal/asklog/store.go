package asklog

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	// sandboxEnabled, when non-nil, is recorded on every new tool_decisions
	// row inserted by this store. nil means "do not set the column"
	// (column will be NULL), which is the appropriate default for tests
	// and for callers that have not opted in to sandbox telemetry.
	sandboxEnabled *bool
}

// SetSandboxEnabled records whether Claude Code's bash sandbox is enabled
// for this hook invocation. The flag is stamped on every subsequent insert.
// Pass-through (nil) callers — including all existing tests — get NULL in
// the new sandbox_enabled column, which means "unknown".
func (s *Store) SetSandboxEnabled(enabled bool) {
	s.sandboxEnabled = &enabled
}

// sandboxEnabledArg returns the value to bind to the sandbox_enabled column
// for an insert: either an *int (0/1) or nil for SQL NULL.
func (s *Store) sandboxEnabledArg() interface{} {
	if s.sandboxEnabled == nil {
		return nil
	}
	if *s.sandboxEnabled {
		one := 1
		return one
	}
	zero := 0
	return zero
}

func DefaultDBPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	newPath := filepath.Join(dataHome, "claude-extended-tool-approver", "asks.db")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		oldPath := filepath.Join(dataHome, "claude-pretool-hook", "asks.db")
		if _, err := os.Stat(oldPath); err == nil {
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: DB not found at %s but exists at old path %s — please copy it\n", newPath, oldPath)
		}
	}
	return newPath
}

func NewStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=3000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

// DecisionRow represents a row from tool_decisions for evaluation.
type DecisionRow struct {
	ID             int
	SessionID      string
	CWD            string
	ToolName       string
	ToolInputJSON  string
	ToolSummary    string
	HookDecision   *string
	Outcome        string
	Excluded       int
	CorrectDec     *string
	SandboxEnabled sql.NullInt64
}

// QueryRows returns non-excluded decision rows, optionally filtered by date.
func (s *Store) QueryRows(sinceDate string) ([]DecisionRow, error) {
	query := `SELECT id, session_id, cwd, tool_name, tool_input_json,
		COALESCE(tool_summary, ''), hook_decision, outcome, excluded, correct_hook_decision,
		sandbox_enabled
		FROM tool_decisions WHERE excluded = 0`
	args := []interface{}{}
	if sinceDate != "" {
		query += " AND created_at >= ?"
		args = append(args, sinceDate)
	}
	query += " ORDER BY id"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DecisionRow
	for rows.Next() {
		var r DecisionRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.CWD, &r.ToolName,
			&r.ToolInputJSON, &r.ToolSummary, &r.HookDecision, &r.Outcome, &r.Excluded, &r.CorrectDec,
			&r.SandboxEnabled); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ShowRow represents a full row from tool_decisions for the show subcommand.
type ShowRow struct {
	ID                    int
	SessionID             string
	CWD                   string
	ToolName              string
	ToolInputJSON         string
	ToolSummary           string
	HookDecision          string
	HookReason            string
	Outcome               string
	Excluded              int
	ExcludedReason        string
	CorrectDec            string
	CorrectDecExplanation string
	CreatedAt             string
	SandboxEnabled        sql.NullInt64
}

// QueryRowsByIDs returns full row data for the given IDs (including excluded rows).
func (s *Store) QueryRowsByIDs(ids []int) ([]ShowRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := `SELECT id, session_id, cwd, tool_name, tool_input_json,
		COALESCE(tool_summary, ''), COALESCE(hook_decision, ''), COALESCE(hook_reason, ''),
		outcome, excluded, COALESCE(excluded_reason, ''),
		COALESCE(correct_hook_decision, ''), COALESCE(correct_hook_decision_explanation, ''),
		created_at, sandbox_enabled
		FROM tool_decisions WHERE id IN (`

	args := make([]interface{}, len(ids))
	for i, id := range ids {
		if i > 0 {
			query += ","
		}
		query += "?"
		args[i] = id
	}
	query += ") ORDER BY id"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ShowRow
	for rows.Next() {
		var r ShowRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.CWD, &r.ToolName, &r.ToolInputJSON,
			&r.ToolSummary, &r.HookDecision, &r.HookReason,
			&r.Outcome, &r.Excluded, &r.ExcludedReason,
			&r.CorrectDec, &r.CorrectDecExplanation,
			&r.CreatedAt, &r.SandboxEnabled); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// MarkExcluded sets the excluded flag on the given row IDs.
func (s *Store) MarkExcluded(ids []int, reason string) error {
	for _, id := range ids {
		_, err := s.db.Exec(
			`UPDATE tool_decisions SET excluded = 1, excluded_reason = ? WHERE id = ?`,
			reason, id)
		if err != nil {
			return fmt.Errorf("mark-excluded id=%d: %w", id, err)
		}
	}
	return nil
}

// SetCorrectDecision sets the correct_hook_decision on the given row IDs.
func (s *Store) SetCorrectDecision(ids []int, decision, explanation string) error {
	for _, id := range ids {
		_, err := s.db.Exec(
			`UPDATE tool_decisions SET correct_hook_decision = ?, correct_hook_decision_explanation = ? WHERE id = ?`,
			decision, explanation, id)
		if err != nil {
			return fmt.Errorf("set-correct-decision id=%d: %w", id, err)
		}
	}
	return nil
}

// TraceRow represents a row from decision_trace_entries.
type TraceRow struct {
	RuleOrder int
	RuleName  string
	Decision  string
	Reason    string
}

// QueryTraceByDecisionID returns all trace entries for a given tool_decision id, ordered by rule_order.
func (s *Store) QueryTraceByDecisionID(decisionID int) ([]TraceRow, error) {
	rows, err := s.db.Query(`
		SELECT rule_order, rule_name, decision, reason
		FROM decision_trace_entries
		WHERE tool_decision_id = ?
		ORDER BY rule_order`, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TraceRow
	for rows.Next() {
		var r TraceRow
		if err := rows.Scan(&r.RuleOrder, &r.RuleName, &r.Decision, &r.Reason); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

type migration struct {
	version int
	up      func(tx *sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
			CREATE TABLE IF NOT EXISTS tool_decisions (
				id                     INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id             TEXT NOT NULL,
				cwd                    TEXT NOT NULL,
				agent_id               TEXT,
				agent_type             TEXT,
				tool_name              TEXT NOT NULL,
				tool_use_id            TEXT,
				tool_input_hash        TEXT NOT NULL,
				tool_input_json        TEXT NOT NULL,
				tool_summary           TEXT,
				hook_decision          TEXT,
				hook_reason            TEXT,
				permission_suggestions TEXT,
				outcome                TEXT NOT NULL DEFAULT 'pending',
				outcome_notes          TEXT,
				created_at             TEXT NOT NULL,
				resolved_at            TEXT
			);

			CREATE INDEX IF NOT EXISTS idx_tool_decisions_correlation
				ON tool_decisions(session_id, tool_name, tool_input_hash, outcome);

			CREATE INDEX IF NOT EXISTS idx_tool_decisions_tool_use_id
				ON tool_decisions(tool_use_id) WHERE tool_use_id IS NOT NULL;

			CREATE INDEX IF NOT EXISTS idx_tool_decisions_pending
				ON tool_decisions(session_id, outcome) WHERE outcome = 'pending';
			`)
			return err
		},
	},
	{
		version: 2,
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
			ALTER TABLE tool_decisions ADD COLUMN excluded INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE tool_decisions ADD COLUMN excluded_reason TEXT;
			ALTER TABLE tool_decisions ADD COLUMN correct_hook_decision TEXT;
			ALTER TABLE tool_decisions ADD COLUMN correct_hook_decision_explanation TEXT;

			CREATE INDEX IF NOT EXISTS idx_tool_decisions_evaluation
				ON tool_decisions(excluded) WHERE excluded = 0;
			`)
			return err
		},
	},
	{
		version: 3,
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
			CREATE TABLE decision_trace_entries (
				id                 INTEGER PRIMARY KEY AUTOINCREMENT,
				tool_decision_id   INTEGER NOT NULL REFERENCES tool_decisions(id) ON DELETE CASCADE,
				rule_order         INTEGER NOT NULL,
				rule_name          TEXT NOT NULL,
				decision           TEXT NOT NULL,
				reason             TEXT NOT NULL DEFAULT ''
			);

			CREATE INDEX idx_trace_tool_decision ON decision_trace_entries(tool_decision_id);
			CREATE INDEX idx_trace_rule_name ON decision_trace_entries(rule_name);
			`)
			return err
		},
	},
	{
		version: 4,
		up: func(tx *sql.Tx) error {
			// sandbox_enabled is nullable: NULL means "unknown" for rows
			// written before sandbox telemetry existed, or by callers that
			// have not opted in. 0/1 reflect the configured state of
			// Claude Code's bash sandbox at insert time.
			_, err := tx.Exec(`
			ALTER TABLE tool_decisions ADD COLUMN sandbox_enabled INTEGER;
			`)
			return err
		},
	},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var currentVersion int
	row := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version")
	if err := row.Scan(&currentVersion); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", m.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record version %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}
	return nil
}
