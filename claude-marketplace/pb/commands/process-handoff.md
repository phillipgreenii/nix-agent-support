---
description: >-
  Find the next handoff bead that needs the operator and get it ready to work. Walks ready handoff
  beads (`human`-labelled first) handing each to a `pb:handoff-triager` subagent that absorbs and
  closes it when nothing is left for a person, and otherwise rewrites it to list only the person
  steps. The walk stops at the first handoff that needs the operator, and this session works
  those steps with them.
argument-hint: "[a handoff bead id, and/or hints such as which beads or topics to skip]"
---

# /process-handoff

You are the OUTER session, and the operator is present. The loop below exists to find ONE handoff
that genuinely needs the operator while clearing the ones that do not, so a drain agent or another
mechanism can take what remains. It is NOT a loop over every handoff.

**Nobody here works the beads.** Neither this session nor the triage subagent implements an item.
The subagent only researches and updates beads (comment, create, label, rewrite, close). This
session only guides the operator through the handoff's person-steps once the walk stops.
Everything an agent could do is filed as a bead for `/drain-beads` or another mechanism.

The contract for handoff beads is the `beads-lifecycle:handoff-bead` skill. **Fit with that
skill:** its rule that a `human`-labelled handoff is left alone exists to keep drain and other
unattended claim loops from picking one up. This command is not that. It never works what needs a
person, it hands those steps to this operator session, and it treats a `human` label on a handoff
as a claim to verify (it may be there by error). Where nothing is left for a person the subagent
absorbs and closes the handoff, where person-steps remain it rewrites the handoff to list only
those, and breaking the non-human parts out into beads is fine.

## Command arguments

The command arguments are free text and MAY be empty (the value substituted here is:
`$ARGUMENTS`). When that is empty there are no arguments: walk as usual. Read them for intent:

- A bead id the operator wants processed: process exactly that handoff, with no walk before or
  after it.
- Hints about the walk, such as beads to skip or topics to prefer or avoid: apply them as extra
  client-side filters or ordering on the selection below. A bead the hints say to skip goes in
  `processed` at startup.
- Hints MUST NOT override a rule in this file or in the skills it names (claim hygiene, the
  triage subagent's checks, no working beads here).

## Startup / resume (survives compaction)

1. Mark this session's mode (best-effort, `|| true`; a missing or broken `session-mode` tool
   MUST NOT block the work): when the command arguments are empty, run
   `session-mode start process-handoff --force`; when they are 24 characters or fewer, run
   `session-mode start process-handoff --force --detail "<the arguments>"`; otherwise pass a
   few-word summary of them as `--detail`.
2. Invoke the `beads-lifecycle` skill before any `bd` command that writes, then run `bd prime`.
3. Your actor id: `pb actor --role handoff --scratchpad <your session's own scratchpad dir>`.
   Keep the printed value as ID and pass `--actor "ID"` on every claim, comment, close and
   release (B-5). If `pb actor` rejects the role, STOP and tell the operator; do not substitute
   another role, because another command's resume query would then recover this command's claim.
4. Resolve the tracker root: the directory part of the first line `bd where` prints, as an
   ABSOLUTE path. Print it once so the operator can correct it (A-1). Call it TRACKER_ROOT.
5. Recover a handoff you already hold (a session that ended mid-way leaves it `in_progress` and
   invisible to `bd ready`):

   ```bash
   bd -C "$TRACKER_ROOT" list --status in_progress --assignee "ID" --json -n 0
   ```

   Apply the selection test below to the result. If one matches, re-read its body and resume
   "Work the steps" on it.

6. Start an empty in-memory `processed` set (plus any ids the hints said to skip). It lives in
   conversation context: after compaction, rebuild it from the handoffs you closed or released
   this session, and when in doubt re-triage (the subagent is idempotent).

## Selection (the ordering is the point)

A handoff is a bead of type `handoff` OR one whose title starts with `Handoff` (case-insensitive,
optional hyphen). Select client-side: the `handoff` type is not registered in every tracker, and
`bd ... --type handoff` then errors with `invalid issue type`. Older title forms (`Resume:`,
`NEXT SESSION`, `Continue`) are deliberately not selected: those beads are ordinary work beads.
To recognize another form, extend the pattern below; the subagent still refuses a bead that is
not unmistakably a handoff.

Tier 1 is `human` handoffs and tier 2 the rest (excluding the other person-routed labels). Within
a tier keep `bd`'s returned (priority) order, with ties broken however `bd` breaks them.

```bash
SEL='(if type=="object" and has("data") then .data else . end) | .[] | select((.is_template // false) != true and (.issue_type=="handoff" or (.title|test("^hand-?off\\b";"i")))) | "\(.id)\t\(.status)\t\(.assignee // "")\t\(.title)"'
bd -C "$TRACKER_ROOT" ready --label human --json -n 0 | jq -r "$SEL"
bd -C "$TRACKER_ROOT" ready --exclude-label human,attention,human-focus,human-focus-required --json -n 0 | jq -r "$SEL"
```

Walk tier 1 fully before tier 2 and skip ids in `processed`. The first survivor is the next
handoff. Keep walking until a handoff needs the operator; passing over all of them is a fine
outcome. An id the operator named replaces the walk.

**Gate before claiming**, for a walked or a named candidate: `bd -C "$TRACKER_ROOT" show <id>
--json` MUST show status `open`, an empty assignee, and a match for the selection test. A closed
bead, a non-handoff or a bead someone else holds is reported in one line and skipped (or, for a
named id, the run ends); it is NOT claimed. Then claim it:

```bash
bd -C "$TRACKER_ROOT" update <id> --claim --actor "ID" --json
```

A failed claim means pick again; never retry the same claim unchanged. Add the id to `processed`
the moment you dispatch triage on it, whatever the outcome.

## Triage (one subagent per handoff)

Dispatch ONE subagent per handoff as `subagent_type: pb:handoff-triager`, with a pointer prompt:
the handoff id, TRACKER_ROOT as an absolute path (stated once, A-3) and ID as its actor. It reads
and writes beads only and runs no build. You MUST NOT dispatch two at once (they write to the
tracker) and MUST NOT do its deep check yourself.

You own the claim. After EVERY result, including a malformed one, a crash or a timeout, verify the
tracker rather than trusting the verdict:

```bash
bd -C "$TRACKER_ROOT" show <id> --json
bd -C "$TRACKER_ROOT" comments <id> --json
```

- `NOTHING_FOR_HUMAN`: status MUST be `closed` and an `ABSORBED` trace comment (the skill's format) MUST exist. A
  close without that trace is reported to the operator with the id, and the walk continues.
- `HUMAN_NEEDED`: status MUST be `in_progress` assigned to ID, an `ORIGINAL BODY:` comment MUST
  exist, and the description MUST contain `## Remaining for the operator`. Otherwise treat it as
  `ERROR`.
- Every other outcome: status MUST be `closed`, or `open` with an empty assignee. If neither,
  release it in ONE call: `bd -C "$TRACKER_ROOT" update <id> --status open --assignee "" --actor
"ID"`.

Act on the `VERDICT`:

| VERDICT             | You do                                                                                                   |
| ------------------- | -------------------------------------------------------------------------------------------------------- |
| `NOTHING_FOR_HUMAN` | One line: `<id> closed: <summary>; filed for drain: <ids>`, plus `DELABELED` and `OBSERVED` if not none. |
| `HUMAN_NEEDED`      | STOP walking. Run "Work the steps" with the operator, then "Finish".                                     |
| `NOT_A_HANDOFF`     | One line with `REASON`. Do NOT retype it.                                                                |
| `ERROR`             | Show `REASON`. The handoff stays open and released; remember it for Finish.                              |

After any verdict other than `HUMAN_NEEDED`, select the next handoff, unless the operator named
this one, in which case go to Finish. A result that does not match the subagent's return contract
is an `ERROR`; never guess what it meant.

## Work the steps (the only interactive phase)

The handoff body now lists ONLY the person-steps, and you hold its claim. Present the returned
`STEPS_FOR_HUMAN` in order, each with why only a person can do it and the first step. Add one line
each for `PARKED_HUMAN` (not actionable now, so not worked) and `OBSERVED` (outside the handoff:
offer it, never act on it unasked), and read the handoff body for `MORE` steps if the subagent
reports any.

**Guide the operator; do not perform the steps.** For each one, state what to do and why only a
person can. The operator performs it. You MAY run read-only checks, or a command the operator
explicitly asks you to run. You MUST NOT run `pn workspace apply` or `update`, `sudo`, a
production deploy or a credential action yourself. Record each outcome on the handoff (strike or
remove a finished step in its body). When a step belongs to an existing `human` bead, close or
update THAT bead per the outcome too, so it is not re-presented later. If the operator's answer
creates agent-doable work, file it as a bead.

When every step is done, close the handoff with a reason naming what was done. If the operator
stops with steps unfinished, then BEFORE closing or releasing, each unfinished step MUST live in
its own `human` bead (file one if it has none: self-contained, with `discovered-from` this
handoff) and the handoff body MUST be updated to point at them. Then either close the handoff, or,
if the operator wants to keep it as the work list, release it
(`--status open --assignee "" --actor "ID"`, keeping `human`). A released handoff is picked up
again, first, by the next run; that is intended, and the triage is cheap the second time.

## Finish

Report only what this session did, as one table: handoff id, outcome (closed with nothing for a
person / worked with the operator / not a handoff / could not triage), beads filed for drain,
steps done, steps left. List any handoff that could not be triaged (`ERROR`, including a handoff
too large to triage) under "needs your attention", with its reason. Run
`session-mode set-status finished` (best-effort) and stop.
