# Runbook — `hack-archive-and-compact`

Manual procedure for running `pack/scripts/hack-archive-and-compact.sh` for
the first time on production. The daily order is **disabled by default**;
this runbook covers the one-shot reclaim, verification, and how to flip
the order to automatic afterward.

See `HACKS.md` (HACK 10) for the underlying problem statement and
`pack/scripts/hack-archive-and-compact.sh` for the script's inline
backstory and step-by-step internals.

---

## What this does

In one run, the script:

1. Exports every closed regular bead to JSONL files in
   `archive/beads/<YYYY-MM-DD>.jsonl`, partitioned by close date.
2. Prunes those closed beads from dolt (`bd prune --pattern '*' --force`).
3. Squashes dolt commit history (`bd flatten --force`).
4. Reclaims noms storage (`CALL DOLT_GC('--full')`).
5. Drops dolt's stats journal (`CALL DOLT_STATS_PURGE()`).
6. Commits `archive/` to this city's git repo.

Sandbox-verified outcome: `hq` from **2.7 GB → 14 MB** with zero
bead-level data loss.

---

## When to run

- **Now / one-shot:** disk pressure is high (>85 %) and you want the big
  reclaim. Subsequent daily runs are deltas; this first one is the big one.
- **Daily (automated):** after the first manual run looks good, flip the
  order's `enabled` flag to `true`. See Step 7 below.

## When NOT to run

- During heavy concurrent bd writes you can't pause. The flatten window
  is short (~1 s) but bd writes in that window could be lost. The
  procedure suspends the city to remove that risk.
- If you don't have ~100 MB of free disk _temporarily_. The flatten +
  GC churns through intermediate state. Disk briefly grows before it
  shrinks.

---

## Prerequisites

- You're at the city root: `cd /Users/phillipg/gc`
- `gc`, `bd`, `dolt`, `jq` on PATH (verify: `command -v gc bd dolt jq`)
- The dolt server is up (verify: `gc dolt health | head -3` shows
  `Server: running ...`)
- Optional: capture current state to compare against later. See Step 1.

---

## Procedure

### Step 1 — Pre-snapshot (informational)

Capture the "before" state so you can verify reclamation worked.

```bash
cd /Users/phillipg/gc
echo "--- BEFORE ---"
du -sh .beads/dolt 2>/dev/null
du -sh .beads/dolt/hq 2>/dev/null
du -sh .beads/dolt/hq/.dolt/stats 2>/dev/null
df -h / | tail -1
bd stats 2>&1 | grep -E "Total|Open|Closed"
```

Note the numbers — you'll compare in Step 4.

### Step 2 — Suspend the city

Stops agents from claiming or writing. The dolt server stays up; bd
commands from your shell still work.

```bash
gc suspend
gc status | grep -E "Suspended|Agents"   # expect "Suspended: yes"
```

### Step 3 — Run the script

```bash
/Users/phillipg/gc/pack/scripts/hack-archive-and-compact.sh
```

Expected single-line tail output:

```
2026-MM-DDTHH:MM:SSZ hack-archive-and-compact: archived=<N> committed=<0|1>
```

**Expected duration on first run:** 1–5 minutes. Breakdown:

| Sub-step                                      | Approx duration          |
| --------------------------------------------- | ------------------------ |
| `bd export` + jq partition                    | 1–2 s                    |
| `bd prune --pattern '*' --force` (10k+ beads) | seconds to a few minutes |
| `bd flatten --force`                          | ~1 s                     |
| `CALL DOLT_GC('--full')`                      | 5–30 s                   |
| `CALL DOLT_STATS_PURGE()`                     | <1 s                     |
| `git add archive/ && git commit`              | <1 s                     |

**Do not kill it mid-stream.** If it hangs >10 minutes, check the dolt
server is still responsive (`gc dolt health`) before deciding.

### Step 4 — Verify before resuming

```bash
echo "--- AFTER ---"
du -sh .beads/dolt
du -sh .beads/dolt/hq
du -sh .beads/dolt/hq/.dolt/stats
df -h / | tail -1
bd stats 2>&1 | grep -E "Total|Open|Closed"
echo "--- ARCHIVE ---"
ls -la archive/beads/
wc -l archive/beads/*.jsonl 2>/dev/null
head -1 "archive/beads/$(ls -t archive/beads/ | head -1)" \
  | jq '{id, status, title, closed_at}' 2>/dev/null
echo "--- GIT ---"
git log --oneline -1
```

**Expected:**

- `du .beads/dolt/hq` drops from ~3 GB to a few tens of MB.
- `du .beads/dolt/hq/.dolt/stats` drops to ~16 KB.
- `bd stats` **Total** now equals **Open** (closed beads were pruned).
- `archive/beads/` contains one or more dated `.jsonl` files.
- A sample line parses as JSON with `id`, `status: "closed"`, `title`,
  `closed_at`.
- `git log -1` shows the script's auto-commit:
  `archive(beads): <N> closed bead(s) @ <ts>`.

**If `bd stats` errors:** dolt server may be busy finishing GC. Wait 30 s
and retry before panicking.

### Step 5 — Resume the city

```bash
gc resume
gc status | grep -E "Suspended|Agents"   # expect "Suspended: no"
```

Agents pick back up where they left off.

---

## Step 6 — Recovery (only if something is wrong)

The original DB state is **not recoverable in dolt** after Step 3
(prune + flatten are irreversible). But the data is in two places:

1. **JSONL archive** — every closed bead is in `archive/beads/*.jsonl`.
   To rehydrate one or all of them back into bd:

   ```bash
   bd import archive/beads/<date>.jsonl
   ```

2. **bd events table (live data)** — bd's audit trail (created/updated/
   closed timestamps + actors) is _table data_, not dolt commit history.
   It survives the flatten. Queryable via:

   ```bash
   bd show <id>           # for any bead still in bd
   ```

   For pruned beads, you'd need to import from JSONL first.

If you want to **un-commit the archive git commit** (keeps the files,
removes the commit):

```bash
git reset --soft HEAD~1
```

If `bd` commands fail entirely after the run, the dolt server may need
a kick:

```bash
gc dolt status
# if unhealthy, the supervisor or manual restart fixes it:
gc dolt restart   # if available, else: kill <pid>; supervisor respawns
```

---

## Step 7 — Enable the daily order (only after Step 4 looks good)

The order at `orders/hack-archive-and-compact.toml` is shipped with
`enabled = false`. Flip it on:

```bash
sed -i.bak 's/^enabled     = false$/enabled     = true/' \
  /Users/phillipg/gc/orders/hack-archive-and-compact.toml
rm /Users/phillipg/gc/orders/hack-archive-and-compact.toml.bak
gc order check 2>&1 | grep hack-archive
```

Expected output:

```
hack-archive-and-compact  cooldown  yes  never run
```

Meaning: due, will fire on the next dispatcher tick, then every 24 h.

---

## Step 8 — Commit the supporting files

The script auto-commits `archive/` on its own run. The supporting files
(HACKS.md entry, order, script, this runbook) need to be committed
separately:

```bash
cd /Users/phillipg/gc
git status   # review
git add HACKS.md \
        orders/hack-archive-and-compact.toml \
        pack/scripts/hack-archive-and-compact.sh \
        pack/scripts/hack-archive-and-compact.RUNBOOK.md
git commit -m "feat(city): hack-archive-and-compact daily lifecycle for dolt bloat"
git log --oneline -3
```

---

## Quick-glance summary

```bash
# After reading the full procedure above:
cd /Users/phillipg/gc

# 1. snapshot before (optional, informational)
du -sh .beads/dolt/hq && df -h / | tail -1

# 2-5. suspend, run, verify, resume
gc suspend
./pack/scripts/hack-archive-and-compact.sh
du -sh .beads/dolt/hq && ls archive/beads/ && bd stats | head -10
gc resume

# 7. enable daily order
sed -i.bak 's/^enabled     = false$/enabled     = true/' \
  orders/hack-archive-and-compact.toml && \
  rm orders/hack-archive-and-compact.toml.bak

# 8. commit scaffolding
git add HACKS.md orders/hack-archive-and-compact.toml \
        pack/scripts/hack-archive-and-compact.sh \
        pack/scripts/hack-archive-and-compact.RUNBOOK.md
git commit -m "feat(city): hack-archive-and-compact daily lifecycle for dolt bloat"
```

---

## Future operational notes

- **Steady-state:** each daily run archives only the closures from that
  day. Typical daily JSONL file: 100 KB–1 MB depending on city activity.
- **Retirement:** if bd / gascity ships a built-in archive lifecycle, or
  the city migrates off dolt, retire the order + script per HACK 10's
  retirement criteria in `HACKS.md`.
- **Manual re-runs:** safe to re-run mid-day. The script is idempotent
  (existing date files are appended-and-deduped).
- **The archive is the durable history:** if you ever consider clearing
  `.beads/dolt/` entirely, the JSONL files in `archive/beads/` survive
  and can rehydrate the bead store via `bd import`.
