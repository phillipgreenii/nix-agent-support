package cli

// HelpText is what --help prints: the synopsis of section 5.1, then the
// outcomes and exit codes of section 6.1, the keep flags and placeholder of
// section 11.3, and the configuration of section 12. Usage errors print only
// the Synopsis.
const HelpText = Synopsis + `

pg-git-lander rebase BRANCH onto REF, or land BRANCH onto TARGET (default: the
primary branch), holding the repository's locks so concurrent runs queue
instead of colliding. Every subcommand and flag is described here; the help of
rebase, land, status, log and config check is this same text.

commands:
  rebase       replay BRANCH onto --onto REF; BRANCH ends at the result
  land         rebase BRANCH onto TARGET and fast-forward TARGET to it
  status       show the operations in this repository (read-only)
  log          print the log of one operation (OP_ID or BRANCH; --tail N)
  config check validate the XDG configuration, or the file given by --file

resuming a stopped operation:
  --continue, --skip, --abort and --quit resume or end a stopped operation.
  They accept no context, worktree or keep flags: a resumed operation keeps
  the ones it started with.

--no-verify (rebase, land, --continue, --skip; not --abort or --quit):
  skip pre-merge-verify hooks and git's own hooks for this run; recorded in the
  outcome (warning verify-skipped); do not use unless the operator asked.
  It skips every pre-merge-verify entry, on every race retry and every
  --continue. The tool's own commits (autofix, conflict resolution) and its
  initial git rebase skip git's hooks too, as run_git_hooks = false does.
  post-rebase-fix hooks, conflict rules and their verify, and the built-in
  preconditions still run. The flag is remembered by a stopped operation:
  pass it again on --continue only to turn it on; --abort ends it.

hooks (configuration, the same names under hooks.land and hooks.rebase):
  post-rebase-fix   runs only after a rebase; may change the tree (autofix
                    commits)
  pre-merge-verify  runs on every path before the branch moves, in a
                    fast-forward too; read-only: a change to a tracked file or
                    a moved HEAD stops the operation (hook-failed,
                    verify-modified-tree)
  Every post-rebase-fix entry runs before any pre-merge-verify entry. The
  former keys before, after_rebase, before_move and after are configuration
  errors.

keep flags (land):
  none                          BRANCH deleted, source worktree removed
  --keep-source-worktree        BRANCH deleted, source worktree parked on the
                                placeholder branch
  --keep-source-branch          BRANCH moved to the result, worktree removed
  both                          BRANCH moved to the result, worktree left on it
  rebase always moves BRANCH to the result; the keep flags are a usage error.

the placeholder:
  After --keep-source-worktree the worktree sits on pg-git-lander/idle/<name>.
  It is a parking branch based on TARGET: nobody works in it, and it MAY be
  deleted.

repository: --repo PATH, else the clone containing the worktrees named by
  --target-worktree, --source-worktree and --work-worktree (or the variables
  below), else the current directory. All named paths MUST be in ONE clone
  (same git common directory); two repositories or two clones are a usage
  error. The resume flags take no worktree flags: pass --repo or run from
  inside the repository.

environment: PGL_TARGET_WORKTREE, PGL_SOURCE_WORKTREE and PGL_WORK_WORKTREE
  supply the worktree flags when they are absent.

output: one line per outcome, or one JSON object with --json.

exit codes:
  0  done: landed, rebased, up-to-date, nothing-to-land, aborted, quit,
     status, log, config-ok
  1  internal-error, before any lock was taken
  2  usage-error: bad arguments; nothing changed
  7  config-error: a configuration file is invalid; nothing changed
     config-unrunnable: config check found a configured hook or conflict
     command that is not on this process's PATH or is a missing absolute path
  9  busy: a lock was not obtained within the wait; always retryable
  21 stopped: the operation stopped; the output names the next commands
  22 refused: a precondition failed; nothing changed (except holder-died)`
