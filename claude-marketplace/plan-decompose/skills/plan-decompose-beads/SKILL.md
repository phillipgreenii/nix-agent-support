---
name: plan-decompose-beads
description: >-
  The beads (bd) medium binding for the plan-decompose skill — maps its abstract operations
  (find-docket, create-docket, create-packet, wire-ordering, read-packet/read-docket-design,
  read/write-metadata, release-set, write-report, append-metric/read-metrics, amend-design,
  close-docket) onto bd commands. Use ONLY when plan-decompose needs its beads binding; it is
  not a general bd reference and not invoked on its own.
---

# plan-decompose-beads — the bd binding

Maps every abstract operation of the `plan-decompose` core skill onto `bd`, one heading per
core-table row, same grouping and order. Empirical facts were probed 2026-08-27 against
bd 1.0.x on this workspace (commands shown); re-verify on a bd major bump.

## `find-docket`

`bd list --label docket --status all -n 0 --json`, match each bead's `.metadata.pd_source`
against the design source. `-n 0` and `--status all` are load-bearing: the default caps at 50
rows AND hides closed beads.

## `create-docket`

Epic bead, label `docket`; design of record VERBATIM in the DESIGN field (`--design-file`).
Field cap: description/design cap at 65,535 bytes (previously verified in this workspace); a
larger design chunks into `bd comment --file` parts with the DESIGN field holding the header
plus a numbered index (`part <n>/<m>: comment <id>`) that `read-docket-design` follows in
order. Produce the chunk files with the `plan-decomposer` agent's `scripts/chunk-for-bd-field.sh`
(one call over the whole design) rather than computing byte offsets by hand. Put a container
note in the description: "do not claim this container bead for direct work". Drain's
`--exclude-type epic` keeps epics out of its queue — that exclusion lives in drain, NOT in bd
(`bd ready` itself returns epics).

## `create-packet`

```bash
bd create "<title>" -t task --parent <epic> --no-inherit-labels \
  -d "<content>" --acceptance "<criteria>" --metadata '<json>'
bd defer <new-id>   # immediately — HELD is status-based
```

`--no-inherit-labels` is REQUIRED — without it every packet inherits the `docket` label and
breaks `find-docket`. HELD = `bd defer` (status-based, indefinite; probed 2026-08-27:
deferred issues are absent from `bd ready` and `bd undefer` restores `open`). Do NOT use
`--defer <date>` — that is a TIMER that expires whether or not curation finished. The
create-then-defer window is one command wide; acceptable, noted.

## `wire-ordering`

`bd dep add <blocked> --blocked-by <blocker>`, then a `bd dep list <blocked>` read-back on
EVERY edge (a reversed edge blocks the wrong issue silently). `bd dep cycles` after bulk
wiring — it is DATABASE-GLOBAL, so filter its output to this docket's packet ids.

## `read-packet` / `read-docket-design`

`bd show <packet-id>` for packet content. For the docket design, extract the design field
from `bd show <epic> --json` (jq path `.data[0].design`), following the chunk index when
chunked.

## `read-metadata` / `write-metadata`

Read: extract `.data[0].metadata` from `bd show <id> --json` with jq (probed 2026-08-27:
that is the JSON path; piping through jq bounds the context cost — a bead with a 39 KB
design field returned 3 bytes of metadata). Write: `bd update <id> --set-metadata k=v`
(repeatable; REPLACE semantics per key). KEYS MUST match `[a-zA-Z_][a-zA-Z0-9_.]*` —
hyphens are REJECTED by `--set-metadata` while the JSON `--metadata` create form skips that
validation, so a hyphenated key would be un-updatable: use `pd_` underscore keys only
(probed 2026-08-27: `--set-metadata pd-rev=2` errors, `pd_rev=2` succeeds). Compare values
as strings (`k=2` stores a number; create-JSON strings stay strings). Never store
current-value state in notes — notes are append-only narrative and cannot supersede
`pd_rev`.

## `release-set`

Per packet: `bd undefer <id>`, updating docket `pd_phase` to `releasing:<n>/<m>` as the
sweep proceeds, then `released`. Assignee-based holding DOES NOT WORK: `bd ready` has no
assignee filter (`-u` is opt-in), so an open-but-assigned bead is claimable.

## `write-report`

`bd comment <target-bead> --file <report.md>` — the docket epic, or the named tracking bead
for pre-docket gap reports. Comments are append-only and immune to bd's whole-row-clobber
concurrency failure; use `--file` (with a quoted heredoc) when the text carries backticks or
dollar signs.

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
  1. **Children list, once** — `bd list --parent <docket> --status all -n 0 --json`. This
     returns id/status/`metadata`/`comment_count` per child, never description or comment
     bodies, so one unbounded call here is cheap regardless of docket size (confirmed
     `metadata` DOES appear as a JSON key on `bd show`/`bd list --json` once any key is set
     on that issue — probed via a scratch bead — but is OMITTED entirely, not `null`, when
     empty; check for key presence, not truthiness).
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
  packet id is the one from the MOST RECENT report that names it — an initial release
  estimate superseded by a reconcile's re-estimate.

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

## `close-docket`

`bd close <epic> --reason "<outcome>"` once children are closed. bd refuses an epic close
with open children; the documented `--force` override is the operator's, not this skill's.

## Field placement (probed 2026-08-27)

- Packet content → `description` (`-d`). Criteria → the dedicated `--acceptance` field — its
  JSON key is `acceptance_criteria` (NOT `acceptance`), and `bd lint` accepts the field with
  no description heading needed (probed: a task with only `--acceptance` set lints clean).
- Design of record → the epic's `design` field only. Packet beads carry NO design field.
- Metadata → the native metadata field only. The curation stamp, sizing deviations, and
  `pd_stale` are metadata, never description/notes text.

## Hygiene citations (not restated here)

Claims, releases (`--status open --assignee ""` in ONE call), `human` labeling, and
dependency-direction rules follow the workspace's standing bd rules (the B-/D-series in the
always-on agent rules). This binding adds only the mappings above.
