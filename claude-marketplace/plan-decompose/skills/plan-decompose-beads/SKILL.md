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
order. Put a container note in the description: "do not claim this container bead for direct
work". Drain's `--exclude-type epic` keeps epics out of its queue — that exclusion lives in
drain, NOT in bd (`bd ready` itself returns epics).

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

Append: `bd comment <packet-id> "<pd_metrics record>"`. Read: `bd comments <id> --json` over
the docket's children (aggregation, when built, MUST scope by docket and paginate).

## `amend-design`

Rewrite the DESIGN field (superseded text struck/rewritten, the ruling recorded verbatim —
never two live instructions); for chunked designs, rewrite the header/index and APPEND
new-revision chunks, striking superseded chunks in the index (comments cannot be edited);
bump `pd_rev` via `--set-metadata`.

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
