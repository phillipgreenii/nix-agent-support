PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE events (
		event_id    TEXT PRIMARY KEY,
		session_id  TEXT NOT NULL,
		source      TEXT NOT NULL,
		kind        TEXT NOT NULL,
		ts          INTEGER NOT NULL,
		tool_use_id TEXT,
		agent_id    TEXT,
		data        TEXT NOT NULL,
		file        TEXT NOT NULL
	);
INSERT INTO events VALUES('b03','sess-b','ceta','ceta.verdict',1790000000103,'tu-b1',NULL,'{"verdict":"ask","delivered":"reject","reason":"network","rule":"net-guard","sandbox_enabled":true}','tool-events.sess-b.ceta.jsonl');
INSERT INTO events VALUES('b01','sess-b','tracker','tracker.call_start',1790000000101,'tu-b1',NULL,'{"tool":"Bash","input":{"command":"curl example.invalid"},"cwd":"/"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('b02','sess-b','tracker','tracker.pre_tool',1790000000102,'tu-b1',NULL,'{"permission_mode":"dontAsk","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('b04','sess-b','tracker','tracker.check',1790000000104,'tu-b1',NULL,'{"decision":"deny","hook":"PreToolUse","reason":"network"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('b05','sess-b','tracker','tracker.call_end',1790000000105,'tu-b1',NULL,'{"outcome":"refused","error":"network"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('b11','sess-b','tracker','tracker.call_start',1790000000111,'tu-b2',NULL,'{"tool":"Write","input":{"file_path":"/nonexistent/example-repo/x.txt","content":"hi"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('b12','sess-b','tracker','tracker.pre_tool',1790000000112,'tu-b2',NULL,'{"permission_mode":"default","tool_name":"Write","cwd":"/nonexistent/example-repo"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('b13','sess-b','tracker','tracker.check',1790000000113,'tu-b2',NULL,'{"decision":"allow","reason":"rule"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('b14','sess-b','tracker','tracker.call_end',1790000000114,'tu-b2',NULL,'{"outcome":"ok"}','tool-events.sess-b.tracker.jsonl');
INSERT INTO events VALUES('a03','sess-a','ceta','ceta.verdict',1790000000003,'tu-a1',NULL,'{"verdict":"approve","delivered":"approve","reason":"read-only","rule":"git-readonly","sandbox_enabled":true}','tool-events.sess-a.ceta.jsonl');
INSERT INTO events VALUES('a04','sess-a','ceta','ceta.command_class',1790000000004,'tu-a1',NULL,'{"class":"git status"}','tool-events.sess-a.ceta.jsonl');
INSERT INTO events VALUES('a05','sess-a','ceta','ceta.trace',1790000000005,'tu-a1',NULL,'{"trace":[{"rule_order":1,"rule_name":"git-readonly","decision":"approve","reason":"read-only"}]}','tool-events.sess-a.ceta.jsonl');
INSERT INTO events VALUES('a13','sess-a','ceta','ceta.verdict',1790000000013,'tu-a2',NULL,'{"verdict":"ask","delivered":"ask","reason":"destructive","rule":"rm-guard","sandbox_enabled":false}','tool-events.sess-a.ceta.jsonl');
INSERT INTO events VALUES('a14','sess-a','ceta','ceta.command_class',1790000000014,'tu-a2',NULL,'{"class":"rm"}','tool-events.sess-a.ceta.jsonl');
INSERT INTO events VALUES('a01','sess-a','tracker','tracker.call_start',1790000000001,'tu-a1',NULL,'{"tool":"Bash","input":{"command":"git status"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a02','sess-a','tracker','tracker.pre_tool',1790000000002,'tu-a1',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a06','sess-a','tracker','tracker.check',1790000000006,'tu-a1',NULL,'{"decision":"allow","hook":"PreToolUse","reason":"read-only"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a07','sess-a','tracker','tracker.post_tool',1790000000007,'tu-a1',NULL,'{"hook_event":"PostToolUse","tool_name":"Bash","permission_mode":"default","duration_ms":12,"is_error":false}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a08','sess-a','tracker','tracker.call_end',1790000000008,'tu-a1',NULL,'{"outcome":"ok"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a11','sess-a','tracker','tracker.call_start',1790000000011,'tu-a2',NULL,'{"tool":"Bash","input":{"command":"rm -rf build"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a12','sess-a','tracker','tracker.pre_tool',1790000000012,'tu-a2',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a15','sess-a','tracker','tracker.check',1790000000015,'tu-a2',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"destructive"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a16','sess-a','tracker','tracker.post_tool',1790000000016,'tu-a2',NULL,'{"hook_event":"PostToolUseFailure","tool_name":"Bash","permission_mode":"default","is_error":true,"error":"boom"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a17','sess-a','tracker','tracker.call_end',1790000000017,'tu-a2',NULL,'{"outcome":"error","error":"boom"}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('a18','sess-a','tracker','tracker.session_end',1790000000018,NULL,NULL,'{"reason":"other","normal":true}','tool-events.sess-a.tracker.jsonl');
INSERT INTO events VALUES('c01','sess-c','ceta','ceta.verdict',1790000000201,'tu-c1',NULL,'{"verdict":"abstain","delivered":"abstain","sandbox_enabled":false}','tool-events.sess-c.ceta.jsonl');
CREATE TABLE calls (
		call_id     INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL,
		tool_use_id TEXT NOT NULL,
		UNIQUE (session_id, tool_use_id)
	);
INSERT INTO calls VALUES(1,'sess-b','tu-b1');
INSERT INTO calls VALUES(6,'sess-b','tu-b2');
INSERT INTO calls VALUES(10,'sess-a','tu-a1');
INSERT INTO calls VALUES(13,'sess-a','tu-a2');
INSERT INTO calls VALUES(25,'sess-c','tu-c1');
CREATE TABLE call_settlement (
		call_id       INTEGER PRIMARY KEY REFERENCES calls(call_id),
		settled_by    TEXT,
		settlement    TEXT,
		converted_ask INTEGER
	, failed_timing INTEGER);
INSERT INTO call_settlement VALUES(1,'ceta','refused',1,0);
INSERT INTO call_settlement VALUES(6,'rule','approved',0,0);
INSERT INTO call_settlement VALUES(10,'ceta','approved',0,0);
INSERT INTO call_settlement VALUES(13,'person','approved',0,1);
INSERT INTO call_settlement VALUES(25,NULL,'unresolved',0,NULL);
CREATE TABLE cwd_repo (
		cwd  TEXT PRIMARY KEY,
		repo TEXT NOT NULL
	);
CREATE TABLE ingest_runs (
		run_id     INTEGER PRIMARY KEY AUTOINCREMENT,
		started_ms INTEGER NOT NULL
	);
INSERT INTO ingest_runs VALUES(1,1793309163553);
INSERT INTO ingest_runs VALUES(2,1793309163577);
CREATE TABLE files (
		file        TEXT PRIMARY KEY,
		session_id  TEXT NOT NULL,
		source      TEXT NOT NULL,
		read_offset INTEGER NOT NULL DEFAULT 0
	);
INSERT INTO files VALUES('tool-events.sess-b.ceta.jsonl','sess-b','ceta',252);
INSERT INTO files VALUES('tool-events.sess-b.tracker.jsonl','sess-b','tracker',1790);
INSERT INTO files VALUES('tool-events.sess-a.ceta.jsonl','sess-a','ceta',1117);
INSERT INTO files VALUES('tool-events.sess-a.tracker.jsonl','sess-a','tracker',2497);
INSERT INTO files VALUES('tool-events.sess-c.ceta.jsonl','sess-c','ceta',220);
CREATE TABLE file_runs (
		run_id                INTEGER NOT NULL REFERENCES ingest_runs(run_id),
		file                  TEXT NOT NULL REFERENCES files(file),
		lines_read            INTEGER NOT NULL,
		events_new            INTEGER NOT NULL,
		events_duplicate      INTEGER NOT NULL,
		skipped_invalid_json  INTEGER NOT NULL,
		skipped_missing_field INTEGER NOT NULL,
		events_without_call   INTEGER NOT NULL,
		PRIMARY KEY (run_id, file)
	);
INSERT INTO file_runs VALUES(1,'tool-events.sess-b.ceta.jsonl',1,1,0,0,0,0);
INSERT INTO file_runs VALUES(1,'tool-events.sess-b.tracker.jsonl',9,8,0,1,0,0);
INSERT INTO file_runs VALUES(2,'tool-events.sess-a.ceta.jsonl',5,5,0,0,0,0);
INSERT INTO file_runs VALUES(2,'tool-events.sess-a.tracker.jsonl',11,11,0,0,0,1);
INSERT INTO file_runs VALUES(2,'tool-events.sess-c.ceta.jsonl',1,1,0,0,0,0);
INSERT INTO sqlite_sequence VALUES('ingest_runs',2);
INSERT INTO sqlite_sequence VALUES('calls',25);
CREATE VIEW tool_calls AS SELECT t.*, r.repo AS repo, s.settled_by AS settled_by, s.settlement AS settlement, s.converted_ask AS converted_ask, s.failed_timing AS failed_timing FROM (SELECT
  c.call_id AS call_id,
  c.session_id AS session_id,
  c.tool_use_id AS tool_use_id,
  (SELECT e.agent_id FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.agent_id IS NOT NULL ORDER BY e.ts, e.rowid LIMIT 1) AS agent_id,
  COALESCE((SELECT json_extract(e.data, '$.tool') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_start' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), (SELECT json_extract(e.data, '$.tool_name') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.pre_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), (SELECT json_extract(e.data, '$.tool_name') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.post_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1)) AS tool_name,
  (SELECT json_extract(e.data, '$.input') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_start' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS tool_input,
  COALESCE((SELECT json_extract(e.data, '$.cwd') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_start' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), (SELECT json_extract(e.data, '$.cwd') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.pre_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1)) AS cwd,
  (SELECT json_extract(e.data, '$.decision') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.check' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS verdict,
  (SELECT json_extract(e.data, '$.hook') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.check' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS deciding_hook,
  (SELECT json_extract(e.data, '$.reason') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.check' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS reason,
  COALESCE((SELECT json_extract(e.data, '$.permission_mode') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.pre_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), (SELECT json_extract(e.data, '$.permission_mode') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.post_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1)) AS permission_mode,
  COALESCE((SELECT json_extract(e.data, '$.agent_type') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.pre_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), (SELECT json_extract(e.data, '$.agent_type') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.post_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1)) AS agent_type,
  (SELECT json_extract(e.data, '$.outcome') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_end' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS result,
  COALESCE((SELECT json_extract(e.data, '$.error') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_end' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), (SELECT json_extract(e.data, '$.error') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.post_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1)) AS error_text,
  (SELECT json_extract(e.data, '$.duration_ms') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.post_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS duration_ms,
  (SELECT e.ts FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_start' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS call_start_ts,
  (SELECT e.ts FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.running' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS running_ts,
  (SELECT e.ts FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_end' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS call_end_ts,
  (SELECT MIN(e.ts) FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id) AS first_event_ts,
  (SELECT MAX(e.ts) FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id) AS last_event_ts,
  COALESCE((SELECT json_extract(e.data, '$.is_error') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.post_tool' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1), CASE (SELECT json_extract(e.data, '$.outcome') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_end' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) WHEN 'error' THEN 1 WHEN 'ok' THEN 0 WHEN 'refused' THEN 0 WHEN 'interrupted' THEN 0 END) AS is_error,
  (SELECT json_extract(e.data, '$.reason') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.denied' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS denial_reason,
  (SELECT json_extract(e.data, '$.sandbox_enabled') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'ceta.verdict' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS sandbox_enabled,
  (SELECT json_extract(e.data, '$.verdict') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'ceta.verdict' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS engine_verdict,
  (SELECT json_extract(e.data, '$.delivered') FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'ceta.verdict' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1) AS delivered_verdict
FROM calls c) t LEFT JOIN cwd_repo r ON r.cwd = t.cwd LEFT JOIN call_settlement s ON s.call_id = t.call_id;
CREATE VIEW annotations AS
SELECT e.event_id AS event_id, e.session_id AS session_id, e.tool_use_id AS tool_use_id,
       e.agent_id AS agent_id, c.call_id AS call_id, e.source AS source, e.kind AS kind,
       e.ts AS ts, e.data AS data
FROM events e LEFT JOIN calls c ON c.session_id = e.session_id AND c.tool_use_id = e.tool_use_id
WHERE e.kind NOT IN ('tracker.call_start', 'tracker.check', 'tracker.running', 'tracker.call_end',
                     'tracker.session_end', 'tracker.pre_tool', 'tracker.post_tool',
                     'tracker.permission_offer', 'tracker.denied');
CREATE VIEW ingest_skips AS
SELECT r.run_id AS run_id, r.started_ms AS ingest_ms, fr.file AS file, f.session_id AS session_id,
       f.source AS source, fr.lines_read AS lines_read, fr.events_new AS events_new,
       fr.events_duplicate AS events_duplicate, fr.skipped_invalid_json AS skipped_invalid_json,
       fr.skipped_missing_field AS skipped_missing_field,
       fr.skipped_invalid_json + fr.skipped_missing_field AS skipped_total,
       fr.events_without_call AS events_without_call
FROM file_runs fr JOIN ingest_runs r ON r.run_id = fr.run_id JOIN files f ON f.file = fr.file;
CREATE VIEW session_sources AS
SELECT f.session_id AS session_id,
       (SELECT group_concat(source, ',') FROM (SELECT DISTINCT source FROM files x WHERE x.session_id = f.session_id ORDER BY source)) AS sources,
       MAX(f.source = 'tracker') AS has_tracker,
       MAX(f.source = 'ceta') AS has_ceta,
       (SELECT COUNT(*) FROM events e WHERE e.session_id = f.session_id AND e.source = 'tracker') AS tracker_events,
       (SELECT COUNT(*) FROM events e WHERE e.session_id = f.session_id AND e.source = 'ceta') AS ceta_events,
       (SELECT MIN(ts) FROM events e WHERE e.session_id = f.session_id) AS first_ts,
       (SELECT MAX(ts) FROM events e WHERE e.session_id = f.session_id) AS last_ts
FROM files f GROUP BY f.session_id;
CREATE VIEW event_span AS
SELECT 'all' AS scope, MIN(ts) AS earliest_ts, MAX(ts) AS latest_ts,
       COUNT(DISTINCT session_id) AS sessions, (SELECT COUNT(*) FROM calls) AS calls, COUNT(*) AS events
FROM events
UNION ALL
SELECT source, MIN(ts), MAX(ts), COUNT(DISTINCT session_id),
       COUNT(DISTINCT CASE WHEN tool_use_id IS NOT NULL THEN session_id || char(31) || tool_use_id END), COUNT(*)
FROM events GROUP BY source;
CREATE INDEX events_call ON events(session_id, tool_use_id, kind);
CREATE INDEX events_source ON events(source);
COMMIT;
