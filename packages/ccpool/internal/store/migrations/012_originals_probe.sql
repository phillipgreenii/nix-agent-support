-- The last "original transcript present" answer the reap sweep got from the
-- recorder for a session, so a later sweep over the same dead row can skip the
-- request. Only a positive answer is stored.
CREATE TABLE IF NOT EXISTS originals_probe (
	external_id     TEXT PRIMARY KEY,
	transcript_path TEXT NOT NULL,
	confirmed_at    INTEGER NOT NULL
);
