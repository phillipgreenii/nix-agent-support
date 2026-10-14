# plan-decompose-beads — reconcile and report mappings

The bd mappings for the operations only mode `reconcile` and mode `report` use: `amend-design`,
"Reconcile restamps" (the restamp mechanics of `write-metadata`), and `append-metric` /
`read-metrics`. Moved out of `SKILL.md` so a mode `decompose` run never loads them; the core
skill's `references/reconcile.md` and `references/report.md` send you here. The "Children list"
mapping both modes read from stays in `SKILL.md`.

## Reconcile restamps

Core skill mode `reconcile` steps 3 and 5: each of the two steps
takes its OWN fresh children read — step 5 MUST NOT reuse step 3's, because consumers may
have claimed, refused, or re-deferred packets in between. The read is
`bd list --parent <docket> --status all -n 0 --json` (probed 2026-09-30, bd 1.2.2: rows sit
under `.data[]`, each with `id`, `status`, `assignee`, `metadata`), skipping
`status == "closed"`. A row is CLAIMED when its `status` is `in_progress`, or its `status` is
`open` with a non-empty `assignee` (neither null nor `""`); a DEFERRED row is never claimed,
even with a leftover assignee — a deferred packet is not being worked. Because a revision may
be stored as a JSON number or a string (see `write-metadata` in `SKILL.md`), compare revisions — for order AND for
equality — through jq `tonumber` on both sides (e.g.
`(.metadata.pd_curated_rev | tonumber) < R`); never lexically (`"10" < "9"` is true) and
never raw (`4 == "4"` is false). Mappings: HOLD a re-curated packet = `bd defer <packet-id>`
plus `bd update <packet-id> --set-metadata pd_curated_rev=<R> --set-metadata pd_stale=<R>`;
restamp an unaffected packet = `bd update <packet-id> --set-metadata pd_curated_rev=<R>`;
clear a marker = `bd update <packet-id> --unset-metadata pd_stale`; release = `bd undefer
<packet-id>`. Read each touched packet back (`read-metadata`) before listing it in the step-6
reconcile report.

## `append-metric` / `read-metrics`

Append: `bd comment <packet-id> "<pd_metrics record>"`.

Read, for mode `report` (scoped to one docket, paginated over its children — probed
2026-08-28 against bd 1.2.2):

- **`--offset` is unreachable from this CLI.** `bd list`'s help documents `--offset` as
  "Only supported under `--proxied-server`", and that flag is not itself exposed:
  `bd list --parent <docket> --status all -n <k> --offset <n> --json` fails with
  `Error: --offset is only supported under --proxied-server`, and adding
  `--proxied-server` fails with `Error: unknown flag: --proxied-server`. So server-side
  offset pagination does not exist here — pagination is done CLIENT-SIDE, over the cheap
  part, never the expensive part:
  1. **Children list, once** — the "Children list" mapping in `SKILL.md`; this procedure
     reads only the id/status/`metadata`/`comment_count` fields and ignores the rest.
  2. **Chunk that id list locally** into pages (default 20) and, for each page, call
     `bd comments <id> --json` for every child IN that page — one call per child, folding
     results into running totals before advancing to the next page. This is the paginated,
     bounded step: it is what "never load the whole docket at once" actually guards, since
     comment threads (not the children list) are the part whose size scales with docket
     history.
  3. **Skip for free**: a child whose `bd list` row already shows `comment_count: 0` needs
     no `bd comments` call at all — count it directly as `no-record`.
- **Estimate side** (mode `report` step 2): `bd comments <docket-id> --json`, find the most
  recent comment matching the `write-report` packet-index shape (from `decompose` step 10 or
  a `reconcile` re-curation report), and read its per-packet fixed-read/budget estimates from
  that comment's text. When more than one such report exists, the estimate for a given
  packet id is the one from the MOST RECENT report that carries an estimate for it — an
  initial release estimate superseded by a reconcile's re-estimate (a reconcile step-6 report
  estimates each packet it re-curated). A reconcile report that lists a packet only as
  restamped-unaffected carries no estimate for it, so it does not supersede an earlier one.

## `amend-design`

Two phases — RESOLVE the merged text, then (only after the core skill's mode-`reconcile`
step 1 pre-check passes it) COMMIT it to bd. Never write to bd before the pre-check runs; the
text resolved here is exactly what gets pre-checked.

**Resolve.** Input is either a **unified diff** against the design text `read-docket-design`
currently returns, or **full replacement text** — the caller states which explicitly; never
infer the shape from content (design prose can itself contain lines starting with `---`/`+++`,
which would misfire a content-sniffing heuristic).

- **No prior revision to diff against** (the docket's design field is empty/absent, or
  `read-metadata` shows no `pd_rev` at all): a diff input MUST be refused with a gap report —
  there is nothing to apply it to. Full-text input MUST be accepted unconditionally and used
  as the resolved text as-is. This is the fallback for the first amend on a docket with no
  design yet.
- **A prior revision exists:**
  - **Full-text input** — the resolved text is the input itself (the pre-existing v1 form;
    always available — diff support does not remove it).
  - **Diff input** — reassemble the current design across chunks when chunked, write it to a
    scratch file, then apply the diff: `patch <scratchfile> < <diff-file>` for a `diff -u`
    hunk, or `git apply --unidiff-zero <diff-file>` against the same scratch file for a
    `git diff --no-index`-style hunk. The apply command's exit status is authoritative: a
    rejected hunk (nonzero exit, a `.rej` file produced) MUST HALT the amendment with a gap
    report — never apply partially or guess past a reject. The patched scratch file's content
    is the resolved text.

**Commit** (only after the pre-check passes the resolved text): rewrite the DESIGN field with
it (superseded text struck/rewritten, the ruling recorded verbatim — never two live
instructions); for chunked designs, rewrite the header/index and APPEND new-revision chunks,
striking superseded chunks in the index (comments cannot be edited); bump `pd_rev` via
`--set-metadata`.
