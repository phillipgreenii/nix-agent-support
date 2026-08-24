# Machine-wide "no rogue dolt auto-start" for beads

**Status**: Accepted
**Date**: 2026-07-24
**Deciders**: Phillip Green II

## Context

On `phillipg-mbp-02`, beads (`bd`) repeatedly broke because a **second,
config-less `dolt sql-server` kept binding `127.0.0.1:25252`**, starving the
legitimate shared server. The legitimate server is a per-user launchd agent
(`org.nixos.beads-dolt-server`, `keepAlive=true`) serving the real data under
`~/.local/share/beads-dolt` (hundreds of MB to tens of GB). While a rogue held
the port, launchd respawned the daemon in a crash-loop (`LastExitStatus=256`,
"Port 25252 already in use") and every `bd` call hit the wrong (near-empty) DB.

Two incidents of the same class occurred (see
`docs/runbooks/beads-dolt-no-autostart.md`):

- **2026-07-24** — the `planet57.vscode-beads` VS Code extension polls
  `bd dolt status` every 3000 ms and, on connect / on error, runs
  `bd dolt start`, spawning a config-less server at a workspace-local empty DB.
- **Prior** — a non-dolt maintenance daemon shelled out to `bd` on a timer
  **without the user-level environment**, resolving config differently and
  starting a competitor.

Key facts established during config research (bd 1.0.4):

- `dolt.auto-start` is **per-project only** (`.beads/config.yaml`); there is no
  global `config.yaml` for it. `BEADS_DOLT_AUTO_START=0` is a true,
  cwd-independent switch for **transparent** auto-start.
- **No** config/env value suppresses an **explicit** `bd dolt start` in
  `dolt_mode: server`; explicit starts can only be prevented by removing/guarding
  the caller or by holding the port.
- The chokepoint that reaches **every** context — shells, GUI apps, and launchd
  daemons (user or root) — is the **overlay/`pkgs/beads/package.nix` wrapper**,
  not `home.packages` or `home.sessionVariables` (those miss the daemon rows).
- Because that chokepoint is darwin/pkgs-scoped, a home-manager option cannot
  gate it.

## Decision

Adopt a machine-wide, flag-gated policy: **forbid transparent dolt auto-start**,
enforced at the `bd` overlay wrapper, with defense-in-depth and a docs/agent
awareness layer. Split across two repos:

1. **Single flag (feature toggle), darwin scope.**
   `phillipgreenii.beads.forbidDoltAutoStart` is declared in
   **agent-support's darwin module** (`darwinModules.default`) as a plain
   `mkOption` (bool, `default = true`, secure-by-default). It is propagated into
   home-manager via `home-manager.extraSpecialArgs.forbidDoltAutoStart` so
   HM-scoped consumers read the **same** value. Nothing hardcodes the policy.

2. **Enforcement at the overlay wrapper (facade / single chokepoint)** — in the
   consumer `your-private-flake`. When the flag is enabled, the
   machine-wide `bd` derivation's single `wrapProgram` adds
   `--set BEADS_DOLT_AUTO_START 0`, so every consumer — shells, GUI apps, and
   launchd daemons — inherits it. Parameterize `package.nix` with
   `forbidDoltAutoStart` and pass the flag through the `machines/default.nix`
   overlay; touch only the build-arg/`postInstall`, never `src`.

3. **Remove the active caller.** When the flag is enabled, `planet57.vscode-beads`
   MUST NOT be installed via nix (VS Code). The inert `~/.cursor` extension copy
   MAY be removed as hygiene — the Cursor editor is not installed, so it is not
   an active poller.

4. **Port invariant as mitigation.** `org.nixos.beads-dolt-server` stays
   `keepAlive=true` and owns `25252`. This is a **mitigation** (it downgrades
   stray explicit starts to no-ops while it holds the port), **not a guarantee**
   — restart/activation/OOM windows exist where the port is briefly free.

5. **Agent awareness + knowledge (this repo).** A flag-gated always-on rule
   (`home/programs/agent-rules/beads-dolt-rule.md`, composed into the user
   `~/.claude/CLAUDE.md`), the `beads-dolt-doctor` skill, and the runbook
   (`docs/runbooks/beads-dolt-no-autostart.md`: use-case matrix + incident log +
   debugging playbook) record the failure mode and equip future agents to debug
   it.

## Alternatives Considered

### Block explicit `bd dolt start` / `dolt sql-server` — **rejected**

There is no config/env switch that suppresses an explicit start in
`dolt_mode: server` (only `embedded` refuses, which is incompatible with the
shared server). Hard-blocking would also break legitimate isolated test servers,
and the prior incident's daemon starts dolt directly. So explicit-start blocking
is a non-goal; the policy targets **transparent** auto-start plus removing the
one caller that ran an explicit start unconditionally.

### Set `dolt.auto-start` in a global config file — **rejected**

`dolt.auto-start` is per-project only; a `~/.config/bd/config.yaml` does not
control it. Retired in favor of the cwd-independent `BEADS_DOLT_AUTO_START=0`.

### Enforce in the home-manager `bd` / `home.sessionVariables` — **rejected**

A home-level override reaches shells but **misses launchd daemons** (user or
root) — the exact blind spot behind the prior incident. Enforcement must be in
the overlay wrapper, which every context resolves.

## Consequences

### Positive

- No context — including launchd daemons — transparently auto-starts a dolt
  server when the flag is on; the whole policy is one opt-in, secure-by-default
  flag.
- Future agents get the failure signature, the use-case matrix, and a debugging
  playbook (rule + skill + runbook).

### Negative

- The port invariant is only a mitigation; a rogue explicit start during a
  restart window can still briefly grab the port.
- Enforcement spans two repos: building the consumer against the new option
  requires an input override / lock bump, and cross-repo landing is coordinated
  separately (the enforcement half lives in `your-private-flake`).

### Neutral

- `keepAlive=true` remains a hardcoded **service-module invariant**
  (documented), not a machine option — no fake `services.beads-dolt-server.keepAlive`
  option is asserted.
- The named module-argument default idiom (`arg ? true`) is **not** honoured by
  the Nix module system; the agent-rules module reads the flag as
  `args.forbidDoltAutoStart or true`, giving a real fail-safe default when a
  consumer does not propagate the flag.

## Related

- Design: `docs/superpowers/specs/2026-07-24-beads-dolt-no-autostart-design.md`
- Runbook: `docs/runbooks/beads-dolt-no-autostart.md`
- Skill: `claude-marketplace/beads-dolt-doctor/`
- Delivery of always-on rules: ADR
  `docs/adr/0017-static-nix-built-local-plugin-marketplace.md` (agent-rules is
  the user `~/.claude/CLAUDE.md`, not a plugin).
