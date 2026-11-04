-- Sizing fixture for the schema v3 proposal (docs/schema-v3-design.md).
--
-- Builds the current layout and the proposed layout from the same synthetic rows in ONE
-- database file, then prints the bytes of each object from the dbstat virtual table.
-- Everything is synthetic and deterministic (no real transcript content, names or paths).
-- The shape is calibrated to a measured live profile: about 1,460 records per session, 56
-- percent of records carrying the five per-session constants, 16 to 20 percent assistant lines
-- carrying nine usage rows each.
--
--   sqlite3 "$TMPDIR/sizing-v3.db" < scripts/sizing-schema-v3.sql
--
-- Change the two numbers in the params table to rescale (records, records per session).

CREATE TABLE params(n INTEGER, per_session INTEGER);
INSERT INTO params VALUES (300000, 1460);

-- Synthetic sessions: 36-character ids, a base cwd and branch from small pools, and a 1 in 5
-- chance that the session switches cwd and branch half way (the live rate is 1,637 of 7,898).
CREATE TABLE s(ref INTEGER PRIMARY KEY, txt TEXT, cwd1 INTEGER, cwd2 INTEGER, br1 INTEGER, br2 INTEGER);
WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM g WHERE i < (SELECT (n+per_session-1)/per_session FROM params))
INSERT INTO s SELECT i,
  printf('%08x-%04x-4%03x-a%03x-%012x', i*2654435761 % 4294967296, i*40503 % 65536, i*977 % 4096, i*131 % 4096, i*1103515245 % 281474976710656),
  1 + (i*7919 % 3000), CASE WHEN i % 5 = 0 THEN 1 + (i*104729 % 3000) ELSE 1 + (i*7919 % 3000) END,
  1 + (i*31 % 900), CASE WHEN i % 5 = 0 THEN 1 + (i*17 % 900) ELSE 1 + (i*31 % 900) END
FROM g;
-- Interned strings for the proposed layout (lengths match the live averages: cwd 44, branch 7).
CREATE TABLE lk_cwd(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE);
WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM g WHERE i < 3000)
INSERT INTO lk_cwd SELECT i, printf('/example/work/project-%04d/sub-%03d/checkout', i, i % 500) FROM g;
CREATE TABLE lk_branch(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE);
WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM g WHERE i < 900)
INSERT INTO lk_branch SELECT i, printf('feat/b-%d', i) FROM g;

-- Source rows, one per record. kind: 0 plain line, 1 assistant line (usage), others no constants.
CREATE TABLE src AS
WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM g WHERE i < (SELECT n FROM params))
SELECT i AS id,
  1 + (i-1) / (SELECT per_session FROM params) AS sref,
  1 + (i-1) / (SELECT per_session FROM params) AS file_id,
  i*173 AS off,
  CASE WHEN i % 100 < 31 THEN 'attachment' WHEN i % 100 < 51 THEN 'assistant' WHEN i % 100 < 62 THEN 'user'
       WHEN i % 100 < 72 THEN 'tool_use' WHEN i % 100 < 82 THEN 'tool_result' WHEN i % 100 < 88 THEN 'thinking'
       WHEN i % 100 < 92 THEN 'text' ELSE 'last-prompt' END AS type,
  CASE WHEN i % 200 = 0 THEN 'claude-status' ELSE 'claude-transcript' END AS source,
  1761768000000000000 + i*1500000000 AS time_ns,
  (i % 1460) AS pos
FROM g;

-- Current layout.
CREATE TABLE records_cur(id INTEGER PRIMARY KEY AUTOINCREMENT, file_id INTEGER NOT NULL, gen INTEGER NOT NULL,
  off INTEGER NOT NULL, item INTEGER NOT NULL, len INTEGER NOT NULL, source TEXT NOT NULL, type TEXT NOT NULL,
  parser_version INTEGER NOT NULL, session TEXT NOT NULL DEFAULT '', time_ns INTEGER NOT NULL DEFAULT 0);
INSERT INTO records_cur SELECT src.id, file_id, 1, off, -1, 400 + (id*37 % 3000), source, type, 1, s.txt, time_ns
  FROM src JOIN s ON s.ref = src.sref;
CREATE TABLE fields_cur(rec_id INTEGER NOT NULL, name_id INTEGER NOT NULL, s TEXT, n INTEGER, f REAL,
  PRIMARY KEY(rec_id, name_id)) WITHOUT ROWID;
-- The five per-session constants on every non-assistant... line that carries them: 56 percent of records.
INSERT INTO fields_cur SELECT src.id, 11, l.v,  NULL, NULL FROM src JOIN s ON s.ref=src.sref
  JOIN lk_cwd l ON l.id = CASE WHEN src.pos < 730 THEN s.cwd1 ELSE s.cwd2 END WHERE src.id % 100 < 56;
INSERT INTO fields_cur SELECT src.id, 13, l.v,  NULL, NULL FROM src JOIN s ON s.ref=src.sref
  JOIN lk_branch l ON l.id = CASE WHEN src.pos < 730 THEN s.br1 ELSE s.br2 END WHERE src.id % 100 < 56;
INSERT INTO fields_cur SELECT id, 14, '2.1.' || (50 + sref % 9), NULL, NULL FROM src WHERE id % 100 < 56;
INSERT INTO fields_cur SELECT id, 15, 'cli', NULL, NULL FROM src WHERE id % 100 < 56;
INSERT INTO fields_cur SELECT id, 16, 'external', NULL, NULL FROM src WHERE id % 100 < 56;
-- Nine usage rows per assistant line (six counters, three small strings). Assistant lines
-- are 20 percent here, a little above the live 16 percent, so the saving is per assistant line.
INSERT INTO fields_cur SELECT id, 25, NULL, 1 + id % 40, NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 26, NULL, 20 + (id*13 % 4000), NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 27, NULL, (id*97 % 30000), NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 28, NULL, (id*7919 % 190000), NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 29, NULL, CASE WHEN id % 3 = 0 THEN (id*97 % 30000) ELSE 0 END, NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 30, NULL, CASE WHEN id % 3 = 0 THEN 0 ELSE (id*97 % 30000) END, NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 31, 'standard', NULL, NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 32, 'not_available', NULL, NULL FROM src WHERE type='assistant';
INSERT INTO fields_cur SELECT id, 33, 'standard', NULL, NULL FROM src WHERE type='assistant';
CREATE INDEX fields_cur_name_n ON fields_cur(name_id, n) WHERE n IS NOT NULL;
CREATE UNIQUE INDEX records_cur_ident ON records_cur(file_id, gen, off, item);
CREATE INDEX records_cur_session ON records_cur(session);
CREATE INDEX records_cur_time ON records_cur(time_ns);
CREATE INDEX records_cur_type ON records_cur(type);
CREATE INDEX records_cur_source_type ON records_cur(source, type);

-- Proposed layout: session_ref and the interned cwd, branch, version, entrypoint, user_type ids
-- on records; the nine usage values in one STRICT one-to-one extension table.
CREATE TABLE lk_version(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE);
INSERT INTO lk_version SELECT sref % 9 + 1, '2.1.' || (50 + sref % 9) FROM src GROUP BY sref % 9;
CREATE TABLE lk_small(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE);
INSERT INTO lk_small VALUES (1,'cli'),(2,'external'),(3,'standard'),(4,'not_available');
CREATE TABLE records_v3(id INTEGER PRIMARY KEY AUTOINCREMENT, file_id INTEGER NOT NULL, gen INTEGER NOT NULL,
  off INTEGER NOT NULL, item INTEGER NOT NULL, len INTEGER NOT NULL, source TEXT NOT NULL, type TEXT NOT NULL,
  parser_version INTEGER NOT NULL, session_ref INTEGER NOT NULL DEFAULT 0, time_ns INTEGER NOT NULL DEFAULT 0,
  cwd_id INTEGER, branch_id INTEGER, version_id INTEGER, entry_id INTEGER, user_type_id INTEGER) STRICT;
INSERT INTO records_v3 SELECT src.id, file_id, 1, off, -1, 400 + (id*37 % 3000), source, type, 1, sref, time_ns,
  CASE WHEN id % 100 < 56 THEN CASE WHEN pos < 730 THEN s.cwd1 ELSE s.cwd2 END END,
  CASE WHEN id % 100 < 56 THEN CASE WHEN pos < 730 THEN s.br1 ELSE s.br2 END END,
  CASE WHEN id % 100 < 56 THEN sref % 9 + 1 END, CASE WHEN id % 100 < 56 THEN 1 END, CASE WHEN id % 100 < 56 THEN 2 END
  FROM src JOIN s ON s.ref = src.sref;
CREATE TABLE ext_assistant_usage(rec_id INTEGER PRIMARY KEY, input_tokens INTEGER, output_tokens INTEGER,
  cache_creation_input_tokens INTEGER, cache_read_input_tokens INTEGER, cache_1h_input_tokens INTEGER,
  cache_5m_input_tokens INTEGER, service_tier_id INTEGER, inference_geo_id INTEGER, speed_id INTEGER) STRICT;
INSERT INTO ext_assistant_usage SELECT id, 1 + id % 40, 20 + (id*13 % 4000), (id*97 % 30000), (id*7919 % 190000),
  CASE WHEN id % 3 = 0 THEN (id*97 % 30000) ELSE 0 END, CASE WHEN id % 3 = 0 THEN 0 ELSE (id*97 % 30000) END, 3, 4, 3
  FROM src WHERE type='assistant';
CREATE UNIQUE INDEX records_v3_ident ON records_v3(file_id, gen, off, item);
CREATE INDEX records_v3_session ON records_v3(session_ref);
CREATE INDEX records_v3_time ON records_v3(time_ns);
CREATE INDEX records_v3_type ON records_v3(type);
CREATE INDEX records_v3_source_type ON records_v3(source, type);
-- Optional filter indexes on the new columns (partial: only records that carry the value).
CREATE INDEX records_v3_cwd ON records_v3(cwd_id) WHERE cwd_id IS NOT NULL;
CREATE INDEX records_v3_branch ON records_v3(branch_id) WHERE branch_id IS NOT NULL;
-- Attribution variant: ONLY session TEXT replaced by session_ref (nothing else changed).
CREATE TABLE records_s(id INTEGER PRIMARY KEY AUTOINCREMENT, file_id INTEGER NOT NULL, gen INTEGER NOT NULL,
  off INTEGER NOT NULL, item INTEGER NOT NULL, len INTEGER NOT NULL, source TEXT NOT NULL, type TEXT NOT NULL,
  parser_version INTEGER NOT NULL, session_ref INTEGER NOT NULL DEFAULT 0, time_ns INTEGER NOT NULL DEFAULT 0);
INSERT INTO records_s SELECT src.id, file_id, 1, off, -1, 400 + (id*37 % 3000), source, type, 1, sref, time_ns FROM src;
CREATE INDEX records_s_session ON records_s(session_ref);
-- Attribution variant: session_ref plus source and type as interned small ids (lk_source, lk_type).
CREATE TABLE records_i(id INTEGER PRIMARY KEY AUTOINCREMENT, file_id INTEGER NOT NULL, gen INTEGER NOT NULL,
  off INTEGER NOT NULL, item INTEGER NOT NULL, len INTEGER NOT NULL, source_id INTEGER NOT NULL, type_id INTEGER NOT NULL,
  parser_version INTEGER NOT NULL, session_ref INTEGER NOT NULL DEFAULT 0, time_ns INTEGER NOT NULL DEFAULT 0);
INSERT INTO records_i SELECT src.id, file_id, 1, off, -1, 400 + (id*37 % 3000),
  CASE source WHEN 'claude-status' THEN 2 ELSE 1 END,
  CASE type WHEN 'attachment' THEN 1 WHEN 'assistant' THEN 2 WHEN 'user' THEN 3 WHEN 'tool_use' THEN 4 WHEN 'tool_result' THEN 5
    WHEN 'thinking' THEN 6 WHEN 'text' THEN 7 ELSE 8 END, 1, sref, time_ns FROM src;
CREATE INDEX records_i_session ON records_i(session_ref);
CREATE INDEX records_i_time ON records_i(time_ns);
CREATE INDEX records_i_type ON records_i(type_id);
CREATE INDEX records_i_source_type ON records_i(source_id, type_id);
CREATE UNIQUE INDEX records_i_ident ON records_i(file_id, gen, off, item);
-- Attribution variants of the current fields: the constants only, and the usage rows only.
CREATE TABLE fields_k(rec_id INTEGER NOT NULL, name_id INTEGER NOT NULL, s TEXT, n INTEGER, f REAL,
  PRIMARY KEY(rec_id, name_id)) WITHOUT ROWID;
INSERT INTO fields_k SELECT * FROM fields_cur WHERE name_id BETWEEN 11 AND 16;
CREATE TABLE fields_u(rec_id INTEGER NOT NULL, name_id INTEGER NOT NULL, s TEXT, n INTEGER, f REAL,
  PRIMARY KEY(rec_id, name_id)) WITHOUT ROWID;
INSERT INTO fields_u SELECT * FROM fields_cur WHERE name_id BETWEEN 25 AND 33;
CREATE INDEX fields_u_name_n ON fields_u(name_id, n) WHERE n IS NOT NULL;
-- Session registry (one row per session) and the mutable stats sibling.
CREATE TABLE sessions(session_ref INTEGER PRIMARY KEY, session_id TEXT NOT NULL UNIQUE, project TEXT,
  first_time_ns INTEGER, first_version_id INTEGER, entrypoint_id INTEGER, user_type_id INTEGER) STRICT;
INSERT INTO sessions SELECT ref, txt, 'example-project', 0, 1, 1, 2 FROM s;
CREATE TABLE session_stats(session_ref INTEGER PRIMARY KEY, last_time_ns INTEGER, record_count INTEGER) STRICT;
INSERT INTO session_stats SELECT sref, MAX(time_ns), COUNT(*) FROM src GROUP BY sref;

ANALYZE;
.mode column
.headers on
SELECT 'records' AS what, (SELECT COUNT(*) FROM records_cur) AS cur_rows, (SELECT COUNT(*) FROM records_v3) AS v3_rows
UNION ALL SELECT 'fields_or_ext', (SELECT COUNT(*) FROM fields_cur), (SELECT COUNT(*) FROM ext_assistant_usage)
UNION ALL SELECT 'sessions', 0, (SELECT COUNT(*) FROM sessions);
-- Bytes per object. cur_* are the current layout, v3 the proposal.
SELECT name, SUM(pgsize) AS bytes, SUM(unused) AS unused FROM dbstat
WHERE name IN ('records_cur','records_cur_ident','records_cur_session','records_cur_time','records_cur_type',
  'records_cur_source_type','fields_cur','fields_cur_name_n','records_v3','records_v3_ident','records_v3_session',
  'records_i','records_i_session','records_i_time','records_i_type','records_i_source_type','records_i_ident','fields_k','fields_u','fields_u_name_n','records_s','records_s_session','records_v3_time','records_v3_type','records_v3_source_type','records_v3_cwd','records_v3_branch',
  'ext_assistant_usage','lk_cwd','lk_branch','lk_version','lk_small','sessions','session_stats')
GROUP BY name ORDER BY name;
SELECT 'cur_total' AS what, SUM(pgsize) AS bytes FROM dbstat WHERE name LIKE '%\_cur%' ESCAPE '\' OR name IN ('fields_cur')
UNION ALL SELECT 'v3_total', SUM(pgsize) FROM dbstat WHERE name LIKE '%v3%' OR name IN ('ext_assistant_usage','lk_cwd','lk_branch','lk_version','lk_small','sessions','session_stats')
;
