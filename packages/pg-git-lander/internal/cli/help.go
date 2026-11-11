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

environment: PGL_TARGET_WORKTREE, PGL_SOURCE_WORKTREE and PGL_WORK_WORKTREE
  supply the worktree flags when they are absent.

output: one line per outcome, or one JSON object with --json.

exit codes:
  0  done: landed, rebased, up-to-date, nothing-to-land, aborted, quit,
     status, log, config-ok
  1  internal-error, before any lock was taken
  2  usage-error or config-error; nothing changed
  3  busy: a lock was not obtained within the wait; always retryable
  4  stopped: the operation stopped; the output names the next commands
  5  refused: a precondition failed; nothing changed (except holder-died)`
