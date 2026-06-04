# Spec: nix-managed Dolt maintenance for gc (breaker + self-gating flatten)

**Date:** 2026-06-04
**Status:** Approved (brainstorming)
**Repo:** `phillipgreenii-nix-agent-support`

## Problem

gc's beads live in Dolt, which commits per write. The city's stock + workaround
orders generate a high, continuous write volume, so hq's commit history and
storage grow without bound. Two failure modes result:

1. **Acute auto-import spiral.** bd ≤ 1.0.4 misreads a slow/empty query (common
   under deep history + load) as "empty database" and re-imports the entire
   `issues.jsonl` — a bulk commit that deepens history, slowing reads further.
   On 2026-06-04 this took hq from ~10k to ~20k commits in minutes and
   crash-looped the dolt server. Mitigated same day by HACK 18 (immutable-empty
   `issues.jsonl`), but that breaker was applied by hand and had been silently
   un-applied by a city restore — which is what triggered the incident.
2. **Chronic bloat.** The derived-stats journal grows ~1 GB/week;
   `DOLT_GC()` alone never purges it. The real compactor that would flatten
   history (`hack-archive-and-compact`, HACK 10) **stopped firing 2026-05-29**
   and is no longer materialized — gc's order dispatch dropped it, and
   `[[orders.overrides]]` can't be trusted in 1.1.0 (HACK 12).

## Goals

- Keep the import breaker **durably applied** without manual intervention.
- Keep Dolt storage bounded (purge the stats journal) on a reliable schedule.
- Flatten commit history when it's worthwhile **and** safe, escaping gc's
  unreliable order dispatch by driving from **launchd**.
- All logic in nix (`phillipgreenii-nix-agent-support`), tunable via module
  options, fully tested.

## Non-goals

- Fixing the separate **archive-repo git push** failure ("JSONL push failed"
  escalations) — different mechanism, tracked separately.
- Upgrading bd to the build that removes the auto-import footgun (would retire
  the breaker entirely — see HACK 3/18 retire-when).
- True ephemeral/durable DB separation (a larger architectural change; future).

## Decisions (from brainstorming, 2026-06-04)

| Decision                  | Choice                                                                        |
| ------------------------- | ----------------------------------------------------------------------------- |
| Placement                 | Everything in `phillipgreenii-nix-agent-support` (scripts + launchd)          |
| Flatten gate              | Multi-signal: need + safety + anti-thrash + max-age force                     |
| Breaker management        | home-manager **activation at switch** + manual command (NOT hourly re-assert) |
| Run context               | **User** (launchd user agent / HM activation). Never root.                    |
| `gc` dependency           | Script **depends on gascity** (gc in runtimeDeps)                             |
| `maxFlattenIntervalHours` | **24**                                                                        |

## Components

All in `phillipgreenii-nix-agent-support`, built with `mkBashBuilders`,
`public = false` (internal ops tools). Follow existing examples
(`packages/agent-activity/…`, `packages/wait-for-agents/…`).

### 1. `gc-bd-import-breaker` (mkBashScript)

The HACK 18 breaker. Logic ported from today's `tools/bd-import-breaker.sh`.

- Actions: `apply` (default), `--status`, `--revert`, `--city DIR`
  (default `$GC_CITY` else `$PWD`).
- `apply`: refuse unless `<city>/.beads/dolt` exists; back up a non-empty
  `issues.jsonl` to `issues.jsonl.breaker-backup-<UTC>`; replace with a 0-byte
  file; `chflags uchg`. Idempotent. macOS only.
- runtimeDeps: coreutils (uses `stat`/`chflags`/`du`; no dolt/gc needed).

### 2. `gc-dolt-maintenance` (mkBashScript, `.sh` + `.bash` split)

- **`gc-dolt-maintenance.bash`** — pure, unit-testable decision function:

  `should_flatten(commit_count, busy_proc_count, hours_since_last,
breaker_applied, has_remote, commit_threshold, busy_threshold,
min_interval_h, max_interval_h) -> "yes|no" + reason`

  Logic, in order:
  1. `has_remote` → **no** (never flatten a remote-connected DB, e.g. zr)
  2. `!breaker_applied` → **no** (refuse: flatten runs `gc bd`, which could
     re-trigger the spiral if the breaker is off)
  3. `commit_count < commit_threshold` → **no** (below need)
  4. `hours_since_last ≥ max_interval_h` → **yes** (max-age force, even if busy)
  5. `hours_since_last < min_interval_h` → **no** (anti-thrash)
  6. `busy_proc_count ≥ busy_threshold` → **no** (system busy)
  7. else → **yes**

- **`gc-dolt-maintenance.sh`** — orchestration:
  - For each local Dolt DB under `<city>/.beads/dolt/*/`:
    - **Cheap tier (always):** `CALL DOLT_STATS_PURGE()` then `CALL DOLT_GC()`
      via direct `dolt sql` (no `bd` → no import risk). Best-effort, logged.
    - **Flatten (gated):** measure signals → call `should_flatten` → if yes,
      `gc bd flatten --force`; on success, write the per-DB timestamp.
  - Signals & measurement:
    - `commit_count`: `SELECT COUNT(*) FROM dolt_log`.
    - `busy_proc_count`: count of live `bd` client processes (`pgrep -fc 'bin/bd '`).
    - `hours_since_last`: from state file `<stateDir>/<db>.last-flatten`
      (default `stateDir = $GC_HOME/dolt-maintenance`, i.e. `~/.gc/...`).
    - `breaker_applied`: `issues.jsonl` is 0-byte AND has `uchg`.
    - `has_remote`: `dolt remote -v` for the DB is non-empty (detection, not a
      hard-coded list — so zr is auto-excluded while it's remote-connected).
  - Logs each decision + reason and each DB's before/after size.
  - `--no-flatten` flag (cheap tier only); `--city DIR`.
  - runtimeDeps: **dolt**, **gascity** (`gc`), coreutils, procps/pgrep.

### 3. home-manager wiring (agent-support HM config)

- `launchd.agents.gc-dolt-maintenance`: runs `gc-dolt-maintenance` hourly
  (`StartCalendarInterval` Minute 0), `StandardOutPath`/`StandardErrorPath` →
  `~/.gc/dolt-maintenance.log`, `RunAtLoad = false`.
- `home.activation.gcBdImportBreaker`: runs `gc-bd-import-breaker apply
--city <cityPath>` (idempotent) on every switch.
- Module options (with defaults): `cityPath = /Users/phillipg/gc`,
  `flattenCommitThreshold = 5000`, `busyProcThreshold = 4`,
  `minFlattenIntervalHours = 6`, `maxFlattenIntervalHours = 24`,
  `stateDir`, `logPath`. Injected into the scripts via mkBashScript `config`.

## Run context

Everything runs as the **user** (`phillipg`), never root: home-manager
`launchd.agents` are per-user LaunchAgents; the activation runs as the user
during `home-manager switch`; `chflags uchg` on a user-owned file needs no
privilege; dolt/gc already run as the user. (User context is also _required_
for `uchg` here.)

## Testing

Per the bash-scripting skill plus explicit isolation requirements.

### Unit (`test-gc-dolt-maintenance-lib.bats`)

- Source `gc-dolt-maintenance.bash`, drive `should_flatten` across a truth
  table: each rule (remote / breaker-off / below-threshold / max-age-force /
  anti-thrash / busy / happy-path) asserted on decision **and** reason. No dolt.

### Integration (`test-gc-dolt-maintenance.bats`, `test-gc-bd-import-breaker.bats`)

**Isolation (hard requirements):**

- `TEST_DIR="$(mktemp -d)"`; override `HOME="$TEST_DIR"`.
- **Wipe all real env** that could point at a live server: unset/clear
  `GC_*`, `BEADS_*`, `DOLT_*`, `BEADS_DOLT_*`, `GC_CITY`, `GC_HOME`.
- A test that needs a real Dolt DB **creates a fresh one inside `TEST_DIR`** and
  starts a `dolt sql-server` bound to `127.0.0.1` on an **ephemeral/random port**
  with `--data-dir` in `TEST_DIR`. **Never** connect to `24158`/`24159` (the real
  city/bare servers). The test asserts it is operating on the **test DB**
  (e.g. seed a sentinel row / known prefix and verify) before exercising logic.
- **Teardown always runs and kills the test server even on error/failure:**
  capture the server PID, and in `teardown` (which bats runs unconditionally)
  `kill` it, wait for exit, and `rm -rf "$TEST_DIR"`. Use a PID file + a
  `trap`-guarded helper so a mid-test failure cannot leak a dolt process.
- Mock `gc` (and `bd` where the cheap tier isn't under test) as PATH shims.
- Do not test builder-injected behavior (`--version`, shebang).

## Cleanup / migration

- Delete from the gc repo: `tools/bd-import-breaker.sh`,
  `tools/gc-dolt-maintenance.sh`, `tools/com.phillipg.gc-dolt-maintenance.plist`.
- Unload + remove the hand-installed
  `~/Library/LaunchAgents/com.phillipg.gc-dolt-maintenance.plist`.
- Update HACKS.md HACK 18 to reference the nix command `gc-bd-import-breaker`
  instead of `tools/…`.
- Note that the launchd job **supersedes** the never-firing HACK 10 gc-order
  flatten (keep HACK 10's doctor check; the order stays disabled).

## Wiring / flake

- Add both packages to agent-support's package set + `scripts.nix`/module wiring
  (shellcheck lists, `nix flake check`, pre-commit), per the bash-scripting
  skill's wiring reference and existing `packages/*` examples.
- Ensure `gascity` is a flake input so `gc` resolves as a runtimeDep; if absent,
  add it.

## Risks & mitigations

- **Flatten still times out under sustained load.** Best-effort + logged +
  retried next hour; the cheap tier (the real size win) always runs; the
  max-age force lets it eventually run during a quieter hour.
- **Breaker off when the job runs.** The `breaker_applied` precondition makes
  the job skip flatten (and warn) rather than risk a spiral.
- **Remote divergence.** `has_remote` detection excludes zr (and any
  remote-connected DB) from flatten.
- **Activation-only breaker leaves a gap** between a mid-cycle un-apply and the
  next switch. Accepted per Q3; the next `home-manager switch` re-asserts it,
  and the hourly job won't flatten while the breaker is off.
