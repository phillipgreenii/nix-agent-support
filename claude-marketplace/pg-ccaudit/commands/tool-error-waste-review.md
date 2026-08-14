---
description: Run an agent-waste review over the indexed Claude Code transcript corpus — failed commands AND the mistakes that were never failures, in one ranked routed report (queries the pg-ccaudit SQLite index; never scans raw JSONL)
argument-hint: "[--since YYYY-MM-DD] [--until YYYY-MM-DD]"
---

# Agent-waste review

**FIRST INSTRUCTION: THE DATABASE ALREADY EXISTS. QUERY IT.**

**You MUST NOT scan the raw transcripts** under `~/.claude/projects` — not with
`Read`, not with `Grep`, not with `Glob`, not with `jq`. The corpus is ~1.7 GiB
and scanning it raw stalled a supervising agent's progress watchdog twice; that is
the failure this tooling exists to prevent. Everything you need is already
indexed.

Invoke the `tool-error-waste-review` skill and follow it. It carries the full
method for **both halves** of the waste: the failure census (canned query set,
runaway discount, measured-cost caveat, per-class main-loop/subagent routing) and
the mistake census (structural candidates, the semantic pass and its cost, the
routing taxonomy, the gold-set evaluation).

Window for this run: `$ARGUMENTS` (if empty, ask the operator for a window rather
than silently censusing all of history — an unbounded window is not comparable
with anything).

Start here:

```bash
pg-ccaudit status      # coverage + staleness; read it before anything else
pg-ccaudit queries     # the named, versioned canned queries
```

Then work the skill's dimensions in order. Two steps you must not skip:

- `sidechain-split` — it decides whether each fix belongs in the always-on user
  rules, in a subagent brief, or in the permission-approver rule set.
- `pg-ccaudit report` — it is what produces ONE ranked list holding mistakes and
  command failures together, each routed to exactly one artifact. Two separate
  lists let the cheap half dominate attention purely by being easier to find.

The semantic classifier makes model calls. Bound it (`--since`/`--until`, or
`--max`) and report what the run cost.
