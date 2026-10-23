# Script-first routing of resolve-conflict and fix-ci work items in pg-router

Status: approved for decomposition (operator rulings: Phillip 2026-10-02 pg-rescue design approval,
2026-10-22 push ruling, session 72907203-1867-4415-af2a-bcf11aa27cac, and the 2026-10-22 push-scope
ruling recorded in D5)
Date: 2026-10-22
Revision: 2 (reconcile of docket pg2-q6a96 after an independent critique; the packets for items 1 to
11 of the work breakdown had already landed when it was amended, so the corrections ride on the
follow-up items 12 to 16 and the open item 11)
Bead: `pg2-q6a96` (docket)
Sources: `docs/superpowers/specs/2026-10-02-pg-rescue-design.md` sections 12, 13 and 15 (follow-ups 6
and 7); `docs/superpowers/specs/2026-09-29-entity-change-flow-design.md` S13, S25, S26, "Where new
logic goes" items 3 and 4, and 9.8.

## Purpose

The entity change flow (S13) makes the pg-decider create `resolve-conflict` and `fix-ci` work items
labelled `worker-ready`, and the ccpool `worker` role executes them. Most conflicts are a plain
rebase and many CI failures are flakes, so an agent session is wasted on the common case. This design
puts a `pg-rescue`-wrapped script in front of the `worker`: the script tries the deterministic path,
and only when it gives up does the item reach the `worker`.

The script role EXECUTES the item (pg-rescue design section 13: "executing it is a role"). Its
label, close and metadata writes on the item it executes therefore do not violate the entity change
flow's "Where new logic goes" items 3 and 4: a decider still decides that the work item exists, and
the role never creates an item or decides that one should.

Design patterns: **Chain of Responsibility** (script role first, then the `worker`, linked by a marker
label), **Decorator** (`pg-rescue` around the rebase), **Adapter** (the role shim between pg-router's
command-role contract and `pg-rescue`), **Strategy** (per-kind producer query and role).

## Scope

In scope: producer queries, command roles and shims for the two kinds, the marker label, the
fallback producer for the `worker`, the `conflict` and `fix-ci` pg-rescue chains, and tests.

Out of scope:

- The deciders. The decider contract is unchanged: a decider decides that the work item exists
  (entity change flow "Where new logic goes" item 3) and S13's `worker-ready` labelling stays.
- `review-pr` and `process-feedback`: agent work from the start.
- `/pn-workspace-sync` and `/pn-workspace-update` (pg-rescue follow-up 8).
- Any change to pg-router's core: it stays generic (G4).

## Existing building blocks (verified 2026-10-22 against the code)

- `pg-rescue` with `--chain`, `-C`, `--verify`, `--context`, `--result-file`; chain `sync` exists in the
  home-manager module `home/programs/pg-rescue/default.nix`; no `conflict` chain exists yet.
- Handlers `pg-rescue-flake-lock-conflict`, `pg-rescue-claude` (instances `fix-small`, `fix-large`),
  `pg-rescue-bead` with `--annotate ID --add-label L` (landed, `pg2-fjjxd`), `pg-rescue-notify`.
- pg-router `type = "command"` roles (`packages/pg-router-ccpool-handler/internal/executor/command.go`),
  configured in the deployment repo `modules/zm/default.nix`.
- `pg-connector ci rerun-failed <pr-id>`, `pg-connector issue update|close|comment|show`.
- `pg-router-source-pg-connector list <type> <query> --backend B [--title-prefix P] [--issue-type T]`:
  only INCLUDE filters exist today.
- `zm-verify-pg-router-queries` in the deployment repo, with a built-in fixture table per query.

## Decisions

### D1. Routing topology

Each kind with a script role gets its own producer query, event type and command role:

| Kind               | Producer query (emits)                               | Role                      |
| ------------------ | ---------------------------------------------------- | ------------------------- |
| `resolve-conflict` | `resolve-conflict-source` (`resolve-conflict.ready`) | `resolve-conflict-script` |
| `fix-ci`           | `fix-ci-source` (`fix-ci.ready`)                     | `fix-ci-script`           |

The script producers select `worker-ready` items whose title starts with the kind prefix and that do
NOT carry the marker label `script-gave-up` (D2). The existing `worker-source` stops offering those
kinds: it excludes the kind title prefixes. A new `worker-fallback-source` selects `worker-ready` items
carrying `script-gave-up` (any kind) and emits the same `work.ready` event the `worker` already binds.
The two `work.ready` producers select disjoint sets. A kind with no script role is untouched.

To express the exclusion, `pg-router-source-pg-connector list` gains a repeatable
`--exclude-title-prefix P` post-filter (the adapter already owns the `--title-prefix` post-filter;
bd's `ready` flags cannot exclude by title).

### D2. Marker label

The marker is the label `script-gave-up`: "the script rung is exhausted, the `worker` may take this
item". It is added by the give-up handler (D3) and by nothing else. It never replaces `worker-ready`.

### D3. Give-up handler and chains

A new default handler instance `give-up` in the pg-rescue home-manager module runs
`pg-rescue-bead --annotate-from-env PG_ROUTER_ITEM_ID --add-label script-gave-up` (tag `deferral`).
`pg-rescue-bead` gains `--annotate-from-env NAME`: the item id to annotate is read from the named
environment variable (mutually exclusive with `--annotate`, an error when the variable is unset or
empty, exit 1). The reason: a handler's argv in the pg-rescue config is static, while the item id is
known only at dispatch time. The variable is `PG_ROUTER_ITEM_ID`, which pg-router already exports to
every command role (`packages/pg-router-ccpool-handler/internal/executor/itemenv.go`) and which
pg-rescue hands to handlers with the rest of the wrapper's environment. The `PG_RESCUE_*` names are
the wrapper's own reserved namespace (`packages/pg-rescue/internal/config/env.go`), so no
`PG_RESCUE_ITEM_ID` exists; a shim re-exports `PG_ROUTER_ITEM_ID=<its --item-id>` before it runs
pg-rescue, so a manual or test run behaves like a dispatched one. New default handler instances and
chains:

- the instances `conflict-fix-small` and `conflict-fix-large` are the `fix-small` and `fix-large`
  commands plus `--disallowed-tools` entries that deny `git push` (and `git remote`/`git config`
  edits of a remote) to the agent, so an agent in the conflict chain cannot publish by itself; the
  `sync` chain keeps the unrestricted `fix-small` and `fix-large` (D5 explains why the agent
  handlers of the conflict chain are restricted);
- `conflict` = `flake-lock-conflict`, `conflict-fix-small`, `conflict-fix-large`, `give-up`
- `fix-ci` = `give-up`
- `give-up` (the chain) = `give-up`, used by a shim that must hand an item over without running any
  other handler (D9, failure cap)

`give-up` returns `deferred` (pg-rescue-bead exit 3), so pg-rescue exits 75 when the chain gave up.
The deployment overrides `give-up` with `--backend <the zr beads instance>` and `--tracker-dir <the
directory that contains the zr tracker's .beads directory>` (no `--backend-map`: the chains run in a worktree
or state directory that no map names, and without `--tracker-dir` the handler cannot find a tracker
outside a git repository that holds `.beads`), so the annotation lands in the tracker that holds the item.

### D4. The `resolve-conflict` shim

A new bash package `pg-rescue-resolve-conflict` is the argv of the `resolve-conflict-script` role. It is
generic: it names no employer and no tracker, and every machine-specific value arrives as an argument
(a repo-to-directory map, a worktree root, a tracker directory). Inputs from the role argv: the item id,
and from the item metadata (9.8) `repo`, `branch`, `base`, `head_sha`. Behaviour, in order:

1. Sweep (D9), then claim the item so it leaves `bd ready` and a decider sees it as claimed; the actor
   is `pg-rescue/<run-id>`. After the claim, apply the failure cap (D9): an item that already failed
   `SCRIPT_MAX_FAILURES` times goes straight to the `give-up` chain and the later steps do not run.
2. Fetch the branch and the base from `origin` in the repository directory the repo-to-directory map
   names. Then compare `origin/<branch>` with `--head-sha`: when they differ the item describes a head
   that no longer exists (S26: a new head is a new item), so the shim closes the item as stale
   (reason naming the run id and both shas), removes nothing else, and exits `0` (outcome `stale`);
   no agent handler ever runs for a stale item. Otherwise create an isolated git worktree of the PR
   branch (a detached checkout of `origin/<branch>`) under the configured worktree root; its name
   carries the run id, so two runs never share one, and `git worktree prune` runs in the repository
   directory first.
3. Run `pg-rescue --chain conflict -C <worktree> --context <text> --verify <check> --result-file F --
git rebase origin/<base>`, with `PG_ROUTER_ITEM_ID=<item id>` exported. `-C` is mandatory because a
   command role sets no working directory. The verify command checks that no rebase is in progress and
   that `origin/<base>` is an ancestor of `HEAD` (the original argv is not idempotent, so the default
   verify is not used). The pg-rescue process, and so every handler, also gets a git configuration
   environment (`GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_0`, `GIT_CONFIG_VALUE_0`) that sets
   `remote.origin.pushurl` to an unusable URL, so a handler that runs `git push origin` fails; the
   shim sets it for that one process only and never writes repository configuration (D5).
4. On pg-rescue exit 0 (rebased, by the plain rebase or by a handler): push with `pg-rescue-push-branch`
   (D5), run without that environment, when `PUSH_AGENT_RESOLVED` allows it (D5), then close the item
   with a reason naming the run id. Helper exit `3` follows the D5 lease rule.
5. On exit 75 (the chain gave up and the marker is set): do not push; release the claim.
6. On any other exit (the chain itself failed): count the failure (D9), release the claim and exit
   non-zero so the failed dispatch is visible.
7. On every path, remove the worktree and release or close the claim (a claim is never left held).

Command-line contract of the shim (the role argv in the deployment repo is written against it; the
metadata values render from the item through the role's argv template, 9.8):

```text
pg-rescue-resolve-conflict --item-id ID --repo REPO --branch BRANCH --base BASE --head-sha SHA
    --repo-dir-map NAME=DIR (repeatable) --worktree-root DIR --tracker-dir DIR
```

`NAME` is the last path component of `REPO`; no map entry for it is a usage error. `--tracker-dir` is
the directory that contains the tracker's `.beads` directory (D9); every `bd` call of the shim runs against it
(claim, comment, close, release). Shim exit codes: `0` the item was resolved, or the chain gave up and set the
marker, or the item was closed as stale (including a rejected push lease, D5), or the item is gone or
held by a peer (D9); `1` any other failure; `2` a usage error. The shim exports
`PG_ROUTER_ITEM_ID=<ID>` for pg-rescue and its handlers.

Exit mapping (pg-router treats only `0` as success and `9` as busy and re-dispatches): the shim maps
`75` to `0`, and remaps a passed-through `9` to `1`. stdout and stderr are discarded by the role
executor, so the shim writes its own one-line outcome record next to pg-rescue's run directory and
passes `--result-file` so the run log is the record. There is no watchdog: the shim bounds its own
steps with timeouts.

### D5. Push policy (operator ruling, Phillip, 2026-10-22)

> The script role MAY push the rebased PR branch, with `--force-with-lease` only, only the PR branch in
> the role's own isolated worktree, never the base branch.

Push-scope ruling (operator Phillip, 2026-10-22, answering the question whether the script role may
push a rebase that an AGENT handler resolved, or only a deterministic one): "Agent-resolved too". It
extends the ruling above (the script role may push the rebased PR branch, `--force-with-lease` only,
in its own worktree only). It supersedes any wording that left the scope open or limited the push to
a deterministic rebase. The scope is one named switch in the `resolve-conflict` shim,
`PUSH_AGENT_RESOLVED`, default `true`: with `true` the shim pushes whatever pg-rescue returned `0`
for (plain rebase, `flake-lock-conflict`, `conflict-fix-small`, `conflict-fix-large`); with `false` it
pushes only a rebase no `agent`-tagged handler resolved and sends an agent-resolved one to the
`give-up` chain instead (nothing pushed, claim released, outcome `gave-up`). To decide, the `false`
branch reads `<state-dir>/<run-id>.result.json` (the one named exception to "exit codes only" in D9)
and treats an unreadable or unrecognised file as agent-resolved. The switch is a single assignment
near the top of the shim, so flipping it later is a one-line change.

Binding consequences: the push is `git push --force-with-lease=refs/heads/<branch>:<head_sha> origin
HEAD:refs/heads/<branch>`, with the lease pinned to the item's `head_sha` and written as the FULL ref
name (a bare `--force-with-lease`, and the short branch name, are forbidden: a tag with the same
name must never be matched). It runs only from the shim's own worktree and only for the PR branch
refspec; no other refspec, no `--force`, no push of the base branch, no `--force-with-lease` to a ref
the item does not name. The remote is always `origin`. The push is made by one small helper,
`pg-rescue-push-branch`, the only place the shims push from:

```text
pg-rescue-push-branch --worktree DIR --worktree-root ROOT --branch BRANCH --base BASE --expected-sha SHA
```

It exits `0` when the push succeeded, `3` when the lease was rejected, `2` on a usage error or a refusal
and `1` on any other failure. It refuses (exit `2`, nothing pushed) when `DIR` is not a linked git
worktree (its `git rev-parse --git-dir` differs from its `--git-common-dir`; the main working tree is
never pushed from), when the real path of `DIR` is not strictly inside the real path of `ROOT`, when
`BRANCH` equals `BASE`, when `BRANCH` is not a plain branch name (it fails
`git check-ref-format --branch`, or starts with `-`), when `SHA` is not a full 40-character hexadecimal
object name, when a rebase is in progress in `DIR`, when `origin/<BASE>` is not an ancestor of `HEAD`,
or on a missing, repeated or unknown option (`--remote` is unknown). Every `git` call of the helper is
`git -C DIR` run with `GIT_DIR` and `GIT_WORK_TREE` (and the other repository-location variables)
unset, so an inherited environment cannot redirect it to another repository. The lease is the explicit
form, so git compares the remote's actual value with `SHA` at push time and no fetch or remote-tracking
ref is involved; a remote branch that does not exist, or that moved, is a rejected lease (exit `3`).
The helper tells a rejected lease from another failure by the failure text git prints for the lease
(the implementer chooses the match and tests it against a real moved remote) and prints one diagnostic
line on stderr. A rejected lease means the item's head is no longer the remote's: the shim does not
retry, sets no marker and closes the item as stale (reason naming the run id and the lease), exactly
as for the stale head of D4 step 2; a new head is a new item (S26), so if the conflict persists the
entity's next hydration produces a new item. Releasing the claim and exiting `0` instead would leave
the item `open` and ready, and the next tick would repeat the whole run. A push failure for any other
reason counts as a failure (D9), releases the claim and exits non-zero. pg-rescue itself makes no
policy (U-5 is unchanged); the authority is these rulings.

The agent handlers of the conflict chain MUST NOT be able to publish by themselves, because a push
made by a handler would bypass the lease, the containment and the ancestry checks above. Two controls
apply together: the `conflict-fix-small` and `conflict-fix-large` instances deny `git push` through
`--disallowed-tools` (D3), and the shim runs pg-rescue with a git configuration environment that
makes the remote's push URL unusable (D4 step 3). The second control is exercised by a test in which
a fixture handler tries `git push origin` from the worktree and the remote is unchanged afterwards.
Neither control is airtight against a handler that spells out a remote URL; the residual risk is
accepted and recorded, and the lease on the helper's push stays the authority for a push that
matters.

### D6. The `fix-ci` shim

A new bash package `pg-rescue-fix-ci` is the argv of the `fix-ci-script` role. Like the D4 shim it
sweeps, claims the item, applies the failure cap (D9), exports `PG_ROUTER_ITEM_ID`, runs `pg-rescue --chain fix-ci -C <state directory> --
pg-rescue-fix-ci step <the shim's own options>`, maps exit `75` to `0` and a passed-through `9` to `1`, and releases the
claim on every path. Its command line is:

```text
pg-rescue-fix-ci --item-id ID --repo REPO --pr-number N --tracker-dir DIR [--ci-backend INSTANCE]
    [--state-dir DIR]
```

with the same meanings, exit codes (D9) and `PG_ROUTER_ITEM_ID` export as the D4 shim; `step` takes the same options as the shim (`--ci-backend` is
the pg-connector ci backend instance passed to `ci rerun-failed` as `--backend`; the deployment omits it while exactly one ci backend instance is registered;
`--state-dir` is the `-C` directory, default the shim's own state directory under `XDG_STATE_HOME`). The `step`
subcommand succeeds when it issued the one rerun or is waiting for it,
and fails when the rerun is already spent and CI is still failing, so the chain's `give-up` annotates
the item for an agent. The step decides by the item's own data
(read with `bd show --json` against `--tracker-dir`; the item carries `failing_builds`, 9.8, each build id = run id +
attempt, S26). `failing_builds` is NOT a JSON array: pg-decider writes it as a comma-separated string
of `<run id>:<attempt>` entries, sorted (`packages/pg-decider/internal/workitem/contract.go` pins the
encoding; the decider's `rules/fixci.go` joins and splits it with `,`). The step reads it by splitting
on `,` and trimming each element, stores `ci_rerun_of` in the same comma form, and compares the two
as SETS of elements (order does not matter, an element is opaque):

- No rerun recorded on the item: FIRST record the item's `failing_builds` value, unchanged, in the
  item metadata `ci_rerun_of` (written with `bd update ID --set-metadata ci_rerun_of=<the comma
string>`; `--set-metadata` merges per key and leaves the item's other keys alone), THEN call
  `pg-connector ci rerun-failed <REPO>#<N>` (`REPO` and `N` are the `--repo` and `--pr-number`
  options, which are authoritative; the item's title is not read), then add a comment and exit 0 (the
  shim, which owns the claim, releases it) (a rerun is asynchronous: the item stays open, the
  decider closes it when CI turns green). The record comes first so that a crash between the two
  actions can only lose a rerun, never repeat one: a failed record is a failed step and the call is
  not made; a failed call leaves the record in place, is a failed step, and the chain's `give-up`
  hands the item to the `worker` (the rerun is spent either way).
- A rerun recorded and every element of `failing_builds` is also in `ci_rerun_of` (CI has not
  reported again): do nothing, exit 0. A subset, or an empty or absent `failing_builds`, counts as
  "nothing new". An absent `failing_builds` on the first run is read as an empty set and is recorded
  as the empty string.
- A rerun recorded and `failing_builds` holds an element not in `ci_rerun_of` (the rerun failed too, or
  a different job failed): exit 1 so `give-up` hands the item to the `worker`.

Rerun-once is per item, and an item is per head commit (S26), so a new commit gets a fresh rerun. The
shim reruns nothing else and never touches the branch.

### D7. Wiring in the deployment repo

The deployment repo (`nix-ziprecruiter`, `modules/zm`, `machines/`) declares the named queries, the
three producers (`resolve-conflict-source`, `fix-ci-source`, `worker-fallback-source`), the two command
roles, the `worker-source` exclusions, the `give-up` handler override, and the repo-to-directory and
tracker arguments. Every new or changed query/role pair MUST be exercised: the shim argv renders
through the real template engine, and `zm-verify-pg-router-queries` gains a fixture shape for each new
query. The `resolve-conflict` and `fix-ci` kinds are wired in separate steps; each step leaves the
config loadable (no producer without a bound role) and leaves no item unreachable (the worker
exclusion for a kind lands in the same step as that kind's producer and the fallback producer).

Both script roles set `max_dispatch_retries = 0` explicitly (pg-router's default is already `0`, and
only a transient `killed`, `deadline` or `unavailable` failure is ever re-run, at most 3 times; the
explicit value pins the intent that the router never re-runs a script role, because a re-run of a
claiming shim is the loop D9's failure cap exists to bound). pg-router re-emits a pull query's items
on every tick (60 s for the bead sources) and deduplicates a re-emit only within one dispatch cycle
(`packages/pg-router/docs/behavior/invariants.md`, `INV-EVT-3`), so an item that stays `open` and
ready is dispatched again on the next tick: every shim exit path must therefore either close the item,
mark it `script-gave-up`, or count a failure toward the cap. The one exemption is the `fix-ci` shim's waiting path (a rerun is recorded and CI has not reported again): it is an idempotent claim, no-op and release on each tick until the decider closes the item, and it changes nothing on the item.

The deployment's `flake.lock` pins `phillipgreenii-agent-support` at an older revision than the local
clone, so the deployment cannot evaluate the new packages from its locked input alone. No work item
bumps that pin: a lock can only pin a published revision, and relocking and publishing are the
operator's (workspace ruling, Phillip, 2026-10-09: nothing blocks on a push or a relock). The
deployment work is verified with the workspace's local overrides (`pn workspace build` or
`pn workspace flake-check`), never with a plain locked evaluation.

### D8. Verification

Everything an agent can prove is proven by tests: `bats` tests for each shim against a real git
fixture (a bare remote, a diverging base, a conflicting rebase) and the real `pg-rescue` binary with
fixture handlers; Go tests for the two flag additions; nix evaluation assertions in the deployment
repo. Claim, release, close and metadata behaviour is tested against the real `bd` in an embedded
throwaway database (the `hand-to-human` `bd_fixture.bash` precedent, never a dolt server). The one
external tool the shims only call with fixed argv, `pg-connector ci rerun-failed`, is a test double
that records its argv. A final post-apply verification bead exercises the applied configuration with
`pg-router run-query` and `run-role` (D8 below lists what it must cover). No step requires mutation
testing, and no step proves an assertion by hand-editing a value and rebuilding: a nix evaluation
assertion is proven by evaluating the same assertion expression against a deliberately bad value
with `nix eval`.

The post-apply verification MUST: (1) find the new queries and roles in the installed router
configuration; (2) check the rendered pg-rescue configuration, with `pg-rescue check` (exit `0`,
every chain of D3 listed) and a read of the `give-up` handler's rendered command, which must carry
`--backend`, `--tracker-dir` and `PG_ROUTER_ITEM_ID` (the deployment override of D3); and (3) drive
`pg-router run-role resolve-conflict-script` with a MAPPED repository (the repository the role's
repo-to-directory map names; an unmapped repository such as `acme/widget` exits `2` before `bd` is
touched and proves nothing about the daemon environment) and a scratch item in the zr tracker that
carries no `worker-ready` label and names a branch that does not exist on the remote. The expected
result is the shim's own, read from `<state-dir>/<run-id>.outcome` and `<run-id>.log` (D9): the claim
and the release succeeded (tracker access under the daemon environment) and the fetch reached the
remote and reported the missing branch (remote credentials under the daemon environment). The write
permission of the push credential is not exercised without a real push; it is the same credential as
the fetch on this remote, and its first live use is outside this design.

### D9. Shared mechanics of the two shims

- **Inputs.** A shim reads no item metadata for its inputs: the role argv renders every value from the
  item and passes it as an option (the deployment writes `{{index .Item.Metadata "branch"}}`, the form
  the existing `desk-pr` role uses, and `{{.BeadID}}` for the id). The only item data a shim reads
  itself is the failure counter `script_failures` (below) and, for the `fix-ci` step, the keys of D6.
- **Outcome record.** The shim writes one line `<run-id> <outcome> <item id>` to
  `<state-dir>/<run-id>.outcome`, `<outcome>` being: `resolved` (the `resolve-conflict` shim pushed and
  closed the item), `ok` (the `fix-ci` shim's chain returned `0`), `gave-up` (pg-rescue exited `75`, or the failure cap sent
  the item to the `give-up` chain), `stale` (the item's head is no longer the remote's: D4 step 2 or a
  rejected push lease, D5; the item was closed), `gone` (the item was already closed when the claim
  failed), `peer-held` (a peer holds the item) or `failed` (every other exit). It is derived from exit
  codes only; the shim never reads pg-rescue's `--result-file` JSON (which is for operators and the run
  log), with the single exception of the `PUSH_AGENT_RESOLVED=false` branch of D5. The shim also
  appends its own stderr to `<state-dir>/<run-id>.log`, because the role executor discards stdout and
  stderr and the outcome line alone does not say why a run failed.
- **Handler environment.** pg-rescue hands handlers the wrapper's environment (pg-rescue design 2.4,
  lowest precedence), so the shim exports `PG_ROUTER_ITEM_ID`, `BEADS_DIR=<tracker-dir>/.beads` and
  `BEADS_ACTOR=pg-rescue/<run-id>` before it runs pg-rescue.
- **Run id.** The shim generates it: the UTC time as `YYYYMMDDTHHMMSSZ`, a hyphen, the shim's process
  id. The claim actor is `pg-rescue/<run-id>`.
- **Tracker.** `--tracker-dir` is the directory that contains the tracker's `.beads` directory (the
  value the zr beads backend instance already receives as `--beads-dir`). Every `bd` call of a shim runs
  with `BEADS_DIR=<tracker-dir>/.beads`, and passes `--actor pg-rescue/<run-id>`.
- **Claim, release, close.** Claim: `bd update ID --claim`. Release (one call, so no window leaves a
  stranded claim): `bd update ID --status open --assignee ""`, which also carries the failure counter
  when a failure is counted (`--set-metadata script_failures=N`). Close: `bd close ID --reason TEXT`,
  the reason naming the run id and the outcome. A claim that fails because a peer holds the item, or
  because the item is already closed, is not an error: the shim changes nothing and exits `0`.
- **Traps and sweep.** A shim installs `EXIT`, `INT` and `TERM` traps before its first `bd` call; the
  `EXIT` trap removes the worktree, releases or leaves closed the claim, and writes the outcome
  record. A `SIGKILL` runs no trap and a stranded claim has no other sweeper, so at the start of every
  run (after option validation, before the claim) a shim sweeps: it records each claim it takes in
  `<state-dir>/<run-id>.claim` (the item id and the actor), removes the file when the claim is
  released or closed, and at the start of a run releases every claim whose file is older than
  `SWEEP_AGE` (default 2 hours, longer than the shim's own step timeouts) and whose item still names
  that actor as assignee, removes the worktree directory of that run id under the worktree root
  (`resolve-conflict` only), runs `git worktree prune` in the mapped repository directory, and deletes
  the file. A sweep failure is reported on stderr and never fails the run.
- **Failure cap.** After the claim, a shim reads the item's `script_failures` metadata (absent or
  unparsable is `0`). A run that ends in outcome `failed` after the claim adds `1` to it, written in
  the same call that releases the claim. When the value read is at least `SCRIPT_MAX_FAILURES`
  (default `3`) the shim runs no agent handler and no rebase: it runs `pg-rescue --chain give-up -C
<state-dir> --verify true -- false`, which labels the item `script-gave-up` through the `give-up`
  handler and hands it to the `worker`, and exits `0` with outcome `gave-up`. This bounds the rebase and
  agent-handler cost of an item whose failure is persistent (a broken fetch, a failing agent) to a fixed
  number of runs instead of one run per tick; a `give-up` handler that itself keeps failing leaves one
  cheap, agent-free run per tick, which is accepted.
- **pg-rescue invocation.** The shim passes `--result-file <state-dir>/<run-id>.result.json`, where
  `<state-dir>` is `${XDG_STATE_HOME:-$HOME/.local/state}/<shim name>`, creating the directory if needed.
  pg-rescue reads its own config from its usual lookup (`PG_RESCUE_CONFIG`, then
  `${XDG_CONFIG_HOME:-~/.config}/pg-rescue/config.toml`); the shim never writes it. pg-rescue's exit
  codes are `0` (resolved, or the command succeeded), `75` (a handler deferred: the chain gave up) and
  anything else (every handler declined or failed, or a wrapper error `70`).
- **Exit mapping.** `75` maps to `0`; a passed-through `9` maps to `1`; every other code is unchanged.
- **Marker.** The label is `script-gave-up` (D2). A shim never adds it itself: the `give-up` handler does.
- **Order and exit codes.** A shim validates every option (for the `resolve-conflict` shim,
  including the repo-to-directory lookup) before it touches `bd`, and exits `2` on a usage error; any other failure exits `1`. The git rules
  apply to the `resolve-conflict` shim only (the `fix-ci` shim does no git work and takes no repository
  directory): every `git` call that needs a remote uses `origin`; the shim fetches the PR branch and
  the base branch from it and creates `--worktree-root` if it does not exist.
- **Peer-held or closed claim.** When `bd update ID --claim` fails, the shim re-reads the item with
  `bd show ID --json` (first element, after unwrapping a `data` envelope): a `status` of `closed`
  means the item is gone (a decider or a peer closed it; exit `0`, no change, outcome `gone`); an
  assignee that is not `pg-rescue/<run-id>` means a peer holds it (exit `0`, no change, outcome
  `peer-held`); anything else, including an item `bd` cannot find, is a failure (exit `1`).
- **Tests.** pg-rescue is configured in a shim's tests through `PG_RESCUE_CONFIG`; the config uses the
  keys of the pg-rescue design 2.4 (`[handler.NAME] command = [...]`, `[chain.NAME] handlers = [...]`)
  and fixture handler scripts follow its handler contract 3.2 (exit `0` resolved, `2` declined, `3`
  deferred, anything else failed). One test per shim puts a stub `pg-rescue` that exits `9` and then
  `75` first on `PATH` to prove the exit mapping; every other test runs the real binary. Each shim's
  tests also cover the traps (`INT` and `TERM` leave no claim and no worktree), the sweep (a stale
  `<run-id>.claim` file is released and its worktree removed), the failure cap, and a closed item at
  claim time.

## Work breakdown

Agent-support repo (`phillipgreenii-nix-agent-support`):

1. `pg-rescue-bead --annotate-from-env` (D3).
2. `pg-router-source-pg-connector list --exclude-title-prefix` (D1).
3. pg-rescue module: `give-up` handler, `conflict` and `fix-ci` chains, label vocabulary entry (D2, D3).
4. `pg-rescue-resolve-conflict` shim: claim, worktree, pg-rescue run, push through the helper, exit
   mapping, close and release (D4).
5. The `pg-rescue-push-branch` helper: the force-with-lease push (D5).
6. `pg-rescue-fix-ci` shim (D6).
7. Commit this design to `docs/superpowers/specs/`.
8. Revision 2 corrections to the pg-rescue module and to the committed design copy: the `give-up`
   default reads `PG_ROUTER_ITEM_ID`, the `conflict-fix-small` and `conflict-fix-large` instances,
   the `give-up` chain, and the committed spec brought to revision 2 (D3, D5).
9. Revision 2 corrections to `pg-rescue-push-branch`: full-ref lease, no `--remote`, `--worktree-root`
   containment, ancestry and rebase checks, environment hygiene (D5).
10. Revision 2 corrections to `pg-rescue-resolve-conflict`: stale-head check, run-id worktree name,
    sweep, traps, closed-item claim, failure cap, `PUSH_AGENT_RESOLVED`, push-disabled agent
    environment, helper call, `PG_ROUTER_ITEM_ID`, stale close on a rejected lease, run log (D4, D5, D9).
11. Revision 2 corrections to `pg-rescue-fix-ci`: comma-set encoding, record before rerun, sweep,
    traps, closed-item claim, failure cap, `PG_ROUTER_ITEM_ID`, run log (D6, D9).

Deployment repo (`phillipg-nix-ziprecruiter`):

8. `give-up` handler override with `--backend` and `--tracker-dir` (D3, D7).
9. `resolve-conflict` path: queries, producer, role, `worker-source` exclusion, `worker-fallback-source` (D1, D7).
10. `fix-ci` path: query, producer, role, extended exclusion (D1, D6, D7).
11. Post-apply verification (D8). It stays gated on the apply of the deployment correction (16), not
    only of item 10, and is picked up only after item 16 has attached its `pn:applied` gate.
12. Revision 2 corrections to the deployment: the `give-up` override reads `PG_ROUTER_ITEM_ID`,
    explicit `max_dispatch_retries = 0` on both script roles, assertions for both; after its commit it
    gates item 11 on that commit with the `pb:pb-gate-lifecycle` skill (D3, D7, D8).

Ordering: 3 after 1; 5 after 3 (they share `flake.nix`); 4 after 5 (it calls the helper); 6 after 4
(`flake.nix`); 8 after 3; 9 after 2, 4, 8; 10 after 6 and 9; 11 after 10; 12 after 3; 13 after 5;
14 after 12, 13 and 6; 15 after 12 and 7; 16 after 12, 14, 15 and 10; 11 after 16.

## Open items

None. The operator decisions on pushing are ruled (D5): the 2026-10-22 push ruling and the 2026-10-22
push-scope ruling ("Agent-resolved too", switch `PUSH_AGENT_RESOLVED`, default `true`).
