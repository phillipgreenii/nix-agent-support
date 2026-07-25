---
name: identify-hook-misses
description: Identify the 2-5 most impactful hook miss patterns from the decision database and create beads for each. Use after a week+ of normal Claude Code usage to surface high-impact hook decision patterns worth fixing.
---

# Identify Hook Misses

Analyze the decision database to find patterns where the hook makes wrong decisions. Present the top 2-5 most impactful miss patterns for user review, then create beads for approved patterns.

**This skill is read-only triage.** It does NOT fix code, modify the DB, or change settings. It creates beads for later deep-dive sessions.

**Target time: 5-10 minutes (longer if jq debugging is needed).**

## Terminology

- `hook_decision` — the decision the hook returned (`allow`/`ask`/`deny`/`abstain`); from the `hook_decision` field of `evaluate`/`show` output.
- `category` — `miss-uncaught`, `miss-caught-by-settings`, `correct`, `needs-review`, `stale-cwd`; from the `category` field of `evaluate`.
- `outcome` — the user's actual decision (ground truth: `approved`/`denied`/`pending`); from the `outcome` field.
- `sandbox_enabled` — `0` or `1` (or `null`) indicating whether the OS bash sandbox was active for this invocation; from the `sandbox_enabled` field.
- `command_class` — stable, non-truncated grouping key; the field to bucket "same command" on.
- `replay_result` — the decision the _current_ rule engine returns when replaying the row (`allow`/`ask`/`deny`/`abstain`, or empty for rows the engine did not replay, e.g. `stale-cwd`).
- `approval_source` — derived approval-MECHANISM bucket: `unknown`, `bypass`, `auto`, `settings`, `hook`, `user`. Classifies CONTEXT, not outcome (an auto-mode _denial_ still buckets as `auto`).
- `permission_mode` — raw Claude Code permission mode at log time (`default`/`plan`/`acceptEdits`/`dontAsk`/`auto`/`bypassPermissions`), `null` on pre-v5 rows.
- `agent_type` — the subagent that issued the call (e.g. `Explore`), or `null` for the main agent. A SEPARATE axis from `approval_source`.
- `outcome_notes` — free-form resolution notes; carries the `auto_mode_classifier: <reason>` string on an auto-mode denial.
- `tool_response` — the PostToolUse result payload (nested JSON) or `null`; a failed call is signalled by `tool_response.is_error == true`.

Derived analytic terms used by the calibration steps (Steps 4-6 below):

- **APPROVE candidate** — a row where CETA `abstain`ed but the invocation was `approved` (a command CETA could learn to APPROVE). Base filter: `.hook_decision=="abstain" and .outcome=="approved"`.
- **Human (PRIMARY) tier** — APPROVE candidates the HUMAN endorsed on the MAIN agent: `approval_source=="user"` (a prompt fired and the human approved) with `agent_type==null`. This is the strongest evidence for a new APPROVE rule.
- **Weaker tier** — APPROVE candidates approved by a MACHINE or inside a SUBAGENT: `approval_source in (auto,bypass)` OR `agent_type != null`. NOT counted as human endorsement. `subagent` is NOT an `approval_source` value — segment this tier by `approval_source × agent_type`.
- **False-denial** — CETA `abstain`ed and the `auto_mode_classifier` DENIED the call (`outcome=="denied"` with `auto_mode_classifier:` in `outcome_notes`). Candidate to teach CETA to APPROVE (if the denial was wrong) — read the mined reason first.
- **False-approval / over-approval** — a command CETA (or auto/bypass mode) let run that is "actually risky". Candidate to teach CETA to ASK/DENY.
- **"Actually risky"** — DEFINED as: the current engine's `replay_result` for the row is `deny` or `ask` (self-consistent; no curated list).
- **"Errored" / "approved-but-errored"** — DEFINED as: `tool_response.is_error == true`. A missing/`null` `tool_response` is treated as "unknown / not errored".

Full schema: [../references/database-schema.md](../references/database-schema.md). Shared definitions: [../references/terminology.md](../references/terminology.md). Two-way interpretation (APPROVE vs ASK/DENY): [../references/auto-mode-signal.md](../references/auto-mode-signal.md).

## Phase 1: Identify (No Approvals Needed)

All commands in this phase use `claude-extended-tool-approver` or `jq` — no raw sqlite3, no file modifications. The four raw fields the calibration steps consume (`permission_mode`, `agent_type`, `outcome_notes`, `tool_response`) are exposed on `evaluate --format=json`, so the "no raw sqlite3" rule holds; the CLI stays the sanctioned interface.

### Step 1: Capture the datasets

The miss-ranking steps (2-3) run over misses only; the calibration steps (4-6) run over the FULL dataset (they are NOT `--misses-only` "misses" — they segment every row). Capture both once, then query the files (re-running `evaluate` replays every row through the engine and is slow):

```bash
claude-extended-tool-approver evaluate --misses-only --format=json > /tmp/ceta-misses.json
claude-extended-tool-approver evaluate --format=json > /tmp/ceta-all.json
```

Check how many misses exist:

```bash
jq 'length' /tmp/ceta-misses.json
```

If zero misses, skip Steps 2-3 and go straight to the calibration steps (4-6) — they run over the full dataset regardless of the miss count.

### Step 2: Group by pattern and rank

Group on the `command_class` field, NOT `tool_summary`. `command_class` is the
full normalized command emitted by `evaluate` (via `CommandClass`); `tool_summary`
truncates Bash commands at the first newline and at 120 chars, so multi-line
compound commands (`cd <dir> && …`) collapsed into phantom `cd` buckets (bead
pg2-okd13.3).

```bash
jq 'group_by(.command_class) | map({
  pattern: .[0].command_class,
  tool_name: .[0].tool_name,
  count: length,
  ids: [.[].id],
  sample_ids: [.[].id][0:3],
  categories: ([.[].category] | unique),
  sandbox: ([.[].sandbox_enabled] | group_by(.) | map({k: (.[0] // "unknown"), n: length})),
  approval_sources: ([.[].approval_source] | group_by(.) | map({k: .[0], n: length})),
  permission_modes: ([.[].permission_mode] | group_by(.) | map({k: (.[0] // "null"), n: length}))
}) | sort_by(-.count) | .[0:10]' /tmp/ceta-misses.json
```

**Prioritize `sandbox_enabled=1` misses.** See [../references/sandbox-enabled.md](../references/sandbox-enabled.md) for prioritization logic.

### Step 3: Get sample rows for top groups

For the top 5-10 pattern groups, get full details on 2-3 sample rows per group:

```bash
claude-extended-tool-approver show <sample_id_1> <sample_id_2> <sample_id_3> --format=json
```

**Tip:** If tracing was enabled (`CLAUDE_TOOL_APPROVER_TRACE=1`), the `show` output includes a `trace` array showing every rule that was evaluated, its decision, and reason. This reveals _why_ each rule abstained — invaluable for deciding which rule module to modify.

### Step 4: APPROVE candidates — segment by `approval_source` tier

An APPROVE candidate is a row where CETA `abstain`ed but the invocation was `approved`. Not all approvals are equal evidence: a human clicking "approve" on a prompt is far stronger than a command that ran under `auto`/`bypass` or inside a subagent. Segment the candidate set so human endorsement is not conflated with machine approval.

```bash
# (1) Partition the APPROVE-candidate set by approval_source. This is a PARTITION, so
#     the counts MUST sum to the candidate total — use that as a sanity check.
jq '[.[] | select(.hook_decision=="abstain" and .outcome=="approved")]
    | group_by(.approval_source)
    | map({approval_source: .[0].approval_source, n: length})' /tmp/ceta-all.json
```

```bash
# (2) PRIMARY (human) tier: approval_source=="user" (a prompt fired and the HUMAN
#     approved) on the MAIN agent (agent_type==null), EXCLUDING approved-but-errored
#     rows (tool_response.is_error==true — the tool failed, so the approval is not
#     evidence the command is safe to auto-approve). These are the strongest candidates
#     for a new APPROVE rule.
jq '[.[]
     | select(.hook_decision=="abstain" and .outcome=="approved")
     | select(.approval_source=="user" and .agent_type==null)
     | select((.tool_response.is_error // false) != true)]
    | group_by(.command_class)
    | map({pattern: .[0].command_class, count: length, sample_ids: ([.[].id][0:3])})
    | sort_by(-.count) | .[0:10]' /tmp/ceta-all.json
```

```bash
# (3) WEAKER tier: machine-approved (approval_source in auto,bypass) OR issued by a
#     subagent (agent_type != null). Segment by the (approval_source, agent_type) PAIR —
#     "subagent" is NOT an approval_source value, so do NOT invent a subagent bucket.
#     Do NOT count these as human endorsement when proposing an APPROVE rule.
jq '[.[]
     | select(.hook_decision=="abstain" and .outcome=="approved")
     | select((.approval_source=="auto" or .approval_source=="bypass") or .agent_type!=null)]
    | group_by([.approval_source, .agent_type])
    | map({approval_source: .[0].approval_source, agent_type: .[0].agent_type, n: length})
    | sort_by(-.n) | .[0:10]' /tmp/ceta-all.json
```

**Tier interpretation.** A pattern with HUMAN-tier support is a strong APPROVE candidate; one that appears ONLY in the weaker tier (auto/bypass/subagent) is weak evidence and SHOULD be flagged as such. **`tool_response` down-weighting:** query (2) drops approved-but-errored rows via `select((.tool_response.is_error // false) != true)` — a `null`/missing `tool_response` counts as "not errored". **Caveat:** if `approval_source` is uniformly `unknown` (every row has `permission_mode == null`, i.e. only pre-v5 rows), the human/machine split is not yet derivable — fall back to the `agent_type` axis (which is a stored column, always available) and state the caveat in your findings. See [../references/auto-mode-signal.md](../references/auto-mode-signal.md).

### Step 5: Auto-mode false-denial → candidate CETA APPROVE

Segment: rows where CETA `abstain`ed AND the `auto_mode_classifier` DENIED the call (`outcome=="denied"` with an `auto_mode_classifier:` note). Mine the classifier reason from `outcome_notes`. A command wrongly denied here is a candidate to teach CETA to APPROVE — but READ the mined reason first: some notes are `Classifier unavailable` (an infrastructure gap, not a risk rationale) and MUST be discarded before proposing a rule.

```bash
jq '[.[]
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
    | sort_by(-.count) | .[0:10]' /tmp/ceta-all.json
```

### Step 6: Auto-mode false-approval / CETA over-approval → candidate ASK/DENY

Two complementary lenses find commands that ran but are "actually risky" (DEFINED as: the current engine's `replay_result` is `deny` or `ask`). These are candidates to teach CETA to ASK/DENY. See [../references/auto-mode-signal.md](../references/auto-mode-signal.md).

```bash
# (1) Commands that RAN under auto/bypass but are "actually risky" (replay deny/ask).
jq '[.[]
     | select(.approval_source=="auto" or .approval_source=="bypass")
     | select(.replay_result=="deny" or .replay_result=="ask")]
    | group_by(.command_class)
    | map({pattern: .[0].command_class, replay: .[0].replay_result, count: length, sample_ids: ([.[].id][0:3])})
    | sort_by(-.count) | .[0:10]' /tmp/ceta-all.json
```

```bash
# (2) Cross-reference against classifier denials of the SAME command at FULL
#     normalized-command granularity (command_class, NOT the leading executable), and
#     the "CETA-APPROVE not classifier-denied elsewhere" check in one pass: take every
#     command_class the auto_mode_classifier DENIED, then find rows where CETA itself
#     returned allow for that SAME command_class. A non-empty result = CETA approves a
#     command the classifier flags as risky elsewhere → candidate to tighten CETA to
#     ASK/DENY. also_risky_by_replay = how many of those CETA-approved rows the current
#     engine would ALSO now deny/ask.
jq '([.[]
      | select(.outcome=="denied" and .outcome_notes!=null and (.outcome_notes|test("auto_mode_classifier")))
      | .command_class] | unique) as $denied_classes
    | [.[]
       | select(.hook_decision=="allow")
       | select(.command_class as $c | $denied_classes | index($c))]
    | group_by(.command_class)
    | map({pattern: .[0].command_class,
           ceta_approved_count: length,
           sample_ids: ([.[].id][0:3]),
           also_risky_by_replay: ([.[] | select(.replay_result=="deny" or .replay_result=="ask")] | length)})
    | sort_by(-.ceta_approved_count) | .[0:10]' /tmp/ceta-all.json
```

### Step 7: Present ranked findings to user

Present exactly this format:

```text
## Hook Miss Patterns (ranked by frequency)

1. **`<command_class pattern>` — <N> misses** (sandbox: on=<X> off=<Y> unknown=<Z>)
   Hook says: <hook_decision> | Expected: <expected from outcome>
   Categories: <miss-uncaught, miss-caught-by-settings, etc.>
   approval_source: <user=<a> auto=<b> settings=<c> unknown=<d> ...>
   permission_mode: <default=<..> auto=<..> null=<..> ...>
   Sample rows: <id1>, <id2>, <id3>

2. ...

## APPROVE candidates (abstain + approved), by tier

- HUMAN tier (approval_source=user, main agent): <patterns with counts + sample rows>
- WEAKER tier (auto/bypass or subagent): <approval_source × agent_type breakdown>
  (weak evidence — NOT human endorsement)
- Excluded as approved-but-errored (tool_response.is_error): <count>

## Auto-mode false-denials → candidate CETA APPROVE

1. **`<command_class pattern>` — <N> rows**
   classifier reason: "<mined auto_mode_classifier reason>"
   Sample rows: <id1>, <id2>, <id3>

## CETA over-approvals → candidate ASK/DENY

1. **`<command_class pattern>` — <N> rows** (also risky by replay: <K>)
   Ran under: <auto|bypass|allow> | classifier-denied the same command elsewhere
   Sample rows: <id1>, <id2>, <id3>

I recommend creating beads for the top <M> patterns across these lists.
Which patterns should I create beads for?
```

Omit any list whose query returned an empty set, but STATE that it was empty and why (e.g. "no auto/bypass rows — `permission_mode` is `null` on every row, so `approval_source` is uniformly `unknown`").

**CRITICAL:** Wait for explicit user approval before proceeding to Phase 2.

## Phase 2: Create Beads (After User Approval)

For each user-approved pattern, create a bead. Both pattern-analysis skills create `task` beads since they propose improvements rather than fix defects:

```bash
bd create \
  --title="Hook miss: <pattern description>" \
  --description="<see template below>" \
  --type=task \
  --priority=2
```

Then label it:

```bash
bd label add <bead-id> claude-extended-tool-approver
```

### Bead Description Template

Use this exact template, filling in the values from Phase 1 data:

```markdown
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

claude-extended-tool-approver evaluate --misses-only --format=json | \
 jq '[.[] | select(.command_class | test("<pattern-regex>"))]'

claude-extended-tool-approver show <id1> <id2> <id3> --format=json

## Debugging

If `CLAUDE_TOOL_APPROVER_TRACE=1` was set when the hook originally ran, the `show` output includes a `trace` array with the full rule evaluation chain — every rule, its decision, and reason. Use this to see exactly why each rule abstained:

claude-extended-tool-approver show <id1> --format=json | jq '.[] | .trace'

If trace data is not available (tracing was off), enable it for future sessions:

export CLAUDE_TOOL_APPROVER_TRACE=1

## Acceptance Criteria

- [ ] Pattern and target rule module identified
- [ ] Tracking ticket filed for the implementation work

This bead covers identifying the pattern and filing a tracking ticket; implementation (modifying the Go rule module, adding tests, running `set-correct-decision` on resolved rows) is a separate ticket.
```

## Constraints

- **Phase 1 MUST NOT modify anything** — no files, no DB, no `settings.local.json`, no approvals required.
- **MUST wait for explicit user approval before Phase 2.**

Beads should include the `claude-extended-tool-approver` label, target 2-5 improvements per run focused on the highest-impact patterns, and reference row IDs and CLI commands rather than `/tmp` paths (which are intermediate-only). Phase 2 creates beads only — no code changes, no `set-correct-decision`, no `mark-excluded`.

## Key Paths

- Binary: `packages/claude-extended-tool-approver/cmd/claude-extended-tool-approver/`
- Rule modules: `packages/claude-extended-tool-approver/internal/rules/*/`
- Database: `~/.local/share/claude-extended-tool-approver/asks.db`
- Trace env var: `CLAUDE_TOOL_APPROVER_TRACE=1` — enables per-rule decision tracing in `show` output
