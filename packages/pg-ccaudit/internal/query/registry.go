// Package query holds the named, versioned canned queries (T-10) and the
// read-only execution path (T-13).
//
// The queries are NAMED AND VERSIONED for one reason: an audit is only useful if
// its numbers can be compared with the next audit's. An agent re-deriving the SQL
// each time produces answers that differ for reasons nobody can reconstruct, so
// every result set here is stamped with the query name, its version, and the
// window it covered.
//
// A version bump means the SQL's MEANING changed. Editing SQL without bumping
// silently invalidates every comparison against an earlier run, so the registry
// test pins each name to its current version.
package query

import (
	"fmt"
	"sort"
	"strings"
)

// Param is one positional CLI argument bound to a SQL named parameter.
type Param struct {
	Name    string
	Doc     string
	Default string
	// Numeric marks a parameter that must parse as an integer.
	Numeric bool
}

// Query is a canned query.
type Query struct {
	Name string
	// Version is bumped whenever the SQL's meaning changes.
	Version int
	Doc     string
	// Notes carries anything a reader must know to interpret the columns
	// correctly. It is printed by `pg-ccaudit queries --verbose`.
	Notes  string
	Params []Param
	SQL    string
	// Window marks a query whose SQL references :since and :until.
	Window bool
}

// windowClause is spelled so the DEFAULT (empty) window includes rows whose ts
// is NULL. A bare `ts >= :since` would evaluate to NULL for those rows and drop
// them silently from every unwindowed run — an events row with no timestamp is
// rare, but losing it invisibly is exactly the class of miscount this index
// exists to stop.
const windowClause = `(:since = '' OR %[1]s.ts >= :since) AND (:until = '' OR %[1]s.ts < :until)`

func win(alias string) string { return fmt.Sprintf(windowClause, alias) }

// prevToolAgg is one row per assistant LINE that issued tool calls, not per call.
//
// The distinction is load-bearing for every "what did the agent do just before
// this?" query: Claude Code writes a batch of parallel sibling tool_use blocks on
// a SINGLE line, so they all share one seq. Joining a human turn to
// `tool_calls` by (path, seq) therefore fans out — measured, 1,209 human turns
// produced 3,004 rows — which would make Tier 1's candidate count 2.5x the number
// of turns that exist and hand Tier 2 the same turn several times over. Rolling
// the line up first keeps the identity "one candidate per turn" true, and reports
// the batch's shape (how many calls, which tools, how many failed) instead of
// silently picking one of them.
const prevToolAgg = `
prev_lines AS (
  SELECT c.path                                                      AS path,
         c.seq                                                       AS seq,
         COUNT(*)                                                    AS calls,
         group_concat(DISTINCT c.tool_name)                          AS tool_names,
         group_concat(DISTINCT c.lead_cmd)                           AS lead_cmds,
         SUM(CASE WHEN r.is_error = 1 THEN 1 ELSE 0 END)             AS errors,
         MAX(CASE WHEN r.is_error = 1 THEN r.signature END)          AS signature
  FROM tool_calls c
  LEFT JOIN tool_results r ON r.tool_use_id = c.tool_use_id
  GROUP BY c.path, c.seq
)`

// registry is the canned-query set. Every row of the bead's query table is
// present; `coverage` is an addition that makes T-2's provable coverage a query
// rather than a manual SELECT.
var registry = func() map[string]Query {
	qs := []Query{
		{
			Name:    "error-rate-by-tool",
			Version: 1,
			Doc:     "Per-tool error counts WITH denominators, in one query.",
			Notes: "The denominator is every tool CALL, including calls whose result was never " +
				"recorded (LEFT JOIN) — an abandoned call still cost something. errors counts " +
				"only results whose is_error is present AND true (T-9).",
			Window: true,
			SQL: `
SELECT c.tool_name                                                             AS tool_name,
       COUNT(*)                                                                AS calls,
       SUM(CASE WHEN r.is_error = 1 THEN 1 ELSE 0 END)                         AS errors,
       ROUND(100.0 * SUM(CASE WHEN r.is_error = 1 THEN 1 ELSE 0 END) / COUNT(*), 3) AS error_pct
FROM tool_calls c
JOIN events e ON e.path = c.path AND e.seq = c.seq
LEFT JOIN tool_results r ON r.tool_use_id = c.tool_use_id
WHERE ` + win("e") + `
GROUP BY c.tool_name
ORDER BY errors DESC, calls DESC, tool_name`,
		},
		{
			Name:    "top-signatures",
			Version: 1,
			Doc:     "Ranked normalized error signatures.",
			Notes: "signature is normalized AT INGEST (T-6), so grouping is stable across runs; " +
				"recomputing it at query time would regroup history whenever the normalizer changed.",
			Params: []Param{{Name: "limit", Doc: "max rows", Default: "50", Numeric: true}},
			Window: true,
			SQL: `
SELECT r.signature                                                        AS signature,
       COUNT(*)                                                           AS errors,
       COUNT(DISTINCT e.session_id)                                       AS sessions,
       SUM(CASE WHEN COALESCE(e.is_sidechain, 0) = 1 THEN 1 ELSE 0 END)   AS subagent
FROM tool_results r
JOIN events e ON e.path = r.path AND e.seq = r.seq
WHERE r.is_error = 1 AND ` + win("e") + `
GROUP BY r.signature
ORDER BY errors DESC, signature
LIMIT :limit`,
		},
		{
			Name:    "bash-by-lead-cmd",
			Version: 1,
			Doc:     "Per-leading-command Bash call and error rates.",
			Notes: "lead_cmd is peeled at ingest from the FULL input (T-3). A NOCMD row here means " +
				"the Bash input genuinely carried no command key — in the shell prototype NOCMD was " +
				"a 470-row artefact of truncating the input before parsing it.",
			Window: true,
			SQL: `
SELECT c.lead_cmd                                                              AS lead_cmd,
       COUNT(*)                                                                AS calls,
       SUM(CASE WHEN r.is_error = 1 THEN 1 ELSE 0 END)                         AS errors,
       ROUND(100.0 * SUM(CASE WHEN r.is_error = 1 THEN 1 ELSE 0 END) / COUNT(*), 3) AS error_pct
FROM tool_calls c
JOIN events e ON e.path = c.path AND e.seq = c.seq
LEFT JOIN tool_results r ON r.tool_use_id = c.tool_use_id
WHERE c.tool_name = 'Bash' AND ` + win("e") + `
GROUP BY c.lead_cmd
ORDER BY errors DESC, calls DESC, lead_cmd`,
		},
		{
			Name:    "session-concentration",
			Version: 1,
			Doc:     "The runaway discount for one signature: total / distinct sessions / worst session.",
			Notes: "A signature firing 40 times in one session is a different problem from one firing " +
				"once in 40 sessions, and only the second justifies a standing rule. Sessions are " +
				"scoped by events.session_id.",
			Params: []Param{{Name: "sig", Doc: "signature (SQL LIKE pattern; a plain string matches exactly)", Default: "%"}},
			Window: true,
			SQL: `
WITH hits AS (
  SELECT e.session_id AS session_id
  FROM tool_results r
  JOIN events e ON e.path = r.path AND e.seq = r.seq
  WHERE r.is_error = 1 AND r.signature LIKE :sig AND ` + win("e") + `
),
per AS (SELECT session_id, COUNT(*) AS n FROM hits GROUP BY session_id)
SELECT (SELECT COUNT(*) FROM hits)                                        AS total,
       (SELECT COUNT(*) FROM per)                                         AS distinct_sessions,
       (SELECT COALESCE(MAX(n), 0) FROM per)                              AS worst_session,
       ROUND(COALESCE(
         (SELECT COUNT(*) FROM hits) * 1.0 / NULLIF((SELECT COUNT(*) FROM per), 0), 0), 3) AS mean_per_session`,
		},
		{
			Name:    "retry-chains",
			Version: 1,
			Doc:     "A failed call followed by the same tool being re-called within N line ordinals.",
			Notes: "N defaults to 6. A retry cycle is at minimum [tool_use] -> [tool_result] -> " +
				"[tool_use] (3 lines), and Claude writes one line per content block, so a batch of " +
				"parallel sibling calls plus their results can separate a failure from its retry by " +
				"several lines; 6 spans roughly two intervening turns while still excluding a " +
				"same-tool call from a genuinely later phase of the session. Candidates are scoped to " +
				"the SAME session_id AND the same file, because seq is a per-file line ordinal — a " +
				"gap computed across two files is meaningless. identical_input = 1 is the strongest " +
				"signal: the same input re-sent after a failure.",
			Params: []Param{{Name: "n", Doc: "max seq gap between failure and retry", Default: "6", Numeric: true}},
			Window: true,
			SQL: `
SELECT fe.session_id                                                AS session_id,
       COALESCE(fe.is_sidechain, 0)                                 AS is_sidechain,
       fc.tool_name                                                 AS tool_name,
       fc.path                                                      AS path,
       fc.seq                                                       AS failed_seq,
       nc.seq                                                       AS retry_seq,
       nc.seq - fc.seq                                              AS seq_gap,
       CASE WHEN nc.input_json = fc.input_json THEN 1 ELSE 0 END     AS identical_input,
       COALESCE(nr.is_error, -1)                                    AS retry_is_error,
       fr.signature                                                 AS signature
FROM tool_calls fc
JOIN tool_results fr ON fr.tool_use_id = fc.tool_use_id AND fr.is_error = 1
JOIN events fe ON fe.path = fc.path AND fe.seq = fc.seq
JOIN tool_calls nc ON nc.path = fc.path AND nc.tool_name = fc.tool_name
                  AND nc.seq > fc.seq AND nc.seq <= fc.seq + :n
JOIN events ne ON ne.path = nc.path AND ne.seq = nc.seq
LEFT JOIN tool_results nr ON nr.tool_use_id = nc.tool_use_id
WHERE ne.session_id IS fe.session_id AND ` + win("fe") + `
ORDER BY fc.path, fc.seq, nc.seq`,
		},
		{
			Name:    "error-then-narration",
			Version: 1,
			Doc:     "Assistant prose written on the line immediately after a failed tool result.",
			Notes: "This is what an agent rediscovers the hard way and says out loud. The join is " +
				"assistant_text at seq + 1, which is adjacency by line ordinal, not by turn.",
			Window: true,
			SQL: `
SELECT e.session_id                    AS session_id,
       COALESCE(e.is_sidechain, 0)     AS is_sidechain,
       r.path                          AS path,
       r.seq                           AS error_seq,
       r.signature                     AS signature,
       a.text                          AS narration
FROM tool_results r
JOIN events e ON e.path = r.path AND e.seq = r.seq
JOIN assistant_text a ON a.path = r.path AND a.seq = r.seq + 1
WHERE r.is_error = 1 AND ` + win("e") + `
ORDER BY r.path, r.seq`,
		},
		{
			Name:    "sidechain-split",
			Version: 1,
			Doc:     "Every error signature split by is_sidechain — decides WHERE a fix belongs.",
			Notes: "The load-bearing query. A uniform main-loop/subagent ratio would be useless; it is " +
				"the PER-CLASS split that says whether a fix belongs in the always-on user rules or in " +
				"the subagent brief. Its absence forced that routing to be done by hand.",
			Params: []Param{{Name: "sig", Doc: "signature (SQL LIKE pattern)", Default: "%"}},
			Window: true,
			SQL: `
SELECT r.signature                                                          AS signature,
       SUM(CASE WHEN COALESCE(e.is_sidechain, 0) = 0 THEN 1 ELSE 0 END)     AS main_loop,
       SUM(CASE WHEN COALESCE(e.is_sidechain, 0) = 1 THEN 1 ELSE 0 END)     AS subagent,
       COUNT(*)                                                             AS total,
       COUNT(DISTINCT e.session_id)                                         AS sessions
FROM tool_results r
JOIN events e ON e.path = r.path AND e.seq = r.seq
WHERE r.is_error = 1 AND r.signature LIKE :sig AND ` + win("e") + `
GROUP BY r.signature
ORDER BY total DESC, signature`,
		},
		{
			Name:    "cost-by-signature",
			Version: 1,
			Doc:     "Measured cost per error signature.",
			Notes: "READ THIS BEFORE QUOTING A NUMBER. Two cost columns are reported because the " +
				"corpus does NOT carry a per-tool-call duration. Claude Code writes a top-level " +
				"durationMs only on `system` events (turn/hook summaries), never on the user event " +
				"that carries a tool_result — so duration_ms_sum, summed over the failing results' own " +
				"events rows, is legitimately near zero and MUST NOT be reported as the cost of the " +
				"failures. elapsed_ms_sum is the real measurement: the wall time between the " +
				"tool_use line's timestamp and its tool_result line's timestamp, both recorded in the " +
				"transcript. It is still MEASURED, not estimated. For a batch of parallel sibling " +
				"calls the per-call elapsed values overlap, so treat the sum as an upper bound on " +
				"serial cost.",
			Window: true,
			SQL: `
SELECT r.signature                                                              AS signature,
       COUNT(*)                                                                 AS errors,
       SUM(COALESCE(re.duration_ms, 0))                                         AS duration_ms_sum,
       CAST(ROUND(SUM(COALESCE((julianday(re.ts) - julianday(ce.ts)) * 86400000.0, 0))) AS INTEGER) AS elapsed_ms_sum,
       CAST(ROUND(AVG(COALESCE((julianday(re.ts) - julianday(ce.ts)) * 86400000.0, 0))) AS INTEGER) AS elapsed_ms_mean
FROM tool_results r
JOIN events re ON re.path = r.path AND re.seq = r.seq
JOIN tool_calls c ON c.tool_use_id = r.tool_use_id
JOIN events ce ON ce.path = c.path AND ce.seq = c.seq
WHERE r.is_error = 1 AND ` + win("re") + `
GROUP BY r.signature
ORDER BY elapsed_ms_sum DESC, errors DESC, signature`,
		},
		{
			Name:    "hook-rejections",
			Version: 1,
			Doc:     "True hook-rejection totals from the recorded hookErrors payloads.",
			Notes: "Counted from events.hook_errors, NOT from grepping error text — a hook rejection " +
				"whose wording changed would vanish from a text grep while still being recorded here. " +
				"An empty array is STORED at ingest (the hooks ran and rejected nothing) and filtered " +
				"out here, so 'no rejections' stays distinguishable from 'no hook data'.\n" +
				"MEASURED 2026-08-14, AND YOU MUST KNOW THIS BEFORE READING A ZERO HERE AS GOOD NEWS: " +
				"across 405,986 indexed events the corpus contains 403,716 rows with NO hookErrors key " +
				"and 2,270 rows with a literal `[]`. Not one non-empty payload exists, so this query " +
				"returns ZERO ROWS on the real corpus today — while hook-authored denials demonstrably " +
				"DO occur and are recorded in the tool_result BODY instead (e.g. 41 occurrences of " +
				"'access to .git directory is blocked' and 23 of the primary-commit refusal, both " +
				"visible in top-signatures). So a zero here means 'Claude Code is not populating the " +
				"structured field', NOT 'no hook rejected anything'. Use `denied-tool-calls` for the " +
				"permission layer and `top-signatures` for hook-authored refusals until the structured " +
				"payload starts arriving; this query is kept because it is the durable detector the day " +
				"it does.",
			Window: true,
			SQL: `
SELECT e.hook_errors                                                        AS hook_errors,
       COUNT(*)                                                             AS events,
       COUNT(DISTINCT e.session_id)                                         AS sessions,
       SUM(CASE WHEN COALESCE(e.is_sidechain, 0) = 1 THEN 1 ELSE 0 END)     AS subagent,
       SUM(COALESCE(e.hook_count, 0))                                       AS hook_invocations
FROM events e
WHERE e.hook_errors IS NOT NULL
  AND TRIM(e.hook_errors) NOT IN ('[]', '{}', '""')
  AND ` + win("e") + `
GROUP BY e.hook_errors
ORDER BY events DESC, hook_errors`,
		},
		{
			Name:    "first-seen",
			Version: 1,
			Doc:     "Earliest and latest occurrence of a signature, ranked by FIRST occurrence.",
			Notes: "This pair closes the loop on a documented fix: it turns \"we added a rule\" into a " +
				"testable claim. In the census that motivated this index, doing it by hand dated one " +
				"class \"last seen 2026-07-08\" when it had in fact recurred six times through " +
				"2026-07-28, and called another rule redundant while its signature was firing 26 " +
				"times in 8 days.",
			Params: []Param{{Name: "sig", Doc: "signature (SQL LIKE pattern)", Default: "%"}},
			Window: true,
			SQL: `
SELECT r.signature                     AS signature,
       MIN(e.ts)                       AS first_seen,
       MAX(e.ts)                       AS last_seen,
       COUNT(*)                        AS occurrences,
       COUNT(DISTINCT e.session_id)    AS sessions
FROM tool_results r
JOIN events e ON e.path = r.path AND e.seq = r.seq
WHERE r.is_error = 1 AND r.signature LIKE :sig AND ` + win("e") + `
GROUP BY r.signature
ORDER BY first_seen ASC, signature`,
		},
		{
			Name:    "last-seen",
			Version: 1,
			Doc:     "Earliest and latest occurrence of a signature, ranked by MOST RECENT occurrence.",
			Notes: "Same columns as first-seen; only the ranking differs, so \"did this stop?\" and " +
				"\"when did this start?\" are one glance apart.",
			Params: []Param{{Name: "sig", Doc: "signature (SQL LIKE pattern)", Default: "%"}},
			Window: true,
			SQL: `
SELECT r.signature                     AS signature,
       MIN(e.ts)                       AS first_seen,
       MAX(e.ts)                       AS last_seen,
       COUNT(*)                        AS occurrences,
       COUNT(DISTINCT e.session_id)    AS sessions
FROM tool_results r
JOIN events e ON e.path = r.path AND e.seq = r.seq
WHERE r.is_error = 1 AND r.signature LIKE :sig AND ` + win("e") + `
GROUP BY r.signature
ORDER BY last_seen DESC, signature`,
		},
		{
			Name:    "concentration-by-signature",
			Version: 1,
			Doc:     "The runaway discount for EVERY signature at once: total / sessions / worst session.",
			Notes: "`session-concentration` answers the same question for ONE signature, which means a " +
				"report that wanted the discount for every finding had to run it once per signature. " +
				"This is that query pivoted, and it exists so the runaway discount is applied to every " +
				"ranked finding by default instead of only to the ones someone remembered to check. " +
				"Read worst_session against total: a signature firing 40 times inside ONE session is " +
				"one agent stuck in a loop and MUST NOT justify a standing rule; the same 40 spread " +
				"over 40 sessions is systemic.",
			Window: true,
			SQL: `
WITH hits AS (
  SELECT r.signature AS signature, e.session_id AS session_id
  FROM tool_results r
  JOIN events e ON e.path = r.path AND e.seq = r.seq
  WHERE r.is_error = 1 AND ` + win("e") + `
),
per AS (SELECT signature, session_id, COUNT(*) AS n FROM hits GROUP BY signature, session_id)
SELECT signature                                  AS signature,
       SUM(n)                                     AS total,
       COUNT(*)                                   AS distinct_sessions,
       MAX(n)                                     AS worst_session,
       ROUND(1.0 * SUM(n) / COUNT(*), 3)          AS mean_per_session
FROM per
GROUP BY signature
ORDER BY worst_session DESC, total DESC, signature`,
		},

		// ── Tier 1: structural mistake/correction candidates (bead pg2-oisvb) ──
		//
		// Every query below is SQL over the index with NO model calls. Tier 1's job
		// is recall at negligible cost so that Tier 2's semantic pass runs over
		// hundreds of candidates instead of hundreds of thousands of records.
		{
			Name:    "human-turns",
			Version: 1,
			Doc:     "Human turns isolated by promptSource, with the inflation factor a naive count would produce.",
			Notes: "The denominator every correction rate needs, and the trap it exists to avoid. " +
				"`type=='user'` is NOT a human turn: measured corpus-wide it is 90,424 records of " +
				"which 1,183 are typed and 26 queued, the rest being harness injections " +
				"(promptSource system/sdk), slash-command and skill expansions (no promptSource), and " +
				"84,417 tool results, which are not turns at all. inflation_factor is " +
				"all_user_records / (typed + queued) — report it whenever quoting a correction rate, " +
				"because reading every user record as a turn understates the rate by that factor. " +
				"no_prose_records is every user record with no user_text row: tool results plus the " +
				"rare empty record.",
			Window: true,
			SQL: `
SELECT SUM(CASE WHEN e.prompt_source = 'typed'  THEN 1 ELSE 0 END)          AS typed,
       SUM(CASE WHEN e.prompt_source = 'queued' THEN 1 ELSE 0 END)          AS queued,
       SUM(CASE WHEN e.prompt_source = 'system' THEN 1 ELSE 0 END)          AS injected_system,
       SUM(CASE WHEN e.prompt_source = 'sdk'    THEN 1 ELSE 0 END)          AS injected_sdk,
       SUM(CASE WHEN e.prompt_source IS NULL AND u.path IS NOT NULL
                THEN 1 ELSE 0 END)                                          AS unlabelled_prose,
       SUM(CASE WHEN u.path IS NULL THEN 1 ELSE 0 END)                      AS no_prose_records,
       COUNT(*)                                                             AS all_user_records,
       COUNT(DISTINCT CASE WHEN e.prompt_source IN ('typed','queued')
                           THEN e.session_id END)                           AS human_turn_sessions,
       SUM(CASE WHEN e.prompt_source IN ('typed','queued')
                 AND COALESCE(e.is_sidechain, 0) = 1 THEN 1 ELSE 0 END)     AS human_turns_subagent,
       ROUND(1.0 * COUNT(*) / NULLIF(SUM(CASE WHEN e.prompt_source IN ('typed','queued')
                                              THEN 1 ELSE 0 END), 0), 1)    AS inflation_factor
FROM events e
LEFT JOIN user_text u ON u.path = e.path AND u.seq = e.seq
WHERE e.type = 'user' AND ` + win("e"),
		},
		{
			Name:    "typed-turn-candidates",
			Version: 1,
			Doc:     "Every human turn paired with the assistant tool call immediately preceding it.",
			Notes: "The highest-recall Tier 1 signal, and the one the mistake census's baseline " +
				"classifier is built from. The pairing is the NEAREST PRECEDING tool-call LINE in the " +
				"same transcript file and session, rolled up per line so parallel sibling calls do not " +
				"multiply the turn (see prev_tool_names / prev_tool_calls).\n" +
				"DO NOT read a small seq_gap as adjacency and a large one as distance without " +
				"calibrating: measured over 1,209 human turns the gap is never below 4 and its median " +
				"is 15 (p25 13, p75 19, max 145), because one assistant turn writes many lines — " +
				"thinking, prose, each tool_use, each tool_result, and the system hook summaries. A " +
				"gap near the median is ordinary immediate adjacency; only the far tail is a genuinely " +
				"later turn. 1,043 of the 1,209 turns have a preceding tool call at all and 80 follow " +
				"a line on which something errored.\n" +
				"prev_tool_seq NULL means no assistant action preceded the turn in this session — the " +
				"turn opened it, so the turn cannot be a correction of anything.",
			Window: true,
			SQL: `
WITH ` + prevToolAgg + `,
turns AS (
  SELECT e.path        AS path,
         e.seq         AS seq,
         e.session_id  AS session_id,
         COALESCE(e.is_sidechain, 0) AS is_sidechain,
         e.ts          AS ts,
         e.prompt_source AS prompt_source,
         u.text        AS text,
         (SELECT MAX(c.seq) FROM tool_calls c
           WHERE c.path = e.path AND c.seq < e.seq) AS prev_seq
  FROM events e
  JOIN user_text u ON u.path = e.path AND u.seq = e.seq
  WHERE e.prompt_source IN ('typed','queued') AND ` + win("e") + `
)
SELECT t.session_id                       AS session_id,
       t.is_sidechain                     AS is_sidechain,
       t.prompt_source                    AS prompt_source,
       t.path                             AS path,
       t.seq                              AS turn_seq,
       t.ts                               AS ts,
       p.seq                              AS prev_tool_seq,
       pe.ts                              AS prev_ts,
       CASE WHEN p.seq IS NULL THEN NULL ELSE t.seq - p.seq END AS seq_gap,
       COALESCE(p.calls, 0)               AS prev_tool_calls,
       p.tool_names                       AS prev_tool_names,
       p.lead_cmds                        AS prev_lead_cmds,
       COALESCE(p.errors, 0)              AS prev_errors,
       p.signature                        AS prev_signature,
       t.text                             AS turn_text
FROM turns t
LEFT JOIN events pe ON pe.path = t.path AND pe.seq = t.prev_seq
                   AND pe.session_id IS t.session_id
LEFT JOIN prev_lines p ON p.path = pe.path AND p.seq = pe.seq
ORDER BY t.path, t.seq`,
		},
		{
			Name:    "interruptions",
			Version: 1,
			Doc:     "Interruption sentinels, with the tool call the person cut short.",
			Notes: "`interrupted` is set AT INGEST by prefix-matching Claude Code's sentinel, which " +
				"covers both observed forms ('[Request interrupted by user]' and '… for tool use'). " +
				"An interruption is a correction a person paid attention to make, so it is high " +
				"precision — but it is a WORDING match, the one in the ingester, so a sentinel " +
				"rewording would zero this query while everything still looked healthy. Compare its " +
				"count against the previous run before concluding interruptions stopped.",
			Window: true,
			SQL: `
WITH ` + prevToolAgg + `,
marks AS (
  SELECT e.path AS path, e.seq AS seq, e.session_id AS session_id,
         COALESCE(e.is_sidechain, 0) AS is_sidechain, e.ts AS ts, u.text AS text,
         (SELECT MAX(c.seq) FROM tool_calls c
           WHERE c.path = e.path AND c.seq < e.seq) AS prev_seq
  FROM user_text u
  JOIN events e ON e.path = u.path AND e.seq = u.seq
  WHERE u.interrupted = 1 AND ` + win("e") + `
)
SELECT m.session_id                  AS session_id,
       m.is_sidechain                AS is_sidechain,
       m.path                        AS path,
       m.seq                         AS seq,
       m.ts                          AS ts,
       p.seq                         AS prev_tool_seq,
       pe.ts                         AS prev_ts,
       COALESCE(p.calls, 0)          AS prev_tool_calls,
       p.tool_names                  AS prev_tool_names,
       p.lead_cmds                   AS prev_lead_cmds,
       m.text                        AS marker
FROM marks m
LEFT JOIN events pe ON pe.path = m.path AND pe.seq = m.prev_seq
                   AND pe.session_id IS m.session_id
LEFT JOIN prev_lines p ON p.path = pe.path AND p.seq = pe.seq
ORDER BY m.path, m.seq`,
		},
		{
			Name:    "denied-tool-calls",
			Version: 1,
			Doc:     "Tool calls a PERSON or the permission layer refused, split by who refused.",
			Notes: "The agent attempted something it was not allowed to do, which is a mistake whose " +
				"detection needs no semantics. `kind` decides the Tier 3 route and the routes differ: " +
				"user-rejected is an instruction problem (the agent proposed the wrong action), while " +
				"permission-denied and classifier-denied are permission-config problems (the action " +
				"was right and the approver did not know it). Matched on the leading text of the " +
				"error body, which the harness generates from a fixed template — unlike " +
				"`hook-rejections`, which reads a structured payload and is therefore the more " +
				"durable of the two.\n" +
				"BEFORE READING user-rejected AS A MISTAKE COUNT, SPLIT IT BY tool_name. Measured " +
				"corpus-wide, 51 of the 61 user-rejected rows are AskUserQuestion, and that is what " +
				"the harness records whenever the person types their own answer instead of picking an " +
				"offered option — an interaction pattern, not an agent error. The remaining 10 (5 " +
				"Bash, 5 ExitPlanMode) are the ones where a person refused an action the agent " +
				"proposed. Reporting all 61 as rejected work overstates the class six-fold.",
			Window: true,
			SQL: `
SELECT CASE
         WHEN r.content LIKE 'The user doesn''t want to proceed%'       THEN 'user-rejected'
         WHEN r.content LIKE 'Permission for this tool use was denied%' THEN 'permission-denied'
         WHEN r.content LIKE 'Permission for this action was denied%'   THEN 'classifier-denied'
         ELSE 'other'
       END                           AS kind,
       e.session_id                  AS session_id,
       COALESCE(e.is_sidechain, 0)   AS is_sidechain,
       c.tool_name                   AS tool_name,
       c.lead_cmd                    AS lead_cmd,
       r.path                        AS path,
       r.seq                         AS seq,
       e.ts                          AS ts,
       r.signature                   AS signature
FROM tool_results r
JOIN events e ON e.path = r.path AND e.seq = r.seq
LEFT JOIN tool_calls c ON c.tool_use_id = r.tool_use_id
WHERE r.is_error = 1
  AND (r.content LIKE 'The user doesn''t want to proceed%'
    OR r.content LIKE 'Permission for this tool use was denied%'
    OR r.content LIKE 'Permission for this action was denied%')
  AND ` + win("e") + `
ORDER BY r.path, r.seq`,
		},
		{
			Name:    "undo-signatures",
			Version: 1,
			Doc:     "Work that had to be taken back: git undo commands, write-then-delete, Edit reversing an Edit.",
			Notes: "The signal the failure census structurally cannot see — every tool call here " +
				"SUCCEEDED, and the work still had to be undone. Three shapes share one result set, " +
				"discriminated by `kind`, because they are one finding class:\n" +
				"  git-undo       a Bash command containing git checkout --/reset/revert/restore\n" +
				"  write-then-delete  a Write, then a later rm naming that exact path in the same session\n" +
				"  edit-reversal  an Edit whose new_string restores an earlier Edit's old_string\n" +
				"undone_seq points at the work being taken back, so the pair is inspectable. Path " +
				"matching uses instr(), not LIKE: '_' is a LIKE wildcard and paths in this workspace " +
				"are full of underscores, so LIKE would silently over-match. git-undo is the one shape " +
				"that can be a FALSE positive by design — `git reset` inside a legitimate rebase or " +
				"cleanup is not an undo of the agent's own work, so Tier 2 must read the context.",
			Window: true,
			SQL: `
WITH edits AS (
  SELECT c.tool_use_id AS tool_use_id, c.path AS path, c.seq AS seq, c.tool_name AS tool_name,
         e.session_id AS session_id, COALESCE(e.is_sidechain, 0) AS is_sidechain, e.ts AS ts,
         json_extract(c.input_json, '$.file_path')  AS file_path,
         json_extract(c.input_json, '$.old_string') AS old_string,
         json_extract(c.input_json, '$.new_string') AS new_string
  FROM tool_calls c
  JOIN events e ON e.path = c.path AND e.seq = c.seq
  WHERE c.tool_name IN ('Edit','Write','MultiEdit','NotebookEdit') AND ` + win("e") + `
),
bash AS (
  SELECT c.path AS path, c.seq AS seq, e.session_id AS session_id,
         COALESCE(e.is_sidechain, 0) AS is_sidechain, e.ts AS ts,
         c.lead_cmd AS lead_cmd,
         json_extract(c.input_json, '$.command') AS cmd
  FROM tool_calls c
  JOIN events e ON e.path = c.path AND e.seq = c.seq
  WHERE c.tool_name = 'Bash' AND json_extract(c.input_json, '$.command') IS NOT NULL
    AND ` + win("e") + `
)
SELECT 'git-undo' AS kind, session_id, is_sidechain, path, seq, ts,
       substr(cmd, 1, 200) AS target, NULL AS undone_seq, NULL AS undone_ts, NULL AS detail
FROM bash
WHERE (cmd LIKE '%git checkout --%' OR cmd LIKE '%git reset%'
    OR cmd LIKE '%git revert%'      OR cmd LIKE '%git restore%')
  -- The verb must START a command, not merely appear inside one. Without this the
  -- query flags any command that MENTIONS an undo: measured, it matched a sqlite3
  -- heredoc whose SQL contained the literal '%git reset%' as a search pattern, and a
  -- python heredoc that happened to quote a checkout line. Those are text, not
  -- undone work, and Tier 2 should not have to spend a model call rejecting them.
  -- lead_cmd is checked first because it is peeled at ingest, so a sudo-prefixed or
  -- VAR=1-prefixed git reset is recognised without enumerating those prefixes here.
  AND (bash.lead_cmd = 'git'
    OR cmd LIKE 'git %'
    OR cmd LIKE '%; git %'  OR cmd LIKE '%;git %'
    OR cmd LIKE '%&& git %' OR cmd LIKE '%&&git %'
    OR cmd LIKE '%|| git %' OR cmd LIKE '%||git %'
    OR cmd LIKE '%' || char(10) || 'git %')
UNION ALL
SELECT 'write-then-delete', w.session_id, w.is_sidechain, b.path, b.seq, b.ts,
       w.file_path, w.seq, w.ts, substr(b.cmd, 1, 200)
FROM edits w
JOIN bash b ON b.path = w.path AND b.session_id IS w.session_id AND b.seq > w.seq
WHERE w.tool_name = 'Write' AND w.file_path IS NOT NULL
  AND b.cmd LIKE '%rm %' AND instr(b.cmd, w.file_path) > 0
UNION ALL
SELECT 'edit-reversal', b.session_id, b.is_sidechain, b.path, b.seq, b.ts,
       b.file_path, a.seq, a.ts, NULL
FROM edits a
JOIN edits b ON b.path = a.path AND b.session_id IS a.session_id AND b.seq > a.seq
            AND b.file_path IS a.file_path
WHERE a.tool_name = 'Edit' AND b.tool_name = 'Edit'
  AND a.old_string IS NOT NULL AND a.old_string <> ''
  AND b.new_string IS a.old_string
ORDER BY path, seq, kind`,
		},
		{
			Name:    "file-churn",
			Version: 1,
			Doc:     "One file edited N or more times inside one session — the first version was wrong N-1 times.",
			Notes: "N DEFAULTS TO 5, and the number is a budget decision rather than a truth about " +
				"editing. Measured over the whole corpus there are 4,060 (session, file) groups and " +
				"NO elbow to cut at: the counts fall off smoothly (2,475 groups at 1 edit, 690 at 2, " +
				"346 at 3, 179 at 4, 115 at 5, 73 at 6, then a long tail to 48). Iterating two or " +
				"three times on a file is ordinary work, so a low N would flood Tier 2 with normal " +
				"editing; N=5 yields 370 groups, the same order of magnitude as the other Tier 1 " +
				"signals, which keeps the semantic pass affordable. Raise it to sharpen precision, " +
				"lower it to trade cost for recall. A FAILED edit retried is a different signal and " +
				"belongs to `retry-chains`; this query counts calls whether or not they errored, " +
				"because the expensive case is the one where every call succeeded. Grouping is by " +
				"(transcript file, session, edited file) — seq is a per-file line ordinal, so a " +
				"first/last seq spanning two transcripts would be meaningless.",
			Params: []Param{{Name: "n", Doc: "minimum edits to one file in one session", Default: "5", Numeric: true}},
			Window: true,
			SQL: `
WITH edits AS (
  SELECT e.session_id AS session_id, COALESCE(e.is_sidechain, 0) AS is_sidechain,
         c.path AS path, c.seq AS seq, e.ts AS ts, c.tool_name AS tool_name,
         json_extract(c.input_json, '$.file_path') AS file_path
  FROM tool_calls c
  JOIN events e ON e.path = c.path AND e.seq = c.seq
  WHERE c.tool_name IN ('Edit','Write','MultiEdit','NotebookEdit')
    AND json_extract(c.input_json, '$.file_path') IS NOT NULL
    AND ` + win("e") + `
)
SELECT session_id                                            AS session_id,
       MAX(is_sidechain)                                     AS is_sidechain,
       path                                                  AS path,
       file_path                                              AS file_path,
       COUNT(*)                                              AS edits,
       SUM(CASE WHEN tool_name = 'Write' THEN 1 ELSE 0 END)   AS writes,
       MIN(seq)                                              AS first_seq,
       MAX(seq)                                              AS last_seq,
       MIN(ts)                                               AS first_ts,
       MAX(ts)                                               AS last_ts
FROM edits
GROUP BY path, session_id, file_path
HAVING COUNT(*) >= :n
ORDER BY edits DESC, file_path, path`,
		},
		{
			Name:    "escaping-retries",
			Version: 1,
			Doc:     "The same Bash command re-issued differing ONLY in whitespace, quoting or backslashes.",
			Notes: "A retry that changed nothing the shell cares about is an agent guessing at syntax " +
				"rather than reading the error. Distinct from `retry-chains`, which pairs a FAILED " +
				"call with any later same-tool call: this one requires the two command strings to be " +
				"byte-different but identical after removing spaces, tabs, newlines, single and " +
				"double quotes and backslashes, and it does not require the first call to have " +
				"errored. N defaults to 40 line ordinals — wider than retry-chains' 6 because an " +
				"escaping fight usually runs across several intervening turns of reading and " +
				"narration, and still narrow enough to exclude the same command re-run in a genuinely " +
				"later phase of the session. Scoped to the same session AND transcript file, like " +
				"every other seq-gap query here.",
			Params: []Param{{Name: "n", Doc: "max seq gap between the two issues", Default: "40", Numeric: true}},
			Window: true,
			SQL: `
WITH cmds AS (
  SELECT c.path AS path, c.seq AS seq, e.session_id AS session_id,
         COALESCE(e.is_sidechain, 0) AS is_sidechain, e.ts AS ts,
         json_extract(c.input_json, '$.command') AS cmd,
         COALESCE(r.is_error, -1) AS is_error
  FROM tool_calls c
  JOIN events e ON e.path = c.path AND e.seq = c.seq
  LEFT JOIN tool_results r ON r.tool_use_id = c.tool_use_id
  WHERE c.tool_name = 'Bash' AND json_extract(c.input_json, '$.command') IS NOT NULL
    AND ` + win("e") + `
),
norm AS (
  SELECT path, seq, session_id, is_sidechain, ts, cmd, is_error,
         replace(replace(replace(replace(replace(replace(
           cmd, ' ', ''), char(9), ''), char(10), ''), '"', ''), '''', ''), char(92), '') AS key
  FROM cmds
)
SELECT a.session_id            AS session_id,
       a.is_sidechain          AS is_sidechain,
       a.path                  AS path,
       a.seq                   AS first_seq,
       b.seq                   AS retry_seq,
       b.seq - a.seq           AS seq_gap,
       a.is_error              AS first_is_error,
       b.is_error              AS retry_is_error,
       a.ts                    AS ts,
       b.ts                    AS retry_ts,
       substr(a.cmd, 1, 200)   AS first_cmd,
       substr(b.cmd, 1, 200)   AS retry_cmd
FROM norm a
JOIN norm b ON b.path = a.path AND b.session_id IS a.session_id
           AND b.seq > a.seq AND b.seq <= a.seq + :n
           AND b.key = a.key AND b.cmd <> a.cmd
WHERE a.key <> ''
ORDER BY a.path, a.seq, b.seq`,
		},
		{
			Name:    "ack-markers",
			Version: 1,
			Doc:     "Assistant acknowledgments: the M-1 `Correction:` stem, plus the older ack phrases as a SUPPLEMENTARY signal.",
			Notes: "SUPPLEMENTARY BY CONSTRUCTION — never a primary detector, and never a mistake RATE. " +
				"Three things this cannot tell you, all of which have bitten this analysis before:\n" +
				"  (1) It fires only when the agent NOTICED and said so. The expensive mistakes are " +
				"the ones never realised, which emit nothing, so the metric is an ACKNOWLEDGED " +
				"mistake rate — reporting it as a mistake rate would make agents getting quieter look " +
				"like agents getting better.\n" +
				"  (2) The ack_phrase vocabulary is shaped by the harness system prompt, which " +
				"suppresses apology ('sorry' and 'i was wrong' both measured 0 occurrences). A prompt " +
				"change will move it for reasons that have nothing to do with mistakes, which is why " +
				"criterion 4's evaluation is re-runnable rather than a one-off.\n" +
				"  (3) The `Correction:` stem is FORWARD-ONLY (rules M-1..M-3, landed 2026-07-30, " +
				"inert until applied), so its series is structurally zero before that date. A rise " +
				"across that boundary is a MARKING artifact, not a rise in mistakes: M-2 forbids the " +
				"rule from changing acknowledgment FREQUENCY at all. Never read the boundary as " +
				"behavioural.\n" +
				"`provenance` is derived STRUCTURALLY, not lexically — from the promptSource of the " +
				"nearest preceding user record — so no second marker phrase is needed to tell " +
				"user-caught (a person spent attention) from self-caught (one round trip).",
			Window: true,
			SQL: `
SELECT CASE WHEN a.text LIKE 'Correction:%'
              OR a.text LIKE '%' || char(10) || 'Correction:%'
            THEN 'correction-marker' ELSE 'ack-phrase' END          AS kind,
       CASE WHEN pu.prompt_source IN ('typed','queued')
            THEN 'user-caught' ELSE 'self-caught' END               AS provenance,
       e.session_id                                                 AS session_id,
       COALESCE(e.is_sidechain, 0)                                  AS is_sidechain,
       a.path                                                       AS path,
       a.seq                                                        AS seq,
       e.ts                                                         AS ts,
       substr(a.text, 1, 200)                                       AS excerpt
FROM assistant_text a
JOIN events e ON e.path = a.path AND e.seq = a.seq
LEFT JOIN events pu ON pu.path = a.path AND pu.seq = (
  SELECT MAX(x.seq) FROM events x
  WHERE x.path = a.path AND x.seq < a.seq AND x.type = 'user'
)
WHERE (a.text LIKE 'Correction:%'
    OR a.text LIKE '%' || char(10) || 'Correction:%'
    OR lower(a.text) LIKE '%you''re right%'
    OR lower(a.text) LIKE '%you are right%'
    OR lower(a.text) LIKE '%good catch%'
    OR lower(a.text) LIKE '%my mistake%'
    OR lower(a.text) LIKE '%i was wrong%')
  AND ` + win("e") + `
ORDER BY a.path, a.seq`,
		},
		{
			Name:    "coverage",
			Version: 1,
			Doc:     "Indexed coverage of the corpus — the proof behind every other number.",
			Notes: "T-2/T-15: lines_bad makes malformed input countable rather than assumed, and " +
				"complete_files distinguishes files whose ingest is FINAL from files still being " +
				"appended. If indexed_bytes < corpus_bytes the remaining bytes are an unparsed tail, " +
				"not a loss.",
			SQL: `
SELECT COUNT(*)                        AS files,
       SUM(complete)                   AS complete_files,
       COUNT(*) - SUM(complete)        AS open_files,
       SUM(lines_ok)                   AS lines_ok,
       SUM(lines_bad)                  AS lines_bad,
       SUM(size)                       AS corpus_bytes,
       SUM(resume_offset)              AS indexed_bytes
FROM files`,
		},
	}
	m := make(map[string]Query, len(qs))
	for _, q := range qs {
		m[q.Name] = q
	}
	return m
}()

// Lookup returns a canned query by name.
func Lookup(name string) (Query, error) {
	q, ok := registry[name]
	if !ok {
		return Query{}, fmt.Errorf("unknown query %q (run `pg-ccaudit queries` to list them)", name)
	}
	return q, nil
}

// Names lists every canned query, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// All lists every canned query in name order.
func All() []Query {
	out := make([]Query, 0, len(registry))
	for _, n := range Names() {
		out = append(out, registry[n])
	}
	return out
}

// Usage renders the invocation form, e.g. `retry-chains [n]`.
func (q Query) Usage() string {
	var sb strings.Builder
	sb.WriteString(q.Name)
	for _, p := range q.Params {
		fmt.Fprintf(&sb, " [%s=%s]", p.Name, p.Default)
	}
	return sb.String()
}
