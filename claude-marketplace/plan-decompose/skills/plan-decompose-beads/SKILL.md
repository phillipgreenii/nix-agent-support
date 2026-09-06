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

**`pd_phase` comparison is EXACT-STRING, never substring/prefix** (`pg2-2sk88`):
`released:partial` contains `released` as a literal substring, so a `jq` filter or shell test
written as `pd_phase | contains("released")` (or a bare prefix check) would match BOTH
literals and silently route a partial release into the core skill's "nothing to do" branch.
Compare the full string — `.metadata.pd_phase == "released"` vs `== "released:partial"` — as
two distinct cases.

**Correction of an earlier claim in this section** (`pg2-2sk88`): a prior revision of this
binding asserted that deliberately-partial decomposition "needs no branch" here, reasoning
that `epic-decompose` always mints a distinct `pd_source` per phase (the synthetic
`<program-epic>#phase<n>` form) before any decomposition runs, so one literal source could
never be invoked twice for two different intended scopes. That reasoning covers ONLY phasing
decided UPFRONT through `epic-decompose`. It does NOT cover a `plan-decompose` docket that,
mid-decomposition (core skill step 3/5), sanctioned leaving some design elements out of that
round's packets on its own initiative (core skill step 7(a)'s "or is recorded via
`write-report` as deliberately not decomposed") — that docket keeps its ORIGINAL, unqualified
`pd_source`; no `epic-decompose` phase-split ever ran; yet its decomposition report's
not-decomposed list is non-empty at release. The core skill's step 1 routing rule DOES need,
and now has, a branch for that: it keys on the docket's `pd_phase` literal
(`released` vs `released:partial`, written by step 10), never on whether `epic-decompose` was
ever invoked.

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
order. Produce the chunk files with the `plan-decomposer` agent's `scripts/chunk-for-bd-field.sh`
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

Run the helper script — do NOT hand-type a `bd create`/`bd defer` pair for a packet:

```bash
claude-marketplace/plan-decompose/scripts/create-packet.sh \
  --parent <epic> --title "<title>" --body-file <content-file> \
  --acceptance "<criteria>" --metadata '<json>'
```

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

**Why a script and not a documented command** (`pg2-oc52e`/`pg2-b439c`): a raw `bd create`
command shown here as prose — even with `--no-inherit-labels` written directly into the
example — was still omitted from hand-typed invocations more than once (`pg2-2j5ac`'s
children `.5`-`.10`, then independently `pg2-84o3m.31`): a caller retyping the command from
memory can drop a flag partway through regardless of how clearly the example reads. Routing
packet creation through this script removes the flag from the call site's typing surface
entirely, rather than relying on the doc being read carefully enough one more time. Still run
`claude-marketplace/plan-decompose/scripts/audit-docket-label-leak.sh` after any batch of
`create-packet` calls anyway (read-only; calls `bd list --label docket --status all -n 0
--json` and flags every returned bead whose `issue_type` is not `epic`) — it is a second,
independent net that also catches a leak from a DIFFERENT operation that still calls `bd
create` directly (`create-docket`'s own phase-bead usage below, which this script does not
cover). Fix a flagged bead with `bd label remove <id> docket` (never re-run `create-packet`
for it).

**Label convention** (epic-decompose/phase-decompose): the same `--parent`-label-inheritance
hazard this note already flags applies to phase and trigger beads too — phase beads carry both
`docket` and `phase` labels; trigger beads carry `phase-trigger`. Which label(s) land on a bead
is controlled by the explicit `--label`/`--no-inherit-labels` flags passed at creation, never by
the parent's own labels.

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
