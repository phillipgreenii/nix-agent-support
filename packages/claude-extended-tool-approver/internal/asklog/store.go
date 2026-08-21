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

// synchronousPragma, when non-empty, is applied as `PRAGMA synchronous=<value>`
// immediately after open — before the WAL conversion and before migrate, so it
// governs every fsync those steps would otherwise perform.
//
// Tests across the MODULE set it to "OFF" via SetSynchronousForTests. Why that
// seam exists — this is a real, reproducible environment interaction, not a
// micro-optimisation:
//
//   - Creating a store costs 11 fsyncs (17 before migrate was made atomic): the
//     journal_mode=WAL conversion, the schema_version create, the migration
//     commit, the first insert, and the checkpoint on Close. Counted, not
//     estimated: `strace -c -e trace=fsync` on one hook invocation.
//   - Every test builds a throwaway DB under t.TempDir(), so it pays that in full.
//   - fsync latency is a property of the HOST FILESYSTEM, and it varies by orders
//     of magnitude. Measured on the Linux dev host for this repo: ~50ms per fsync
//     on the ext4 root (which backs /tmp, and therefore t.TempDir()), versus
//     ~0.8us on tmpfs — a ~60,000x spread. Re-measured 2026-08-12 on the loaded
//     QEMU VM that builds monorepod, with `fsync()` on a 23-byte write: 1.1s to
//     3.6s. A SINGLE asklog.NewStore there costs 5-16 SECONDS of pure I/O wait.
//   - Result on such a host: the 73-test asklog suite took 2m10s wall for 0.9s of
//     CPU, and the 47-test cmd suite took 572s and tripped `go test`'s 10m default
//     timeout, failing the whole nixosConfigurations.monorepod build (tc-fqu7).
//     It was not hung and not deadlocked; it was ~100% fsync wait.
//
// THE POINT OF THE SEAM is not that the suite is slow, it is that without it the
// suite's wall clock is a MULTIPLE OF AN UNBOUNDED HOST PROPERTY. No -timeout can
// be chosen that a slower disk cannot blow through, so the fix has to remove the
// fsyncs from the tests rather than budget for them.
//
// Durability is meaningless for a database deleted when the test exits, so tests
// opt out of it. Anything asserting on-disk crash behaviour must set this back to
// "" for the duration of that test.
//
// Production leaves it EMPTY, so SQLite keeps its default (FULL) and every commit
// to the ask log is durably flushed. Nothing outside a _test.go file may set it;
// TestNoProductionCodeDisablesDurability enforces that mechanically.
var synchronousPragma string

// SetSynchronousForTests points every store opened AFTERWARDS at
// `PRAGMA synchronous=<value>`, and returns the previous value so a caller can
// restore it. Passing "" restores SQLite's shipped default (FULL).
//
// This exists because the seam it fronts has to reach further than the package
// that owns it: the throwaway databases that dominate the suite's wall clock are
// created in package main's test helpers (cmd_show_test.go, cmd_evaluate_test.go),
// which cannot touch an unexported var. asklog is under internal/, so exporting it
// widens the seam to THIS MODULE and no further.
//
// It is deliberately NOT an environment variable or a flag. A knob readable at
// runtime by the binary that decides whether a command may run is exactly what
// was rejected for the input-processor deadline (16e1fd4d, pg2-iay90), and the
// reasoning carries: at runtime a test-only env var is indistinguishable from a
// production one, only undocumented. A Go function in an internal package has no
// runtime surface at all — the shipped binary cannot be talked into calling it.
func SetSynchronousForTests(value string) string {
	previous := synchronousPragma
	synchronousPragma = value
	return previous
}

func NewStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	// _txlock=immediate makes every db.Begin()/db.BeginTx() on connections
	// opened from this *sql.DB issue `BEGIN IMMEDIATE` instead of SQLite's
	// lazy default (`BEGIN DEFERRED`), which takes no lock at all until the
	// transaction's first write statement. migrate() below depends on this:
	// it reads schema_version's MAX(version) INSIDE its transaction, so the
	// BEGIN IMMEDIATE acquires SQLite's write lock before that read runs. A
	// second connection's own BEGIN IMMEDIATE then blocks (bounded by the
	// busy_timeout pragma set below) until this one commits or rolls back,
	// so it can only re-read MAX(version) once this transaction's
	// migrations, if any, are already visible — never the stale
	// pre-migration value. Without this, two independent connections could
	// both read the SAME pre-migration version before either takes a lock,
	// decide to run the same pending migrations, and double-apply them
	// (pg2-5lkyp: on this machine, every Bash tool call's hook opens a Store
	// and thus calls migrate(), so concurrent openers are the normal case).
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// busy_timeout MUST be set FIRST, before any other pragma or statement
	// on this connection: it governs how long SQLite retries a lock
	// acquisition (including the one the journal_mode=WAL conversion and
	// the synchronous pragma below need) instead of failing immediately
	// with SQLITE_BUSY. Setting it later left those two statements with
	// SQLite's default zero-retry budget, which a concurrent opener could
	// (and, once exercised by
	// TestMigrate_ConcurrentOpeners_NoDuplicateVersionRows, reliably did)
	// trip on a brand-new database file — a second bug on the same
	// concurrent-openers path as the migrate() race this pragma reordering
	// was found alongside (pg2-5lkyp).
	if _, err := db.Exec("PRAGMA busy_timeout=3000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if synchronousPragma != "" {
		if _, err := db.Exec("PRAGMA synchronous=" + synchronousPragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set synchronous: %w", err)
		}
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{db: db}, nil
}

// NewReadOnlyStore opens the asklog database for READ-ONLY access: it is for
// callers that only ever SELECT out of a database some other writer already
// created and migrated — today that's `evaluate`, `show`, `report`,
// `baseline`, and `compare`. It performs no write of any kind at open time:
// no os.MkdirAll (the directory must already exist — something else had to
// have created it in order to have written the rows being read), no
// PRAGMA journal_mode=WAL conversion, and no migrate(). Skipping migrate is
// deliberate, not an oversight: forcing a schema upgrade is a WRITE, and a
// read path silently DDL-ing the shared corpus just because it was built
// from a newer commit than the one that last wrote it is exactly the kind of
// accidental mutation this constructor exists to rule out. If the corpus's
// schema is behind what this binary's queries expect, the query fails
// loudly ("no such column") instead of silently upgrading the file.
//
// DSN: "file:<path>?immutable=1". The immutable query parameter tells SQLite
// the file will not change for the lifetime of this connection, which skips
// the usual WAL/SHM locking dance entirely — no probing for a -shm file, no
// attempt to create one if it is missing. That is load-bearing: plain
// mode=ro (equivalently, the sqlite3 CLI's -readonly) still touches
// -wal/-shm to check for and roll forward any committed-but-uncheckpointed
// WAL frames, and against this project's real corpus that probe fails
// outright with SQLITE_CANTOPEN (14) — see
// docs/engine-ab-replay-runbook.md's "Asklog read access" section, and
// pg2-cbihz, which reconfirmed it directly against the live corpus: a bare
// "mode=ro" (or "mode=ro" combined with "immutable=1") momentarily CREATED
// empty -wal/-shm sidecars that "immutable=1" alone never touched. So
// "mode=ro" is deliberately NOT added alongside "immutable=1" here, even
// though it would be redundant for blocking writes — it is not redundant
// for the sidecar-untouched property, which is the property this
// constructor is required to guarantee.
//
// Tradeoff, also documented in the runbook: an immutable connection cannot
// see rows still sitting in an unmerged WAL file, and racing a full-table
// scan against a live checkpoint can (rarely) surface SQLite's generic
// "database disk image is malformed" (11) — a torn-read artifact, not real
// corruption (`PRAGMA quick_check` on the same file reports ok). That is an
// acceptable batch-analysis tradeoff for what these subcommands are (manual,
// occasional replay/report runs, never a live path), and the runbook names
// the mitigation (read a snapshot copy) for anyone hitting it.
//
// os.Stat is checked first so a missing database produces a clear error
// instead of SQLite's less legible CANTOPEN.
func NewReadOnlyStore(dbPath string) (*Store, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("open db read-only: %w", err)
	}

	dsn := "file:" + dbPath + "?immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db read-only: %w", err)
	}

	// Ping forces the connection open now, so a bad DSN or an unopenable file
	// fails here — at construction — rather than silently on the first query.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open db read-only: %w", err)
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
	// Richer hook context (nullable; NULL on pre-v5 rows). PermissionMode,
	// AgentType, OutcomeNotes and ToolResponse are the four raw fields exposed
	// in `evaluate --format=json`; PromptID is read back only to discriminate
	// settings vs hook vs user in ApprovalSource.
	PermissionMode *string
	AgentType      *string
	OutcomeNotes   *string
	ToolResponse   *string
	PromptID       *string
}

// ApprovalSource derives the approval-MECHANISM bucket for a decision row from
// its raw context. The axis is {unknown,bypass,auto,settings,hook,user}. It is
// orthogonal to agent_type (subagent segmentation is a SEPARATE column) and
// classifies CONTEXT, not outcome — a denied/pending row still gets its bucket
// (e.g. an auto-mode denial derives to "auto"). Evaluated top-to-bottom:
//
//  1. permission_mode IS NULL -> "unknown" (all pre-migration rows)
//  2. "bypassPermissions"     -> "bypass"
//  3. "auto" / "dontAsk"      -> "auto"
//  4. no prompt (prompt_id absent): CETA returned Approve -> "hook";
//     otherwise the tool ran with no CETA approval and no prompt, i.e. the user
//     pre-authorized it in settings -> "settings"
//  5. prompt present -> "user"
//
// "acceptEdits" and "default"/"plan"/empty are NOT their own buckets — they
// fall through to steps 4/5 (acceptEdits does not auto-approve Bash). Historical
// rows have a NULL prompt_id, so a no-prompt row logged before prompt_id was
// persisted cannot be split from a prompted one — a documented limitation.
func ApprovalSource(permissionMode, promptID, hookDecision *string) string {
	if permissionMode == nil {
		return "unknown"
	}
	switch *permissionMode {
	case "bypassPermissions":
		return "bypass"
	case "auto", "dontAsk":
		return "auto"
	}
	if promptID != nil && *promptID != "" {
		return "user"
	}
	if hookDecision != nil && *hookDecision == "allow" {
		return "hook"
	}
	return "settings"
}

// QueryRows returns non-excluded decision rows, optionally filtered by date.
func (s *Store) QueryRows(sinceDate string) ([]DecisionRow, error) {
	query := `SELECT id, session_id, cwd, tool_name, tool_input_json,
		COALESCE(tool_summary, ''), hook_decision, outcome, excluded, correct_hook_decision,
		sandbox_enabled,
		permission_mode, agent_type, outcome_notes, tool_response, prompt_id
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
	defer func() { _ = rows.Close() }()

	var result []DecisionRow
	for rows.Next() {
		var r DecisionRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.CWD, &r.ToolName,
			&r.ToolInputJSON, &r.ToolSummary, &r.HookDecision, &r.Outcome, &r.Excluded, &r.CorrectDec,
			&r.SandboxEnabled,
			&r.PermissionMode, &r.AgentType, &r.OutcomeNotes, &r.ToolResponse, &r.PromptID); err != nil {
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
	defer func() { _ = rows.Close() }()

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
			reason, id,
		)
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
			decision, explanation, id,
		)
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
	defer func() { _ = rows.Close() }()

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
	{
		version: 5,
		up: func(tx *sql.Tx) error {
			// Richer hook context, all nullable for back-compat (NULL means
			// "unknown / logged before this field was captured"):
			//   permission_mode — raw permission mode string, stored VERBATIM
			//     (no normalization; unknown/future values survive). Feeds the
			//     approval_source derivation and primarycommit hardening.
			//   prompt_id       — identifies the triggering user prompt; its
			//     presence discriminates user vs settings/hook approval.
			//   tool_response   — PostToolUse result payload (raw JSON); lets
			//     downstream analysis detect a failed tool call (pg2-okd13.2).
			//   transcript_path — pointer to the session transcript file.
			// agent_type and outcome_notes already exist (v1), so they are not
			// re-added here — v5 only exposes them via SELECT.
			_, err := tx.Exec(`
			ALTER TABLE tool_decisions ADD COLUMN permission_mode TEXT;
			ALTER TABLE tool_decisions ADD COLUMN prompt_id TEXT;
			ALTER TABLE tool_decisions ADD COLUMN tool_response TEXT;
			ALTER TABLE tool_decisions ADD COLUMN transcript_path TEXT;
			`)
			return err
		},
	},
	{
		version: 6,
		up: func(tx *sql.Tx) error {
			// background_shells tracks agent-spawned background shells so the
			// KillShell rule can verify ownership (hook-support parity). A row is
			// inserted on PostToolUse of a `run_in_background` Bash call; the
			// killshell rule reads creator back on a KillShell PreToolUse. creator
			// is always 'agent' today (ceta only ever records shells IT saw the
			// agent spawn), but the column keeps the shape open for future
			// user/unknown classification.
			_, err := tx.Exec(`
			CREATE TABLE background_shells (
				shell_id    TEXT PRIMARY KEY,
				session_id  TEXT,
				creator     TEXT NOT NULL DEFAULT 'agent',
				created_at  TEXT NOT NULL
			);
			`)
			return err
		},
	},
	{
		version: 7,
		up: func(tx *sql.Tx) error {
			// Backfill the outcome split (see outcomes.go). Historically all three
			// of "the hook refused", "somebody declined" and "nobody ever resolved
			// it" were written as outcome='denied', so 'denied' carried three
			// meanings and a bulk SessionEnd sweep was indistinguishable from a
			// user saying no.
			//
			// This is NOT a heuristic — it inverts the three writers, each of which
			// leaves a unique, already-stored fingerprint:
			//
			//	RecordPreToolDecision (hook Reject) sets the outcome at INSERT time
			//	  together with hook_decision='deny'. Such a row is never 'pending',
			//	  so it can never have been swept -> hook_decision='deny' is
			//	  sufficient and exclusive.  => 'rejected'
			//	RecordPermissionDenied is the only writer that sets outcome_notes on
			//	  a denial (always prefixed 'auto_mode_classifier: '), and it only
			//	  ever updates a still-'pending' row, so it can never collide with
			//	  the above.  => stays 'denied'
			//	ResolveUnresolvedAll (formerly ResolveDeniedAll) sets neither
			//	  outcome_notes nor hook_decision.  => 'unresolved'
			//
			// Corroborated on the 11,435 'denied' rows of the author's corpus: all
			// 82 hook_decision='deny' rows have resolved_at - created_at <= 2s (an
			// INSERT-time resolution); all 80 outcome_notes rows resolve within an
			// hour of the call; and 10,616 of the remaining 11,273 share their
			// resolved_at with at least one sibling in the same session — the
			// signature of a one-statement bulk sweep.
			//
			// Idempotent: each statement's predicate requires outcome='denied', which
			// no longer holds for the rows it just rewrote. Reversible: the
			// predicates stay true after the rewrite, so the exact inverse UPDATE
			// restores the prior state.
			_, err := tx.Exec(`
			UPDATE tool_decisions
			   SET outcome = 'rejected'
			 WHERE outcome = 'denied' AND hook_decision = 'deny';

			UPDATE tool_decisions
			   SET outcome = 'unresolved'
			 WHERE outcome = 'denied'
			   AND outcome_notes IS NULL
			   AND (hook_decision IS NULL OR hook_decision <> 'deny');
			`)
			return err
		},
	},
	{
		version: 8,
		up: func(tx *sql.Tx) error {
			// rule_errors is the DURABLE half of ADR 0043's per-rule failure sink.
			//
			// The ADR requires genuine rule failures to be recorded per rule "so a
			// systematically-failing resolver is detectable", and explicitly refuses
			// to accept a stderr line as discharge of that. internal/metrics does the
			// counting, but the hook is ONE SHORT-LIVED PROCESS PER TOOL CALL, so an
			// in-process counter can never aggregate — "systematically" is only
			// observable across calls, which means across processes, which means on
			// disk.
			//
			// It deliberately does NOT reuse decision_trace_entries: those rows are
			// written only when tracing is enabled (RecordPreToolDecision keys on
			// result.Trace != nil), so a failure on an untraced call — i.e. almost
			// every real call — would leave no row at all.
			//
			// tool_decision_id is nullable and carries NO foreign key: a failure is
			// worth keeping even when the decision INSERT that would have anchored it
			// failed or was skipped, and losing the failure record because its anchor
			// is missing is the opposite of what this table is for.
			_, err := tx.Exec(`
			CREATE TABLE IF NOT EXISTS rule_errors (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				tool_decision_id INTEGER,
				session_id       TEXT,
				cwd              TEXT,
				tool_name        TEXT,
				rule_name        TEXT NOT NULL,
				error_count      INTEGER NOT NULL,
				error_sample     TEXT,
				created_at       TEXT NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_rule_errors_rule ON rule_errors(rule_name, created_at);
			`)
			return err
		},
	},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	// The version READ, the pending-migration DECISION, and every migration
	// APPLY all happen inside this ONE transaction — and because NewStore
	// opens the DSN with `_txlock=immediate`, db.Begin() issues `BEGIN
	// IMMEDIATE`, which takes SQLite's write lock right here, before the
	// SELECT below ever runs. That is what closes the race: a plain
	// read-then-Begin sequence lets a second connection read the SAME
	// pre-migration MAX(version) before either connection takes a lock, so
	// both independently decide to run (and apply) the same pending
	// migrations. With the lock taken first, a second connection's own
	// BEGIN IMMEDIATE blocks (bounded by the busy_timeout pragma) until this
	// transaction commits or rolls back, and its SELECT then observes
	// whatever this transaction just committed — so it can only ever see an
	// up-to-date version and compute an empty (or smaller) pending list.
	// See pg2-5lkyp; regression coverage in
	// TestMigrate_ConcurrentOpeners_NoDuplicateVersionRows.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}

	var currentVersion int
	row := tx.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version")
	if err := row.Scan(&currentVersion); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read schema version: %w", err)
	}

	pending := make([]migration, 0, len(migrations))
	for _, m := range migrations {
		if m.version > currentVersion {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		// Nothing to apply, but the transaction still holds the write lock
		// it took at BEGIN IMMEDIATE — release it without committing any
		// change.
		if err := tx.Rollback(); err != nil {
			return fmt.Errorf("release migration lock: %w", err)
		}
		return nil
	}

	// All pending migrations run in this ONE transaction, not one each.
	//
	// Correctness first: SQLite DDL is transactional, so a single transaction
	// makes migration ATOMIC — an interrupted upgrade leaves the database at
	// its old version, never stranded halfway with schema_version disagreeing
	// with the actual schema. Per-migration transactions made that half-migrated
	// state reachable and unrecoverable-in-place.
	//
	// It is also what makes creating a database cheap. Each commit is a durable
	// flush, and flush latency is a HOST property spanning orders of magnitude
	// (see synchronousPragma above), so the fsync COUNT is the only part this
	// code controls. Collapsing N commits into one takes a fresh-database open
	// from 17 fsyncs to 11 (measured with `strace -c -e trace=fsync`), and keeps
	// that number flat as migrations accumulate instead of growing one flush per
	// migration forever.
	for _, m := range pending {
		if err := m.up(tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", m.version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record version %d: %w", m.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
