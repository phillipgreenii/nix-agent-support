# Script-first routing of resolve-conflict and fix-ci work items in pg-router

Status: approved for decomposition (operator rulings: Phillip 2026-10-02 pg-rescue design approval,
and 2026-10-22 push ruling, session 72907203-1867-4415-af2a-bcf11aa27cac)
Date: 2026-10-22
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
`pg-rescue-bead --annotate-from-env PG_RESCUE_ITEM_ID --add-label script-gave-up` (tag `deferral`).
`pg-rescue-bead` gains `--annotate-from-env NAME`: the item id to annotate is read from the named
environment variable (mutually exclusive with `--annotate`, an error when the variable is unset or
empty, exit 1). The reason: a handler's argv in the pg-rescue config is static, while the item id is
known only at dispatch time, and the role shim exports it in the environment pg-rescue passes to
handlers. New default chains:

- `conflict` = `flake-lock-conflict`, `fix-small`, `fix-large`, `give-up`
- `fix-ci` = `give-up`

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

1. Claim the item so it leaves `bd ready` and a decider sees it as claimed; the actor is
   `pg-rescue/<run-id>`.
2. Create an isolated git worktree of the PR branch (a detached checkout of `origin/<branch>`) under a
   configured worktree root, from the repository directory the repo-to-directory map names. Fetch the
   branch and the base first.
3. Run `pg-rescue --chain conflict -C <worktree> --context <text> --verify <check> --result-file F --
git rebase origin/<base>`, with `PG_RESCUE_ITEM_ID=<item id>` exported. `-C` is mandatory because a
   command role sets no working directory. The verify command checks that no rebase is in progress and
   that `origin/<base>` is an ancestor of `HEAD` (the original argv is not idempotent, so the default
   verify is not used).
4. On pg-rescue exit 0 (rebased, by the plain rebase or by a handler): push with `pg-rescue-push-branch`
   (D5), then close the item with a reason naming the run id. Helper exit `3` follows the D5 lease rule.
5. On exit 75 (the chain gave up and the marker is set): do not push; release the claim.
6. On any other exit (the chain itself failed): release the claim and exit non-zero so the failed
   dispatch is visible.
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
marker, or the push lease was rejected; `1` any other failure; `2` a usage error. The shim exports
`PG_RESCUE_ITEM_ID=<ID>` for pg-rescue and its handlers.

Exit mapping (pg-router treats only `0` as success and `9` as busy and re-dispatches): the shim maps
`75` to `0`, and remaps a passed-through `9` to `1`. stdout and stderr are discarded by the role
executor, so the shim writes its own one-line outcome record next to pg-rescue's run directory and
passes `--result-file` so the run log is the record. There is no watchdog: the shim bounds its own
steps with timeouts.

### D5. Push policy (operator ruling, Phillip, 2026-10-22)

> The script role MAY push the rebased PR branch, with `--force-with-lease` only, only the PR branch in
> the role's own isolated worktree, never the base branch.

Binding consequences: the push is `git push --force-with-lease=<branch>:<head_sha> origin
HEAD:refs/heads/<branch>`, with the lease pinned to the item's `head_sha` (a bare `--force-with-lease`
is forbidden). It runs only from the shim's own worktree and only for the PR branch refspec; no other
refspec, no `--force`, no push of the base branch, no `--force-with-lease` to a ref the item does not
name. The push is made by one small helper, `pg-rescue-push-branch`, the only place the shims push from:

```text
pg-rescue-push-branch --worktree DIR --branch BRANCH --base BASE --expected-sha SHA [--remote NAME]
```

It exits `0` when the push succeeded, `3` when the lease was rejected, `2` on a usage error or a refusal
and `1` on any other failure. It refuses (exit `2`, nothing pushed) when `DIR` is not a linked git
worktree (its `git rev-parse --git-dir` differs from its `--git-common-dir`; the main working tree is
never pushed from), when `BRANCH` equals `BASE`, when `BRANCH` is not a plain branch name (it fails
`git check-ref-format --branch`, or starts with `-`), when `SHA` is not a full 40-character hexadecimal
object name, or on a missing, repeated or unknown option. `--remote` defaults to `origin`. The lease is
the explicit form, so git compares the remote's actual value with `SHA` at push time and no fetch or
remote-tracking ref is involved; a remote branch that does not exist, or that moved, is a rejected lease
(exit `3`). The helper tells a rejected lease from another failure by the failure text git prints for
the lease (the implementer chooses the match and tests it against a real moved remote) and prints one
diagnostic line on stderr. A rejected lease (the branch moved) is not a failure of the rebase: the shim does not retry,
sets no marker, releases the claim and exits `0`; the entity's next hydration produces a new conflict
context (S26) if the conflict persists. A push failure for any other reason releases the claim and
exits non-zero. pg-rescue itself makes no policy (U-5 is unchanged); the authority is this ruling.

### D6. The `fix-ci` shim

A new bash package `pg-rescue-fix-ci` is the argv of the `fix-ci-script` role. Like the D4 shim it
claims the item, exports `PG_RESCUE_ITEM_ID`, runs `pg-rescue --chain fix-ci -C <state directory> --
pg-rescue-fix-ci step <the shim's own options>`, maps exit `75` to `0` and a passed-through `9` to `1`, and releases the
claim on every path. Its command line is:

```text
pg-rescue-fix-ci --item-id ID --repo REPO --pr-number N --tracker-dir DIR [--ci-backend INSTANCE]
    [--state-dir DIR]
```

with the same meanings, exit codes (D9) and `PG_RESCUE_ITEM_ID` export as the D4 shim; `step` takes the same options as the shim (`--ci-backend` is
the pg-connector ci backend instance passed to `ci rerun-failed` as `--backend`; the deployment omits it while exactly one ci backend instance is registered;
`--state-dir` is the `-C` directory, default the shim's own state directory under `XDG_STATE_HOME`). The `step`
subcommand succeeds when it issued the one rerun or is waiting for it,
and fails when the rerun is already spent and CI is still failing, so the chain's `give-up` annotates
the item for an agent. The step decides by the item's own data
(read with `bd show --json` against `--tracker-dir`; the item carries `failing_builds`, 9.8, each build id = run id +
attempt, S26):

- No rerun recorded on the item: call `pg-connector ci rerun-failed <REPO>#<N>` (`REPO` and `N` are the `--repo` and `--pr-number`
  options, which are authoritative; the item's title is not read; a failed call is a failed step and
  records nothing), record the item's `failing_builds`
  value, unchanged, in the item metadata `ci_rerun_of` (written with `bd update ID --set-metadata
ci_rerun_of=<JSON array>` and `bd comment`) and add a comment, exit 0 (the shim, which owns the claim, releases it) (a rerun is asynchronous: the item stays open, the decider closes it when CI turns green).
- A rerun recorded and every element of `failing_builds` is also in `ci_rerun_of` (CI has not
  reported again): do nothing, exit 0. A build id is opaque to the shim: elements
  are compared as JSON values and as sets (order does not matter), so a subset or an empty or absent
  `failing_builds` counts as "nothing new". An absent `failing_builds` on the first run is read as an
  empty list.
- A rerun recorded and `failing_builds` holds an element not in `ci_rerun_of` (the rerun failed too, or
  a different job failed): exit 1 so `give-up` hands the item to the `worker`. A failed
  `ci rerun-failed` call is also a failed step, and so is handed to the `worker` the same way.

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

### D8. Verification

Everything an agent can prove is proven by tests: `bats` tests for each shim against a real git
fixture (a bare remote, a diverging base, a conflicting rebase) and the real `pg-rescue` binary with
fixture handlers; Go tests for the two flag additions; nix evaluation assertions in the deployment
repo. Claim, release, close and metadata behaviour is tested against the real `bd` in an embedded
throwaway database (the `hand-to-human` `bd_fixture.bash` precedent, never a dolt server). The one
external tool the shims only call with fixed argv, `pg-connector ci rerun-failed`, is a test double
that records its argv. A final post-apply verification bead exercises the applied configuration with
`pg-router run-query` and `run-role` against a scratch item. No step requires mutation testing.

### D9. Shared mechanics of the two shims

- **Inputs.** A shim reads no item metadata: the role argv renders every value from the item and
  passes it as an option (the deployment writes `{{index .Item.Metadata "branch"}}`, the form the
  existing `desk-pr` role uses, and `{{.BeadID}}` for the id).
- **Outcome record.** The shim writes one line `<run-id> <outcome> <item id>` to
  `<state-dir>/<run-id>.outcome`, `<outcome>` being: `resolved` (the `resolve-conflict` shim pushed and
  closed the item), `ok` (the `fix-ci` shim's chain returned `0`), `gave-up` (pg-rescue exited `75`),
  `lease-rejected` (the push helper exited `3`), `peer-held` or `failed` (every other exit). It is
  derived from exit codes only; the shim never reads pg-rescue's `--result-file` JSON, which is for
  operators and the run log.
- **Handler environment.** pg-rescue hands handlers the wrapper's environment (pg-rescue design 2.4,
  lowest precedence), so the shim exports `PG_RESCUE_ITEM_ID`, `BEADS_DIR=<tracker-dir>/.beads` and
  `BEADS_ACTOR=pg-rescue/<run-id>` before it runs pg-rescue.
- **Run id.** The shim generates it: the UTC time as `YYYYMMDDTHHMMSSZ`, a hyphen, the shim's process
  id. The claim actor is `pg-rescue/<run-id>`.
- **Tracker.** `--tracker-dir` is the directory that contains the tracker's `.beads` directory (the
  value the zr beads backend instance already receives as `--beads-dir`). Every `bd` call of a shim runs
  with `BEADS_DIR=<tracker-dir>/.beads`, and passes `--actor pg-rescue/<run-id>`.
- **Claim, release, close.** Claim: `bd update ID --claim`. Release (one call, so no window leaves a
  stranded claim): `bd update ID --status open --assignee ""`. Close: `bd close ID --reason TEXT`, the
  reason naming the run id and the outcome. A claim that fails because a peer holds the item is not an
  error: the shim changes nothing and exits `0`.
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
- **Peer-held claim.** When `bd update ID --claim` fails, the shim re-reads the item with
  `bd show ID --json`: an assignee (the `assignee` field of the first element, after unwrapping a `data` envelope) that is not `pg-rescue/<run-id>` means a peer holds it (exit `0`, no
  change); anything else is a failure (exit `1`).
- **Tests.** pg-rescue is configured in a shim's tests through `PG_RESCUE_CONFIG`; the config uses the
  keys of the pg-rescue design 2.4 (`[handler.NAME] command = [...]`, `[chain.NAME] handlers = [...]`)
  and fixture handler scripts follow its handler contract 3.2 (exit `0` resolved, `2` declined, `3`
  deferred, anything else failed). One test per shim puts a stub `pg-rescue` that exits `9` and then
  `75` first on `PATH` to prove the exit mapping; every other test runs the real binary.

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

Deployment repo (`phillipg-nix-ziprecruiter`):

8. `give-up` handler override with `--backend` and `--tracker-dir` (D3, D7).
9. `resolve-conflict` path: queries, producer, role, `worker-source` exclusion, `worker-fallback-source` (D1, D7).
10. `fix-ci` path: query, producer, role, extended exclusion (D1, D6, D7).
11. Post-apply verification (D8).

Ordering: 3 after 1; 5 after 3 (they share `flake.nix`); 4 after 5 (it calls the helper); 6 after 4
(`flake.nix`); 8 after 3; 9 after 2, 4, 8; 10 after 6 and 9; 11 after 10.

## Open items

None. The operator decision on pushing is ruled (D5).
