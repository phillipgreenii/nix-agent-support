CREATE TABLE turns (
    turn_id         TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    prompt          TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',   -- 'pending' | 'resolved'
    transcript_path TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    resolved_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX turns_name_pending ON turns(name, created_at) WHERE status = 'pending';
