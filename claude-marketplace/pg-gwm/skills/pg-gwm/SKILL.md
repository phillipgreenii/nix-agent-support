---
name: pg-gwm
description: Use when you need an isolated worktree for a change in ONE repository (not a multi-repo workforest set) and pg-gwm is configured for that repository; leases the worktree from pg-gwm, renews it while working, and releases it when done. Do NOT use for a coordinated multi-repo workforest set, or in a repository whose pg-gwm config check shows no worktree-root inside your writable paths; follow the existing worktree rules there.
---

# Leasing a worktree from pg-gwm

pg-gwm hands out git worktrees under a lease, so concurrent sessions never collide on a path or a
branch and abandoned worktrees are reclaimed safely. Use it for a single-repo worktree outside a
workforest set.

## When to use it

- Use it only in a repository whose `pg-gwm config check` shows a configured worktree-root that is
  inside your session's writable paths.
- Otherwise (no configuration, root outside your writable paths, or a multi-repo change) follow the
  existing worktree rules. Do not work around the check.

## Lease

Choose a unique task name and use your session id as the owner:

```bash
p=$(pg-gwm lease --name <unique task name> --owner <session id> -b <branch> --ttl 4h --print=path) || stop and report
```

If the command fails, stop and report the refusal; do not create a worktree by hand.

A `cd` does not carry between tool calls, so in every later command use absolute paths under `"$p"`
or `git -C "$p"`. Keep the path in your notes, because shell variables do not persist either.

## Renew

While working, renew at least every hour so the lease does not expire:

```bash
pg-gwm renew --name <task name>
```

## Finish

Commit your work, or discard it, then release:

```bash
pg-gwm release --name <task name>
```

If release refuses with `dirty`, it lists the unsaved work and the commands that resolve it. Save that
work (commit it, or discard what you do not need), then release again.

## Refusals

- `max-reached`: every worktree is in use. Retry once with `--wait 10m` added to the lease command; if
  it still refuses, report and stop.
- `name-held`: the name is already leased. If the lease is yours (your own earlier session), run the
  lease again with `--or-renew` (it ignores branch arguments and warns naming the current branch).
  Otherwise choose another name.
- `lease-lost`: the lease expired and the manager reclaimed the worktree. Run `pg-gwm preserved list`
  to find the branch it saved your work on, then lease that branch under a branch of your own with
  `pg-gwm lease --name <task name> -b <your branch> <preserved branch>`.

## Never

Never delete, unlock or prune a pg-gwm worktree yourself (`git worktree remove`, `git worktree
unlock`, `git worktree prune`, `rm -rf`). Release it and let the manager reclaim it.
