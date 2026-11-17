---
name: identify-hook-misses
description: Identify the 2-5 most impactful hook miss patterns from the decision database and create beads for each. Use after a week+ of normal Claude Code usage to surface high-impact hook decision patterns worth fixing.
---

# Identify Hook Misses

**Retargeted 2026-09-27 per operator ruling R1** (docket `tc-o14i5.1`): the old Go rule engine
(`internal/rules/*`) is frozen — deletions only, no new fixes. The target architecture retires
per-command Go policy rules in favor of DATA specs (command/path/target specs, P13/P14). This
skill's triage mechanics (Steps 1-7, calibration tiers) are unchanged; only what Phase 2 proposes
has changed: instead of "fix this rule in internal/rules" it now proposes "author/regenerate the
command spec for `<command>` to cover this case."

Analyze the decision database to find patterns where the hook makes wrong decisions. Present the top 2-5 most impactful miss patterns for user review, then create spec-authoring beads for approved patterns.

**Two engines, and a spec feeds only one of them.** The live PreToolUse hook still runs the old
rule chain; command specs (the Go registry and the JSON layers) feed only the NEW effect engine,
which `shapes`/`replay` and `evaluate --corpus` run offline until the cutover (`packages/claude-extended-tool-approver/docs/ARCHITECTURE.md`,
"Effect engine" section; `docs/adr/0075`). So "the hook abstained" (the recorded decision) and
"the spec is missing" are different facts: Step 8 labels each pattern with the engine it needs.
`evaluate` (and `--baseline`) replays the OLD chain, so it cannot show a spec's effect.

**This skill is read-only triage.** It does NOT fix code, modify the DB, or change settings. It creates beads for later deep-dive sessions.

**Target time: 5-10 minutes of attention, plus background jobs (ingest, export, replay) that can run for several minutes each; longer if jq debugging is needed.**

**Scratch files.** Every file this skill writes goes under one directory below `CLAUDE_CODE_TMPDIR`, never `/tmp`. Set it once per shell and reuse it in every block below:

```bash
WORK="${CLAUDE_CODE_TMPDIR:-$TMPDIR}/ceta-hook-misses"
mkdir -p "$WORK"
```

## Terminology

- `hook_decision` — the decision the hook returned (`allow`/`ask`/`deny`/`abstain`); from the `hook_decision` field of `evaluate`/`show` output.
- `category` — `miss-uncaught`, `miss-caught-by-settings`, `correct`, `needs-review`, `unresolved`, `stale-cwd`; from the `category` field of `evaluate`.
- `outcome` — what actually happened to the call (ground truth): `approved`, `denied` (somebody declined it — always carries a reason in `outcome_notes`), `rejected` (CETA itself refused it; nobody was asked), `unresolved` (never resolved — interrupted/abandoned/session died; **NOT a denial** and never a miss), `pending`; from the `outcome` field. Full table: [../references/database-schema.md](../references/database-schema.md#outcomes).
- `settled_by` / `settlement` / `converted_ask` / `grading_cell` — the settlement dimension, present only with `--source tracker` (Step 0): who settled the call (`ceta`, `person`, `mode`, `rule`, `classifier`), how (`approved`, `refused`, `pending`, `unresolved`), whether CETA's ask was converted to a reject in an unattended session (counts as an ask), and the call's cell of the grading table. Full table: [../references/database-schema.md](../references/database-schema.md#who-settled-it---source-tracker).
- `sandbox_enabled` — `0` or `1` (or `null`) indicating whether the OS bash sandbox was active for this invocation; from the `sandbox_enabled` field.
- `command_class` — stable, non-truncated grouping key; the field to bucket "same command" on.
- `replay_result` — the decision the _current_ rule engine returns when replaying the row (`allow`/`ask`/`deny`/`abstain`, or empty for rows the engine did not replay, e.g. `stale-cwd`).
- `approval_source` — derived approval-MECHANISM bucket: `unknown`, `bypass`, `auto`, `settings`, `hook`, `user`. Classifies CONTEXT, not outcome. Under `auto`/`dontAsk` a CETA allow buckets as `hook`, a prompted row as `user`, and only the rest (classifier/unknown, including an un-prompted auto-mode _denial_) as `auto` — use `permission_mode` to select by raw mode.
- `permission_mode` — raw Claude Code permission mode at log time (`default`/`plan`/`acceptEdits`/`dontAsk`/`auto`/`bypassPermissions`), `null` on pre-v5 rows.
- `agent_type` — the subagent that issued the call (e.g. `Explore`), or `null` for the main agent. A SEPARATE axis from `approval_source`.
- `outcome_notes` — free-form resolution notes; carries the `auto_mode_classifier: <reason>` string on an auto-mode denial.
- `tool_response` — the PostToolUse result payload (nested JSON) or `null`; a failed call is signalled by `tool_response.is_error == true`.

Derived analytic terms used by the calibration steps (Steps 4-6 below):

- **APPROVE candidate** — a row where CETA `abstain`ed but the invocation was `approved` (a command CETA could learn to APPROVE). Base filter: `.hook_decision=="abstain" and .outcome=="approved"`.
- **Human (PRIMARY) tier** — APPROVE candidates the HUMAN endorsed on the MAIN agent. Under `--source ceta`: `approval_source=="user"` (a prompt fired and the human approved) with `agent_type==null`. Under `--source tracker`: `approval_source` is `user` only when a person settled the call (`asklog.ApprovalSource` with the tracker's prompt id), and in auto mode a person is prompted only when CETA asks, so "abstain + user" is empty by construction; use grading cell `ask/person/approved` plus `abstain/person` with `settlement=="approved"` instead. This is the strongest evidence for a new APPROVE rule.
- **Weaker tier** — APPROVE candidates approved by a MACHINE or inside a SUBAGENT: `approval_source in (auto,bypass)` OR `agent_type != null`. NOT counted as human endorsement. `subagent` is NOT an `approval_source` value — segment this tier by `approval_source × agent_type`.
- **False-denial** — CETA `abstain`ed and the `auto_mode_classifier` DENIED the call. Under `--source ceta`: `outcome=="denied"` with `auto_mode_classifier:` in `outcome_notes`. Under `--source tracker`: grading cell `abstain/classifier/refused`, where `outcome_notes` holds the raw denial text ("Permission for this action was denied by the Claude Code auto mode classifier. Reason: [...]"), not `auto_mode_classifier:`. Candidate to teach CETA to APPROVE (if the denial was wrong) — read the mined reason first. Discard "gave no verdict" and "judged this action dangerous (it gave no explanation)" texts: they are classifier infrastructure noise, not a risk rationale. A row whose `outcome` is `unresolved` is NOT a false-denial: nobody denied it.
- **False-approval / over-approval** — a command CETA (or auto/bypass mode) let run that is "actually risky". Candidate to teach CETA to ASK/DENY.
- **"Actually risky"** — DEFINED as: the current engine's `replay_result` for the row is `deny` or `ask` (self-consistent; no curated list).
- **"Errored" / "approved-but-errored"** — DEFINED as: `is_error == true` in the slim file Step 1 builds, which takes the top-level `is_error` (the tracker never stores `tool_response`) and falls back to `tool_response.is_error` under `--source ceta`. A missing value is treated as "unknown / not errored".

Full schema: [../references/database-schema.md](../references/database-schema.md). Shared definitions: [../references/terminology.md](../references/terminology.md). Two-way interpretation (APPROVE vs ASK/DENY): [../references/auto-mode-signal.md](../references/auto-mode-signal.md).

## Phase 1: Identify (No Approvals Needed)

All commands in this phase use `claude-extended-tool-approver` or `jq` — no raw sqlite3, no file modifications. The four raw fields the calibration steps consume (`permission_mode`, `agent_type`, `outcome_notes`, `tool_response`) are exposed on `evaluate --format=json`, so the "no raw sqlite3" rule holds; the CLI stays the sanctioned interface.

### Step 0: Pick the source

`evaluate`, `report` and `show` read either CETA's own ask log (`--source ceta`, the default) or the claude-tool-tracker database (`--source tracker`, read-only). The tracker records who settled each call, but this skill needs a week or more of its data. Bring the tracker up to date first. The ingest can run for more than five minutes, so run it as a background job and wait for it:

```bash
bgrun tracker-ingest -- claude-tool-tracker ingest
```

```bash
bgwait tracker-ingest --timeout 540   # Bash timeout 600000; exit 2 = still running, call it again
```

Read the tracker's earliest event from the `event_span` view (`scope` `all`, `earliest_ts` in epoch milliseconds; documented in `packages/claude-tool-tracker/docs/views.md`):

```bash
DB="${XDG_STATE_HOME:-$HOME/.local/state}/claude-tool-tracker/tracker.db"
EARLIEST_MS=$(sqlite3 -readonly "$DB" "SELECT earliest_ts FROM event_span WHERE scope = 'all'")
if [ -n "$EARLIEST_MS" ] && [ "$EARLIEST_MS" -le $(( ($(date +%s) - 7*86400) * 1000 )) ]; then
  SRC="--source tracker"
else
  SRC=""
fi
echo "SRC=[$SRC]"
```

Pass `$SRC` to every `evaluate`, `report` and `show` below. It is `--source tracker` ONLY when the earliest event is at least 7 days old; otherwise it stays empty and the default source `ceta` is used. State which source you used in the findings. The two histories are never mixed, and under the tracker source the ids are the tracker's `call_id`. If `event_span` is empty or the database is missing, use the default source. Steps 3b, 4 (human tier), 5 and 6 below have a tracker variant and a ceta variant; run the one that matches `$SRC`.

### Step 1: Capture the dataset once, slim it, query the slim file

The miss-ranking steps (2-3) run over the misses; the calibration steps (4-7) run over the FULL dataset. Export once, stream the export down to the fields the queries use, and query the slim file in every later step. The export of a tracker window is about 200 MB: never slurp it with `jq -s` and never query it again. `evaluate` replays every row through the old rule chain (slow) and writes about 57 MB of rule log to stderr, so send stderr to `/dev/null`. Write the slim filter once, then run both commands as one background job:

```bash
cat > "$WORK/slim.jq" <<'JQ'
fromstream(1 | truncate_stream(inputs))
| {id, tool_name, command_class, hook_decision, replay_result, replay_module, category, outcome,
   sandbox_enabled, approval_source, permission_mode, agent_type, outcome_notes,
   settled_by, settlement, converted_ask, grading_cell,
   is_error: (.is_error // .tool_response.is_error // false)}
JQ
SLIM="$WORK/slim.jsonl"
```

```bash
bgrun ceta-export -- env WORK="$WORK" SRC="$SRC" bash -c \
  'claude-extended-tool-approver evaluate $SRC --format=json 2>/dev/null > "$WORK/ceta-all.json" \
   && jq -cn --stream -f "$WORK/slim.jq" "$WORK/ceta-all.json" > "$WORK/slim.jsonl"'
```

```bash
bgwait ceta-export --timeout 540   # Bash timeout 600000; trust only the DONE exit=0 line
```

`slim.jsonl` is one JSON object per line, so every query below reads it with `jq -s`. The misses are the rows whose `category` starts with `miss` (`select(.category | test("^miss"))`); there is no separate `--misses-only` export. Check the size:

```bash
jq -s 'length, ([.[] | select(.category | test("^miss"))] | length)' "$SLIM"
```

If zero misses, skip Steps 2-3 and go straight to the calibration steps (4-7).

Keep the shapes and replay files Step 8 produces (`$WORK/shapes.jsonl`, `$WORK/replay.jsonl`): they are the "before" side of Phase 2's before/after replay diff. Start them now, in the background, while you read the rest of Phase 1 (they read the same database, read-only, and take several minutes):

```bash
SHAPE_SRC="${SRC:---source ceta}"   # shapes/replay default to the tracker, so always pass the source
bgrun ceta-shapes -- env WORK="$WORK" SHAPE_SRC="$SHAPE_SRC" bash -c \
  'claude-extended-tool-approver shapes $SHAPE_SRC --out "$WORK/shapes.jsonl" \
   && claude-extended-tool-approver replay $SHAPE_SRC --shapes "$WORK/shapes.jsonl" --out "$WORK/replay.jsonl"'
```

`shapes` and `replay` default to the last 30 days; pass the same `--since YYYY-MM-DD` to both when the window should differ.

### Step 2: Group by pattern and rank

**Under `--source ceta`**, group on the `command_class` field, NOT `tool_summary`. `command_class` is the
full normalized command emitted by `evaluate` (via `CommandClass`); `tool_summary`
truncates Bash commands at the first newline and at 120 chars, so multi-line
compound commands (`cd <dir> && …`) collapsed into phantom `cd` buckets.

```bash
jq -s '[.[] | select(.category | test("^miss"))]
  | group_by(.command_class) | map({
    pattern: .[0].command_class,
    tool_name: .[0].tool_name,
    count: length,
    sample_ids: [.[].id][0:3],
    categories: ([.[].category] | unique),
    sandbox: ([.[].sandbox_enabled] | group_by(.) | map({k: (.[0] // "unknown"), n: length})),
    approval_sources: ([.[].approval_source] | group_by(.) | map({k: .[0], n: length})),
    permission_modes: ([.[].permission_mode] | group_by(.) | map({k: (.[0] // "null"), n: length}))
  }) | sort_by(-.count) | .[0:10]' "$SLIM"
```

**Under `--source tracker`**, `command_class` is too fragmented to rank by: at tracker volume (194,657 calls, 39,558 misses) the largest `command_class` group held only 48 misses. Rank by program and subcommand instead, from the replay file Step 1 started (`shapes.jsonl` and `replay.jsonl` carry `program` and `subcommand`, and `rows` per shape; `old_decision` is what the live hook recorded). Wait for it first with `bgwait ceta-shapes --timeout 540`. Calls the live hook did not approve, Bash only, by program and subcommand, with what the NEW engine says about them:

```bash
jq -s '[.[] | select(.tool == "Bash" and (.old_decision == "abstain" or .old_decision == "ask"))]
  | group_by([.program, .subcommand])
  | map({program: .[0].program, subcommand: .[0].subcommand,
         calls: (map(.rows) | add), shapes: length,
         engine_now: (group_by(.new_decision) | map({k: .[0].new_decision, calls: (map(.rows) | add)})),
         example_row_ids: (map(.example_row_ids[0]) | .[0:3])})
  | sort_by(-.calls) | .[0:15]' "$WORK/replay.jsonl"
```

Replay replays one representative row per shape, so `calls` is shape population, not a count of misses; use it to rank, and use Step 3b's cells for the cost of each group.

**Prioritize `sandbox_enabled=1` misses.** See [../references/sandbox-enabled.md](../references/sandbox-enabled.md) for prioritization logic.

### Step 3: Get sample rows for top groups

For the top 5-10 pattern groups, get full details on 2-3 sample rows per group (`example_row_ids` from the query above under the tracker source, `sample_ids` under ceta):

```bash
claude-extended-tool-approver show $SRC <sample_id_1> <sample_id_2> <sample_id_3> --format=json
```

Under `--source tracker`, `show` prints the call's stored `ceta.trace` as `trace` and who settled it (`settled_by`, `settlement`, `grading_cell`).

**Tip:** If tracing was enabled (`CLAUDE_TOOL_APPROVER_TRACE=1`), the `show` output includes a `trace` array showing every rule that was evaluated, its decision, and reason. This reveals _why_ each rule abstained — invaluable for deciding which command's spec needs updating.

### Step 3b: Who settled it (tracker source only)

Skip this step under the default source. Under `--source tracker`, `$SLIM` already places every call by CETA's verdict and by who settled it. The calls CETA did not settle are the work list: they cost a person's attention, classifier tokens or an unattended agent's detour. Print only the grading-cell totals (never the whole `evaluate` report: its text output is about 11 MB), then rank the groups of one cell:

```bash
jq -s 'map(select(.grading_cell != null)) | group_by(.grading_cell)
       | map({cell: .[0].grading_cell, calls: length})' "$SLIM"
```

```bash
# Groups of one cell, by count (Bash by command_class; change the cell name to rank another).
jq -s 'map(select(.grading_cell == "abstain/classifier/approved"))
       | group_by(if .tool_name == "Bash" then .command_class else .tool_name end)
       | map({group: (if .[0].tool_name == "Bash" then .[0].command_class else .[0].tool_name end),
              count: length, sample_ids: ([.[].id][0:3])})
       | sort_by(-.count) | .[0:5]' "$SLIM"
```

How to read the cells (a converted ask, `ask/converted`, counts as an ask):

- `ask/person/approved` and `abstain/classifier/approved`: a person or the classifier approved what CETA did not, so a CETA approve rule is missing. A person approving is the stronger evidence.
- `ask/person/refused` and `abstain/classifier/refused`: a CETA reject rule is missing.
- `ask/converted`: an unattended agent was refused where CETA was unsure; an approve or reject rule would spare it the detour.
- `abstain/person`: either direction; read `settlement` per call.
- `abstain/rule-or-mode`: NOT free. It holds the calls an unattended worker's `dontAsk` mode refused (`settled_by == "mode"`, `settlement == "refused"`; 1,920 calls in the 2026-10-29 to 2026-11-17 tracker window), each one a detour. Rank them next to `ask/converted`:

```bash
jq -s '[.[] | select(.settled_by == "mode" and .settlement == "refused")]
       | {mode_refused: length,
          top: (group_by(if .tool_name == "Bash" then .command_class else .tool_name end)
                | map({group: (if .[0].tool_name == "Bash" then .[0].command_class else .[0].tool_name end),
                       count: length, sample_ids: ([.[].id][0:3])})
                | sort_by(-.count) | .[0:5])}' "$SLIM"
```

The remainder of `abstain/rule-or-mode` (a rule settled it) is lowest priority.

Rows with `settlement` `pending` or `unresolved` are never graded (their `category` is `unresolved`); they are counted, not dropped. The correctness check (`category`) is graded against `settlement`: `approved` expects `allow`, `refused` expects `deny`, and a call that ran and errored (`outcome` `failed`) expects `allow`.

### Step 4: APPROVE candidates — segment by evidence tier

An APPROVE candidate is a row where CETA `abstain`ed but the invocation was `approved`. Not all approvals are equal evidence: a human clicking "approve" on a prompt is far stronger than a command that ran under `auto`/`bypass` or inside a subagent. Segment the candidate set so human endorsement is not conflated with machine approval.

**Under `--source tracker`.** `approval_source` cannot find the human tier (see Terminology: "abstain + user" is empty by construction). Use the grading cells. The `hook_decision` of a row with no CETA verdict (a built-in ask, a tool outside CETA's matcher such as `Edit`, `Write`, `Agent`) is empty: read the `top` groups by tool name before proposing anything for them.

```bash
# (1) PRIMARY (human) tier: a person approved a call CETA did not approve. Approved-but-errored
#     rows are dropped (is_error is true), because the approval is not evidence the command is safe.
jq -s '[.[]
        | select(.grading_cell == "ask/person/approved"
                 or (.grading_cell == "abstain/person" and .settlement == "approved"))
        | select(.is_error != true)]
       | {total: length,
          main_agent: ([.[] | select(.agent_type == null)] | length),
          top: (group_by(if .tool_name == "Bash" then .command_class else .tool_name end)
                | map({pattern: (if .[0].tool_name == "Bash" then .[0].command_class else .[0].tool_name end),
                       count: length, sample_ids: ([.[].id][0:3])})
                | sort_by(-.count) | .[0:10])}' "$SLIM"
```

```bash
# (2) WEAKER tier: the classifier (or a subagent) approved. Segment by (settled_by, agent_type).
jq -s '[.[] | select(.grading_cell == "abstain/classifier/approved")]
       | group_by([.permission_mode, .agent_type])
       | map({permission_mode: .[0].permission_mode, agent_type: .[0].agent_type, n: length})
       | sort_by(-.n) | .[0:10]' "$SLIM"
```

**Under `--source ceta`:**

```bash
# (1) Partition the APPROVE-candidate set by approval_source. This is a PARTITION, so
#     the counts MUST sum to the candidate total — use that as a sanity check.
jq -s '[.[] | select(.hook_decision=="abstain" and .outcome=="approved")]
    | group_by(.approval_source)
    | map({approval_source: .[0].approval_source, n: length})' "$SLIM"
```

```bash
# (2) PRIMARY (human) tier: approval_source=="user" (a prompt fired and the HUMAN
#     approved) on the MAIN agent (agent_type==null), EXCLUDING approved-but-errored
#     rows (is_error is true — the tool failed, so the approval is not
#     evidence the command is safe to auto-approve). These are the strongest candidates
#     for a new APPROVE rule.
jq -s '[.[]
     | select(.hook_decision=="abstain" and .outcome=="approved")
     | select(.approval_source=="user" and .agent_type==null)
     | select(.is_error != true)]
    | group_by(.command_class)
    | map({pattern: .[0].command_class, count: length, sample_ids: ([.[].id][0:3])})
    | sort_by(-.count) | .[0:10]' "$SLIM"
```

```bash
# (3) WEAKER tier: machine-approved (approval_source in auto,bypass) OR issued by a
#     subagent (agent_type != null). Segment by the (approval_source, agent_type) PAIR —
#     "subagent" is NOT an approval_source value, so do NOT invent a subagent bucket.
#     Do NOT count these as human endorsement when proposing an APPROVE rule.
jq -s '[.[]
     | select(.hook_decision=="abstain" and .outcome=="approved")
     | select((.approval_source=="auto" or .approval_source=="bypass") or .agent_type!=null)]
    | group_by([.approval_source, .agent_type])
    | map({approval_source: .[0].approval_source, agent_type: .[0].agent_type, n: length})
    | sort_by(-.n) | .[0:10]' "$SLIM"
```

**Tier interpretation.** A pattern with HUMAN-tier support is a strong APPROVE candidate; one that appears ONLY in the weaker tier (auto/bypass/classifier/subagent) is weak evidence and SHOULD be flagged as such. **Errored down-weighting:** the human-tier queries drop approved-but-errored rows via `select(.is_error != true)`; a missing value counts as "not errored". **Caveat (ceta source only):** if `approval_source` is uniformly `unknown` (every row has `permission_mode == null`, i.e. only pre-v5 rows), the human/machine split is not yet derivable — fall back to the `agent_type` axis (which is a stored column, always available) and state the caveat in your findings. See [../references/auto-mode-signal.md](../references/auto-mode-signal.md).

### Step 5: Auto-mode false-denial → candidate CETA APPROVE

Segment: rows where CETA `abstain`ed AND the auto-mode classifier DENIED the call. Mine the classifier reason from `outcome_notes`. A command wrongly denied here is a candidate to teach CETA to APPROVE — but READ the mined reason first: some notes are infrastructure noise (`Classifier unavailable`; under the tracker "gave no verdict" and "judged this action dangerous (it gave no explanation)") and MUST be discarded before proposing a rule.

**Under `--source tracker`**, the cell is `abstain/classifier/refused` and `outcome_notes` is the raw denial text, with the reason after `Reason:` (e.g. `Reason: [External System Writes]`):

```bash
jq -s '[.[]
        | select(.grading_cell == "abstain/classifier/refused")
        | select(.outcome_notes != null
                 and (.outcome_notes | test("gave no verdict|gave no explanation") | not))]
       | {kept: length,
          top: (group_by(if .tool_name == "Bash" then .command_class else .tool_name end)
                | map({pattern: (if .[0].tool_name == "Bash" then .[0].command_class else .[0].tool_name end),
                       count: length, sample_ids: ([.[].id][0:3]),
                       classifier_reasons: ([.[].outcome_notes
                                             | capture("Reason: (?<r>\\[[^\\]]*\\]|[^.]*)").r] | unique)})
                | sort_by(-.count) | .[0:10])}' "$SLIM"
```

**Under `--source ceta`**, the note starts with `auto_mode_classifier:`:

```bash
jq -s '[.[]
     | select(.hook_decision=="abstain" and .outcome=="denied")
     | select(.outcome_notes != null and (.outcome_notes | test("auto_mode_classifier")))]
    | group_by(.command_class)
    | map({pattern: .[0].command_class,
           count: length,
           ids: [.[].id],
           sample_ids: ([.[].id][0:3]),
           classifier_reasons: ([.[].outcome_notes]
                                | map(sub("^auto_mode_classifier: "; ""))
                                | unique)})
    | sort_by(-.count) | .[0:10]' "$SLIM"
```

### Step 6: Auto-mode false-approval / CETA over-approval → candidate ASK/DENY

Two complementary lenses find commands that ran but are "actually risky" (DEFINED as: the current engine's `replay_result` is `deny` or `ask`). These are candidates to teach CETA to ASK/DENY. See [../references/auto-mode-signal.md](../references/auto-mode-signal.md).

```bash
# (1) Commands that RAN under auto/bypass but are "actually risky" (replay deny/ask).
#     Select on the raw permission_mode: approval_source=="auto" now means only
#     "classifier/unknown", and CETA-allowed / user-prompted auto-mode rows are hook/user.
jq -s '[.[]
     | select(.permission_mode=="auto" or .permission_mode=="dontAsk" or .permission_mode=="bypassPermissions")
     | select(.replay_result=="deny" or .replay_result=="ask")]
    | group_by(.command_class)
    | map({pattern: .[0].command_class, replay: .[0].replay_result, count: length, sample_ids: ([.[].id][0:3])})
    | sort_by(-.count) | .[0:10]' "$SLIM"
```

```bash
# (2) Cross-reference against classifier denials of the SAME command at FULL
#     normalized-command granularity (command_class, NOT the leading executable), and
#     the "CETA-APPROVE not classifier-denied elsewhere" check in one pass: take every
#     command_class the classifier DENIED (a real denial: the ceta `auto_mode_classifier:` note
#     or the tracker's raw denial text, without the no-verdict / no-explanation noise), then find
#     rows where CETA itself returned allow for that SAME command_class. A non-empty result =
#     CETA approves a command the classifier flags as risky elsewhere → candidate to tighten
#     CETA to ASK/DENY. also_risky_by_replay = how many of those CETA-approved rows the current
#     engine would ALSO now deny/ask.
jq -s 'def clf: .outcome_notes != null
         and (.outcome_notes | test("auto_mode_classifier|denied by the Claude Code auto mode classifier"))
         and (.outcome_notes | test("gave no verdict|gave no explanation") | not);
    ([.[] | select(clf) | .command_class] | unique) as $denied_classes
    | [.[]
       | select(.hook_decision=="allow")
       | select(.command_class as $c | $denied_classes | index($c))]
    | group_by(.command_class)
    | map({pattern: .[0].command_class,
           ceta_approved_count: length,
           sample_ids: ([.[].id][0:3]),
           also_risky_by_replay: ([.[] | select(.replay_result=="deny" or .replay_result=="ask")] | length)})
    | sort_by(-.ceta_approved_count) | .[0:10]' "$SLIM"
```

### Step 7: Commands called that have no spec

A command with no spec abstains, and inside `$(...)` the abstain turns into a deny, so a missing spec is the structural root cause that the per-row miss counts of Steps 2-6 only hint at. This step asks the question directly: which executables were called that have no command spec? It is read-only and runs over the FULL dataset (`$SLIM`), because a spec-less command that always runs unprompted never shows up as a miss.

`claude-extended-tool-approver specs list` is the machine-readable answer to "does a spec exist for X?". It merges `cmddesc.DefaultRegistry()`, the embedded built-in specs, the user layer (`~/.config/claude-extended-tool-approver`) and the repo layer (`./.ceta`), and reports per name the `layers` that supply it. It needs a build that includes the `specs` subcommand; if the installed binary treats `specs` as hook mode (it hangs on stdin or prints `decode JSON: EOF`), skip this step and say so.

**Only the built-in Go registry feeds replay and the effect engine today** (`internal/shapereplay/shapereplay.go`: `New` uses `cmddesc.DefaultRegistry()`; `specfmt.LoadLiveRegistry` has no non-test caller). A name supplied ONLY by the user or repo layer therefore reads "no schema" in replay forever, and a spec bead for it would change nothing. List those names first, and keep them out of the spec-bead list:

```bash
claude-extended-tool-approver specs list --format=json \
  | jq -c '[.[] | select(.layers | index("registry") | not) | .name]' > "$WORK/ceta-user-layer.json"
```

```bash
# (1) Every command name that HAS a spec in ANY layer (one JSON array of names).
claude-extended-tool-approver specs list --format=json | jq -c '[.[].name]' > "$WORK/ceta-spec-names.json"
```

```bash
# (2) Names called but absent from the spec list, ranked by SOLE-BLOCKER call count.
#     command_class already writes a pipeline as `a && b`, so splitting on `&&`, `||`, `;`
#     and newlines catches every pipeline stage; `$(`, `<(`, `>(` and backticks start a new
#     command, so a command inside a substitution counts. Leading `VAR=x` assignments and the
#     wrappers bash/sh/zsh -c, bgrun NAME --, nice [-n N], timeout N, env, time, command, exec,
#     nohup and pg-nix-log-wrapped are peeled off, so the WRAPPED command is the one counted.
#     Shell keywords, shell builtins and Gas City `gc` (removed; never author specs for it)
#     are excluded. A call is credited to a name ONLY when that name is the ONLY spec-less
#     command in it: a pipeline of three spec-less commands is blocked by all three, and
#     authoring one spec would not free it, so crediting it to each name inflates every count.
#     human_tier counts the person-approved calls on the main agent.
cat > "$WORK/missing-spec.jq" <<'JQ'
def strip:
  if length == 0 then .
  else .[0] as $t
  | if ($t | test("^[A-Za-z_][A-Za-z0-9_]*=")) then .[1:] | strip
    elif $t == "nice" and .[1] == "-n" then .[3:] | strip
    elif $t == "timeout" then .[2:] | strip
    elif $t == "bgrun" then .[3:] | strip
    elif ($t | IN("bash", "sh", "zsh")) and ((.[1] // "") | test("^-[a-z]*c$")) then .[2:] | strip
    elif ($t | IN("env", "nice", "time", "command", "exec", "nohup", "pg-nix-log-wrapped")) then .[1:] | strip
    else . end
  end;
def cmdnames:
  gsub("\\$\\(|<\\(|>\\(|`"; " ; ")
  | split("&&|\\|\\||;|\\n"; null)
  | map(split("\\s+"; null) | map(select(. != "")) | strip | .[0] // empty | sub("\\)+$"; "") | sub("^.*/"; ""))
  | map(select(test("^[a-z_][A-Za-z0-9_.+-]+$")))
  | map(select(IN("if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done", "case", "esac", "in", "function",
                  "set", "unset", "local", "declare", "typeset", "return", "break", "continue", "source", "alias", "eval", "trap", "wait",
                  "gc") | not))
  | unique;
($specs[0]) as $have
| [.[] | select(.tool_name == "Bash")
       | . + {missing: ((.command_class | cmdnames) - $have)}
       | select((.missing | length) == 1)
       | . + {name: .missing[0]}]
| group_by(.name)
| map({
    command: .[0].name,
    sole_blocker_calls: length,
    abstained: ([.[] | select(.hook_decision == "abstain")] | length),
    human_tier: ([.[] | select(.outcome == "approved" and .agent_type == null
                               and (.approval_source == "user" or .grading_cell == "ask/person/approved"
                                    or (.grading_cell == "abstain/person" and .settlement == "approved")))] | length),
    sample_ids: ([.[].id] | .[0:3]),
    sample_class: (.[0].command_class | .[0:100])
  })
| sort_by(-.sole_blocker_calls) | .[0:25]
JQ
jq -c -s --slurpfile specs "$WORK/ceta-spec-names.json" -f "$WORK/missing-spec.jq" "$SLIM"
```

Example output shape (one object per command; the query reads the whole slim file, so it takes about a minute at tracker volume):

```json
{
  "command": "uptime",
  "sole_blocker_calls": 1317,
  "abstained": 376,
  "human_tier": 0,
  "sample_ids": [5119, 27153, 50462],
  "sample_class": "uptime && pgrep -fl ..."
}
```

**Read the list critically.** `command_class` drops quoting, so a few non-commands slip through when prose or a script body is passed as an unquoted argument or heredoc (typically short English words such as `the`, `no`, `a`, or `import`/`print` from a Python body). Read `sample_class` for each candidate, and confirm the survivors are real executables with one call: `command -v <name1> <name2> ...` (an absent name prints nothing). Drop a name that `command -v` does not print, a name whose only evidence is a single call, and any name in `$WORK/ceta-user-layer.json` (it has a user-layer spec already; it is an engine gap, Step 8). Keep the human-tier versus other split visible when ranking, as in Step 4 (a command the human keeps approving is the stronger case for a spec).

### Step 8: Which engine does each pattern need?

The live hook runs the rule chain; a command spec feeds only the new effect engine. So before proposing ANY spec bead, find out what the new engine says about the pattern. Step 1 started `shapes` and `replay` (the read-only offline commands; neither writes to the database). Wait for them if they are not done (`bgwait ceta-shapes --timeout 540`). `replay.jsonl` has one line per shape with `old_decision` (what the live hook recorded), `new_decision` and `new_reason` (what the new effect engine says, in the form `node nN (<command>): <reason>`). Label every shape the live hook did not approve:

| Label            | Test (on the shape)                                                                                                                                                                                                                                                        | What it means                                                                                                                                      |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| spec gap         | `new_reason` says `no schema for X` or `unmodeled subcommand Y`, and X is in the built-in registry's reach (not user-layer only)                                                                                                                                           | The new engine would settle it if a spec existed. The ONLY label that earns a spec bead.                                                           |
| fixed at cutover | `new_decision == "approve"` while `old_decision` is `abstain` or `ask`                                                                                                                                                                                                     | A spec already exists; the live hook has not been cut over. No spec bead. Relief before the cutover is a data lever (below).                       |
| engine gap       | `unsupported tool T` (the adapter does not map the tool); `untrusted executable identity` (an absolute argv0, `internal/patheval/trustedexec.go`); `no schema for source` or `no schema for .` (the `source` / `.` leaves); or a name only the user or repo layer supplies | A spec cannot help: the engine, adapter or loader must change. File an engine bead (or note it), not a spec bead.                                  |
| other            | anything else (`unknown flag -1`, a runtime-expansion or `path-access` reason)                                                                                                                                                                                             | A flag or path-policy question, not a missing spec. Read `new_reason`; an undocumented flag is a flag-level spec fix, a path zone is a data lever. |

```bash
jq -s 'def blocker: (.new_reason // "") | (capture("no schema for (?<n>[^ ]+)").n // null);
  [.[] | select(.old_decision == "abstain" or .old_decision == "ask")
       | select(.state_dependent | not)
       | . + {label:
           (if .new_decision == "approve" then "fixed at cutover"
            elif (.new_reason // "" | test("unsupported tool|untrusted executable identity")) then "engine gap"
            elif (blocker as $n | $n == "source" or $n == "." or ($n != null and ($user[0] | index($n)))) then "engine gap"
            elif (.new_reason // "" | test("no schema for|unmodeled subcommand")) then "spec gap"
            else "other" end)}]
  | group_by(.label)
  | map({label: .[0].label, calls: (map(.rows) | add), shapes: length,
         top: (map({program, subcommand, new_reason: (.new_reason | .[0:110]), rows}) | sort_by(-.rows) | .[0:3])})' \
  --slurpfile user "$WORK/ceta-user-layer.json" "$WORK/replay.jsonl"
```

(`state_dependent` shapes change verdict with the environment, config or filesystem; replay reports them separately and they are left out here.) Rank the spec gaps by the command or subcommand that blocks them:

```bash
jq -s 'def blocker: (.new_reason // "") | (capture("(?<k>no schema for|unmodeled subcommand) (?<n>[^ ]+)") | "\(.k) \(.n)");
  [.[] | select(.old_decision == "abstain" or .old_decision == "ask")
       | select(.state_dependent | not)
       | select(.new_decision != "approve")
       | select(.new_reason // "" | test("unsupported tool|untrusted executable identity") | not)
       | select(.new_reason // "" | test("no schema for|unmodeled subcommand"))
       | {blocker: blocker, program, rows, ex: .example_row_ids[0]}
       | select(.blocker | test("no schema for (source|\\.)$") | not)
       | select(.blocker | sub("^no schema for "; "") | IN($user[0][]) | not)]
  | group_by(.blocker)
  | map({blocker: .[0].blocker, calls: (map(.rows) | add), sample_ids: (map(.ex) | .[0:3])})
  | sort_by(-.calls) | .[0:15]' \
  --slurpfile user "$WORK/ceta-user-layer.json" "$WORK/replay.jsonl"
```

A pattern that is a spec gap in this list AND the sole blocker in Step 7 is the strongest spec-bead candidate.

**Live data levers.** A pattern that needs relief BEFORE the cutover (the live hook still abstains) is not fixed by a spec. The two levers are data in the operator's machine configuration: `approvedCommands` (a command the hook may approve) and the home-manager option `extraReadWriteRoots` (a literal directory prefix that becomes a write-allowed root; it widens EVERY write-capable check, not just the one pattern). Mention them in the finding; the operator applies them.

**Prior rulings.** Before proposing a bead for a path or command, grep the ADRs for it; the pattern may already have a ruling (for example `docs/adr/0109` ruled "keep abstain" on heredoc file writes under `~/.cache`):

```bash
rg -n -i -- '<path-or-command>' docs/adr/
```

Run it from the root of the repository that holds `docs/adr/`. If an ADR rules the pattern, report the ruling and do not propose a bead (unless new evidence contradicts it, which the bead MUST state).

### Step 9: Present ranked findings to user

Present exactly this format. Every pattern carries its Step 8 engine label (spec gap, fixed at cutover, engine gap, other) and any prior-ruling hit; only spec gaps are proposed as spec beads, and the rest are reported with what they need instead:

```text
## Hook Miss Patterns (ranked by frequency)

1. **`<command_class pattern>` or `<program> <subcommand>` — <N> misses** (sandbox: on=<X> off=<Y> unknown=<Z>)
   Hook says: <hook_decision> | Expected: <expected from outcome>
   Engine: <spec gap | fixed at cutover | engine gap | other> (<new_reason>) | Prior ruling: <ADR or none>
   Categories: <miss-uncaught, miss-caught-by-settings, etc.>
   approval_source: <user=<a> auto=<b> settings=<c> unknown=<d> ...>
   permission_mode: <default=<..> auto=<..> null=<..> ...>
   Sample rows: <id1>, <id2>, <id3>

2. ...

## APPROVE candidates (abstain + approved), by tier

- HUMAN tier (ceta source: approval_source=user, main agent; tracker source: ask/person/approved + abstain/person approved): <patterns with counts + sample rows>
- WEAKER tier (auto/bypass/classifier or subagent): <breakdown>
  (weak evidence — NOT human endorsement)
- Excluded as approved-but-errored (is_error): <count>

## Calls refused by dontAsk mode (tracker source: settled_by=mode, settlement=refused)

1. **`<group>` — <N> calls** — an unattended worker's detour; ranked with ask/converted
   Sample rows: <id1>, <id2>, <id3>

## Auto-mode false-denials → candidate CETA APPROVE

1. **`<command_class pattern>` — <N> rows**
   classifier reason: "<mined auto_mode_classifier reason>"
   Sample rows: <id1>, <id2>, <id3>

## CETA over-approvals → candidate ASK/DENY

1. **`<command_class pattern>` — <N> rows** (also risky by replay: <K>)
   Ran under: <auto|bypass|allow> | classifier-denied the same command elsewhere
   Sample rows: <id1>, <id2>, <id3>

## Commands called with no spec (Step 7)

1. **`<command>` — <N> sole-blocker calls** (abstained: <A>; human tier: <H>) | `command -v`: <path>
   Engine: <spec gap | engine gap (user-layer spec only) | ...>
   Sample rows: <id1>, <id2>, <id3> | sample: `<sample_class>`

I recommend creating beads for the top <M> patterns across these lists.
Which patterns should I create beads for?
```

Omit any list whose query returned an empty set, but STATE that it was empty and why (e.g. "no auto/bypass rows — `permission_mode` is `null` on every row, so `approval_source` is uniformly `unknown`").

**CRITICAL:** Wait for explicit user approval before proceeding to Phase 2.

## Phase 2: Create Beads (After User Approval)

For each user-approved pattern, create a bead. In the Reproduce and Verifying commands of the templates below, `$SRC` is Step 0's flag: write it out literally (`--source tracker`, or nothing), so the bead reproduces against the same source. Both pattern-analysis skills create `task` beads since they propose improvements rather than fix defects:

```bash
bd create \
  --title="Hook miss: <pattern description>" \
  --description="<see template below>" \
  --type=task \
  --priority=2
```

Then label it (`agent-support` is this repository's required repo label; add it to every bead, including the missing-spec beads below):

```bash
bd label add <bead-id> claude-extended-tool-approver
bd label add <bead-id> spec-authoring
```

### Missing-spec beads (Step 7 findings)

For each approved "commands called with no spec" finding, create ONE bead per command, with the same labels, and use the variant below instead of the template that follows. This is bead creation only: do not author the spec here (the `ceta-spec-gen` skill does that in the bead's own session).

```bash
bd create \
  --title="Spec missing: <command>" \
  --description="<see variant below>" \
  --type=task \
  --priority=2
```

```markdown
## Problem

`<command>` was called <N> times in the decision database but has no command spec
(`claude-extended-tool-approver specs list --format=json` does not list it), so the hook abstains
on it, and denies it inside `$(...)`.

## Evidence

- **Finding type:** missing-spec
- **Count:** <N> calls (<A> abstained)
- **Tier:** human <H> | weaker <W> (Step 4 definitions)
- **Row IDs (samples):** <id1>, <id2>, <id3>
- **Sample command_class:** `<sample_class>`

## Reproduce

claude-extended-tool-approver specs list --format=json | jq -r '.[].name' | grep -x '<command>' # no output = still no spec

claude-extended-tool-approver show $SRC <id1> <id2> <id3> --format=json

## Next step

Author the spec with the `ceta-spec-gen` skill (claude-extended-tool-approver plugin), then confirm the
name now appears in `specs list` and re-run `shapes`/`replay` and diff against the before file
(see "Verifying the fix" below; `evaluate --baseline` replays the OLD rule chain and cannot show a
spec's effect).

## Acceptance Criteria

- [ ] `<command>` is listed by `claude-extended-tool-approver specs list`
- [ ] The replay diff shows the shapes blocked by `<command>` (sample rows <id1>, <id2>, <id3>) no longer abstain for lack of a spec
```

### Bead Description Template

Use this exact template, filling in the values from Phase 1 data:

````markdown
## Problem

The hook currently returns `<hook_decision>` for `<pattern>` commands, but the
expected decision is `<expected_decision>` (based on user outcome: <outcome>).

## Evidence

- **Finding type:** <miss | approve-candidate | auto-mode false-denial | CETA over-approval>
- **Count:** <N> rows
- **approval_source breakdown:** <user=<a> auto=<b> settings=<c> unknown=<d> ...>
- **permission_mode breakdown:** <default=<..> auto=<..> null=<..> ...>
- **Tier (for APPROVE candidates):** <human (primary) | weaker (auto/bypass/subagent)>
- **Classifier reason (for false-denials / over-approvals):** "<mined `auto_mode_classifier` reason>"
- **Row IDs:** <id1>, <id2>, ..., <idN>
- **Sample rows (from show):**
  - ID <id1>: `<tool_summary>` — hook=<hook_decision>, outcome=<outcome>, approval_source=<approval_source>, permission_mode=<permission_mode>
  - ID <id2>: `<tool_summary>` — hook=<hook_decision>, outcome=<outcome>, approval_source=<approval_source>, permission_mode=<permission_mode>
  - ID <id3>: `<tool_summary>` — hook=<hook_decision>, outcome=<outcome>, approval_source=<approval_source>, permission_mode=<permission_mode>

## Reproduce

claude-extended-tool-approver evaluate $SRC --misses-only --format=json | \
 jq '[.[] | select(.command_class | test("<pattern-regex>"))]'

claude-extended-tool-approver show $SRC <id1> <id2> <id3> --format=json

## Verifying the fix (spec-authoring ticket)

`evaluate --baseline` replays the OLD rule chain (the ask-log path in `cmd_evaluate.go` still
uses `setup.EngineCache`'s `internal/engine` + `internal/rules` chain), so a spec change shows no
delta there. Verify with a before/after replay through the NEW effect engine instead:

1. Capture the "before": `shapes` and `replay` with the pre-change binary, written to a scratch
   directory under `CLAUDE_CODE_TMPDIR` (`shapes.jsonl`, `replay-before.jsonl`). Both read the
   database read-only. Pass the same source and window to both ($SRC's source, e.g.
   `--source tracker --since YYYY-MM-DD`; `shapes` and `replay` default to the tracker and the last 30 days).
2. Land the spec change in the working tree and rebuild (`go run ./cmd/claude-extended-tool-approver`
   from `packages/claude-extended-tool-approver`, or the rebuilt wrapper).
3. Capture the "after" on the SAME shapes file: `replay --shapes <scratch>/shapes.jsonl --out <scratch>/replay-after.jsonl`.
4. Diff by shape:

```bash
jq -n -c --slurpfile b <scratch>/replay-before.jsonl --slurpfile a <scratch>/replay-after.jsonl '
($b | map({key: .shape_key, value: .new_decision}) | from_entries) as $was
| [$a[] | select($was[.shape_key] != null and $was[.shape_key] != .new_decision)
| {program, subcommand, calls: .rows, was: $was[.shape_key], now: .new_decision, example_row_id: .example_row_ids[0]}]
| group_by([.program, .subcommand, .was, .now])
| map({program: .[0].program, subcommand: .[0].subcommand, was: .[0].was, now: .[0].now,
calls: (map(.calls) | add), example_row_id: .[0].example_row_id})
| sort_by(-.calls) | .[0:20]'
```

Confirm this pattern's shapes moved in the intended direction (abstain to approve for an APPROVE
fix; toward ask or reject for an ASK/DENY fix) and that no other program or subcommand moved.

**Replay trap (ADR 0109).** An unwrapped binary (`go run`, `go build`) has none of the installed
wrapper's `CETA_EXTRA_READWRITE_ROOTS`, `CETA_EXTRA_READONLY_ROOTS`, `CETA_EXTRA_TEMP_ROOTS` and
`CETA_DENIED_ROOTS` exports, so it replays every row against only the built-in zones and a path
pattern shows a false delta. Take the BEFORE and the AFTER under the same environment: source the
wrapper's `export CETA_...` lines first, in the same shell, for both runs:

```bash
W=$(command -v claude-extended-tool-approver)
eval "$(grep -E '^export CETA_' "$W")"
```

## Debugging

If `CLAUDE_TOOL_APPROVER_TRACE=1` was set when the hook originally ran, the `show` output includes a `trace` array with the full rule evaluation chain — every rule, its decision, and reason. Use this to see exactly why each rule abstained:

claude-extended-tool-approver show $SRC <id1> --format=json | jq '.[] | .trace'

If trace data is not available (tracing was off), enable it for future sessions:

export CLAUDE_TOOL_APPROVER_TRACE=1

## Acceptance Criteria

- [ ] Pattern and target command spec identified
- [ ] Tracking ticket filed for the spec-authoring work

This bead covers identifying the pattern and filing a tracking ticket; spec authoring (authoring/regenerating the command's data spec per P13/P14 via the Phase 3 spec-generation skill once it exists, running `set-correct-decision` on resolved rows) is a separate ticket.
````

## Constraints

- **Phase 1 MUST NOT modify anything** — no files, no DB, no `settings.local.json`, no approvals required.
- **MUST wait for explicit user approval before Phase 2.**

Beads should include the `agent-support` (this repository's required repo label), `claude-extended-tool-approver` and `spec-authoring` labels, target 2-5 improvements per run focused on the highest-impact patterns, and reference row IDs and CLI commands rather than scratch paths (which are intermediate-only). Only Step 8 spec gaps become spec beads. Phase 2 creates beads only — no code changes, no `set-correct-decision`, no `mark-excluded`.

## Key Paths

- Binary: `packages/claude-extended-tool-approver/cmd/claude-extended-tool-approver/`
- Rule modules (frozen per R1 — deletions only, no new fixes): `packages/claude-extended-tool-approver/internal/rules/*/`
- Database (default source `ceta`): `~/.local/share/claude-extended-tool-approver/asks.db`
- Tracker database (`--source tracker`): `${XDG_STATE_HOME:-~/.local/state}/claude-tool-tracker/tracker.db` (read-only; `--tracker-db PATH` overrides)
- Scratch directory: `${CLAUDE_CODE_TMPDIR:-$TMPDIR}/ceta-hook-misses` (never `/tmp`)
- Engine facts: `packages/claude-extended-tool-approver/docs/ARCHITECTURE.md` ("Effect engine" section); `docs/adr/0075`; prior path rulings under `docs/adr/` (for example 0109)
- Trace env var: `CLAUDE_TOOL_APPROVER_TRACE=1` — enables per-rule decision tracing in `show` output
