-- One row per session RUN (a launch or a resume). ended_at is NULL while the
-- run is open. end_reason doubles as the PENDING reason while the run is open
-- (written before /exit by a ccpool-initiated close; '' when none). end_source
-- is close | hook | reaper once ended. session_id is sessions.id; the live
-- store sets no foreign_keys pragma, so Store.Delete removes runs explicitly.
CREATE TABLE session_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  INTEGER NOT NULL,
    started_at  INTEGER NOT NULL,
    ended_at    INTEGER,
    end_reason  TEXT NOT NULL DEFAULT '',
    end_source  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_session_runs_session ON session_runs(session_id);
