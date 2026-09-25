---
name: service-daemon-checklist
description: >-
  Use before registering ANY new background service in this workspace's nix-darwin/NixOS/
  home-manager configuration: a `launchd.daemons`/`launchd.user.agents` entry, a
  `phillipgreenii.system.launchdServices.{daemons,userAgents}` registration, or a
  `systemd.services` entry. Walks through the seven-question authoring checklist before the
  registration is written — process identity (does it show its own name, not `bash`), log
  collection (otel `logSources`), log rotation (`manageLogs`), metrics/alerts, liveness
  alerting (`launchdHealthCheck`, `checkMode` for periodic jobs), restart-on-change, and
  cross-platform (Linux/`systemd`) support. Fires on intents like "add a new launchd service",
  "register a daemon", "create a LaunchAgent/LaunchDaemon for X", "add a systemd service", "wire
  up phillipgreenii.system.launchdServices", or any nix-darwin/NixOS module change introducing a
  new background process that should keep running. Do NOT use for debugging or modifying an
  EXISTING already-registered service that's misbehaving (that's ordinary troubleshooting, not
  an authoring checklist), for generic launchd/systemd conceptual questions unrelated to
  authoring a new registration, or for the beads/dolt server specifically (that has its own
  dedicated `beads-dolt-doctor` skill).
---

# Service/Daemon Authoring Checklist

Before writing any new `launchd.daemons`/`launchd.user.agents` entry, a
`phillipgreenii.system.launchdServices.{daemons,userAgents}` registration, or (once it exists) a
`systemd.services` entry, work through the seven questions below in order. Each one either names
the mechanism that already answers it, or states plainly that no mechanism exists yet for it —
skipping a question on the assumption that "the default is probably fine" is exactly the failure
mode this checklist exists to catch: the 2026-09-24 disk-full incident (`pg2-jw8ji`) traced back to
question 3 being skipped on a continuously-logging service.

**The canonical, load-bearing version of this checklist lives in `phillipgreenii-nix-personal`'s
`docs/service-daemon-checklist.md`.** Read that file directly for the exact option names, file
paths, and worked reference examples (`ollama-probe`, `zr-local-proxy-probe`, `ccpool-reap`) — this
skill is a trigger + summary, not a fork of that content. If the underlying mechanisms change,
update the canonical doc, not this skill's body.

## The seven questions

1. **Does the process show its own name, not `bash`?** Register through
   `phillipgreenii.system.launchdServices.{daemons,userAgents}`
   (`phillipgreenii-nix-personal`'s `lib/options/launchd-services.nix`,
   `darwin/system/launchd-services.nix`). The default `script` wrapper is a shell script, so the
   FIRST loaded Mach-O image is `bash` — fine for most services, but macOS TCC's
   responsible-identity resolution for a personal-information consent prompt (Calendar/Contacts/
   etc.) keys on that first image, not on whatever the script later `exec`s. If this service will
   ever request such a permission, set `execPath = "${pkg}/bin/<name>";` instead of `script`
   (`phillipgreenii-nix-personal` ADR 0054) — mutually exclusive with `script`, and incompatible
   with `manageLogs.enable = true` (rotation needs a shell preamble a bare symlink can't
   provide).

2. **Will its logs be collected by otel?** Declare a
   `phillipgreenii.observability.logSources.<name>` entry (`phillipgreenii-nix-support-apps` ADR
   0038, `darwin/modules/observability/registration.nix`) if this service emits a structured
   JSONL log worth shipping to Loki. This is a SEPARATE, manually declared registration from
   `launchdServices` — nothing wires it automatically, so you must add it yourself. A service with
   nothing worth shipping (e.g. a shared log-rotator daemon) can skip this, but make that a
   deliberate decision, not an oversight.

3. **Do its logs rotate?** Set `manageLogs.enable = true;` on the `launchdServices` entry
   (default `false`). Bounds `StandardOutPath`/`StandardErrorPath` at a size threshold with a
   retained archive count, both at startup and — for user agents — via an hourly shared rotator.
   Requires the log path to be STATIC at nix eval time. **This is the mechanism the 2026-09-24
   disk-full incident (`pg2-jw8ji`) existed to close** — a service that skips this and logs
   continuously WILL eventually fill the disk.

4. **What metrics/alerts does it need?** No generic mechanism answers this — it's inherently
   service-specific. Decide explicitly: what (if anything) it exports on `/metrics` and whether
   something already scrapes it; what Grafana panel(s)/dashboard would make its health legible at
   a glance; and what alert rule(s) beyond the generic "is it running" liveness check (question 5)
   it needs. Don't skip this by assuming the liveness check is enough — it only proves the process
   is running, not that it's doing its job correctly.

5. **Does it alert when it isn't running?** For anything registered via `launchdServices`, the
   watch-list is automatic — `phillipgreenii.observability.launchdHealthCheck`
   (`phillipgreenii-nix-support-apps`'s `darwin/modules/observability/launchd-health.nix`) derives
   it, exports Prometheus metrics, and renders Grafana alert rules. Getting INTO that watch-list
   correctly still needs two things: classify periodic jobs correctly (a `StartInterval`-triggered
   job that exits between runs needs `checkMode = "periodic"`, or the activation-time check
   misreads the gap between runs as a failure), and override `healthCheckTimeoutSeconds`/
   `bootstrapSettleSeconds` if this service's startup is slower than the 10s default.

6. **Does it restart when its binary or config changes?** Automatic, for anything registered via
   `launchdServices` — the helper embeds the wrapper's own store-path hash into the plist, forcing
   nix-darwin's bootout/bootstrap on every rebuild that changes it. No action needed.

7. **Does it need to run on Linux too?** No implementation exists yet. The option API is
   nominally cross-platform (`phillipgreenii.system.launchdServices` is declared for every
   platform), but the IMPLEMENTATION is darwin-only — setting these options on NixOS is a silent
   no-op. If this service genuinely needs to run on Linux, that's new design work (a
   `systemd.services`-backed implementation of the same option API), not something to build ad
   hoc for one service. Ask first whether that work already exists on the operator's other
   (Linux) machine before starting it.

## When this doesn't fully apply

- A NEW otel-stack service currently inherits log rotation from `phillipgreenii-nix-support-apps`'
  `mkServiceWrapper.nix` startup-rotation preamble instead of `manageLogs`, because the migration
  of the otel-stack services themselves onto `launchdServices` hit a recursion bug and hasn't
  landed yet. Questions 1, 2, 4, and 6 still apply to such a service normally; see the canonical
  doc's status note under question 3 for the detail.
- This checklist covers the launchd/nix REGISTRATION layer only, not the service's own
  application-level correctness, nor the `keepAlive`/`RunAtLoad`/`launchOnlyOnce` choice for a
  service that isn't periodic in the question-5 sense (see the option docstrings in
  `lib/options/launchd-services.nix` directly for that).

## References

- `phillipgreenii-nix-personal`'s `docs/service-daemon-checklist.md` — the full checklist, kept
  current; read it for anything this summary abbreviates.
- `phillipgreenii-nix-personal` ADRs 0049, 0051, 0054 — stable-path indirection, off-PATH
  wrappers, `execPath` native-exec mode.
- `phillipgreenii-nix-support-apps` ADR 0038 — observability app registration and JSONL logging.
