---
name: identify-slow-tools
description: Find the slowest tools and commands in the tool-call tracker's timing rankings (by total time and by p90) and file one deep-dive bead per tool or command asking whether it can be sped up. Use after a week or more of normal Claude Code usage with the tracker enabled.
---

# Identify Slow Tools

Read the tracker's timing rankings and file one deep-dive bead for each of the slowest tools or
commands, asking whether it can be sped up. It follows the shape of the `identify-hook-misses` skill:
analyse first, present ranked findings, wait for approval, then file beads.

**This skill is read-only triage.** It does NOT change code, settings or the database. Its only data
source is `claude-tool-tracker timing`; it reads nothing else.

## How many entries

Take the **top 5 of each ranking** (by total time, and by p90). Fewer if a ranking has fewer rows.

## Phase 1: Analyse

### Step 1: Get the rankings

```bash
claude-tool-tracker timing --format json > "$(mktemp -t slow-tools.XXXXXX.json)"
```

Keep the file name it prints. Pass `--db PATH` only when the database is not at the default location.
Run time already excludes approval waits, classifier time and hook overhead; errored and interrupted
calls are excluded by default and counted in `excluded`. Report the `calls`, `used` and `excluded`
numbers to the user so they know how much data the rankings rest on.

The json holds `by_total` and `by_p90`, each a list of groups with `tool`, `class` (Bash only),
`repo`, `runs`, `total_ms`, `median_ms`, `p90_ms` and `max_ms`.

### Step 2: Fold command text into command names

For Bash the `class` is often the full command text (for example `bgwait <job> --timeout 540`), so one
real command appears as many groups that differ only in their arguments. Before ranking, fold them:

1. Reduce each Bash `class` to its command name: the first word, after peeling leading `VAR=x`
   assignments and wrappers (`bgrun NAME --`, `nice [-n N]`, `timeout N`, `env`, `time`, `nohup`,
   `command`, `exec`). Keep a subcommand as part of the name for commands that are always used with
   one (`go test`, `git push`, `nix build`).
2. Merge groups with the same tool, name and repo: sum `runs` and `total_ms`, take the largest
   `max_ms` and the largest `p90_ms` of the merged groups. A merged median is not recoverable from
   the summary, so state the median of the largest-`runs` member and say so.
3. Re-rank the merged groups by `total_ms` and by `p90_ms` (largest first).

Skip this step for groups whose `class` is already short and stable.

### Step 3: Select

Take the top 5 of the re-ranked by-total list and the top 5 of the re-ranked by-p90 list. A tool or
command (same tool, name and repo) that appears in both is **one** candidate, filed once, and its bead
notes that it is in both rankings. The same command name in two repos is two candidates, because
`go test` in one repo is not `go test` in another.

### Step 4: Present

Show a table, then ask which to file:

```markdown
## Slow tool candidates

| #   | Tool / command | Repo | Runs | Total | Median | p90 | Max | Ranking    |
| --- | -------------- | ---- | ---- | ----- | ------ | --- | --- | ---------- |
| 1   | <name>         | <r>  | <n>  | <s>   | <s>    | <s> | <s> | total, p90 |

Data: <used> of <calls> calls used; excluded <failed> failed, <interrupted> interrupted,
<refused> refused, <no_usable_time> without usable time.
```

Format times in seconds (milliseconds divided by 1000). **CRITICAL:** wait for explicit user approval
before Phase 2.

## Phase 2: File beads (after approval)

For each approved candidate file ONE `task` bead, in the issue tracker of the repository where the fix
would land (the repo the slow command runs in, or the repo that owns the tool), not necessarily the
current directory:

```bash
bd -C <tracker-root> create \
  --title="Slow tool: <tool or command> in <repo>" \
  --description="<see template below>" \
  --type=task \
  --priority=3
```

Then label it with that repository's own required labels (its repo label, and its project label when
the repository's `CLAUDE.md` defines one) plus `claude-tool-tracker`:

```bash
bd -C <tracker-root> label add <bead-id> claude-tool-tracker
```

Description template:

```markdown
## Question

Can `<tool or command>` in `<repo>` be sped up? It is among the slowest in the tool-call timing
rankings (<ranking: by total time | by p90 | both>).

## Evidence

- **Runs:** <runs>
- **Total:** <total in seconds>
- **Median:** <median in seconds>
- **p90:** <p90 in seconds>
- **Max:** <max in seconds>
- **Sample command texts:** <up to 3 of the folded commands, when step 2 merged groups>
- **Data basis:** <used> of <calls> calls; excluded <failed> failed, <interrupted> interrupted.

## Reproduce

claude-tool-tracker timing --format json

## Next step

Investigate where the time goes (cache misses, repeated work, avoidable flags, a faster equivalent,
running it less often) and propose or make the change in this repository.

## Acceptance Criteria

- [ ] The cause of the slow runs is identified, or recorded as inherent
- [ ] A speed-up is made, or the reason none is possible is written down
```

Report the bead ids filed.
