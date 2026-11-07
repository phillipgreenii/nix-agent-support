# Monorepo checkout create, reset and remove: measurements for the checkout pool

Date: 2026-11-07. Inputs to the pool sizing, keep list and skip-hook decisions.

## Method

- Target: the real monorepo checkout (about 228,800 tracked files, 2.9 GB working tree, git 2.54.0, `core.fsmonitor=true`, `core.untrackedCache=true`), probed through dedicated throwaway worktrees created next to the canonical clone and removed by the probe. No pre-existing worktree, branch or stash was touched; the worktree count was 21 before and after, and no probe branch or fsmonitor daemon was left behind.
- Operations timed (wall seconds, via `nice -n 10`, bash `EPOCHREALTIME`):
  - create: `git worktree add -b <probe> <path> HEAD`
  - first and warm `git status`
  - reset: `git checkout -q -f -B <probe> <commit>` followed by `git clean -fdxq -e <keep paths>`
  - remove: `git worktree remove --force`
- Dirty set before each reset: 500 tracked files modified, 500 untracked files, 200 ignored `*.o` files, and a marker file under `.flox/cache`. Reset targets alternate between a commit 100 commits behind HEAD and HEAD.
- fseventsd CPU: cumulative CPU seconds of the single fseventsd process (`ps -o cputime`) before and after each operation. The same fseventsd process (not restarted) served every sample.
- "Hook on" means the monorepo's own post-checkout hook ran. The checkout's configured `core.hooksPath` resolves to a directory that does not exist on this machine, so by default NO hook runs; hook-on runs override `core.hooksPath` to the checkout's real hooks directory for that one command, and hook-off runs override it to `/dev/null` for that one command. Nothing was written to git config.
- n = 3 per cell (hook-on create and remove: n = 6, from two independent batches of 3).

## Host condition (not idle)

The host was NOT idle and these are not off-peak numbers. One-minute load average ranged from 20.9 to 82.6 across the batches (about 30 at the start). Absolute wall times are inflated and noisy; ratios are the useful output. Disk was 85% used with 67 to 70 GB free throughout (no stop condition reached). The probe's own free-space guard was broken in the first batch (GNU `df` flag mismatch), so free space was checked by hand before and after that batch; the second batch's guard worked.

## What the post-checkout hook writes

The hook (via the worktree-state convergence script) writes, all ignored (untracked, git-excluded) files:

| Path                          | Kind                                                            |
| ----------------------------- | --------------------------------------------------------------- |
| `.envrc.local`                | file, content derives from the worktree directory name and path |
| `.claude/settings.local.json` | symlink to a per-user config file                               |
| `CLAUDE.local.md`             | file (personal-rules trampoline)                                |
| `.vscode/settings.json`       | file (editor colour, keyed by worktree name)                    |

It also appends one line to the shared exclude file when missing (already present), and runs `direnv allow` when `.envrc.local` changed. The probe ran `direnv deny` on each probe path afterwards. `.flox` is partly tracked (`.flox/env` and similar) with `.flox/cache`, `.flox/log`, `.flox/run` ignored and untracked.

## Timings (wall seconds; median, range)

| Operation                               | Hook on                  | Hook off                                                    |
| --------------------------------------- | ------------------------ | ----------------------------------------------------------- |
| Create (`worktree add`)                 | 79.7 (55.5 to 99.7), n=6 | 66.1 (63.7 to 73.1), n=3                                    |
| First `status` after create             | 8.5 (7.9 to 10.1), n=6   | 8.1 (8.0 to 8.4), n=3                                       |
| Warm `status`                           | 0.8 (0.6 to 0.9)         | 0.9 (0.7 to 0.9)                                            |
| Reset, dirty tree, 2-path keep list     | 10.5 (8.6 to 13.2), n=3  | 8.8 (7.9 to 14.0), n=3                                      |
| Reset, dirty tree, 5-path keep list     | not measured             | 8.5 (7.7 to 10.4) to T1, 7.4 (6.5 to 9.3) to HEAD, n=3 each |
| Reset, already-clean tree (same commit) | 9.4 (7.5 to 10.4), n=3   | 7.5 (6.7 to 7.5), n=3                                       |
| `status` right after a skip-hook reset  | n/a                      | 0.6 to 1.3                                                  |
| Remove (`worktree remove --force`)      | 32.0 (27.4 to 34.2), n=6 | 34.4 (25.7 to 35.2), n=3                                    |

Reading the table:

- Hook on versus hook off at create (79.7 against 66.1) and at reset (10.5 against 8.8) differ by an amount well inside the load-driven spread, so the hook cost is not distinguishable from noise here; it is at most a few seconds (a few small file writes, one `jq`, one `direnv allow`).
- A full lease today (create + first status + remove) is about 80 + 8.5 + 32 = 120 s. A pooled lease (reset + warm status) is about 9 + 0.8 = 10 s. That is about 12 times faster (120 / 10).

## fseventsd CPU (cumulative CPU seconds per operation)

| Operation                                | fseventsd CPU seconds                                       |
| ---------------------------------------- | ----------------------------------------------------------- |
| Create                                   | 20.4 to 24.4 typical (one outlier 36.4 in the hook-off set) |
| First status                             | 0.0 to 0.8                                                  |
| Remove                                   | 16.2 to 18.5                                                |
| Reset (dirty set, any hook/keep variant) | 0.2 to 0.9                                                  |
| Warm status                              | 0.0                                                         |

A create + remove cycle costs about 20 + 17 = 37 fseventsd CPU seconds against about 0.3 for a reset (about 120 times less; 37 / 0.3). Instantaneous `%CPU` sampled right after remove was 22 to 40 percent; the burst is short and sits on top of a loaded host. The per-lease filesystem event count of create + remove is on the order of 2 x 228,800 inode operations, against the dirty set plus branch delta for a reset (these runs did not count events directly).

Daemon hygiene: with fsmonitor on, each created worktree starts its own `git fsmonitor--daemon`. `worktree remove --force` without a prior `fsmonitor--daemon stop` left no orphan daemon in any of the 12 cycles (daemon count unchanged before and after each).

## Tree clean and keep list intact after a skip-hook reset

Results, with the keep list passed to `git clean -fdx` as `-e` patterns:

| Keep list                                  | Hook at reset                           | Tree clean (`status` non-ignored lines) | `.flox` and `.envrc.local` | `.claude/settings.local.json`, `CLAUDE.local.md`, `.vscode/settings.json`                            |
| ------------------------------------------ | --------------------------------------- | --------------------------------------- | -------------------------- | ---------------------------------------------------------------------------------------------------- |
| `.flox`, `.envrc.local` (ruled ZR default) | off, n=3                                | 0 (clean)                               | intact                     | DELETED, not regenerated                                                                             |
| `.flox`, `.envrc.local`                    | on, n=3                                 | 0 (clean)                               | intact                     | DELETED even with the hook on (the hook runs during checkout, then `clean -x` removes what it wrote) |
| the above plus the three paths             | off, n=6 resets (2 per slot, n=3 slots) | 0 (clean)                               | intact                     | intact in all 6                                                                                      |

So skip-hook is safe for tree cleanliness in every variant, but with only the two ruled keep paths a reset silently destroys the Claude settings symlink, the personal-rules trampoline and the editor settings, and neither skipping nor running the hook brings them back (the hook runs before the clean). With the extended keep list a skip-hook reset leaves the tree clean and every kept path present.

## Recommendations

1. Final keep list (per repository, ZR): `.flox`, `.envrc.local`, `.claude/settings.local.json`, `CLAUDE.local.md`, `.vscode/settings.json`. Pass each as a `clean -fdx -e <path>` pattern. This extends the ruled two-path default by the three hook-written paths.
2. Skip the post-checkout hook on reset-in-place: the tree stayed clean and the extended keep list stayed intact in 6 of 6 resets, so the default can be skip, provided the five-path keep list is used. Run the full hook once at slot creation (that is when the hook-written files come from). With the two-path list alone the default MUST NOT be skip-and-forget, because the three hook-written files are lost either way.
3. Per-slot paths that derive from the worktree directory name (`.envrc.local`, the editor colour) stay valid across resets only because the slot directory name is stable; a slot MUST NOT be renamed.
4. Slot count: a reset costs about 9 s and about 0.3 fseventsd CPU seconds, so reset time does not limit concurrency; the limit is concurrent agent sessions, disk, and one fsmonitor daemon per slot. Each slot is about 2.9 GB of working tree (the object database is shared by worktrees). Recommend a default of 4 slots, configurable, with a documented ceiling of 6 (6 x 2.9 GB = 17.4 GB, about a quarter of the 68 GB free on this host). A lease that finds no free slot SHOULD queue rather than create an extra checkout, since an extra create costs about 120 s and about 37 fseventsd CPU seconds.
5. Caveats: single host, one commit pair, heavy host load, git 2.54.0. Re-measure on an idle host before treating the absolute seconds as a baseline; the ratios (about 12 times for time, about 120 times for fseventsd CPU) are the decision-relevant output.
