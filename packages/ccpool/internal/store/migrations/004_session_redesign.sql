-- ADR 0015: ccpool tracks session FACTS, not work judgments.
-- Drop & recreate sessions with a surrogate id PK, a unique external_id (the
-- caller's handle), a unique claude_session_id (the Claude session UUID used to
-- resume), and a nullable, non-unique display name. turns is re-keyed by
-- external_id. No production data exists, so a destructive recreate is safe.
DROP TABLE IF EXISTS turns;
DROP TABLE IF EXISTS sessions;

CREATE TABLE sessions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    external_id       TEXT NOT NULL UNIQUE,
    claude_session_id TEXT UNIQUE,
    name              TEXT,
    cwd               TEXT NOT NULL DEFAULT '',
    transcript_path   TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL,
    generation        INTEGER NOT NULL DEFAULT 1,
    created_at        INTEGER NOT NULL,
    last_activity_at  INTEGER NOT NULL,
    tmux_session      TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    flags             TEXT NOT NULL DEFAULT '',
    pending_question  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE turns (
    turn_id         TEXT PRIMARY KEY,
    external_id     TEXT NOT NULL,
    prompt          TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    transcript_path TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    resolved_at     INTEGER
);
CREATE INDEX turns_external_id_pending ON turns(external_id, created_at) WHERE status = 'pending';
