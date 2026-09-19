-- pg2-24f89/pg2-qye99: session-labels mechanism (store.MarkAsLabel/store.Labels)
-- flags an existing session_metadata key as label-eligible — low-cardinality
-- metadata (e.g. role, pool) safe to surface as an OTel/metrics attribute. One
-- boolean column on the existing table; no new table.
ALTER TABLE session_metadata ADD COLUMN is_label INTEGER NOT NULL DEFAULT 0;
