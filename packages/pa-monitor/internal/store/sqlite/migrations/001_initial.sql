-- pa-monitor schema, migration 001 (initial).
-- All timestamps stored as TEXT in RFC3339 UTC for human readability.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE,
    pid INTEGER,
    command_hash TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    entrypoint TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    terminal_host TEXT NOT NULL DEFAULT '',
    branch TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    first_prompt TEXT NOT NULL DEFAULT '',
    labels TEXT NOT NULL DEFAULT '{}',
    transcript_mtime TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL DEFAULT '',
    context_tokens INTEGER NOT NULL DEFAULT 0,
    session_tokens INTEGER NOT NULL DEFAULT 0,
    subagent_count INTEGER NOT NULL DEFAULT 0,
    subshell_count INTEGER NOT NULL DEFAULT 0,
    burn_rate_short REAL NOT NULL DEFAULT 0,
    burn_rate_long REAL NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    awaiting_input INTEGER NOT NULL DEFAULT 0,
    last_error_kind TEXT NOT NULL DEFAULT '',
    last_error_text TEXT NOT NULL DEFAULT '',
    last_error_at TEXT NOT NULL DEFAULT '',
    last_error_terminal INTEGER NOT NULL DEFAULT 0,
    last_error_retryable INTEGER NOT NULL DEFAULT 0,
    last_processed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE INDEX idx_sessions_session_id ON sessions(session_id);
CREATE INDEX idx_sessions_freshness ON sessions(deleted_at, last_processed_at);

CREATE TABLE IF NOT EXISTS blocks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    block_id TEXT NOT NULL UNIQUE,
    started_at TEXT NOT NULL,
    ended_at TEXT NOT NULL,
    plan_cap_usd REAL NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    rate_limit_resets_at TEXT,
    cap_hit_at TEXT,
    last_processed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE INDEX idx_blocks_active ON blocks(deleted_at, started_at, ended_at);

CREATE TABLE IF NOT EXISTS weeks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id TEXT NOT NULL UNIQUE,
    started_at TEXT NOT NULL,
    ended_at TEXT NOT NULL,
    week_cap_usd REAL NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    cap_hit_at TEXT,
    last_processed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE INDEX idx_weeks_active ON weeks(deleted_at, started_at, ended_at);

CREATE TABLE IF NOT EXISTS session_block_contributions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    block_id INTEGER NOT NULL REFERENCES blocks(id) ON DELETE CASCADE,
    cost_usd REAL NOT NULL DEFAULT 0,
    tokens INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    UNIQUE(session_id, block_id)
);

CREATE INDEX idx_sbc_block ON session_block_contributions(block_id);

CREATE TABLE IF NOT EXISTS session_week_contributions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    cost_usd REAL NOT NULL DEFAULT 0,
    tokens INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    UNIQUE(session_id, week_id)
);

CREATE INDEX idx_swc_week ON session_week_contributions(week_id);

CREATE TABLE IF NOT EXISTS system_toggles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    value INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE TABLE IF NOT EXISTS nudge_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    result TEXT NOT NULL,
    error_text TEXT NOT NULL DEFAULT '',
    caused_by_error_at TEXT,
    escalated INTEGER NOT NULL DEFAULT 0,
    fired_at TEXT NOT NULL
);

CREATE INDEX idx_nudge_history_session_fired ON nudge_history(session_id, fired_at DESC);

CREATE TABLE IF NOT EXISTS nudge_history_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nudge_history_id INTEGER NOT NULL REFERENCES nudge_history(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    UNIQUE(nudge_history_id, source)
);

CREATE INDEX idx_nhs_source ON nudge_history_sources(source, nudge_history_id);
