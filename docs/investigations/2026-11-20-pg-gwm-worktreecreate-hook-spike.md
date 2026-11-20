# Investigation: can a WorktreeCreate hook redirect worktree creation to pg-gwm? (spike, 2026-11-20)

Measured 2026-11-20 on **Claude Code 2.1.328** (`claude --version`), using `claude -p` in a throwaway git
repository with a throwaway `--settings` file holding a `WorktreeCreate` and a `WorktreeRemove` command
hook. Three runs. No production settings, plugin, hook, or real pg-gwm lease was touched, and pg-gwm
itself was not run: the throwaway hook stood in for it by running `git worktree add` then
`git worktree lock` into a lease directory and printing the path. The throwaway directory has been
deleted.

Documentation claims (from code.claude.com hooks and worktrees pages, read through a summarising fetch,
so exact wording is unconfirmed) are marked **documented**. Everything else is **observed**.

## Finding

A `WorktreeCreate` command hook can fully replace Claude Code's own worktree creation, for both
`EnterWorktree` and `Agent` with `isolation: "worktree"`. A hook that prints a path to a directory it
created makes the session work in that directory, and the agent gets a normal-looking success result.
What the hook cannot do is choose the branch or base: it receives only a slug.

### Hook input (observed, exact fields)

`WorktreeCreate` receives this JSON on stdin (values shortened):

```json
{
  "session_id": "...",
  "transcript_path": "...",
  "cwd": "<session cwd, the main checkout>",
  "prompt_id": "...",
  "hook_event_name": "WorktreeCreate",
  "name": "<slug>"
}
```

- `name` is the only worktree-specific field. For `EnterWorktree` it is the `name` the model passed
  (`spikeone`). For `Agent` isolation it is generated: `agent-<agentId>`
  (`agent-a2640c81a291b1ea4`).
- There is **no branch, no base ref, and no repository field**. `cwd` is the session cwd, so the
  hook must derive the repository from it (a session started in a subdirectory or another worktree
  would pass that, not the main checkout).

### Hook output (observed, and documented)

- A command hook prints the absolute worktree path as the last non-empty stdout line. Everything else
  must go to stderr. Observed: stdout `…/lease/spikeone` was accepted and became the session cwd.
- Documented: command hooks cannot return JSON (HTTP hooks use
  `hookSpecificOutput.worktreePath`); a non-zero exit or no path fails creation; absolute paths with
  `.`/`..` or passing through a symlink below the repo root are refused; `.worktreeinclude` is not
  processed.
- Observed failure mode: a hook script that is not executable fails creation, and `EnterWorktree`
  returns `WorktreeCreate hook failed: <script>: /bin/sh: <script>: Permission denied`. The session
  stays in the original checkout. This also proves `EnterWorktree` invokes the hook (the pages do not
  say so).

### Replacement, per entry point (observed)

| Entry point                          | Hook fired | Result                                                                                                                                         |
| ------------------------------------ | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `EnterWorktree`                      | yes        | No Claude-made worktree. Tool result: `Created worktree at "<hook path>". The session is now working in the worktree.` `pwd` is the hook path. |
| `Agent` with `isolation: "worktree"` | yes        | Subagent ran in the hook path on the hook's branch. The Agent tool result carried the worktree path; no error.                                 |

The branch is whatever the hook created (`gwm-<name>` in the spike). Claude Code does not name or
create one when a hook is present. Agent worktrees are keyed by a generated slug, so a lease would be
named `agent-<id>` unless the hook maps it.

### WorktreeRemove (observed, and documented)

`WorktreeRemove` receives `session_id`, `transcript_path`, `cwd` (the worktree), `prompt_id`,
`hook_event_name`, and **`worktree_path`**. No `name`. Output is ignored; the exit code is the signal.
Documented: exit 0 counts as removed and Claude reads nothing else, so the hook must delete the
directory; Claude never deletes a branch of a hook-created worktree.

Observed with a hook-created worktree that the hook had `git worktree lock`ed (the stand-in for a
pg-gwm lease):

- `ExitWorktree action=remove` first refused with `Could not verify worktree state at "<path>".
Refusing to remove without explicit confirmation` (the locked, hook-created tree defeats its
  state check), and only succeeded with `discard_changes: true`. It then reported
  `Exited and removed worktree`, the hook ran with `worktree_path`, and the hook's exit 0 was
  accepted as success **even though the directory, the branch, and the git lock were all still
  there** (`git worktree list` still showed it `locked`). So a lock never blocks removal; the hook
  alone decides, and a hook that does nothing leaks the worktree while telling the agent it was
  removed.
- For the unchanged `Agent` worktree, no `WorktreeRemove` call was seen by the end of the run, and the
  locked worktree remained. Whether it is swept later was not observed.

Consequence for pg-gwm: a redirect would need its `WorktreeRemove` to release the lease (not remove a
git lock by hand), and must treat `discard_changes` and "directory still present" itself, because
Claude Code will not check.

## What an agent sees

Observed: a usable result in both entry points (path in the tool result, `pwd` and the branch match the
hook's worktree). Nothing tells the agent that pg-gwm, not Claude Code, made it, and the lease's
renewal heartbeat is not wired to anything: the hook returns once, so a lease that expires on a
timer would need a separate renewer.

## Cannot do / not verified

- Cannot receive a branch or base from the caller (only `name`), so a feature branch name or base
  ref the agent wanted is lost.
- Not verified: scope beyond `--settings`; `claude --worktree`; background sessions; plugin-delivered
  hooks (documented: a plugin hook does not run with `--worktree`/`--tmux`, define it in a settings
  file); the stale-worktree sweep on a hook-created tree; behaviour when two hooks are configured.
- Not verified against real pg-gwm (lease acquisition latency, config lookup, failure modes). The
  hook is global to the session, so it also fires in repositories pg-gwm does not manage and must
  pass those through (there is no "defer to default" output: a hook either returns a path or fails).

## Recommendation

**Keep deny-only for now; do not build the redirect yet.**

1. The hook has no "decline" output. Because it replaces creation entirely, a redirect would have to
   re-implement Claude Code's default creation for every repository pg-gwm does not manage, or make
   those fail. The existing PreToolUse deny rule only acts where pg-gwm is configured and steps aside
   elsewhere.
2. Removal is entirely the hook's job and Claude Code treats exit 0 as success, so a bug leaks leases
   and locked worktrees silently while the agent is told removal worked.
3. Branch and base are lost, and the lease heartbeat has no home.
4. The deny rule already works for `EnterWorktree` and `Agent` isolation, with an explicit
   exception marker, which is the policy the parent design wants (explicit exceptions).

Revisit if the redirect gains a decline path, or if the exception volume shows the deny-and-lease
detour is a real cost. If built, the minimum design is: scope to configured repositories by deriving
the repository from `cwd`; fall back by shelling out to plain `git worktree add` for unmanaged ones;
lease and print the path; and implement `WorktreeRemove` to release the lease and delete the branch.
