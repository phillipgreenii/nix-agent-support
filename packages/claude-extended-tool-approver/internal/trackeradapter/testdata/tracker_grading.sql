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
INSERT INTO events VALUES('g1c','sess-g','ceta','ceta.verdict',1790000001103,'tu-g1',NULL,'{"verdict":"ask","delivered":"ask","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g1d','sess-g','ceta','ceta.trace',1790000001104,'tu-g1',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g2c','sess-g','ceta','ceta.verdict',1790000001203,'tu-g2',NULL,'{"verdict":"ask","delivered":"ask","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g2d','sess-g','ceta','ceta.trace',1790000001204,'tu-g2',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g3c','sess-g','ceta','ceta.verdict',1790000001303,'tu-g3',NULL,'{"verdict":"ask","delivered":"ask","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g3d','sess-g','ceta','ceta.trace',1790000001304,'tu-g3',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g4c','sess-g','ceta','ceta.verdict',1790000001403,'tu-g4',NULL,'{"verdict":"ask","delivered":"ask","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g4d','sess-g','ceta','ceta.trace',1790000001404,'tu-g4',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g5c','sess-g','ceta','ceta.verdict',1790000001503,'tu-g5',NULL,'{"verdict":"ask","delivered":"reject","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g5d','sess-g','ceta','ceta.trace',1790000001504,'tu-g5',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g6c','sess-g','ceta','ceta.verdict',1790000001603,'tu-g6',NULL,'{"verdict":"abstain","delivered":"abstain","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g6d','sess-g','ceta','ceta.trace',1790000001604,'tu-g6',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"abstain","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g7c','sess-g','ceta','ceta.verdict',1790000001703,'tu-g7',NULL,'{"verdict":"abstain","delivered":"abstain","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g7d','sess-g','ceta','ceta.trace',1790000001704,'tu-g7',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"abstain","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g8c','sess-g','ceta','ceta.verdict',1790000001803,'tu-g8',NULL,'{"verdict":"abstain","delivered":"abstain","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g8d','sess-g','ceta','ceta.trace',1790000001804,'tu-g8',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"abstain","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g9c','sess-g','ceta','ceta.verdict',1790000001903,'tu-g9',NULL,'{"verdict":"abstain","delivered":"abstain","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g9d','sess-g','ceta','ceta.trace',1790000001904,'tu-g9',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"abstain","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g10c','sess-g','ceta','ceta.verdict',1790000002003,'tu-g10',NULL,'{"verdict":"abstain","delivered":"abstain","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g10d','sess-g','ceta','ceta.trace',1790000002004,'tu-g10',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"abstain","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g11c','sess-g','ceta','ceta.verdict',1790000002103,'tu-g11',NULL,'{"verdict":"abstain","delivered":"abstain","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g11d','sess-g','ceta','ceta.trace',1790000002104,'tu-g11',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"abstain","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g12c','sess-g','ceta','ceta.verdict',1790000002203,'tu-g12',NULL,'{"verdict":"ask","delivered":"ask","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g12d','sess-g','ceta','ceta.trace',1790000002204,'tu-g12',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g13c','sess-g','ceta','ceta.verdict',1790000002303,'tu-g13',NULL,'{"verdict":"ask","delivered":"ask","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g13d','sess-g','ceta','ceta.trace',1790000002304,'tu-g13',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g14c','sess-g','ceta','ceta.verdict',1790000002403,'tu-g14',NULL,'{"verdict":"ask","delivered":"ask","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g14d','sess-g','ceta','ceta.trace',1790000002404,'tu-g14',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"ask","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g15c','sess-g','ceta','ceta.verdict',1790000002503,'tu-g15',NULL,'{"verdict":"approve","delivered":"approve","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g15d','sess-g','ceta','ceta.trace',1790000002504,'tu-g15',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"approve","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g16c','sess-g','ceta','ceta.verdict',1790000002603,'tu-g16',NULL,'{"verdict":"reject","delivered":"reject","reason":"fixture","sandbox_enabled":false}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g16d','sess-g','ceta','ceta.trace',1790000002604,'tu-g16',NULL,'{"trace":[{"rule_order":1,"rule_name":"fixture-rule","decision":"reject","reason":"fixture"}]}','tool-events.sess-g.ceta.jsonl');
INSERT INTO events VALUES('g1a','sess-g','tracker','tracker.call_start',1790000001101,'tu-g1',NULL,'{"tool":"Bash","input":{"command":"git push origin main"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g1b','sess-g','tracker','tracker.pre_tool',1790000001102,'tu-g1',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g1e','sess-g','tracker','tracker.check',1790000001105,'tu-g1',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g1f','sess-g','tracker','tracker.call_end',1790000001108,'tu-g1',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g2a','sess-g','tracker','tracker.call_start',1790000001201,'tu-g2',NULL,'{"tool":"Bash","input":{"command":"git push origin main"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g2b','sess-g','tracker','tracker.pre_tool',1790000001202,'tu-g2',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g2e','sess-g','tracker','tracker.check',1790000001205,'tu-g2',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g2f','sess-g','tracker','tracker.call_end',1790000001208,'tu-g2',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g3a','sess-g','tracker','tracker.call_start',1790000001301,'tu-g3',NULL,'{"tool":"Write","input":{"file_path":"/nonexistent/example-repo/x.txt","content":"hi"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g3b','sess-g','tracker','tracker.pre_tool',1790000001302,'tu-g3',NULL,'{"permission_mode":"default","tool_name":"Write","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g3e','sess-g','tracker','tracker.check',1790000001305,'tu-g3',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g3f','sess-g','tracker','tracker.call_end',1790000001308,'tu-g3',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g4a','sess-g','tracker','tracker.call_start',1790000001401,'tu-g4',NULL,'{"tool":"Bash","input":{"command":"docker run example"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g4b','sess-g','tracker','tracker.pre_tool',1790000001402,'tu-g4',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g4e','sess-g','tracker','tracker.check',1790000001405,'tu-g4',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g4f','sess-g','tracker','tracker.call_end',1790000001408,'tu-g4',NULL,'{"outcome":"refused","error":"declined"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g5a','sess-g','tracker','tracker.call_start',1790000001501,'tu-g5',NULL,'{"tool":"Bash","input":{"command":"curl example.invalid"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g5b','sess-g','tracker','tracker.pre_tool',1790000001502,'tu-g5',NULL,'{"permission_mode":"dontAsk","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g5e','sess-g','tracker','tracker.check',1790000001505,'tu-g5',NULL,'{"decision":"deny","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g5f','sess-g','tracker','tracker.call_end',1790000001508,'tu-g5',NULL,'{"outcome":"refused","error":"declined"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g6a','sess-g','tracker','tracker.call_start',1790000001601,'tu-g6',NULL,'{"tool":"Bash","input":{"command":"make build"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g6b','sess-g','tracker','tracker.pre_tool',1790000001602,'tu-g6',NULL,'{"permission_mode":"auto","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g6e','sess-g','tracker','tracker.check',1790000001605,'tu-g6',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g6f','sess-g','tracker','tracker.call_end',1790000001608,'tu-g6',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g7a','sess-g','tracker','tracker.call_start',1790000001701,'tu-g7',NULL,'{"tool":"Bash","input":{"command":"make build"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g7b','sess-g','tracker','tracker.pre_tool',1790000001702,'tu-g7',NULL,'{"permission_mode":"auto","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g7e','sess-g','tracker','tracker.check',1790000001705,'tu-g7',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g7f','sess-g','tracker','tracker.call_end',1790000001708,'tu-g7',NULL,'{"outcome":"refused","error":"declined"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g8a','sess-g','tracker','tracker.call_start',1790000001801,'tu-g8',NULL,'{"tool":"Bash","input":{"command":"kubectl get pods"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g8b','sess-g','tracker','tracker.pre_tool',1790000001802,'tu-g8',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g8e','sess-g','tracker','tracker.check',1790000001805,'tu-g8',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g8f','sess-g','tracker','tracker.call_end',1790000001808,'tu-g8',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g9a','sess-g','tracker','tracker.call_start',1790000001901,'tu-g9',NULL,'{"tool":"Bash","input":{"command":"kubectl delete pod x"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g9b','sess-g','tracker','tracker.pre_tool',1790000001902,'tu-g9',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g9e','sess-g','tracker','tracker.check',1790000001905,'tu-g9',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g9f','sess-g','tracker','tracker.call_end',1790000001908,'tu-g9',NULL,'{"outcome":"refused","error":"declined"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g10a','sess-g','tracker','tracker.call_start',1790000002001,'tu-g10',NULL,'{"tool":"Bash","input":{"command":"ls"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g10b','sess-g','tracker','tracker.pre_tool',1790000002002,'tu-g10',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g10e','sess-g','tracker','tracker.check',1790000002005,'tu-g10',NULL,'{"decision":"allow","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g10f','sess-g','tracker','tracker.call_end',1790000002008,'tu-g10',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g11a','sess-g','tracker','tracker.call_start',1790000002101,'tu-g11',NULL,'{"tool":"Bash","input":{"command":"ls"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g11b','sess-g','tracker','tracker.pre_tool',1790000002102,'tu-g11',NULL,'{"permission_mode":"bypassPermissions","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g11e','sess-g','tracker','tracker.check',1790000002105,'tu-g11',NULL,'{"decision":"allow","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g11f','sess-g','tracker','tracker.call_end',1790000002108,'tu-g11',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g12a','sess-g','tracker','tracker.call_start',1790000002201,'tu-g12',NULL,'{"tool":"Bash","input":{"command":"go test ./..."},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g12b','sess-g','tracker','tracker.pre_tool',1790000002202,'tu-g12',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g12e','sess-g','tracker','tracker.check',1790000002205,'tu-g12',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g12f','sess-g','tracker','tracker.post_tool',1790000002207,'tu-g12',NULL,'{"hook_event":"PostToolUseFailure","tool_name":"x","permission_mode":"default","is_error":true,"error":"boom"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g12g','sess-g','tracker','tracker.call_end',1790000002208,'tu-g12',NULL,'{"outcome":"error","error":"boom"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g13a','sess-g','tracker','tracker.call_start',1790000002301,'tu-g13',NULL,'{"tool":"Bash","input":{"command":"make build"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g13b','sess-g','tracker','tracker.pre_tool',1790000002302,'tu-g13',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g13e','sess-g','tracker','tracker.check',1790000002305,'tu-g13',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g13f','sess-g','tracker','tracker.call_end',1790000002308,'tu-g13',NULL,'{"outcome":"refused","error":"declined"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g14a','sess-g','tracker','tracker.call_start',1790000002401,'tu-g14',NULL,'{"tool":"Bash","input":{"command":"npm test"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g14b','sess-g','tracker','tracker.pre_tool',1790000002402,'tu-g14',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g14e','sess-g','tracker','tracker.check',1790000002405,'tu-g14',NULL,'{"decision":"ask","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g15a','sess-g','tracker','tracker.call_start',1790000002501,'tu-g15',NULL,'{"tool":"Bash","input":{"command":"git status"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g15b','sess-g','tracker','tracker.pre_tool',1790000002502,'tu-g15',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g15e','sess-g','tracker','tracker.check',1790000002505,'tu-g15',NULL,'{"decision":"allow","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g15f','sess-g','tracker','tracker.call_end',1790000002508,'tu-g15',NULL,'{"outcome":"ok"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g16a','sess-g','tracker','tracker.call_start',1790000002601,'tu-g16',NULL,'{"tool":"Bash","input":{"command":"rm -rf /"},"cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g16b','sess-g','tracker','tracker.pre_tool',1790000002602,'tu-g16',NULL,'{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g16e','sess-g','tracker','tracker.check',1790000002605,'tu-g16',NULL,'{"decision":"deny","hook":"PreToolUse","reason":"fixture"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g16f','sess-g','tracker','tracker.call_end',1790000002608,'tu-g16',NULL,'{"outcome":"refused","error":"declined"}','tool-events.sess-g.tracker.jsonl');
INSERT INTO events VALUES('g99','sess-g','tracker','tracker.session_end',1790000009999,NULL,NULL,'{"reason":"other","normal":true}','tool-events.sess-g.tracker.jsonl');
CREATE TABLE calls (
		call_id     INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL,
		tool_use_id TEXT NOT NULL,
		UNIQUE (session_id, tool_use_id)
	);
INSERT INTO calls VALUES(1,'sess-g','tu-g1');
INSERT INTO calls VALUES(3,'sess-g','tu-g2');
INSERT INTO calls VALUES(5,'sess-g','tu-g3');
INSERT INTO calls VALUES(7,'sess-g','tu-g4');
INSERT INTO calls VALUES(9,'sess-g','tu-g5');
INSERT INTO calls VALUES(11,'sess-g','tu-g6');
INSERT INTO calls VALUES(13,'sess-g','tu-g7');
INSERT INTO calls VALUES(15,'sess-g','tu-g8');
INSERT INTO calls VALUES(17,'sess-g','tu-g9');
INSERT INTO calls VALUES(19,'sess-g','tu-g10');
INSERT INTO calls VALUES(21,'sess-g','tu-g11');
INSERT INTO calls VALUES(23,'sess-g','tu-g12');
INSERT INTO calls VALUES(25,'sess-g','tu-g13');
INSERT INTO calls VALUES(27,'sess-g','tu-g14');
INSERT INTO calls VALUES(29,'sess-g','tu-g15');
INSERT INTO calls VALUES(31,'sess-g','tu-g16');
CREATE TABLE call_settlement (
		call_id       INTEGER PRIMARY KEY REFERENCES calls(call_id),
		settled_by    TEXT,
		settlement    TEXT,
		converted_ask INTEGER
	, failed_timing INTEGER);
INSERT INTO call_settlement VALUES(1,'person','approved',0,0);
INSERT INTO call_settlement VALUES(3,'person','approved',0,0);
INSERT INTO call_settlement VALUES(5,'person','approved',0,0);
INSERT INTO call_settlement VALUES(7,'person','refused',0,0);
INSERT INTO call_settlement VALUES(9,'ceta','refused',1,0);
INSERT INTO call_settlement VALUES(11,'classifier','approved',0,0);
INSERT INTO call_settlement VALUES(13,'classifier','refused',0,0);
INSERT INTO call_settlement VALUES(15,'person','approved',0,0);
INSERT INTO call_settlement VALUES(17,'person','refused',0,0);
INSERT INTO call_settlement VALUES(19,'rule','approved',0,0);
INSERT INTO call_settlement VALUES(21,'mode','approved',0,0);
INSERT INTO call_settlement VALUES(23,'person','approved',0,1);
INSERT INTO call_settlement VALUES(25,'person','refused',0,0);
INSERT INTO call_settlement VALUES(27,'person','unresolved',0,NULL);
INSERT INTO call_settlement VALUES(29,'ceta','approved',0,0);
INSERT INTO call_settlement VALUES(31,'ceta','refused',0,0);
CREATE TABLE cwd_repo (
		cwd  TEXT PRIMARY KEY,
		repo TEXT NOT NULL
	);
CREATE TABLE ingest_runs (
		run_id     INTEGER PRIMARY KEY AUTOINCREMENT,
		started_ms INTEGER NOT NULL
	);
INSERT INTO ingest_runs VALUES(1,1794287031420);
CREATE TABLE files (
		file        TEXT PRIMARY KEY,
		session_id  TEXT NOT NULL,
		source      TEXT NOT NULL,
		read_offset INTEGER NOT NULL DEFAULT 0
	);
INSERT INTO files VALUES('tool-events.sess-g.ceta.jsonl','sess-g','ceta',7692);
INSERT INTO files VALUES('tool-events.sess-g.tracker.jsonl','sess-g','tracker',14591);
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
INSERT INTO file_runs VALUES(1,'tool-events.sess-g.ceta.jsonl',32,32,0,0,0,0);
INSERT INTO file_runs VALUES(1,'tool-events.sess-g.tracker.jsonl',65,65,0,0,0,1);
INSERT INTO sqlite_sequence VALUES('ingest_runs',1);
INSERT INTO sqlite_sequence VALUES('calls',96);
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
  EXISTS (SELECT 1 FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.source = 'tracker') AS tracker_seen,
  (EXISTS (SELECT 1 FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.source = 'tracker') AND NOT EXISTS (SELECT 1 FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = 'tracker.call_start')) AS missing_call_start,
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
