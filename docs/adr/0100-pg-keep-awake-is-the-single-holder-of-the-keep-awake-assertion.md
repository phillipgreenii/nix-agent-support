# pg-keep-awake is the single holder of the keep-awake assertion

**Status**: Proposed, amended 2026-10-22 (decision 8) (extends ADR 0095 and ADR 0024; decisions O1 to O9 in the spec were confirmed as proposed on 2026-10-15)
**Date**: 2026-10-14
**Deciders**: Phillip Green II

## Context

ADR 0095 made pa-monitor release its own `caffeinate` on a low battery. It could not cover the other
holders: Claude Code's built-in keep-awake, interactive `caffeinate` commands and wrapper scripts all
hold idle-sleep assertions that no battery rule governs. On 2026-10-09 and 2026-10-10 the Mac drained to
1% to 3% under those holders and stalled for 42 to 46 minutes of awake time each time. Claude Code
offers no switch to turn its keep-awake off. The design first assumed Claude Code resolved `caffeinate`
through `PATH`, so that a shim on `PATH` could cover it; the amendment of 2026-10-22 below shows it does
not, and decision 8 covers it by identity instead.

The full design, evidence, schemas, test plan and the reviewers' findings are in
`docs/superpowers/specs/2026-10-14-pg-keep-awake-design.md`.

## Decision

1. **One keeper holds the assertion.** `pg-keep-awake keeper`, a launchd keep-alive agent, is the only
   process that starts `caffeinate`. At steady state one `caffeinate` holds the union of the live
   leases' flags and follows a small `anchor` process with `-w`.
2. **Requests are leases, and the CLI is the only contract.** A client records a lease through
   `pg-keep-awake acquire`, `release` or `run`, or through the `caffeinate`-compatible shim. The lease
   files are private. `pg-keep-awake status --check` reports health. pa-monitor enables and disables
   itself through the CLI, not through a shared library.
3. **A PATH shim covers callers that spawn `caffeinate` by name.** `pg-caffeinate-shim`, installed as
   `caffeinate` by the home-manager module, translates `-t`, `-w` and the utility form into a lease and
   stays alive for the lease's lifetime, so callers that background it and kill its pid need no change.
4. **The battery rule lives only in the keeper and the anchor.** The keeper applies hold, release and
   hysteresis (ADR 0095's values). The anchor is autonomous and exits at the release level, so a wedged
   keeper cannot hold the Mac awake on a low battery. Reaching the release level drops the assertion; it
   never forces sleep or shutdown.
5. **The assertion survives a keeper restart.** The anchor is detached and does not exit when the keeper
   goes away. It exits only at the release level, at the hold level when the battery cannot be read for
   three consecutive reads (the blind rule), or after the watchdog timeout without a heartbeat. A new
   keeper adopts the survivors and reconciles.
6. **No fallback to a bare `caffeinate`.** If the keeper is down nothing holds the Mac awake; launchd
   restarts the keeper and an alert covers a keeper that stays down.
7. **Observability follows the existing OTel pattern.** Only the keeper emits. `status` lists the live
   leases and the external assertion holders, so "what is holding my Mac awake" has an answer.

8. **Claude Code's own `caffeinate` is governed by identity, not by the shim** (amendment, 2026-10-22,
   bead pg2-0n6og; operator ruling "(a) then (b)", Phillip, 2026-10-22). An external `caffeinate` holder
   is _managed_ when ALL of these hold at the moment the keeper looks: it runs `/usr/bin/caffeinate`;
   its argv is exactly `caffeinate -i -t <seconds>`; its parent is a running `claude` process (argv[0]
   or executable name `claude`). Managed holders are marked `managed_by: "claude"` in `status` (the
   `external` list, text and JSON). While the gate is `released` (the release level, or the blind rule)
   the keeper stops each managed holder: SIGTERM, then SIGKILL after one second, signalling a pid only
   while its start time still matches the one identified. Any other holder is never stopped. Claude Code
   re-arms its inhibitor every 240 seconds while a turn is busy; a replacement started while the gate is
   still released is stopped on a later tick. The gates `ok` and `no_spawn` leave managed holders alone
   (`no_spawn` keeps a running assertion, as for the keeper's own).

## Mechanism evidence (amendment, 2026-10-22)

Step (a) of the ruling, read-only inspection of the installed Claude Code 2.1.303: the nix `bin/claude`
is a launcher script ending in `exec -a "claude" .../bin/.claude-wrapped "$@"`, so a Claude Code process
has argv[0] `claude` and executable `.claude-wrapped`. `.claude-wrapped` (a bundled JavaScript runtime
image, 267973136 bytes) was searched with `rg -a`, with no `fs_usage`, no `dtruss`, no sudo and no
network. The sleep-inhibitor class reads, verbatim from the bundle:

```text
getSpawnArgs(){...if(O()==="macos")return["caffeinate",["-i","-t",e]];return null}   // e = String(300)
spawnInhibitor(){...let[n,r]=e,s=Si(n,r,{stdio:"ignore",windowsHide:!0,env:YFr(os(),O()==="windows"?"win32":"posix")});...}
YFr(e,n){let r={...e}; ... else r.GIT_CONFIG_GLOBAL=$e,r.PATH=ki,r.ZDOTDIR=$e,...;return r}
ki="/usr/bin:/bin:/usr/sbin:/sbin"          // import{spawn as Si}from"child_process";var Ci=300,Ai=240000,vi=30000
```

So Claude Code spawns the BARE name `caffeinate` with `child_process.spawn`, but passes a child
environment whose `PATH` is replaced by the constant `/usr/bin:/bin:/usr/sbin:/sbin`. The name is
resolved against that `PATH`, not the user's, so the real `/usr/bin/caffeinate` runs with argv[0]
`caffeinate` and a `caffeinate` shim earlier on the user's `PATH` is never consulted. This matches the
live evidence of 2026-10-21 (shim directory ahead of `/usr/bin` in the session's `PATH`, yet the holder
was `/usr/bin/caffeinate` with args `caffeinate -i -t 300`). The string `/usr/bin/caffeinate` does not
occur in the bundle (the earlier guess of a hard-coded absolute path was wrong in detail, right in
effect). The restart interval is 240 s (`Ai`) against the 300 s `-t`; the inhibitor is reference-counted
and killed with SIGKILL 30 s (`vi`) after the last release. The holders are therefore identifiable: the
argv is Claude Code's fixed request and the parent is the `claude` process that spawned it. The
identification rule above uses only the process table of the keeper's own user (another user's argv is
unreadable, so it never matches).

## Consequences

- Every keep-awake request that goes through `PATH` or the CLI obeys one battery rule, and so does Claude
  Code's own inhibitor (decision 8). Any other holder that calls `/usr/bin/caffeinate` by absolute path
  or with a `PATH` of its own, or any non-`caffeinate` assertion, is not covered; `status` lists such
  holders. Decision 8 is the one exception to "the product MUST NOT stop another holder", and it is
  narrow by construction. A future Claude Code that changes its request (argv, the `claude` parent) falls
  out of the rule silently and shows as an unmanaged `external` holder; the identification rule is the
  seam to update.
- The shim is a new executable on `PATH` named `caffeinate`. It MUST track the real tool's argument and
  exit-status behavior, which the design pins with parity tests.
- With the keeper down and no held assertion, the Mac can sleep mid-task. The accepted remedy is the
  keeper's restart plus an alert, not a fallback.
- pa-monitor's battery policy and `caffeinate` process management move out of pa-monitor, and its
  battery home-manager options are deprecated. The existing `pa_monitor.caffeinate.*` series keep their
  meaning through `status --json`.
- A behavior-docs set for the CLI and the state invariants is a follow-up.
