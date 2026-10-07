CREATE TABLE entity (
    repo         TEXT NOT NULL,
    entity_type  TEXT NOT NULL,
    entity_id    TEXT NOT NULL,
    facts        TEXT NOT NULL,
    as_of        TEXT NOT NULL,
    stale        INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL,
    head_sha     TEXT,
    PRIMARY KEY (repo, entity_type, entity_id)
);

CREATE TABLE interpretation (
    repo             TEXT NOT NULL,
    entity_type      TEXT NOT NULL,
    entity_id        TEXT NOT NULL,
    ownership        TEXT,
    enrichment       TEXT,
    urgency          TEXT,
    category         TEXT,
    dispositions     TEXT,
    approvals        TEXT,
    gate_state       TEXT,
    match_reasons    TEXT,
    panel            TEXT,
    ready_to_promote INTEGER NOT NULL DEFAULT 0,
    degraded         INTEGER NOT NULL DEFAULT 0,
    sync_error       TEXT,
    as_of            TEXT NOT NULL,
    PRIMARY KEY (repo, entity_type, entity_id)
);

CREATE TABLE xref (
    repo           TEXT NOT NULL,
    from_type      TEXT NOT NULL,
    from_id        TEXT NOT NULL,
    to_type        TEXT NOT NULL,
    to_id          TEXT NOT NULL,
    evidence       TEXT,
    first_seen     TEXT NOT NULL,
    last_confirmed TEXT NOT NULL,
    PRIMARY KEY (repo, from_type, from_id, to_type, to_id)
);

CREATE TABLE annotation (
    repo          TEXT NOT NULL,
    entity_type   TEXT NOT NULL,
    entity_id     TEXT NOT NULL,
    comment_id    TEXT NOT NULL DEFAULT '',
    hidden        INTEGER,
    hidden_reason TEXT,
    wip           INTEGER,
    disposition   TEXT,
    set_by        TEXT NOT NULL,
    set_at        TEXT NOT NULL,
    PRIMARY KEY (repo, entity_type, entity_id, comment_id)
);

CREATE TABLE ledger (
    repo                     TEXT NOT NULL,
    entity_type              TEXT NOT NULL,
    entity_id                TEXT NOT NULL,
    kind                     TEXT NOT NULL,
    bead_id                  TEXT NOT NULL,
    last_synced_content_hash TEXT,
    last_synced_at           TEXT,
    last_reviewed_head_sha   TEXT,
    PRIMARY KEY (repo, entity_type, entity_id, kind)
);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
PRAGMA user_version = 1;
INSERT INTO meta (key, value) VALUES ('schema_version', '1');
