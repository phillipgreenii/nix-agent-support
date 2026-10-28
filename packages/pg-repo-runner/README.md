# pg-repo-runner

`pg-repo-runner` is the kernel and facade of the pg-router-managed async repo runners (design
`pg-router-async-runners`, origin bead `pg2-na2nr`; ADR 0108 in `docs/adr/`). It is a bash
`mkBashScript` package (`pg-repo-runner/pg-repo-runner.sh`, the kernel functions in
`pg-repo-runner/pg-repo-runner.bash`) with a home-manager module,
`phillipgreenii.programs.pg-repo-runner` (`home/programs/pg-repo-runner`).

Beads: `pg2-zj2v7m` (F1) is the kernel, the verdict store and `status`; `pg2-j0itft` (F2) is runner B, the
post-land verifier (the `tips` source and the `verify` role); `pg2-loswfh` (F3) is failure surfacing;
`pg2-28h96s` (F5) is runner A's report-only `sync`; `pg2-nvye55` (F6) is runner A's push step;
`pg2-xg9ppr` (F8) is runner C, the cross-repo workspace builder (the `workspace-tips` source and the
`build` role, below). Operator procedures (state, status, kill switch, red tips, first
enablement) are in the runbook `docs/runbooks/pg-repo-runner.md`; this README is the package reference.

**No repo is enabled for push by this package, its module defaults, its tests or any deployment
config in this repository.** `repos.<name>.push` defaults to `false` for every repo, the module
asserts that default, and the only code that can publish (the push step of `sync`, below) never runs for
a repo whose `push` is not exactly `true`. Enabling a repo needs an operator ruling recorded on a bead
(ADR 0108, decision 1), and the first enablement runs dry-run (decision 6): `dry_run` defaults to true.

## Commands

| Command          | What it does                                                                   |
| ---------------- | ------------------------------------------------------------------------------ |
| `tips`           | Source: one JSONL record per allow-listed repo tip with no check verdict.      |
| `workspace-tips` | Source: the debounced workspace tip tuple, as `workspace@<hash>` (runner C).   |
| `verify`         | Role: an immutable-snapshot `nix flake check` of the tip, then a verdict.      |
| `sync`           | Role: fetch, classify, fast-forward a behind clone, surface divergence, push.  |
| `build`          | Role (workspace-wide, item `workspace@<hash>`): `pn workspace build` in a set. |
| `status`         | Prints the verdict store, the lease and the kill switch (`--json`, `--repo`).  |

## State, status and the kill switch

Everything the runner writes is under the state directory
`${XDG_STATE_HOME:-$HOME/.local/state}/pg-repo-runner/` (mode `0700`; never a path under a repo). The
paths below were checked against the built package.

| Path under the state directory      | What it is                                                                      |
| ----------------------------------- | ------------------------------------------------------------------------------- |
| `run.log`                           | One line per step of every run (pg-router discards a role's stdout and stderr). |
| `lease`                             | Single-flight lease, a symlink to `<pid>:<process start time>`.                 |
| `verdicts/<repo>/<sha>.<kind>.json` | The verdict store.                                                              |
| `logs/<repo>/<sha>.check.log`       | The nix output behind a `check` verdict.                                        |
| `sync/<repo>.json`                  | The last sync pass of a repo.                                                   |
| `surface/<repo>.<kind>.json`        | The failure transition records.                                                 |
| `push.disabled`                     | Kill switch file (the runner never creates or removes it).                      |
| `push-live/<repo>`                  | Dry-run exit flag for one repo (the operator creates it).                       |
| `push-audit.jsonl`                  | One line per real push.                                                         |

**Kill switch.** Two files stop every role (`verify`, `sync`, `build`; a hand run too) with exit 9: the
state directory's `push.disabled` and the file named by `kill_switch` in the config (the home module
defaults it to `<state dir>/kill`). `tips` ignores both. Beyond the files, `pg-router pause` (the
`SYSTEM_PAUSE` gate) stops the deployment's roles, and `repos.<key>.push = false` (the default of every
repo) keeps `sync` from reaching the push step. `pg-repo-runner status` prints `push.disabled` only, not
the config `kill_switch` file.

**`status`.** `pg-repo-runner status [--json] [--repo KEY]` reads the state directory only. The text form is
`state dir:`, `kill switch:` (`absent` or the file), `lease:` (`free`, `held by <pid>:<start>` or `stale (...)`),
`verdicts:` (a tab-separated row per verdict: repo, sha[0:12], kind, result, inputs, finishedAt),
`open transitions:` (repo, kind, sha[0:12], bead id or `bead-pending`, `notified` or `notify-pending`) and
`sync:` (repo, outcome, behind, ahead, behind-before-fetch, at); `--json` prints only the verdicts, as one
array.

**Reserved branch prefix.** `runner-build-` is reserved for the workspace builder (runner C, below). That
runner creates its throwaway workforest set on a `runner-build-<utc timestamp>` branch and removes any
leftover set with that prefix, so nobody MAY name a branch or a set that way.

## The kernel (Template Method)

A role runs these steps in order and stops at the first that fails. `build` skips steps 2 and 3 (its
item is `workspace@<hash>`, not a repo; the allow-list and path checks run over every repo of the set
in its role body, before anything is created).

1. **resolve.** `PG_ROUTER_ITEM_ID` (which pg-router always exports) is `<key>` or `<key>@<sha>`.
   `<key>` MUST be in the config allow-list (`repos.<key>`) AND be a repo key of
   `<workspace_root>/pn-workspace.toml`. Otherwise exit 2.
2. **path check.** The real path of `<workspace_root>/<key>` MUST equal
   `<real workspace_root>/<key>` and be the top level of a git repository. A symlinked key, a plain
   directory, or a path outside the workspace (in particular the employer's private monorepo, which
   is not a workspace repo) is refused with exit 2.
3. **role flag.** `repos.<key>.verify` (for `verify`) or `repos.<key>.sync` (for `sync`) MUST be
   true; if it is false the run is skipped and exits 0.
4. **kill switch.** `<state dir>/push.disabled` or the file named by `kill_switch` in the config
   exists: exit 9.
5. **load check.** The 1-minute load average MUST be strictly below the threshold
   (`thresholds.max_load`, else the core count): otherwise exit 9.
6. **single-flight lease.** One lease for all roles, taken without waiting (see below): held by a
   live process, exit 9.
7. **act**, then record a verdict, then release the lease.

Exit codes: `0` for any verdict (a red check is data, not a failure), `9` for "not now" (pg-router
re-offers the item until it expires), `2` for a usage error, a refusal or a stub, `1` for a
malfunction (unreadable config, no load reading, an unusable state directory).

## Runner B: `tips` and `verify`

### `tips` (the source)

For each repo of the allow-list with `verify = true` that passes the resolve and path checks, it
reads the tip of the primary branch (`repos.<key>.branch` of `pn-workspace.toml`, default `main`;
never the checked-out branch) from git objects, and, when no check verdict is stored for that sha,
prints one JSONL record on stdout and nothing else:

```text
{"id":"<repo>@<sha>","type":"repo-tip","title":"<repo> tip","metadata":{"repo":"<repo>","sha":"<sha>","branch":"main"},"at":"<now>","expiresAt":"<now + retry window>"}
```

The id carries the sha, so a re-emit is the same event and a new tip is a new one; `expiresAt` is
the retry window (`thresholds.retry_window` seconds, default 3600). "Tips without a verdict" is
derived state, so a crash resumes by construction. A repo that fails a check is logged in the run
log and skipped. `tips` takes no lease, does not look at the load and ignores the kill switch: it
only reads refs and the verdict store.

### `verify` (the role)

`PG_ROUTER_ITEM_ID` MUST be `<repo>@<sha>` (a bare key is exit 2). After the kernel steps:

1. A check verdict already stored for the sha ends the run at once with exit 0, before the kill
   switch, the load check and the lease (Idempotent Receiver); no nix runs.
2. Precheck: `<sha>` MUST be a commit of the repo; if not, the run is skipped with exit 0 (logged,
   no verdict). The canonical working tree is never read.
3. Latest wins: if the primary branch no longer points at `<sha>`, a `superseded` verdict is stored
   and the run exits 0 without running nix (the newer tip has its own event).
4. Otherwise, under the lease:

   ```text
   nice -n 10 pg-nix-log-wrapped nix flake check --no-write-lock-file "git+file://<canonical abs path>?rev=<sha>"
   ```

   The target is an immutable commit read from git objects, so a dirty or untracked working tree
   cannot change the result.

5. Verdict (`kind` `check`, `inputs` `locked`): exit 0 is `green`; any other non-zero exit is `red`;
   a signal exit (128 + n, that is 129 to 192) is a malfunction: exit 1, NO verdict is stored, and
   pg-router retries (`max_dispatch_retries`). There is no "inconclusive" class and a red is never
   retried automatically. A red verdict exits 0 (it is data, not a failure of the runner).

The verdict also records the measured duration and the sampled load, the calibration input for
`thresholds.max_load`: `durationS` (whole seconds between `startedAt` and `finishedAt`) and
`loadStart` and `loadEnd` (the 1-minute load average read when the check started and when it ended).
The output of nix is kept at `$XDG_STATE_HOME/pg-repo-runner/logs/<repo>/<sha>.check.log`
(`logPath` in the verdict).

### The `nix-ziprecruiter` snapshot probe

See the design document's "Open probe" section for the recorded result: the repo checks from a
commit snapshot, so it needs no exception (its gitignored dev symlink is not part of any nix path).

### Lease

`$XDG_STATE_HOME/pg-repo-runner/lease` is a symlink whose target is `<pid>:<process start time>`.
`ln -s` is atomic and fails when the name exists, so creating it IS taking the lease. A lease is
stale when its pid is dead, or alive with a different start time (a recycled pid); a stale lease is
renamed aside (atomic, so one reclaimer wins), re-checked, and taken. This is the pattern
`pg-test-runner` uses for its run slots, without the wait and without the fail-open.

### Verdict store

`$XDG_STATE_HOME/pg-repo-runner/verdicts/<repo>/<sha>.<kind>.json`, with the fields `repo`, `sha`,
`kind` (`check` or `build`), `result` (`green`, `red`, `superseded`; the writer also accepts `error`, which no role writes today), `inputs` (`locked` or `override`),
`startedAt`, `finishedAt`, `exit`, `logPath`, and for a verify check `durationS`, `loadStart`,
`loadEnd`. A `superseded` result means the tip moved before the check ran. `rr_verdict_write` builds the document in a temp file
in the same directory and renames it over the final name, so a reader sees the old verdict or the
new one, and a failed write leaves no temp file. The run log is
`$XDG_STATE_HOME/pg-repo-runner/run.log` (pg-router discards a role's stdout and stderr, so this file
is where a run is explained).

### nix

`rr_nix_run ARGS...` is the one way a runner starts nix: `nice -n <level> pg-nix-log-wrapped nix
ARGS...`, with every binary resolved by the absolute store path the nix build injects
(`RR_NICE_BIN`, `RR_NIX_LOG_WRAPPED_BIN`, `RR_NIX_BIN`; the config's `thresholds.nice_level`,
default 10). F1 offers it; the roles of the later beads call it.

## Failure surfacing

Bead `pg2-loswfh` (F3). The kernel surfaces TRANSITIONS, never steady states (design "Failure
surfacing", decision D7: a P1 bead per repo plus a notification on transitions; no SwiftBar
plugin). It is off unless the config carries `[surface] enabled = true`.

| Transition                                   | Raised by                                                 |
| -------------------------------------------- | --------------------------------------------------------- |
| `red`: a repo's primary tip fails its check  | `verify` (this version)                                   |
| `diverged`                                   | `sync` (runner A), `rr_surface_raise REPO diverged SHA`   |
| `needs-relock`: `push blocked: needs relock` | `rr_surface_raise REPO needs-relock SHA [DETAIL]`         |
| `build-red`: the workspace build is red      | `rr_surface_raise REPO build-red SHA [DETAIL]` (runner C) |

Per (repo, kind) the state is a Memento, `$XDG_STATE_HOME/pg-repo-runner/surface/<repo>.<kind>.json`
(`status` lists the open ones). The rules:

1. The first report after a clear (or ever) is the transition: it files ONE bead (type task, priority
   1, the repo's label from `surface.repo_labels`, metadata `runner = pg-repo-runner`, `repo`, `kind`,
   `sha`) and posts ONE macOS notification (title and body travel as argv items of a fixed AppleScript,
   never inside the script text).
2. A repeated report while the record is open files nothing and notifies nothing. A newer red sha is
   commented on the bead.
3. A green check at a sha that DESCENDS from the red tip (`git merge-base --is-ancestor`, a read) comments
   the green verdict on the bead and closes it, but only a bead whose metadata says `runner =
pg-repo-runner`; any other bead is left alone and the record is cleared. A green that does not
   descend leaves everything open. Recovery posts no notification.
4. Each part (bead, notification, close) is recorded the moment it succeeds. A part that fails (tracker
   unreachable, notifier failed) stays owed and the next run of that repo finishes exactly that part
   (BF-2: the runner never files in another tracker; without a label for the repo it files nothing
   rather than a label-less bead). Before creating, the runner looks for its own open bead for the pair,
   so a crash between "bead created" and "id stored" adopts the bead instead of duplicating it.
5. Surfacing never changes the exit status of a role, never reverts, resets, edits or pushes a repo
   (the expectation is fix-forward), and a red verdict still exits 0. pg-router events and metrics are
   for malfunction (non-zero exit) only.

The beads go to ONE tracker, `surface.tracker_dir` (a directory holding `.beads`; the workspace uses one
shared tracker, so the repo label is what tells repos apart, BF-1). `bd` is resolved from the ambient
PATH of the run (`PG_REPO_RUNNER_BD_BIN` replaces it), like every bd-calling script here, so the
deployment's pg-router role MUST put the machine's `bd` on that PATH.

**One filing path.** The runner is the only thing that files these beads: it files, comments and closes
through `bd` directly and reads its labels from `surface.repo_labels`. It does NOT go through pg-rescue
(ruling `pg2-8x6t6t`): `pg-rescue-bead` cannot set the `runner = pg-repo-runner` metadata and cannot
close a bead, and the auto-close needs both, so a pg-rescue chain would have been a second,
undriven implementation of the same transition. The design's failure-surfacing requirements (the bead
with its repo label, one notification per transition, dedup, auto-close of its own beads) are all met
here.

## Sync (runner A, report-only)

Bead `pg2-28h96s` (F5). `PG_ROUTER_ITEM_ID=<repo> pg-repo-runner sync` is one pass over one repo of the
allow-list (`repos.<key>.sync = true`), after the kernel steps (resolve, path, enabled, kill, load, lease).
Dependency order across repos is the order in which the deployment's `repo.sync` events are offered, not
something this tool sequences. The pass:

1. **Precheck, read-only (R-3).** The canonical clone MUST have no `core.worktree`, be on the primary branch
   (from `pn-workspace.toml`, default `main`), have an empty `git status --porcelain` and no rebase, merge,
   cherry-pick or revert in progress. Otherwise the repo is skipped this pass, the record says
   `yielded` with `canonical not steady: <reason>`, and nothing is repaired: no fetch, no stash, no checkout. The
   precheck writes nothing (`GIT_OPTIONAL_LOCKS=0`: not even a refreshed index), which the tests prove by
   hashing the whole `.git` directory before and after.
2. **Fetch.** `git fetch origin +refs/heads/<primary>:refs/remotes/origin/<primary>`: a remote-tracking
   ref update only. The count read BEFORE the fetch is kept as `behindBeforeFetch`, because a remote-tracking
   ref that was never fetched understates "behind" (the design's stale-ref caveat); the classification re-reads
   after the fetch. A failed fetch is recorded (`fetch-failed`) and the pass exits 0: an unreachable remote is
   not a malfunction of the runner, and the next event retries.
3. **Classify** `git rev-list --left-right --count origin/<primary>...<primary>`:

| Behind, ahead | Outcome (`status`) | Action                                                                                                                 |
| ------------- | ------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| 0, 0          | `in-sync`          | Nothing (an open `diverged` bead of the runner is closed).                                                             |
| more, 0       | `fast-forwarded`   | `git merge --ff-only origin/<primary>` in the clean canonical clone. A refusal is `ff-refused`, recorded, not retried. |
| 0, more       | `ahead`            | Record the count and do NOTHING else: no bead, no notification (unpushed commits are not in themselves a problem).     |
| more, more    | `diverged`         | ONE deduplicated bead (the F3 transition) "diverged, resolve with pn workspace rebase or /pn-workspace-sync".          |

A diverged repo is never rebased, reset or forced (decision D3): the runner has no such code. The bead is closed
by the runner's own next pass that finds the repo no longer diverged (comment with the evidence, then close; only
a bead with metadata `runner = pg-repo-runner`).

### The push step (ADR 0108, bead `pg2-nvye55`)

An ahead-only repo (`behind 0, ahead > 0`) is a push candidate. The step does nothing but record the count
unless `repos.<key>.push` is exactly `true` (no configuration in this repository sets it). The command is
exactly

```text
git -C <canonical clone> push origin <verified-sha>:refs/heads/<primary>
```

(`rr_push_exec`, the only `git push` in the code base): no `--force`, `--force-with-lease`, `+` refspec,
deletion, other ref, `--tags`, `--mirror` or `--no-verify` (the repo's own pre-push hooks run), and never
`pn workspace push|update|apply`. Gates, all in the same pass; if one fails the record stays `ahead` with
`push withheld: <reason>` and nothing is pushed:

| Gate | Check                                                                                                                                                                                                |
| ---- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| (a)  | `push = true`, and no kill switch (`<state dir>/push.disabled`, the config `kill_switch` file, the pg-router `SYSTEM_PAUSE` gate that stops the role before it runs): re-read right before the push. |
| (b)  | A `check` verdict, `green`, over the locked inputs, for exactly the primary tip; the tip is re-read right before the push (no partial push of a green ancestor).                                     |
| (c)  | The canonical precheck of this pass passed (a dirty, off-primary or mid-operation clone is `yielded` before the step is reached).                                                                    |
| (d)  | `origin/<primary>` is an ancestor of the tip. Otherwise the repo is diverged and takes the F5 branch (one deduplicated bead). A remote that moves after the check makes git refuse: `push-rejected`. |

A red tip while a sibling named in `repos.<key>.consumes` has commits its remote lacks is "blocked, needs
sibling push and relock: run `pn workspace push`": a `needs-relock` transition (one bead, one notification),
closed by the runner's own next successful push. The runner never pushes the sibling.

**Dry run (decision 6) is the default.** With `push = true` and the dry run on, the pass records
`push-dry-run`, logs `would push <old>..<new> (N commit(s))` and one line per commit, and pushes nothing.
The operator reviews the range, then removes the dry run: the flag file `<state dir>/push-live/<repo>`, or
`repos.<key>.dry_run = false`.

**Audit (decision 7).** A real push records `pushed` and writes one JSON record (`event`, `repo`, `remote`
without credentials, `branch`, `oldRemoteSha`, `newRemoteSha`, `verdict` with its path, sha, result and
finish time, `at`) to `<state dir>/push-audit.jsonl`, the run log (`push-audit {...}`) and stderr. A push
that git refuses is `push-rejected`, never forced and not retried in the pass.

The tests are `test-pg-repo-runner-push.bats` (real git, bare local remotes, a recording git wrapper, and a
one-shot hook that moves the tip, the remote or the kill switch in the middle of a pass; negative control
2026-10-28: a gate that always passes, and a `--force` inserted into the argv, both fail it) and a source
guard that the push appears in exactly one code line with exactly the allowed argv.

The result of every pass is `$XDG_STATE_HOME/pg-repo-runner/sync/<repo>.json` (`repo`, `branch`, `outcome`,
`behind`, `ahead`, `behindBeforeFetch`, `detail`, `at`), printed by `status` under `sync:`.

### Evidence table (the first bead re-reads "behind")

The design's table was computed on 2026-10-23 without a fetch. Re-read on 2026-10-28 with
`git rev-list --left-right --count origin/main...main` in each canonical clone, again WITHOUT a fetch (the
implementing session opened no network connection), so "behind" is still the stale reading; the runner's
first real passes replace it (`behindBeforeFetch` next to the post-fetch `behind` in the record).

| Repo                | Behind (stale refs) | Ahead, 2026-10-23 | Ahead, 2026-10-28 |
| ------------------- | ------------------- | ----------------- | ----------------- |
| `nix-repo-base`     | 0                   | 8                 | 2                 |
| `nix-personal`      | 0                   | 1                 | 0                 |
| `nix-support-apps`  | 0                   | 48                | 2                 |
| `nix-ziprecruiter`  | 0                   | 70                | 9                 |
| `nix-overlay`       | 0                   | 0                 | 0                 |
| `nix-agent-support` | 0                   | 181               | 33                |
| `x`                 | 0                   | 0                 | 0                 |

Total ahead on 2026-10-28: 2 + 0 + 2 + 9 + 0 + 33 + 0 = 46 (down from 308: the operator has been publishing).
Behind after a real fetch is the post-deploy check (see the bead's verification).

## Runner C: `workspace-tips` and `build`

Bead `pg2-xg9ppr` (F8). One build of the WHOLE workspace at one tuple of primary tips, in a throwaway
coordinated workforest set, never in a canonical working tree. It is the only runner that exercises the
host's module system with the local siblings (what `pn workspace apply` does next).

**Opt-in.** The set holds every repo of `pn-workspace.toml`, so every repo key MUST be on the allow-list
with `repos.<key>.build = true` (default false, like every flag); otherwise the source emits nothing and the
role refuses the whole run (exit 2) before it creates anything. All repos MUST share one primary branch
(`workforest add` takes one start point). `thresholds.quiet_window` (default 900 s) is the Debounce.

### `workspace-tips` (the source, a Debounce)

Reads the primary tip of every repo (git objects only), hashes the sorted `repo=sha` lines (sha256), and
prints one JSONL item, `{"id":"workspace@<hash>","type":"workspace-tips",...,"metadata":{"hash","branch","tips"}}`,
only when (a) no `build` verdict is stored for that hash, so the tuple changed since the last build verdict,
and (b) the tuple has been unchanged for the quiet window. The state is one file,
`<state dir>/workspace-tips.json` (`{hash, since}`): the first sight of a tuple, and every change of it,
restarts the window, so a burst of landings yields ONE event, for the final tuple (3 ticks of the 5 minute
poll = 15 minutes with the defaults). Later ticks re-offer the same id until a verdict exists; the item
expires after the retry window. It takes no lease and ignores the load and the kill switch.

### `build` (the role)

After the kernel steps (resolve of the item, idempotent receiver, kill switch, load, lease):

1. The allow-list, path and tip checks over every repo (a refusal is exit 2, nothing created).
2. **Leftovers first.** Any set directory or member-repo branch named `runner-build-*` whose encoded UTC
   time is at or before this run's start (or that encodes none) is removed: the prefix `runner-build-` is
   RESERVED for this runner, an operator-created set of that name would be removed too, and nothing
   without the prefix is ever touched. A set from the future is left alone and logged.
3. Latest wins: if the tuple is no longer the item's, a `superseded` verdict is stored and no pn runs.
4. `PN_WORKSPACE_ROOT=<canonical root> pn workspace workforest add runner-build-<utc> refs/heads/<primary> --no-hooks`
   (`--no-hooks`: no per-member `install-pre-commit-hooks`).
5. The set's repos MUST hash to the item (a tip may have moved during creation); if not, the set is
   removed and NO verdict is stored (the next tuple is offered again).
6. In the set: `PN_WORKSPACE_ROOT=<set dir> nice -n <level> pn workspace build`. `pn` is NOT prefixed with
   `pg-nix-log-wrapped` (it wraps its own nix call; a test proves the wrapper is never called).
7. A `build` verdict keyed by the hash (`repo` is `workspace`, `inputs` is `override`, with `tips`, `set`,
   `durationS`, `loadStart`, `loadEnd`): exit 0 is green, any other exit is red, and a red build exits 0.
   Not verdicts: exit 75 (pn's single-flight lock is held) is busy (exit 9, the item is re-offered); a signal
   exit, 69, 126, 127, a hook-trust refusal ("not trusted") and a workspace id collision (see below) are
   malfunctions (exit 1, no verdict).
8. The set is removed whatever happened (`pn workspace workforest remove <branch> --force`, a prune, and
   `git branch -D <branch>` in each member repo, only for the reserved prefix). A run that dies with SIGKILL
   leaves the set; step 2 of the next run removes it. After a full cycle the canonical clones are byte-identical:
   the tests hash every file of `.git` and of the working tree, `git status --porcelain` and the refs.

A red build is a `build-red` transition (F3): ONE P1 bead (`runner = pg-repo-runner`, `repo = workspace`,
`kind = build-red`) and one notification; a newer red tuple only comments; a later green build comments and
closes the runner's own bead. The bead carries `surface.repo_labels.workspace` when set, else the label of the
workspace's terminal repo (the repo whose host closure the build realizes), else none (the bead stays
pending, BF-1).

The deployment MUST give the role a PATH that provides `pn`, `nix` and `darwin-rebuild`
(`PG_REPO_RUNNER_PN_BIN` replaces `pn`), and the canonical workspace MUST have been trusted once with
`pn workspace allow` (a set inherits the trust).

### Known limit: pn's workspace id check

`pn workspace build` runs the machine-local workspace id uniqueness check of its prelude
(`checkWsidUnique`, nix-repo-base ADR 0012) against the root it runs in. A set carries a copy of the
canonical `[workspace] id`, so once the canonical workspace has claimed the id (any apply or build from the
canonical root does), a build in a set fails with `wsid "<id>" already used by workspace "<canonical root>"
(must be unique per machine)`, exit 1. The runner reports that as a malfunction (exit 1, no verdict, the
set removed) and never works around it (no edit of the set's `pn-workspace.toml`, no trust change, no
private `XDG_DATA_HOME`, which would also hide nix's own trusted-settings). The tests prove it with the real
pn (`real pn: a workspace id the canonical workspace already registered ...`). Runner C therefore needs
`pn` to exempt a workforest set from that check before it can be enabled on a machine that has applied the
workspace; no repo enables `build` in this repository.

### Measurements (2026-10-28, a throwaway fixture only)

Both were taken on a throwaway workspace (a bare-remote-backed `nix-alpha` flake and `nix-gamma`, a consumer
that locks `alpha`; real pn `0.0.0-16add71a`, nix 2.34.9, `aarch64-darwin`), never on a real workspace.

- **`nix flake check ... --override-input` does NOT rewrite `flake.lock`.** In the set's consumer repo,
  `nix flake check --override-input alpha git+file://<set>/nix-alpha` and `pn workspace flake-check` both
  left `git status --porcelain` empty and `git diff --stat` empty, while the locked `alpha` rev differed
  from the overridden one. Control: the same flake edited to need a new lock entry, WITHOUT `--override-input`,
  rewrote `flake.lock` (` M flake.lock`); WITH `--override-input` it left only the edit (` M flake.nix`).
  `pn workspace build` (a `nix build` build command) printed an `Updated input 'alpha'` line (the override
  differs from the lock), built `alpha-v2` (the overridden tip, not the locked `alpha-v1`) and left `flake.lock`
  untouched: `git status --porcelain` showed only `?? result`. Inside a set none of this could reach a
  canonical clone anyway.
- **The `result` symlink lands in the set's terminal repo.** `pn workspace build` runs its build command with
  its working directory set to the terminal repo directory of the root it runs in: in the canonical
  workspace that is `<root>/nix-gamma` (a `result` symlink appeared there, untracked), in a set it is
  `<set>/nix-gamma` (`?? result` in the set only; the canonical clone stayed clean). The fixture used
  `nix build` as the build command; a real `darwin-rebuild build --flake` writes its `result` the same way
  (out-link in the working directory) but that was not run (no darwin configuration in a throwaway).

## Configuration

The home module renders `$XDG_CONFIG_HOME/pg-repo-runner/config.toml` (override with
`PG_REPO_RUNNER_CONFIG`):

```toml
workspace_root = "/path/to/workspace"
kill_switch = "/path/to/state/pg-repo-runner/kill"

[thresholds]
max_load = 11.0 # omitted when unset: the core count
nice_level = 10
retry_window = 3600 # seconds a tip item stays offered
quiet_window = 900 # seconds the workspace tip tuple must stay unchanged (runner C)

[repos.nix-example]
verify = false
sync = false
build = false # take part in the workspace build (runner C): EVERY workspace repo must opt in
push = false
dry_run = true # only read when push = true: log the range, push nothing
consumes = [] # sibling repo keys whose unpublished commits can make this repo red
```

Failure surfacing adds, when enabled:

```toml
[surface]
enabled = true
tracker_dir = "/path/to/dir-with-.beads"
notify = true

[surface.repo_labels]
nix-example = "example-label"
```

Every flag defaults to `false`. The allow-list is the ONLY source of repos a runner may touch. The
pg-router queries and roles that drive the commands belong to the deployment repository, not to this
package.

## Test seams (environment)

| Variable                       | Effect                                                         |
| ------------------------------ | -------------------------------------------------------------- |
| `PG_REPO_RUNNER_CONFIG`        | Path of the config file.                                       |
| `PG_REPO_RUNNER_LOADAVG`       | Replaces the 1-minute load reading (the bats suites use this). |
| `PG_REPO_RUNNER_MAX_LOAD`      | Replaces the load threshold.                                   |
| `PG_REPO_RUNNER_NCPU`          | Replaces the core count used as the default threshold.         |
| `PG_REPO_RUNNER_NIX_BIN`       | Replaces the nix binary (the bats suites use it to classify).  |
| `PG_REPO_RUNNER_GIT_BIN`       | Replaces the git binary (the sync suite records git calls).    |
| `PG_REPO_RUNNER_PN_BIN`        | Replaces the pn binary (default: `pn` on PATH).                |
| `PG_REPO_RUNNER_EPOCH`         | Replaces the clock, in epoch seconds (the debounce tests).     |
| `PG_REPO_RUNNER_QUIET_WINDOW`  | Replaces the quiet window, in seconds.                         |
| `PG_REPO_RUNNER_BD_BIN`        | Replaces the bd binary (default: `bd` on PATH).                |
| `PG_REPO_RUNNER_OSASCRIPT_BIN` | Replaces the macOS notifier (the surfacing suite records it).  |
| `PG_REPO_RUNNER_RETRY_WINDOW`  | Replaces the tip item's retry window, in seconds.              |
| `XDG_STATE_HOME`               | Root of the state directory.                                   |

## Load threshold: the first calibration sample

The design asks the first bead to record the sampled 1-minute load while a nix check runs, as the
calibration input for `thresholds.max_load` (default: the core count).

Measured 2026-10-28 on the development Mac (`sysctl -n hw.ncpu` = 11). How: while this bead's own
targeted build ran, `nix build -L --no-link .#checks.aarch64-darwin.test-pg-repo-runner
.#checks.aarch64-darwin.test-pg-repo-runner-hm-render` (through `pg-nix-log-wrapped`, 57 bats tests plus
the module render check), `uptime` was sampled every 10 seconds, 11 samples from 01:18:14Z to
01:19:54Z.

| Quantity                        | Value                                                                                                   |
| ------------------------------- | ------------------------------------------------------------------------------------------------------- |
| 1-minute load, first sample     | 11.06                                                                                                   |
| 1-minute load, last sample      | 7.65                                                                                                    |
| 1-minute load, minimum, maximum | 7.65, 11.06                                                                                             |
| 1-minute load, mean of 11       | (11.06 + 10.67 + 9.96 + 9.74 + 9.33 + 8.99 + 8.46 + 8.18 + 7.69 + 7.96 + 7.65) / 11 = 99.69 / 11 = 9.06 |
| 5-minute load, first and last   | 40.90 and 31.23                                                                                         |

Reading it: the sample is NOT attributable to the runner. It is a small check build, not a
`nix flake check`, and the host was carrying other sessions' work (the 5-minute average of 31 to 41
shows the load that preceded the sample, and the 1-minute average was falling, from 11.06 to 7.65,
while the build ran). With the default threshold of 11 (the core count), a run would have been
admitted at 10 of the 11 samples and refused only at the first (11.06 is not below 11). A
`nix flake check` run by a runner will add its own load on top of whatever the host carries, so the
default admits only when the host is otherwise quieter than this sample. Treat the number as the
first data point for the operator's calibration, not as a conclusion; the F2 bead records the
measured duration and load of the real runner checks.

## Tests

- `pg-repo-runner/tests/test-pg-repo-runner.bats`: the real script against a real temporary
  workspace (a `pn-workspace.toml` and real git clones of bare local remotes), live and dead
  processes for the lease, the real verdict store. Covers the allow-list, the path check, the kill
  switch, the load check, the single-flight lease, stale-lease reclaim, atomic verdicts and `status`.
- `pg-repo-runner/tests/test-pg-repo-runner-lib.bats`: the kernel functions one at a time.
- `pg-repo-runner/tests/test-pg-repo-runner-verify.bats`: runner B. Real git and a real workspace;
  the classification tests (green, red, superseded, signal exit, duplicate, duration, `tips`) run
  with a recorder script standing in for the nix binary, because a signal exit cannot be produced
  on demand by nix. The "real nix" tests at the end run a REAL `nix flake check` on a tiny fixture
  flake (a derivation check whose builder passes or fails) through the real snapshot URL; they skip
  with a reason where no usable nix is on PATH (inside a nix build sandbox), so they run wherever
  `nix` is on PATH (`bats` by hand, a runner that exposes nix), and are skipped under
  `nix build .#checks`.
- `pg-repo-runner/tests/test-pg-repo-runner-surface.bats`: failure surfacing (F3). Real git, a real
  workspace and the REAL bd over an embedded throwaway database (`test-support/bd_fixture.bash`: no
  dolt server, never the shared tracker); the only stand-ins are nix (as above) and a recording script
  for the macOS notifier. Proves one bead and one notification per transition, no duplicates, the
  auto-close of only the runner's own bead on a green descendant, and that every failure stays owed.
- `pg-repo-runner/tests/test-pg-repo-runner-sync.bats`: runner A, report-only (F5). Real git against bare
  local remotes, a recording wrapper around every git call, a `pre-push` hook as a tripwire, and the REAL bd
  over a throwaway database for the diverged bead. Covers 0,0, behind-only (with a stale remote-tracking ref),
  ahead-only (push forced to true, no green verdict), diverged (one bead, no ref change, closes on resolution), a dirty,
  mid-rebase, off-branch, `core.worktree` or marker-bearing canonical clone (skipped, `.git` hash unchanged), a
  refused fast-forward, a failed fetch.
- `pg-repo-runner/tests/test-pg-repo-runner-push.bats`: the push step (F6). Real git against bare local
  remotes (the fixture config sets `push = true` for one fixture repo only), a recording git wrapper, a
  one-shot mid-pass hook. Proves the exact refspec and argv when every gate holds (remote ref moved, no other
  ref changed, repo hook ran), no push with `push = false`, either kill-switch file, a kill switch appearing
  mid-pass, no/red/error/superseded/override verdict, a verdict for an older sha, a moved tip, a dirty or
  off-branch clone, a diverged remote, a remote that moves after the check (git refuses, no force), dry run
  by default and its two ways out, the audit record, needs-relock surfacing, and that no argv ever carries a
  forbidden flag.
- `pg-repo-runner/tests/test-pg-repo-runner-workspace.bats`: runner C (F8). A miniature workspace of three
  tiny repos (real git, bare local remotes, a real `pn-workspace.toml` and lock) and the REAL pn where one is on
  PATH, else a stand-in doing the same set operations with real git (`RR_TEST_PN=fake` forces it); the build
  command is a fixture script, so no nix runs. Proves: one event for a burst of tip changes, the quiet window,
  the set created, built in at exactly the tuple's tips under `nice` and removed, a crash (SIGKILL of the runner
  mid-build) leaving a leftover that the next run removes, only reserved-prefix sets older than the run going,
  the canonical clones byte-identical before and after (green, red, crash and recovery), busy and malfunction
  classification, and pn never being wrapped. Negative controls 2026-10-28: deleting the branch cleanup, and
  removing the quiet-window wait, each fail it.
- `pg-repo-runner/tests/test-pg-repo-runner-workspace-surface.bats`: a red build files one bead through F3
  (real bd over a throwaway database), deduplicated, commented on a newer red, closed by a green build.
- `checks.<system>.test-pg-repo-runner`: all eight suites, built through `mkBashScript`.
- `checks.<system>.test-pg-repo-runner-hm-render`: evaluates the home module (push, verify and sync
  default to false for every repo, no case enables push, the assertions fire), builds the rendered
  `config.toml`, and runs the real package against it.

Run one check: `nix build .#checks.<system>.test-pg-repo-runner` (in the background, through
`pg-nix-log-wrapped`).
