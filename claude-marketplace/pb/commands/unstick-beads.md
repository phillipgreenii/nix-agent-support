---
description: >-
  Periodic sweep of this pn-workspace's open-but-not-ready beads (dependency-blocked,
  deferred, `human`-labelled but not ready): pre-triages cheaply with jq, then fans the rest
  out to `pb:unstick-batch-worker` subagents. Each worker decides per bead whether to
  undefer, de-label, fix dependencies, or close as stale, and writes a sweep marker so the
  next sweep can skip beads whose reason is still valid. Never asks questions mid-sweep; ends
  with one report of what changed and what needs the operator. Ready `human` beads belong to
  /unblock-human-beads, not here. Accepts optional narrowing $ARGUMENTS.
argument-hint: "[optional narrowing: --label X, an id prefix, or --full to ignore sweep markers]"
---

# /unstick-beads

You are the ORCHESTRATOR of a sweep over every open bead in this pn-workspace (the workspace
containing your current working directory) that is NOT in `bd ready`: blocked by
dependencies, deferred (`defer_until` set or `status=deferred`), or held by a `human` label
behind a blocker. The goal is to unstick what can be unstuck: wrong dependency edges, defers
whose trigger has already happened, `human` labels where no person is actually needed, and
beads that are already done or moot. "Nothing to do" is a normal outcome for most beads.

The operator runs this occasionally and walks away, so you MUST NOT ask questions during the
sweep: anything that needs a person goes into the final report. You and your workers MAY file
new beads for genuine operator questions or discovered bugs, sparingly.

Design: the orchestrator is a **Pipeline** of cheap mechanical stages (inventory → pre-triage
→ cluster → fact sheets) feeding a bounded **worker pool** of `pb:unstick-batch-worker`
subagents, with claim liveness checked in parallel. The orchestrator MUST keep its own context
small. It reads bead data only through jq over the export, and it keeps all state in files.

## Actor id, workspace root, work dir (do this ONCE)

- **ACTOR:** prefer `${CLAUDE_SESSION_ID}-unstick`. If `$CLAUDE_SESSION_ID` is unset, use the
  UUID from this session's own scratchpad path with an `-unstick` suffix.
- **ROOT:** `$PN_WORKSPACE_ROOT` if set, otherwise the nearest ancestor of the cwd containing
  `pn-workspace.toml`. Run every bd command as `bd -C ROOT ...`.
- **WORKDIR:** a FRESH directory `/tmp/bead-unstick-<YYYY-MM-DD>[-N]/`, which MUST NOT reuse an
  earlier one.
  - Create `batches/`, `results/`, `facts/`, `probes/`, and `work/` inside it, plus
    `progress.txt` and `followups.txt`.
  - Record the sweep START time as the first line of `progress.txt`
    (`date -u +%Y-%m-%dT%H:%M:%SZ`).
- Invoke the Skill `beads-lifecycle:beads-lifecycle` once.
- **Shell:** hooks in this environment commonly deny `$(...)` substitution and `cd DIR && cmd`
  compounds. Write intermediate results to files under WORKDIR and pass literal paths.

## Standing permissions (operator-specific)

Read the workspace `CLAUDE.md` at ROOT. If it has a section headed
`## /pb:unstick-beads standing permissions`, its text is PERMISSIONS: pass it VERBATIM to
every worker, and apply it yourself. Otherwise PERMISSIONS is `none`. Permissions MUST NOT be
inferred from anything else.

Typical entries:

- a service that workers are allowed to pause and resume for a verification. The entry MUST
  name the service's status and resume commands, because Stage 8 uses them.
- whether claims held by dead sessions are allowed to be released.
- whether cheap read-only checks are allowed to close verify beads.

## Stage 1 — inventory

Write each result to a file in WORKDIR and record the counts in `progress.txt`.

- **Export:** `bd -C ROOT export -o WORKDIR/export.jsonl`. You MUST pass `-o`: a bare
  `bd export` can clobber the tracker's jsonl.
- **Ready:** `bd -C ROOT ready -n 0 --json`.
- **DRAINABLE:** the subset of ready that `/pb:drain-beads` would actually claim. Exclude
  `human`-labelled beads, epics, and anything else its claim query excludes.
- **Gate check:** run `pb gate check --dry-run --json` from the session cwd (it has no `-C`)
  and write the output to `WORKDIR/probes/gate-check.json`. Workers use it instead of
  hand-comparing gate patch-ids.

Export row shape. It is the ONLY source for claim state, because `bd list` JSON omits
`assignee`.

- Fields are OMITTED when empty, so default every one (`// []`, `// ""`).
- `dependencies` is a list of `{issue_id, depends_on_id, type}`. The `blocks` rows are
  blockers and the `parent-child` rows are the hierarchy.
- Rows also carry `assignee`, `comments`, `notes`, `defer_until`, `acceptance_criteria`,
  `closed_at`, `updated_at`, and `labels`.
- The export includes closed beads, which you need for blocker status. A dependency id missing
  from the export is UNKNOWN, which forces REVIEW; it does not mean closed.

**"Open"** means status `open` or `blocked`.

**TARGETS** = open beads not in ready, plus `status=deferred` beads, narrowed by `$ARGUMENTS`
if given.

- REMOVE from TARGETS any open bead with a non-empty `assignee`; it goes to Stage 2 only.
- `in_progress` beads are never TARGETS; they also go to Stage 2.

## Stage 2 — claim liveness (in parallel with the batches)

The candidates are every `in_progress` bead and every open bead with a non-empty `assignee`.

- If PERMISSIONS do not allow releasing dead claims, list the candidates in the report and do
  nothing else.
- Otherwise dispatch ONE `general-purpose` subagent at the start. It counts toward the
  4-agent cap. Give it `ROOT=<abs> ACTOR=<actor> WORKDIR=<abs>`, the candidate ids, and this
  procedure:

> Prove the claimer is dead BEFORE releasing. The actor id is not always the session id.
>
> 1. Find live sessions in `~/.claude/sessions/<pid>.json` and cross-check them with
>    `ps -axo pid,comm`.
> 2. For each assignee that is not a live session, search the transcripts
>    (`rg -l -F <assignee> ~/.claude/projects`). If a LIVE session's transcript or its
>    `subagents/` files contain `--actor "<assignee>"`, that session owns the claim: keep it.
> 3. Otherwise release in ONE call, with the timestamp taken from
>    `date -u +%Y-%m-%dT%H:%M:%SZ`:
>
>    ```text
>    bd -C ROOT update <id> --status open --assignee "" --append-notes "[unstick <ts>] released: claim by <assignee> dormant since <t>, may resume; worktree: <git cherry and dirty summary, or none>; recheck-when: on-change" --actor ACTOR
>    ```
>
>    Keep any worktree, and never call the release lossless.
>
> Reply one line per bead.

## Stage 3 — pre-triage (jq, in the orchestrator)

Classify every TARGET from `export.jsonl` and write the lists to `WORKDIR/triage-*.txt`. Each
pre-filtered bead gets NO subagent and NO note; it only counts in the report.

**SKIP-live-chain** is computed as a LEAST fixpoint:

1. Seed LIVE with DRAINABLE, plus `in_progress` beads whose `updated_at` is within 24h.
2. Repeatedly add a TARGET to LIVE when all of these hold:
   - it is not `human`-labelled, has no `defer_until`, and is not `status=deferred`;
   - every open `blocks` blocker of it is already in LIVE;
   - neither it nor any of those blockers has a deferred, blocked, or `human`-labelled
     ancestor (by `parent-child`);
   - it is not blocked by its own descendant.
3. A bead in a dependency cycle is never added.

The TARGETS that end up in LIVE are correctly sequenced chains that clear by themselves.

**SKIP-marker-valid** applies unless `$ARGUMENTS` contains `--full`. The bead's newest
well-formed sweep marker matches exactly this grammar:

```text
[unstick YYYY-MM-DDTHH:MM:SSZ] <outcome>: <reason>; recheck-when: <YYYY-MM-DD | <bead-id> closes | on-change>
```

Any other form, including date-only markers, counts as no marker. The bead is skipped when all
of these hold:

- the bead's `updated_at` is within 15 minutes of the marker timestamp, so nothing but the
  marker touched it since;
- no comment is newer than the marker;
- no blocker, parent, or child has `closed_at` or `updated_at` after the marker;
- its `recheck-when` is not yet due:
  - a date in the future;
  - `<bead-id> closes`, where that bead is still open;
  - `on-change`, as long as the conditions above still hold.

**Overrides** send a bead to REVIEW despite a skip rule ONLY when:

- a blocker, parent, or child closed AFTER the bead's newest marker (with no marker: closed in
  the last 7 days);
- its `defer_until` has elapsed;
- it is `status=deferred` with an elapsed or empty `defer_until`.

**REVIEW** is every TARGET not skipped.

## Stage 4 — cluster and batch the REVIEW set

Related beads MUST go to the SAME worker, so a shared question is investigated once. Build
connected components over these links:

- dependency edges (`blocks`, `parent-child`). An edge to a bead outside REVIEW joins the two
  REVIEW beads it connects, but you MUST NOT follow that outside bead's own further edges (one
  hop only). Following them would merge whole epics;
- bead ids referenced in another REVIEW bead's title, description, or notes;
- an identical `defer_until` timestamp, which usually means the same trigger.

Labels MUST NOT be used as links, because shared topic labels collapse most beads into one
component. Use labels only to order singletons.

Pack components into batches of **10–15 beads**.

- A component MUST NOT be split unless it alone exceeds 15.
- Fill leftover space with singletons sorted by repo label, then title.
- Write `batches/B<NN>`, one id per line, and confirm that every REVIEW id is in exactly one
  batch.

## Stage 5 — fact sheets

Write ONE fact file per batch, `WORKDIR/facts/<BATCH>.json`, using a single jq pass over
`export.jsonl` per batch. It is a JSON array with one object per bead, containing:

- the bead row with defaults applied;
- each `blocks` dependency and dependent as id, title, status, labels, `defer_until`, and
  `closed_at`;
- the parent and children as id, title, and status;
- all comments.

Workers read this instead of running most `bd show` / `bd comments` calls.

## Stage 6 — dispatch the worker pool

- Dispatch each batch as `subagent_type: pb:unstick-batch-worker`, in the background, with at
  most **4** agents running at once (Stage 2 included). Higher concurrency has hit API spend
  limits. Refill a slot as soon as an agent hands back.
- Dispatch prompt:

  ```text
  WORKDIR=<abs> BATCH=<name> ACTOR=<actor> ROOT=<abs> PERMISSIONS=<verbatim text or none>.
  Review the beads in WORKDIR/batches/<name> per your agent instructions.
  ```

- On each hand-back:
  - append one line to `progress.txt`;
  - append any `OPERATOR:` / `FOLLOWUP:` lines to `followups.txt`;
  - launch the next batch.
- A completion notification for a hand-back you have ALREADY logged MUST be ignored: no tool
  call, at most one word of output.
- If a hand-back reveals a mistake that affects the whole sweep, put the correction in the
  remaining dispatch prompts, and re-queue the beads that were already mishandled. A
  re-queued bead's new marker supersedes its old one.

## Stage 7 — follow-ups

When the last worker hands back, check `followups.txt`:

- If it has actionable `FOLLOWUP:` items, dispatch ONE more `pb:unstick-batch-worker` with the
  same dispatch prompt and `BATCH=FOLLOWUPS`. Write `batches/FOLLOWUPS` with the referenced
  bead ids, and the matching `facts/FOLLOWUPS.json`. Typical items are a stale gate outside
  every batch, or a fixture to delete.
- `OPERATOR:` items are NOT dispatched; they go straight to the report.

## Stage 8 — verify and report

1. **Services:** for each service PERMISSIONS let workers pause, run its status command
   YOURSELF. Resume it if it is paused, and report it loudly if it is not running.
2. **Counts:** re-run the Stage 1 counts.
3. **Attribution:** THIS sweep's changes are the beads with a marker timestamp at or after the
   recorded start time and a non-`unchanged` outcome, plus beads closed by ACTOR. Everything
   else that moved in that window was done by peer sessions.
4. **Report**, in at most 40 lines:
   - before/after counts with the arithmetic (open, ready, deferred, open-not-ready), split
     into this sweep versus peers;
   - triage counts: live-chain skips, marker skips, reviewed;
   - closed beads, as id plus reason;
   - undeferred, retargeted, de-labelled, and dependency-fixed beads;
   - claims released;
   - new beads filed;
   - **needs you**: one line per item, starting with an action verb (grant, decide, run,
     approve, publish, schedule), naming the bead ids. Beads that need the same action are
     grouped onto one line.

## Rules

- The orchestrator MUST NOT mutate beads itself. The only exception is the Stage 8 service
  check and resume.
- The orchestrator MUST NOT use `/loop` or ScheduleWakeup. Agent hand-backs drive progress.
- Workers inherit the hard prohibitions in their agent definition: no code changes, commits,
  pushes, applies, `sudo`, dolt server starts, or launchd/systemd restarts, and no routing
  around a denied command.
- Every reviewed bead that stays open MUST end the sweep with exactly one marker from this
  run. A closed bead needs none, because its close reason is its record. Pre-filtered beads
  MUST NOT get a marker.
