<!-- Loaded by plan-decompose SKILL.md "Other modes" before running mode `report`. Mode `decompose`'s steps are in SKILL.md. The beads mappings for `read-metrics` and the estimate side are in plan-decompose-beads `references/reconcile-and-report.md`. -->

## Mode `report` — aggregate one docket's metrics (D10)

Reads a docket's children plus their `pd_metrics` comments and reports **escalation rate**,
**validation-retry rate**, **actual-vs-estimated budget**, and **stuck/released counts** — the
signals named in the design's metrics table. Read-only: it writes nothing unless the caller
explicitly asks for a durable copy (step 5).

**Scope is mandatory and singular.** This mode takes exactly one docket id and reports on that
docket ALONE — it MUST NOT default to a workspace-wide scan. Unlike `find-docket` (used by the
other modes to dedup across ALL dockets), `report` never enumerates dockets: given no id, or an
id that is not a docket (`read-metadata` shows no `pd_rev`), it refuses rather than guessing a
target.

1. **Resolve the binding** (as in every mode) and confirm the argument is a docket (`pd_rev`
   set in its metadata); refuse otherwise.
2. **Locate the estimate side once.** Read the docket's decomposition/release report (the
   `write-report` comment from mode `decompose` step 10 (SKILL.md)) and any later `reconcile` step-6 reports,
   and take each packet's fixed-read/budget estimate from the MOST RECENT report that carries
   one for it (a reconcile report estimates only the packets it re-curated). A
   docket with no such report has no "estimated" side for step 4c — say so plainly rather than
   inventing a number; the other three metrics are unaffected.
3. **Page over the children — never load the whole docket at once.** List the docket's
   children ids once (a lightweight, structured read: id/status/metadata/comment-count only,
   never comment bodies or description text), then walk that list in bounded pages (binding
   default: 20 ids/page). For each page, fold its contribution into RUNNING totals before
   advancing to the next page — never hold every child's full comment thread in context at
   once. A child whose comment count is already known to be zero contributes to `no-record`
   without an extra read.
4. **Per child in a page**, read its `pd_metrics` comments (`read-metrics`; one record per
   closeout attempt — a packet re-claimed after a stuck-release carries more than one, and
   each record is its own curation-quality data point per the design's metrics table, so count
   RECORDS, not packets). Parse the fixed key order (`outcome`, `escalation-reads`,
   `validation-retries`, `tokens`); a malformed line is skipped and counted as `unparsed`,
   never guessed at. Fold in:
   a. **escalation rate** = records with `escalation-reads > 0` ÷ total records parsed
   b. **validation-retry rate** = records with `validation-retries > 0` ÷ total records parsed
   c. **actual-vs-estimated budget** = for every packet with both an actual `tokens` value
   (its latest record) and a step-2 estimate: `actual ÷ estimated`; report the aggregate
   `Σactual ÷ Σestimated` plus every packet missing either side, named, never dropped
   d. **stuck/released counts** = records with `outcome=blocked` ("stuck" — validation never
   passed) and records with `outcome=released` (escalated to the docket design and still
   insufficient, handed back) respectively. `outcome=done` MAY be reported alongside for
   context; it is not one of the two counts asked for.
5. **Report.** Return the four metrics, the docket id they were scoped to, total children
   seen, and the `no-record`/`unparsed` counts, to the caller. This mode does not itself
   `write-report` — every ad-hoc query would otherwise spam the docket with a comment; a
   caller that wants a durable copy calls `write-report(docket, report)` explicitly (the same
   op `decompose`/`reconcile` use for their own reports).
