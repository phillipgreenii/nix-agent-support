# ccpool reap-all over a permanent pool registry

**Status**: Accepted
**Date**: 2026-06-15
**Deciders**: phillipg

## Context

ccpool's pool isolation (bead pg2-dckz, design
`docs/superpowers/specs/2026-06-12-ccpool-pool-isolation-design.md`) made a pool a
**directory path** and stated flatly: _"There is no registry and no naming scheme —
the path is the name."_ That was a deliberate simplification: a pool is discovered by
the caller passing `--pool`/`CCPOOL_POOL`, never by enumerating a central list.

The cost surfaced immediately as a documented v1 limitation. Reaping (closing idle /
over-cap sessions) is driven by a launchd agent (darwin) / systemd timer (nixos) that
runs **bare `ccpool reap`** with no `--pool`. With no flag/env, `reap` resolves the
**default** (XDG) pool only. So after pool isolation landed, every named pool's
`idle_ttl`/`max_sessions` governance never ran automatically — an owner had to run
their own `ccpool --pool P reap` via cron/direnv/manual. Sessions in named pools
accumulated unbounded.

The follow-up bead (pg2-4zvg) originally framed the fix as a **nix-module change**:
register multiple reapers (per-pool timer units, or a timer that iterates pool dirs).
That framing was rejected in discussion. Per-pool timer units multiply launchd/systemd
state per pool and need teardown when a pool is deleted; a timer that scans a fixed set
of dirs needs that set configured somewhere. Both push pool inventory into the system
layer, where it is hard to keep in sync with the pools a user actually creates at
runtime.

## Decision

Reintroduce a **pool registry** — the very thing v1 avoided — but as a minimal,
self-maintaining artifact, and drive reaping from **one timer run**, not many reapers.

1. **One timer run.** The reap timer invokes a new `ccpool reap-all` instead of bare
   `ccpool reap`. `reap-all` reaps the default pool, then sweeps the registry and reaps
   each registered pool in-process. `ccpool reap` is unchanged (reaps exactly the
   active pool via `--pool` > `CCPOOL_POOL` > default).

2. **Permanent symlink registry.** `$XDG_STATE_HOME/ccpool/pools.d/` (override
   `CCPOOL_REGISTRY_DIR`) holds one symlink per pool: name = the pool's socket hash
   (`SocketFor`), target = the canonical pool dir. The target _is_ the data — the
   directory itself is the machine's pool inventory. It is permanent (survives
   reboots), not runtime/ephemeral. Go owns lazy creation of the dir, so the timer
   and tests never depend on the nix module provisioning it.

3. **Register on creation only.** A pool enrolls the moment `ccpool` first creates its
   directory (the `os.Mkdir` success branch of `ensurePoolDir`). Resolving an existing
   pool takes the validate branch and is registry-side-effect-free, so read-only
   verbs (`list`/`state`/`doctor`) never enroll a pool in auto-reaping. The default
   (XDG) pool early-returns before `ensurePoolDir`, so it never self-registers —
   which is what keeps `reap-all`'s separate default-pool reap from double-reaping.

4. **GC, never destroy.** `reap-all` removes a registry symlink whose target is dangling
   or has gone foreign (fails the read-only pool-dir validator). It unlinks the
   **symlink only** — never the target or any pool data — re-checks immediately before
   unlink, and tolerates `ENOENT`. Per-pool failures are logged but never abort the
   sweep (a timer must not go silent).

5. **`auto_reap` opt-out.** A new per-pool config key `auto_reap` (default `true`)
   gates only the `reap-all` sweep: `false` skips that pool entirely (idle AND
   over-cap), while a manual `ccpool reap` still reaps it and it stays registered.
   Distinct from `idle_ttl = 0`, which disables only TTL closures but still enforces
   the cap.

The bulk of this is **core Go** (registry, register-on-create, read-only validator +
GC, the `reap-all` driver, `auto_reap`), not a nix-module change — inverting the
original bead's "nix-only" framing. The nix change is reduced to swapping the timer
invocation and updating doc strings.

## Consequences

### Positive

- Named pools are governed automatically by the existing single timer; no per-pool
  units to provision or tear down.
- `pools.d/` is a durable, inspectable machine-wide pool inventory for free.
- Registration is a side effect of normal pool creation — zero extra user action.
- GC keeps the inventory self-healing without ever risking pool data.

### Negative

- Reverses the explicit v1 "no registry" stance, adding a second source of truth
  (the symlink set) alongside the pool dirs themselves. GC is what keeps them
  reconciled.
- **Pre-existing pools are not enrolled**: a pool whose directory already existed
  before this feature (or one a user `mkdir`s by hand) never hits the create branch,
  so it is absent from `pools.d/` and ungoverned until re-created. Acceptable at the
  current small pool count; a future `ccpool register <dir>` convenience can backfill.
- A moved/renamed pool transiently shows both a dangling old link and a new link
  until the next sweep GCs the old one.

### Neutral

- The registry lives under `$XDG_STATE_HOME`, not in the nix store or a tracked file;
  it is per-machine runtime state.
- `reap-all` is timer-facing; interactive users keep using `ccpool reap` /
  `ccpool --pool P reap` exactly as before.

## Alternatives Considered

### Per-pool timer units (the bead's original framing)

One launchd agent / systemd timer per pool. Rejected: multiplies system-layer state
per pool, requires teardown on pool deletion, and couples pool inventory to the nix
module rather than to the pools a user creates at runtime.

### Timer iterates a configured set of pool dirs

A single timer that scans a fixed list of pool directories. Rejected: the list has to
be configured somewhere (nix or a config file) and drifts from the pools actually in
use; the self-advertising registry removes that sync burden.

### Runtime/ephemeral registry

Keep the registry under `$XDG_RUNTIME_DIR` so it clears on reboot. Rejected: a durable
inventory that survives reboots is more useful, and reap governance should resume after
a reboot without waiting for each pool to be touched again.

### Pool-resolution redesign (cwd-as-pool / `--use-default-pool`)

Considered alongside this work and **dropped** (not parked): it is orthogonal to reap
governance and would have expanded scope without serving the timer fix.

## Related Decisions

- Pool isolation design (bead pg2-dckz):
  `docs/superpowers/specs/2026-06-12-ccpool-pool-isolation-design.md` — establishes the
  "a pool is a directory, no registry" model this ADR partially reverses. Its "Known v1
  limitation" section is annotated as resolved by this ADR.
- See also: the parallel darwin/nixos launchd/systemd registration split (ADR 0049 in
  the consuming machine flake) — this ADR only changes which command that unit runs.
