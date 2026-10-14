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
core-table row, same grouping and order, with two exceptions. The mappings only mode `reconcile`
and mode `report` use — `amend-design`, "Reconcile restamps" (the restamp mechanics of
`write-metadata`) and `append-metric` / `read-metrics` — live in
`references/reconcile-and-report.md`; load it only when running those modes, never for mode
`decompose`. And a "Children list" heading, which no core-table row names, holds the lightweight
children read that mode `decompose` step 3's RESUME dedup and both of those modes use, so it stays
here. Empirical facts were probed 2026-08-27 against
bd 1.0.x on this workspace (commands shown); the workspace now runs bd 1.3.1 and these
probes have NOT been re-run on it (only the epic/task `blocks` edge check was, 2026-10-09; see
`epic-decompose`'s Wire step); re-verify on a bd major bump.

## `find-docket`

`bd list --label docket --status all -n 0 --json`, match each bead's `.metadata.pd_source`
against the design source. `-n 0` and `--status all` are load-bearing: the default caps at 50
rows AND hides closed beads.

**`pd_phase` comparison** (the rule — EXACT-STRING, never substring/prefix — is core skill
step 1's; `pg2-2sk88`): in `jq` or shell write `.metadata.pd_phase == "released"` and
`== "released:partial"` as two distinct cases; `pd_phase | contains("released")` or a bare
prefix test matches BOTH literals and silently routes a partial release into the "nothing to do"
branch.

**Partial decomposition needs a branch** (`pg2-2sk88`; an earlier revision of this section
claimed it did not, reasoning that `epic-decompose` mints a distinct `pd_source` per phase —
true only for phasing decided UPFRONT through `epic-decompose`): a `plan-decompose` docket that
sanctions leaving design elements out mid-decomposition (core skill step 7(a)) keeps its
ORIGINAL, unqualified `pd_source` yet has a non-empty not-decomposed list at release. Core
skill step 1 routes on the `pd_phase` literal (`released` vs `released:partial`, written by
step 10), never on whether `epic-decompose` ran.

**Minting `<design-source>#remainder<n>`** (core skill step 1, when a `released:partial` hit
has unchanged design text): `bd list --label docket --status all -n 0 --json`, filter
`.metadata.pd_source` for the prefix `<design-source>#remainder`, and take the next unused
integer suffix — the same minting discipline `epic-decompose` uses for
`<program-epic>#phase<n>`, just scoped to one docket's leftover slice instead of a whole
program epic's phase split. `create-docket` then runs normally against that new `pd_source`,
with the deferred design slice (read from the partial docket's not-decomposed list / report)
as the new docket's design text.

## `create-docket`

Epic bead, label `docket`; design of record VERBATIM in the DESIGN field (`--design-file`).
Field cap: description/design cap at 65,535 bytes (previously verified in this workspace); a
larger design chunks into `bd comment --file` parts with the DESIGN field holding the header
plus a numbered index (`part <n>/<m>: comment <id>`) that `read-docket-design` follows in
order. Produce the chunk files with the core skill's "Helper scripts" `chunk-for-bd-field.sh`
(one call over the whole design) rather than computing byte offsets by hand. Put a container
note in the description: "do not claim this container bead for direct work". Drain's
`--exclude-type epic` keeps epics out of its queue — that exclusion lives in drain, NOT in bd
(`bd ready` itself returns epics).

**Phase-bead usage** (epic-decompose/phase-decompose): epic-decomposer reuses this same
operation AS-IS to create each phase bead — with the full field set plan-decompose's own step 2
sets on a freshly-created docket (`pd_rev=1`, `pd_phase` set explicitly to `precheck`, never
left absent; `pd_source` set to the synthetic `<program-epic>#phase<n>` form; sizing-policy
metadata), plus `--no-inherit-labels` and an explicit `--label docket,phase`. There is no
separate "adopt an existing bead as a docket" operation — the phase bead is a docket from the
moment it is created.

## `create-packet`

Run the helper script — do NOT hand-type a `bd create`/`bd defer` pair for a packet. The
`--body-file` is the packet's ASSEMBLED body (core skill step 3, "Exact excerpts"), never the
draft that holds directives:

```bash
claude-marketplace/plan-decompose/scripts/create-packet.sh \
  --parent <epic> --title "<title>" --body-file <content-file> \
  --acceptance-file <acceptance-file> --metadata '<json>' --actor "<session-id>[-<role>]"
```

The acceptance criteria live ONLY in the acceptance field: the script runs the shared acceptance
guard (`scripts/lib/acceptance-guard.bash`) before any bd call and exits 3 when the body repeats
them (an "Acceptance criteria" heading followed by a checklist, or 2 or more whole acceptance
lines appearing as whole lines in the body).

`--actor` is the caller's explicit agent identity (B-5): the script passes it to BOTH the inner
`bd create` and `bd defer`. A caller MUST pass it. Without it `bd` records its default actor
(`BEADS_ACTOR`, else `git user.name`), which in this workspace is the operator's own name, so
packets created by an agent would be indistinguishable from the operator's in the audit trail
(`pg2-0pnww`).

`--no-inherit-labels` is baked into this script as its DEFAULT — it is not a flag typed at
the call site, so it cannot be dropped by accident. Without it, a packet would inherit the
`docket` label (or a `human` label, on a `human`-labeled parent) and break `find-docket`'s
label-based epic scan; the script's `--allow-inherit-labels` flag is the only way to opt back
in, and it must be named explicitly — omission can no longer produce the leak. The script
also runs the `bd defer <new-id>` step immediately after create — HELD is status-based
(probed 2026-08-27: deferred issues are absent from `bd ready` and `bd undefer` restores
`open`). Do NOT use `--defer <date>` — that is a TIMER that expires whether or not curation
finished. The create-then-defer window is one script call wide, not a two-command sequence a
caller has to remember to chain.

**Why a script** (`pg2-oc52e`/`pg2-b439c`): a raw `bd create` shown as prose, even with
`--no-inherit-labels` in the example, was still dropped from hand-typed invocations more than
once (`pg2-2j5ac`'s children `.5`-`.10`, then `pg2-84o3m.31`); the script removes the flag from
the call site's typing surface. Still run
`claude-marketplace/plan-decompose/scripts/audit-docket-label-leak.sh` after any batch of
`create-packet` calls (read-only; calls `bd list --label docket --status all -n 0 --json` and
flags every returned bead whose `issue_type` is not `epic`): it is a second net that also catches
a leak from an operation that still calls `bd create` directly (`create-docket`'s phase-bead
usage above). Fix a flagged bead with `bd label remove <id> docket` (never re-run
`create-packet` for it).

**Label convention** (epic-decompose/phase-decompose): the same `--parent`-label-inheritance
hazard this note already flags applies to phase and trigger beads too — phase beads carry both
`docket` and `phase` labels; trigger beads carry `phase-trigger`. Which label(s) land on a bead
is controlled by the explicit `--label`/`--no-inherit-labels` flags passed at creation, never by
the parent's own labels.

**Revising an already-created packet's content** (core skill mode `decompose` steps 5-7's
loop-backs to step 3; incident on docket `tc-rjzd3`, fixed as `tc-g7tmr`):
`claude-marketplace/plan-decompose/scripts/update-packet.sh --id <packet-id> --body-file
<fixed-content-file> [--acceptance-file <file>] --actor "<session-id>[-<role>]"` (a re-assembled
body) — never a raw `bd update`, never `create-packet` again. It issues ONE `bd update <packet-id>
--body-file <file>` (plus `--acceptance` from `--acceptance-file`), after the acceptance guard run
against the given acceptance file or, without one, the stored acceptance read with
`bd show <packet-id> --json` (the `.data` envelope or bare array unwrapped; key
`acceptance_criteria`, absent when empty). It rejects every other flag, so it cannot stamp, relabel
or defer; `create-packet` runs once per packet. The stamp rule that forbids `create-packet` again and any `--set-metadata
pd_curated_rev=...` here is the core skill's "Curation stamp". The restamps mode `reconcile`
performs are in "Reconcile restamps" in `references/reconcile-and-report.md`.

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
`pd_rev`. Remove a key with `bd update <id> --unset-metadata <key>` (listed by
`bd update --help`, bd 1.2.2) — this is how `pd_stale` is cleared.

## Children list

`bd list --parent <docket> --status all -n 0 --json` — the lightweight children read: mode
`decompose` step 3's RESUME dedup, mode `reconcile` and mode `report` all use it. Rows sit under
`.data[]`, each with `id`, `status`, `assignee`, `metadata` and `comment_count` (probed
2026-09-30, bd 1.2.2) and (probed 2026-10-01) also `description` and `acceptance_criteria`, but
never comment bodies, so one unbounded call is cheap regardless of docket size; a caller reads
only the fields it needs. `metadata` DOES appear as a JSON key on `bd show`/`bd list --json` once
any key is set on that issue (probed via a scratch bead) but is OMITTED entirely, not `null`, when
empty; check for key presence, not truthiness. `-n 0` and `--status all` are load-bearing here as
in `find-docket`.

## `release-set`

Per packet: `bd undefer <id>`, updating docket `pd_phase` to `releasing:<n>/<m>` as the
sweep proceeds, then `released` — or `released:partial` when the decomposition report's
not-decomposed list is non-empty at that moment (core skill step 10) — via
`bd update <docket-id> --set-metadata pd_phase=released:partial`, the same generic
`write-metadata` mapping as any other `pd_phase` transition; no new bd mechanics, just a new
literal value. Assignee-based holding DOES NOT WORK: `bd ready` has no assignee filter (`-u`
is opt-in), so an open-but-assigned bead is claimable.

## `write-report`

`bd comment <target-bead> --file <report.md>` — the docket epic, or the named tracking bead
for pre-docket gap reports. Comments are append-only and immune to bd's whole-row-clobber
concurrency failure; use `--file` (with a quoted heredoc) when the text carries backticks or
dollar signs.

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
