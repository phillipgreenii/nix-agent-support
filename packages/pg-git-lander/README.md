# pg-git-lander

A lock-safe rebase and land tool. `pg-git-lander rebase` replays a branch onto a commit; `pg-git-lander land` rebases a
branch onto its target (by default the primary branch) and fast-forwards the target to the result. Both hold the
repository's locks for the whole operation, so concurrent runs queue (and report `busy`) instead of colliding, and both
leave every worktree the tool switched in its starting state.

## Synopsis

```text
pg-git-lander rebase BRANCH --onto REF [--source-worktree PATH] [--work-worktree PATH]
                     [--wait DURATION] [--context KEY=VALUE]... [--json]
pg-git-lander land   BRANCH [--into TARGET] [--target-worktree PATH] [--source-worktree PATH] [--work-worktree PATH]
                     [--keep-source-worktree] [--keep-source-branch]
                     [--wait DURATION] [--context KEY=VALUE]... [--json]
pg-git-lander rebase [BRANCH] --continue | --skip | --abort | --quit [--wait DURATION] [--json]
pg-git-lander land   [BRANCH] --continue | --skip | --abort | --quit [--wait DURATION] [--json]
pg-git-lander status [BRANCH] [--json]
pg-git-lander log    OP_ID | BRANCH [--tail N]
pg-git-lander config check [--file PATH] [--json]
```

- Every form accepts `--repo PATH`: any path inside the clone or one of its worktrees. Without it, land and rebase use the clone that contains the worktrees named by `--target-worktree`, `--source-worktree` and `--work-worktree` (or `PGL_TARGET_WORKTREE`, `PGL_SOURCE_WORKTREE`, `PGL_WORK_WORKTREE`), so they work from a directory outside any repository; with no worktree flag the default is the current directory. Every named path (and `--repo`, and a current directory inside a repository) MUST be in ONE git clone, compared by git common directory: two repositories, or two clones of one repository, are a usage error naming each path and its clone, before any lock is taken. A nonexistent path, or one outside a repository, is a usage error naming it. The resume flags take no worktree flags, so they need `--repo` or a current directory inside the repository.
- BRANCH and TARGET are local branch names. BRANCH equal to TARGET is a usage error.
- `--onto REF` names the commit to replay BRANCH onto, like the argument of `git rebase REF` (git's `<upstream>`), not
  like git's own `--onto` option. It accepts any commit-ish and is resolved once.
- `--wait` overrides `lock.wait` for this invocation.
- With `--continue`, `--skip`, `--abort` or `--quit`, BRANCH MAY be omitted when the current directory is the stopped
  operation's work worktree. The operation word MUST match the stopped operation (refusal `operation-mismatch`). The
  context, worktree and keep flags are not accepted, and the `PGL_*` input variables are not read: a resumed operation
  keeps its original context, roles and flags.
- Every next-step command the tool prints repeats `--repo` when the invocation used it.
- The worktree flags fall back to the variables `PGL_TARGET_WORKTREE`, `PGL_SOURCE_WORKTREE` and `PGL_WORK_WORKTREE`.

## The end state and the keep flags

Every worktree the tool switched is restored to its starting state. The exception is the source worktree (SW), which is
where BRANCH lived: a typical `land` deletes BRANCH and removes the SW, but only a worktree that was on BRANCH when the
tool started is ever removed. A spare worktree named as the SW is restored and kept.

| Flags                                         | BRANCH              | Source worktree (if it was on BRANCH) |
| --------------------------------------------- | ------------------- | ------------------------------------- |
| none                                          | deleted             | removed                               |
| `--keep-source-worktree`                      | deleted             | parked on the placeholder branch      |
| `--keep-source-branch`                        | moved to the result | removed                               |
| `--keep-source-worktree --keep-source-branch` | moved to the result | left on BRANCH, clean, at the result  |
| `rebase` (the keep flags are a usage error)   | moved to the result | left on BRANCH, clean, at the result  |

The tool refuses up front (`cwd-would-be-removed`) when the current directory is the SW and it would be removed: run
from another path, pass `--source-worktree <path>` to confirm the removal, or pass `--keep-source-worktree`. The main
worktree is never removed (`main-worktree-removal`). The SW is removed with plain `git worktree remove`, never forced;
a failure is the warning `source-worktree-not-removed` and the outcome stays `landed`.

### The placeholder

After `--keep-source-worktree` the worktree sits on `pg-git-lander/idle/<name>`. It is a parking branch based on
TARGET: nobody works in it, and it MAY be deleted.

## Outcomes and exit codes

Every invocation ends with exactly one outcome. The codes 0, 1, 2, 7 and 9 are the shared values of `x/exitcode`; 21 and 22 are app-specific. With `--json` the tool prints one JSON object on stdout; otherwise one
line, plus the exact next commands after a stop or refusal. Progress goes to stderr.

| Exit | Outcomes                                                                                              | Meaning                                                                                       |
| ---- | ----------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| 0    | `landed`, `rebased`, `up-to-date`, `nothing-to-land`, `aborted`, `quit`, `status`, `log`, `config-ok` | Done. No record of this operation remains (after `quit` the worktree and work branch remain). |
| 1    | `internal-error`                                                                                      | Unexpected failure before any lock was taken. Exit 1 never carries a branchable meaning.      |
| 2    | `usage-error`                                                                                         | Bad arguments. No change.                                                                     |
| 7    | `config-error`                                                                                        | The XDG configuration (or `config check`'s file) is invalid. No change.                       |
| 9    | `busy`                                                                                                | A lock was not obtained within the wait. Nothing changed. Always retryable.                   |
| 21   | `stopped`                                                                                             | The operation stopped and its record is `stopped`; resume with the printed next commands.     |
| 22   | `refused`                                                                                             | A precondition failed. No ref, worktree or record changed, except for `holder-died`.          |

`up-to-date` is a `rebase` whose BRANCH already contains ONTO. `nothing-to-land` is a `land` where TARGET already holds
every change of BRANCH, by ancestry or because the rebase dropped every commit as already upstream.

## Configuration

The XDG file is `$XDG_CONFIG_HOME/pg-git-lander/config.toml` (default `~/.config/pg-git-lander/config.toml`); a repo
file `.pg-git-lander.toml` is read from the tip of the relevant ref. Validate either with `pg-git-lander config check`.

| Key                         | Where     | Default                             | Meaning                                                                       |
| --------------------------- | --------- | ----------------------------------- | ----------------------------------------------------------------------------- |
| `root`                      | XDG entry | required                            | Any path inside the clone.                                                    |
| `trust_repo_config`         | XDG entry | `false`                             | Run commands named in the repo file.                                          |
| `ignore_repo_config`        | XDG entry | `false`                             | Skip the repo file entirely.                                                  |
| `primary`                   | XDG       | `origin/HEAD`'s branch, else `main` | The default TARGET.                                                           |
| `lock.lease_ttl`            | XDG       | `30s`                               | Lease length; the holder renews every third of it.                            |
| `lock.wait`                 | XDG       | `60s`                               | How long a waiter waits for a live holder before `busy`.                      |
| `run_git_hooks`             | all       | `true`                              | The tool's own commits run git's commit hooks; `false` passes `--no-verify`.  |
| `max_race_retries`          | all       | `2`                                 | Returns to the rebase after TARGET moved.                                     |
| `max_conflict_rounds`       | all       | `10`                                | Conflict stops allowed in one operation.                                      |
| `autofix.attempts`          | all       | `2`                                 | Runs per hook before a stop; 2 means one fix commit and one more passing run. |
| `hooks.<operation>.<point>` | all       | empty                               | `<operation>` is `rebase` or `land`; `<point>` is listed below.               |
| `[[conflict]]`              | all       | none                                | Conflict rules: `glob`, `run_per`, `resolve`, `verify`, `timeout`.            |
| `conflict_default`          | all       | none                                | A conflict rule without `glob`, for any path no rule matches.                 |

A hook entry is `{ run = [argv], timeout = duration, skip_exit_codes = [int], cwd = "worktree" | "repo", env = { } }`.
`cwd` defaults to `worktree`, except at `before`, which defaults to `repo`. A missing `timeout` is 10 minutes.

### Hook points

| Point          | Runs                                                                                  | A non-zero exit               |
| -------------- | ------------------------------------------------------------------------------------- | ----------------------------- |
| `before`       | At the start, after the preconditions, before any worktree is switched; once only     | refusal `before-hook-refused` |
| `after_rebase` | After a rebase that replayed commits onto a moved base; never on the fast-forward way | autofix, else stop            |
| `before_move`  | Immediately before the move, on every path that moves a ref                           | autofix, else stop            |
| `after`        | After the move                                                                        | warning `after-hook-failed`   |

Hooks run in list order and hold every lock of the operation while they run, so a `land`'s `before_move` serialises
landings onto the same TARGET for the hooks' duration.

## The outcome object

With `--json` every outcome has every field, `null` or empty when it does not apply, so a caller parses one shape. The
object carries `schema` (1), `outcome` and `exit`, the operation (`op_id`, `operation`, `branch`, `target`, `onto`),
for a stop or refusal `reason`, `detail`, `retryable`, `stage`, `next` and `hint`, for `busy` the `holder`, the worktree
records, `branch_fate`, `worktree_fate`, `tips`, `landed_commit`, `path_taken`, `stats`, `warnings` and `log_tail`.
`pg-git-lander log OP_ID` prints the full log.

## Installed extras

The package installs bash and zsh completions and the tldr page `pg-git-lander` (under `share/tldr/pages.common`).
