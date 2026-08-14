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
				"out here, so 'no rejections' stays distinguishable from 'no hook data'.",
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
