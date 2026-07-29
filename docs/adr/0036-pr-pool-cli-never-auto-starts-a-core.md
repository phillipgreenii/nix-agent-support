# pr-pool CLI never auto-starts a core; "no running core" is an error

**Status**: Accepted
**Date**: 2026-07-28
**Deciders**: Phillip Green II

## Context

pr-pool's core runs as a **socket service** (`INV-LIFE-1`), and everything that talks to it —
the `ingest-event` manager callback, the operator `push-inject`, `status` — first has to **locate**
it: an injected socket path (env/arg), else discovery of the running socket service
(`INTF-CLI` "Locating the core").

What happens when nothing is found was deliberately left open, tracked in the pr-pool behavior set
as the open question `OQ-AUTOSTART` (uuid `f0cc2ca2-9f58-4c57-bfff-d81d003370fb`): **auto-start a
core, or fail with a hint?** It blocked `INTF-CLI`'s locate behavior and `JOURNEY-RUN`. The
socket-service foundation (bead `pg2-f3mcb.1`) cannot be built without answering it, because the
answer IS the behavior of every locate path.

Auto-start is attractive at first glance: a push source could fire an `ingest-event` callback into a
cold machine and "just work". The costs are what decide it.

## Decision

**A CLI entry point that cannot locate a RUNNING core MUST fail with a clear "no running core"
error and a non-zero exit code. It MUST NOT spawn one.** Locating means _reaching_ a core, so a
stale discovery record left by a crashed core is the same outcome as no record at all.

Rationale:

- **Asymmetric reversibility.** Spawning is the larger behavior commitment. Adding auto-start later
  is easy; **un-spawning** it is not — by then callers depend on a core materialising, and removing
  it breaks them.
- **Lifecycle ownership.** Auto-start would put **daemon lifecycle ownership in a CLI entry point** —
  and specifically in a **callback** invoked by a participant, where nothing owns the daemon's
  shutdown, its logs, or its restart policy. The core's lifecycle belongs to whoever runs it
  (`run` / `run-until-idle`, a launchd/systemd unit), not to an incidental caller.
- **It needs a lock.** Two concurrent callbacks finding no core would both spawn, racing over one
  socket path and one discovery record. That means introducing a spawn lock — real machinery in the
  service of a convenience.
- **Observability.** Keeping it an error leaves **"is a core running?" answerable from the caller's
  exit code**. Auto-start silently erases that signal: every call succeeds, and a machine where the
  core keeps dying looks healthy.

Consequently `OQ-AUTOSTART` is **resolved and removed** from the pr-pool behavior set: the open
question is deleted from `journeys.md` and `interfaces.md` now states the locate behavior as a
requirement.

## Consequences

- Every locate path shares one terminal error (`core.ErrNoRunningCore` in the Go realization), and
  the diagnostic names the remedy — start the core's socket service, or pass `--socket`/`--token`
  from a core-issued callback.
- A push source's events are **not** delivered while no core is running. This is not a new loss:
  under the durable queue, delivery is guaranteed only once an event is **enqueued**, and a source
  that cannot reach the core has not enqueued anything. The source sees a non-zero exit code and can
  re-emit; the core de-duplicates by event `id` within `ttl` (`INV-EVT-3`), so re-emitting is safe.
- Operators must run the core explicitly. That is the intended posture for a daemon — the same
  posture as the machine-wide "no rogue auto-start" stance taken for beads/dolt in
  [ADR 0032](0032-beads-dolt-no-autostart.md), for the same underlying reason: a process that
  silently spawns competitors is harder to reason about than one that reports it is absent.
- If auto-start is ever wanted, it arrives as an **explicit opt-in** (a flag or config key) on top of
  this default, with the spawn lock it requires — not as a change to the default.
