# Roles and queries get a minimal base env and expand it with wrap entries

**Status**: Accepted (operator-approved in the pg2-12gr4.1 review and rulings 20 to 28; extends ADR 0065 and ADR 0085, which pg2-12gr4.5 amends for the split)
**Date**: 2026-10-20
**Deciders**: Phillip Green II

## Context

A pg-router role payload and a `[[query]]` producer inherit whatever environment the launchd daemon
holds. That ambient environment is neither documented nor stable: a tool a role needs may or may not be
on `PATH`, and nothing says which of the daemon's variables a payload may rely on. A role that fails
because its environment is wrong looks exactly like a role whose payload failed, so the failure is
struck against the item and the real cause is hidden.

The design of record is bead `pg2-12gr4` (design, rulings 20 to 28), cited in the form the behavior sets
use: phillipgreenii-nix-agent-support · bead `pg2-12gr4` · the heading named in each decision below.

## Decision

1. **Environment is a property of the role.** Every role payload and every `[[query]]` producer starts
   from a documented minimal base environment. Wraps are the only way to expand it. There is no
   permanent fallback to the daemon's ambient environment. Adapters keep their own ambient environment,
   because handler-side `git` (origin probe, review prefetch) still needs SSH. A per-role switch turns the
   strict base on as each role family passes review and is removed once all are on. Resume uses the
   current wrap.
2. **`wrap` is the single primitive.** A role, and a `[[query]]`, declares `wrap`: an ordered list of
   entries, outermost first. Each entry is a command prefix that runs whatever follows it (a `flox
activate -d ... --`, a `nix develop ... -c`, an `env K=V`, a `timeout --foreground 25m`). There are no
   per-tool types. There is no live probe; on-demand verification is `pg-router run-role`.
3. **Two entry forms.**
   - A **string entry** is shell-parsed, so `$VAR` expands inside the previous wrapper's environment. It
     MUST NOT contain template actions; config validation rejects one that does.
   - An **array entry** (argv form) has each element rendered through the same per-element template
     engine command roles already use for their argv (fields such as `{{.BeadID}}`), then is exec'd
     directly with the rest of the chain appended. It is never shell-parsed, so item data cannot become
     shell code.
   - **Templated array entries are allowed only where the adapter builds the argv for each dispatch,
     that is, command roles.** A ccpool role's launcher is a static per-pool store script, so a template
     action in a ccpool role's wrap is a build-time validation error. ccpool roles get item context from
     environment variables, which the adapter passes through `ccpool new --env`. For ccpool roles,
     isolation (worktree and branch) stays the adapter's job through `[role.config]` isolation, because
     the adapter owns worktree creation, prefetch, cleanup, abandoned-worktree sweeps and the `--cwd`
     that ccpool stores for resume.
4. **Composition is one layer per wrap entry.** For a string entry, layer _i_ is
   `/bin/sh -c 'exec <wrap_i> "$@"' sh <layer i+1...>`. For an array entry, layer _i_ is the rendered
   elements followed by layer _i+1..._, with no shell. The innermost layer's rest is the payload argv.
   `$PATH` in a string entry therefore expands inside the previous wrapper's environment. Two nested
   `env PATH=/opt/X:$PATH` wraps measured on 2026-10-20: a single `sh -c` gave `/opt/b:/usr/bin:/bin`
   (the outer prefix was clobbered); per-layer gave `/opt/b:/opt/a:/usr/bin:/bin`. The payload argv
   arrives as `"$@"` at every layer, so untrusted item data is never parsed as shell. The shell is POSIX
   `/bin/sh` with no rc files.
5. **Absolute paths.** The first word of every string entry and the first element of every array entry
   MUST be an absolute path. The base `PATH` is only `/usr/bin:/bin:/usr/sbin:/sbin`, and macOS has no
   GNU `timeout` there. Nix helpers that render wrap entries from store paths are therefore required, not
   sugar. They also apply `lib.escapeShellArg` to arguments of string entries, because nix to TOML to sh
   is three quoting layers.
6. **The base environment list.** Initial list, revisable as each role's review greps what its payload
   actually reads:
   - system variables: `HOME`, `USER`, `LOGNAME`, `TMPDIR`, `LANG`, and
     `PATH=/usr/bin:/bin:/usr/sbin:/sbin`;
   - terminal variables the payload's terminal supplies (`TERM`, `COLORTERM` where set), which the
     claude TUI needs;
   - the participant's markers, kept by explicit name and never by prefix: `BEADS_ACTOR`, `BEADS_DIR`,
     `WORKSPACE_ROOT` and `PG_ROUTER_TEST_MODE` today, and for claude sessions ccpool's
     `CCPOOL_EXTERNAL_ID`, `PA_MONITOR_NO_NUDGE`, `CCPOOL_POOL` and `CCPOOL_AUTONOMOUS`. A prefix
     keep-list (`CCPOOL_*`, `BEADS_*`) would drop `PA_MONITOR_NO_NUDGE` and `WORKSPACE_ROOT`. The Claude
     child-session markers that ccpool blanks (`CLAUDE_CODE_CHILD_SESSION`, `CLAUDECODE` and similar) MUST
     NOT be re-introduced.
   - the item-context and ready-marker names in decision 8.

   The pg-router behavior docs state the base list generically (system variables, terminal variables and
   "the markers the participant injects"); the concrete marker names live in each adapter's own docs.
   Open per role: `PG_ROUTER_CONFIG` and `PG_ROUTER_REPO_ROOT`, `PG_CONNECTOR_ISSUE_BEADS_DIR`,
   `PG_DESK_ROUTER_CONFIG` and `XDG_*`, `CCPOOL_REGISTRY_DIR`.

7. **The wrapper contract.** Every wrap entry, adapter, launcher and query runner obeys WC-1 to WC-9:
   - **WC-1** A wrapper MUST run the rest of its argv as the payload, with argv and stdin unmodified.
   - **WC-2** It MUST return the payload's exit status unchanged. It MAY return its own non-zero status
     only for its own failure; on activation failure it MUST exit before starting the payload, with the
     reason on stderr. Exit codes alone cannot tell wrapper from payload failure (flox exits 1, timeout
     124, and the command adapter treats 9 as busy), so detection uses the ready marker (decision 9).
     A wrap entry whose command cannot be run is no exception: the shell's own status comes back
     unnormalised, 127 for a bare name or a missing command behind `env`, and 126 or 127 for a missing
     absolute path that `/bin/sh` itself execs (macOS bash 3.2 gives 126, POSIX dash 127). Nothing may
     depend on which of the two; the absent marker is the signal.
   - **WC-3** It MUST NOT write to stdout. It MAY write to stderr, and SHOULD prefix its own messages
     `pg-wrap[<stage>]:`.
   - **WC-4** It MUST leave the payload with the same controlling terminal and in the foreground process
     group (claude runs as a TUI in tmux).
   - **WC-5** It MUST deliver SIGTERM, SIGINT and SIGHUP to the payload, then run its own cleanup and
     exit (exec'ing satisfies this). Go's `exec.CommandContext` cancels with SIGKILL to the direct child
     only, so the adapters (and core for queries) MUST start the payload chain in its own process group
     and cancel by SIGTERM to the group, escalating to SIGKILL; otherwise a non-exec wrapper such as
     `timeout` orphans the payload.
   - **WC-6** It MUST work under the base environment alone, reaching its own tools by absolute path. It
     MUST NOT prompt for input.
   - **WC-7** It SHOULD exec when it has no cleanup to do.
   - **WC-8** It MUST get item data only from environment variables or from templated elements of an
     array-form entry, never from text interpolated into a string-form entry.
   - **WC-9** The first word of a string entry, and the first element of an array entry, MUST be an
     absolute path (checked by the nix helper and by core's config validation).
8. **Item context.** Wrappers get item data from environment variables or from the templated elements of
   an array entry. The adapter sets exactly `PG_WRAP_ROLE` (role name), `PG_WRAP_ITEM_ID`,
   `PG_WRAP_ITEM_TYPE` and `PG_WRAP_READY_MARKER`. The values come from the item's ID and Type. The title
   is deliberately left out because it is untrusted free text; a command role that needs it can template
   it into an array entry. The command adapter sets no environment today, so the implementing packet
   introduces these.
9. **The ready marker and the outcome rule.** The participant sets `PG_WRAP_READY_MARKER` to a
   per-dispatch marker path. The marker stage belongs to the adapter, not to the wrap list: the command
   adapter always appends it as the innermost layer and the ccpool launcher template always ends with it,
   so a hand-written role or one with an empty wrap list still gets a marker. The stage writes the marker
   just before exec'ing the payload. A non-zero exit (or, for claude, the session dying) WITHOUT the
   marker is `wrap-failed`; with the marker, the payload's own outcome is classified as today, and the
   command adapter applies its exit-9 busy mapping only when the marker exists. An optional
   `require <tools...> --` wrap entry only checks the role's declared tools and variables and has nothing
   to do with the marker. Because ccpool does not persist injected environment, every ensure and resume
   the ccpool adapter issues MUST re-send the marker path and the item-context variables through `--env`.
   If the marker stage starts with its variable unset it MUST fail loudly: a `pg-wrap[marker]:` message on
   stderr and a non-zero exit before the payload starts, never a silent skip.
10. **`wrap-failed` is its own failure reason and carries no strike.** It joins the closed failure
    signature set, is exempt from the strike policy as the limit and outage reasons are, and is routed
    explicitly in the failure-rate alert so it pages. The command adapter puts the stage's redacted
    stderr tail into the failure evidence; the ccpool launcher redirects each stage's stderr to a
    per-dispatch file beside the marker, because a pane's stderr never reaches a transcript, and the
    adapter reads it on `wrap-failed`.
11. **A handler-side role gate brakes a broken wrap.** Without a brake, a permanently broken wrap would
    re-dispatch every item for ever. A `wrap-failed` outcome therefore pauses that one role until it is
    cleared. The gate is built exactly like the per-origin decline gate:
    - **State**: one small JSON file per paused role under the handler's state directory, recording when
      the pause started, the redacted stage stderr tail, and a hash of the role's rendered config.
    - **Effect**: a dispatch to a paused role gets the existing pre-accept busy decline (exit 9) with a
      fixed, bounded reason `wrap-gated`. It mutates no bead, makes no beads or ccpool call, adds no
      strike, and core re-offers the event. Every other role keeps dispatching.
    - **Clearing**: a new config generation (the role's current rendered-config hash differs from the
      recorded one), or the explicit operator command `wrap-gate clear <role>` on a new handler
      subcommand (`wrap-gate list` shows paused roles). A successful `pg-router run-role` does NOT clear
      it, because `run-role` runs under the operator's shell environment, not launchd's.
    - **Rejected**: (a) a per-role scope on core gates, because core gates are global with one active gate
      per type and block every participant that did not exempt the type, so scoping them amends INV-LIFE-2
      and the gate set schema for one handler's need; (b) a mirror record in core, which also amends
      INV-LIFE-2 and leaves two copies of state that can disagree. Core gates and INV-LIFE-2 are unchanged.
12. **Shared fragments are interim.** A generic `claudeBaseline` (the tools every claude role needs:
    claude, gh, bd, jq, pg-connector, pn, plus the OTel endpoint and protocol without
    `OTEL_SERVICE_NAME`) lives in nix-agent-support as the shared start of every claude role's wrap list
    (`claudeBaseline ++ [ <role-specific> ]`). It is used until flox environments exist for the claude
    roles, which then replace it. nix-agent-support is public, so organization-specific additions live in
    the downstream nix-ziprecruiter repo and are not named here.
13. **Before the split, the carrier is per-role JSON and core's `[[query]]`.** A role's `wrap` and its
    `strictBaseEnv` switch travel in the handler's per-role JSON (the handler module's `roles.<name>`
    option). Core's `[[role]]` gets no `wrap` until pg2-12gr4.6. A query's `wrap` and its `strict_base_env`
    key live on `[[query]]` in core's TOML, because core runs query producers itself. Both switches
    default off. Rejected: a new dispatch-request field, and pulling the split forward.
14. **Observability.** A `dispatch_start` event, a new handler event-log kind, records the wrap strings,
    the base-environment variable names (no values) and the launcher path. `pg-router config --show` (and
    `--json`) prints each `[[query]]`'s composed command; each role's composed command follows with
    pg2-12gr4.6.

## Consequences

- A payload's environment is documented and the same under launchd and under a wrap that names its
  tools. A wrong environment surfaces as `wrap-failed` with the stage's stderr, not as a struck item.
- Every wrap entry needs an absolute path, so the nix helpers that render store paths are a required
  part of the mechanism.
- `pg-router run-role` runs under the operator's shell environment, not launchd's. While a role's
  strict-base switch is off it is not representative of the payload's environment. This MUST be
  documented where `run-role` is.
- A broken wrap pauses one role until the config changes or the operator clears the gate; the alert
  pages when it happens, and other roles are unaffected.
- The base list is a starting point. Each role's review may add names, and the list is revisable.
- `claudeBaseline` is a temporary fragment that a later flox-environment change removes.
- ADR 0065, ADR 0085 and DEC-WIRE-3 are amended for the split by pg2-12gr4.5; ADR 0089 is unchanged. The
  behavior docs for pg-router and pg-router-ccpool-handler follow as separate changes and land after
  this ADR.
