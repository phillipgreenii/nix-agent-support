CREATE TABLE sessions (
    name             TEXT PRIMARY KEY,
    uuid             TEXT UNIQUE,
    cwd              TEXT NOT NULL DEFAULT '',
    transcript_path  TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL,
    generation       INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    last_activity_at INTEGER NOT NULL,
    tmux_session     TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    flags            TEXT NOT NULL DEFAULT ''
);
