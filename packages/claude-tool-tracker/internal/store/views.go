package store

import (
	"fmt"
	"strings"
)

// Views lists the published views, in docs/views.md order. A test checks that
// the document names exactly these.
var Views = []string{"tool_calls", "annotations", "ingest_skips", "session_sources", "event_span"}

// latest is a scalar subquery: the value of expr (over the event row aliased
// e) from the newest event of kind for the call aliased c.
func latest(kind, expr string) string {
	return fmt.Sprintf(`(SELECT %s FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.kind = '%s' ORDER BY e.ts DESC, e.rowid DESC LIMIT 1)`, expr, kind)
}

func field(kind, key string) string {
	return latest(kind, fmt.Sprintf("json_extract(e.data, '$.%s')", key))
}

func eventTS(kind string) string { return latest(kind, "e.ts") }

func coalesce(parts ...string) string { return "COALESCE(" + strings.Join(parts, ", ") + ")" }

func viewsSQL() string {
	const callStart, check, running, callEnd = "tracker.call_start", "tracker.check", "tracker.running", "tracker.call_end"
	const pre, post, denied, verdict = "tracker.pre_tool", "tracker.post_tool", "tracker.denied", "ceta.verdict"

	cols := []string{
		"c.call_id AS call_id",
		"c.session_id AS session_id",
		"c.tool_use_id AS tool_use_id",
		"(SELECT e.agent_id FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id AND e.agent_id IS NOT NULL ORDER BY e.ts, e.rowid LIMIT 1) AS agent_id",
		coalesce(field(callStart, "tool"), field(pre, "tool_name"), field(post, "tool_name")) + " AS tool_name",
		field(callStart, "input") + " AS tool_input",
		coalesce(field(callStart, "cwd"), field(pre, "cwd")) + " AS cwd",
		field(check, "decision") + " AS verdict",
		field(check, "hook") + " AS deciding_hook",
		field(check, "reason") + " AS reason",
		coalesce(field(pre, "permission_mode"), field(post, "permission_mode")) + " AS permission_mode",
		coalesce(field(pre, "agent_type"), field(post, "agent_type")) + " AS agent_type",
		field(callEnd, "outcome") + " AS result",
		coalesce(field(callEnd, "error"), field(post, "error")) + " AS error_text",
		field(post, "duration_ms") + " AS duration_ms",
		eventTS(callStart) + " AS call_start_ts",
		eventTS(running) + " AS running_ts",
		eventTS(callEnd) + " AS call_end_ts",
		"(SELECT MIN(e.ts) FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id) AS first_event_ts",
		"(SELECT MAX(e.ts) FROM events e WHERE e.session_id = c.session_id AND e.tool_use_id = c.tool_use_id) AS last_event_ts",
		coalesce(field(post, "is_error"),
			fmt.Sprintf("CASE %s WHEN 'error' THEN 1 WHEN 'ok' THEN 0 WHEN 'refused' THEN 0 WHEN 'interrupted' THEN 0 END", field(callEnd, "outcome"))) + " AS is_error",
		field(denied, "reason") + " AS denial_reason",
		field(verdict, "sandbox_enabled") + " AS sandbox_enabled",
		field(verdict, "verdict") + " AS engine_verdict",
		field(verdict, "delivered") + " AS delivered_verdict",
	}

	var b strings.Builder
	b.WriteString("DROP VIEW IF EXISTS tool_calls;\n")
	b.WriteString("CREATE VIEW tool_calls AS SELECT t.*, r.repo AS repo, s.settled_by AS settled_by, s.settlement AS settlement, s.converted_ask AS converted_ask FROM (SELECT\n  ")
	b.WriteString(strings.Join(cols, ",\n  "))
	b.WriteString("\nFROM calls c) t LEFT JOIN cwd_repo r ON r.cwd = t.cwd LEFT JOIN call_settlement s ON s.call_id = t.call_id;\n")

	b.WriteString(`
DROP VIEW IF EXISTS annotations;
CREATE VIEW annotations AS
SELECT e.event_id AS event_id, e.session_id AS session_id, e.tool_use_id AS tool_use_id,
       e.agent_id AS agent_id, c.call_id AS call_id, e.source AS source, e.kind AS kind,
       e.ts AS ts, e.data AS data
FROM events e LEFT JOIN calls c ON c.session_id = e.session_id AND c.tool_use_id = e.tool_use_id
WHERE e.kind NOT IN ('tracker.call_start', 'tracker.check', 'tracker.running', 'tracker.call_end',
                     'tracker.session_end', 'tracker.pre_tool', 'tracker.post_tool',
                     'tracker.permission_offer', 'tracker.denied');

DROP VIEW IF EXISTS ingest_skips;
CREATE VIEW ingest_skips AS
SELECT r.run_id AS run_id, r.started_ms AS ingest_ms, fr.file AS file, f.session_id AS session_id,
       f.source AS source, fr.lines_read AS lines_read, fr.events_new AS events_new,
       fr.events_duplicate AS events_duplicate, fr.skipped_invalid_json AS skipped_invalid_json,
       fr.skipped_missing_field AS skipped_missing_field,
       fr.skipped_invalid_json + fr.skipped_missing_field AS skipped_total,
       fr.events_without_call AS events_without_call
FROM file_runs fr JOIN ingest_runs r ON r.run_id = fr.run_id JOIN files f ON f.file = fr.file;

DROP VIEW IF EXISTS session_sources;
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

DROP VIEW IF EXISTS event_span;
CREATE VIEW event_span AS
SELECT 'all' AS scope, MIN(ts) AS earliest_ts, MAX(ts) AS latest_ts,
       COUNT(DISTINCT session_id) AS sessions, (SELECT COUNT(*) FROM calls) AS calls, COUNT(*) AS events
FROM events
UNION ALL
SELECT source, MIN(ts), MAX(ts), COUNT(DISTINCT session_id),
       COUNT(DISTINCT CASE WHEN tool_use_id IS NOT NULL THEN session_id || char(31) || tool_use_id END), COUNT(*)
FROM events GROUP BY source;
`)
	return b.String()
}
