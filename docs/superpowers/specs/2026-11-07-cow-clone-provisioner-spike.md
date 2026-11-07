# Spike: CoW-clone-backed checkout pool slots vs worktree slots

Date: 2026-11-07. Scope: a `Clone` implementation of the `Provisioner` interface in
`packages/pg-checkout-pool/internal/provision`, compared with the `Worktree` provisioner.

## What was built

- `Clone` (`clone.go`): a slot is a complete, independent repository made by ONE directory-level
  `clonefile(2)` of a "golden" checkout (a clean, non-bare checkout with no linked worktrees and no
  running fsmonitor daemon, on the same APFS volume as the slots).
  - `Create` clones the golden, deletes inherited `.git/worktrees`, sets `core.checkStat=minimal` and
    `core.trustctime=false` BEFORE any status (clonefile changes every inode and ctime, so otherwise the
    first `git status` re-hashes the whole tree; the earlier CoW spike measured about 20 s per 100k files
    against under 1 s with the two settings), records the golden path in the slot's own config, cleans to
    the keep list, then checks out the commit detached with the post-checkout hook enabled (the one full
    hook run of the slot's life, as for worktrees).
  - `Verify` additionally requires the slot's git directory to be its own and the recorded golden to match
    the configured repository (so a foreign repository is unhealthy).
  - `Reset`, `Detach`, `Resolve`, `Rescue` are the `Worktree` behaviour run inside the slot's own repository.
    `Reset` first fetches `refs/heads/*` from the golden when the slot's object store lacks the commit
    (the slot's objects are a snapshot of the golden at creation time).
  - Lock detection now asks git for the common directory instead of assuming a linked worktree; this is the
    only change to the worktree code path.
- `clone_darwin.go` calls `unix.Clonefile`; `clone_other.go` returns `ErrCloneUnsupported`, so Linux builds
  compile and the clone tests skip (they also skip on a macOS volume without clone support).
  A raw `syscall.Syscall(SYS_clonefile)` was tried first and fails with EINVAL on arm64 macOS (libc
  `syscall` is variadic, which breaks argument passing on the Apple arm64 ABI), hence the new
  `golang.org/x/sys` dependency (v0.47.0, the newest version that builds with the Go 1.25 toolchain here).
- The shared provisioner test suite (`suite_test.go`, `runProvisionerSuite`) is the former worktree test
  file made implementation-agnostic, with real temporary directories and real git (no mock of the
  provisioner), and extended with a Resolve/Rescue case. `worktree_test.go` and `clone_test.go` each run it.
  `clone_test.go` adds clone-only cases (independent `.git`, same branch checked out in golden and slot,
  fetch-on-demand for a later golden commit, refusal to overwrite an existing slot).
- `measure_test.go` (build tag `measure`, never in the unit gate) is the measurement used below.

## Method

Disk was nearly full (62 to 63 GB free, active disk-space alert), so the real monorepo (2.9 GB tree, about
228,800 files) was NOT cloned or copied. Measurements use a synthetic repository in a fresh temporary
directory, removed by the test: 20,000 tracked files of about 1 KB in about 2,000 directories, two commits
(500 files differ), packed with `git gc`. Both provisioners work on the same golden repository in turn.
Filesystem events were counted by the existing FSEvents counter (`cowspike-fscount`, file-level events,
watching the test directory). Time is wall seconds from the test. n = 1 per cell, host load average about 23,
so treat seconds as noisy and events as exact.

Disk free on `/` (`df -h /`), before and after (the test directory was removed; peak extra use was about
0.3 GB):

```
before: /dev/disk3s1s1  461G  399G   62G  87% /
after:  /dev/disk3s1s1  461G  399G   62G  87% /
```

(63G free at session start.)

## Results (20,000 files)

| Operation                                 | Worktree: seconds | Worktree: events | Clone: seconds | Clone: events |
| ----------------------------------------- | ----------------- | ---------------- | -------------- | ------------- |
| Create (`Create(dir, c1)`)                | 3.45              | 22,027           | 0.77           | 1,023         |
| First `git status`                        | 0.28              | 4                | 0.23           | 4             |
| Warm `git status`                         | 0.13              | 2                | 0.09           | 2             |
| Reset, dirty tree (to c2, branch)         | 0.46              | 1,419            | 0.74           | 1,415         |
| Reset, tree at c2 (to c1, detached)       | 0.33              | 1,012            | 0.55           | 1,012         |
| Remove (`worktree remove --force` / `rm`) | 2.25              | 22,014           | 1.51           | 22,056        |

Reading the table:

- Create: 22,027 / 1,023 is about 21 times fewer events and 3.45 / 0.77 is about 4.5 times faster. The 1,023
  events are not the clone itself (3 events, per the earlier CoW spike): the golden sits at c2 and the slot is
  checked out to c1, so git rewrites the 500 differing files and the hook runs. Creating a slot at the
  golden's own commit would cost close to 3 events, so the clone cost scales with the diff from the golden,
  not with tree size. Event counts scale linearly in tracked files, so the monorepo (about 11 times 20,000
  files) would be about 250,000 events for a worktree create against a diff-sized count for a clone; that
  extrapolation is NOT measured.
- First status: with the two stat settings the clone's first status is as cheap as the worktree's, so the
  re-hash cost of a clone is fully mitigated here.
- Reset: equal events (1,415 against 1,419; 1,012 against 1,012) because reset in place touches the same
  files whatever the slot kind. The clone is not faster there (0.74 against 0.46 and 0.55 against 0.33, single
  samples under load; no claim of a real difference).
- Remove: clone removal is NOT cheaper. 22,056 against 22,014 events (a clone's remove emits one event per
  path, as the earlier CoW spike found), so CoW gives no remove-side event win. Only recycling a slot avoids it.
- Disk (not measured here, from the earlier CoW spike): a clone shares data extents with the golden until
  written, about 330 B per file against 4.5 KB per file for a checkout, but each clone owns its own `.git`
  administration and diverges on fetch or gc, whereas worktrees share one object store.

## Comparison with the monorepo baseline

The worktree baseline on the real monorepo is in
`docs/superpowers/specs/2026-11-07-monorepo-checkout-pool-measurements.md`: create about 66 to 80 s, first
status about 8 s, reset about 8.8 to 10.5 s, remove about 32 to 34 s, about 120 s for a full create, status
and remove lease, about 37 fseventsd CPU seconds per create plus remove. Real-monorepo numbers for the clone
provisioner are a FOLLOW-UP (they need a golden checkout of the monorepo and a second 2.9 GB tree, which the
disk could not take now). The synthetic ratios above (create about 4.5 times faster, about 21 times fewer
events) are the only evidence for the clone's create cost.

## Costs and risks of Clone

- A golden checkout must be maintained: clean, no worktrees, no live fsmonitor daemon, same volume as the
  slots (clonefile across volumes fails with EXDEV, reported as `ErrCloneUnsupported`).
- Each slot carries its own `.git` copy (hooks, config, refs, packs); objects are shared only until the slot
  fetches or runs gc. A slot learns of new commits only by fetching from the golden, which `Reset` does on
  demand for branch-reachable commits; commits that exist only on a detached HEAD in the golden are not fetched.
- Hooks and hook-written ignored files are copied from the golden verbatim; an absolute `core.hooksPath`
  still points at the golden's hook bundle.
- Untracked and ignored files in the golden leak into new slots except those removed by the keep-list clean at
  creation, so the golden MUST stay pristine.

## Recommendation

ADOPT as an opt-in provisioner for slot creation (pool growth), keeping `Worktree` as the default; do not
replace it and do not wire it as default yet.

Why: the pool already makes creation rare (a pooled lease is about 10 s against about 120 s), reset and remove
cost the same for both kinds, and the clone adds golden maintenance and per-slot `.git` divergence. The only
measured win is create-time events and seconds, which matters mainly while the pool warms up or grows during
agent bursts. That is worth keeping available because the implementation is small, passes the shared suite,
and degrades to an explicit `ErrCloneUnsupported` off macOS. The decision to make it the default should wait
for the follow-up: one real-monorepo create on a host with room for a second 2.9 GB tree, comparing create
seconds, event count and fseventsd CPU against the worktree baseline above. If the real create is not at least
several times cheaper, DROP the clone provisioner.

## Reproduce

```sh
cd packages/pg-checkout-pool
PG_CHECKOUT_POOL_FSCOUNT=<path to FSEvents counter> \
  nice -n 10 go test -tags measure -run TestMeasureCloneVsWorktree -count=1 -v ./internal/provision/
```

`PG_CHECKOUT_POOL_MEASURE_FILES` sets the tracked file count (default 20,000). Omit the counter variable to
get times only.
