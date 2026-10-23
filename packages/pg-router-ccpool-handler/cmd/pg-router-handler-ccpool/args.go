package main

const usageLine = "usage: pg-router-handler-ccpool <dispatch|postStartup|preShutdown|validate|register|self-status|origin|version|help> [flags]"

const helpText = `pg-router-handler-ccpool — the ccpool adapter for pg-router.

` + usageLine + `

A [[role]] that sets handler = "<this binary>" launches one ccpool session per
dispatched event. Every request is JSON on stdin; its "role" object ({"name",
"config"}) names the role and carries its [role.config], which this adapter
decodes strictly on every call (an unknown key or a wrong type is an error):

  [role.config]
  sessionEnv = { PG_WRAP_SPEC = "/etc/pg-wrap/worker.toml" }   # optional, passed to ccpool new --env
  [role.config.ccpool]
  actor = "worker-actor"            # required: the bd identity the session claims under
  completion = "close-only"
  onFailure = "unclaim"
  onDispatchFail = "unclaim"
  promptBody = "Work bead {{.BeadID}}"
  # also: skillMD, authorshipGuard, budget{tokens,cost,time}, isolation, poolDir,
  # beadsDir, budgetStopEscalateAfter, worktreeQuietWindow, needsInputStall,
  # extraAllowedTools, effort, precheck

Subcommands:
  dispatch      INTF-HANDLER: run one dispatched event (reads the
                 handler.dispatch request on stdin, writes handler.dispatch-reply
                 JSON to stdout): the origin gate, the bead refresh, the optional
                 precheck, then the ccpool executor. --config (or
                 PG_ROUTER_CCPOOL_HANDLER_CONFIG) names the handler-wide launch
                 config JSON.
  postStartup   INTF-HANDLER: called by core right after it boots, once per
                 enabled role of this adapter. Reads the handler.postStartup
                 request on stdin and runs the post-startup reconcile for the
                 request's role: the closed-bead session reconcile, the
                 default-pool pass, the role's orphan reconcile, the worktree
                 sweep and the lock-file GC (the same passes a dispatch runs
                 before its executor). Every pass is best effort; replies
                 outcome ok. --config as for dispatch.
  preShutdown   INTF-HANDLER: called by core at shutdown. Reads the
                 handler.preShutdown request on stdin (its role object is
                 required but not decoded) and runs the global shutdown
                 teardown: sessions actively working or still supervised are
                 spared; the rest of this handler's sessions are closed and
                 their worktrees removed. Replies outcome ok. --config as for
                 dispatch.
  register      outbound manager contract: register this participant with a
                 running pg-router core (--socket/--token or
                 PG_ROUTER_SOCKET/PG_ROUTER_TOKEN; --id, --self
                 healthy|degraded|unavailable). The launchd agent runs it.
  self-status   outbound manager contract: push this participant's own health
                 to the core (reads the cli.self-status request as JSON on
                 stdin; --socket/--token as for register).
  validate      INTF-HANDLER (optional): the one validation entry point, used by
                 the nix build, pg-router's startup and its doctor. Reads the
                 handler.validate request on stdin and replies {problems,
                 warnings} on stdout, exit 0 even when it lists problems. Reports
                 every problem it finds: the strict shape, the sessionEnv keys
                 (a secret-looking key is a warning), the load rules, and that
                 the pool directory (ccpool.poolDir, else CCPOOL_POOL) exists.
                 --skip-pool-dirs skips ONLY the pool-directory check, for the
                 nix build sandbox where the pools do not exist yet; startup and
                 doctor never pass it. --config is accepted and not checked.
  origin        NOT part of the wire contract: an operator tool for the
                 per-origin availability probe (INV-CCH-10): origin status,
                 origin ignore <key>, origin unignore <key>. Configured by the
                 launch config's originProbe block.
  version       print the version.
  help          print this text.

Exit codes: 0 ok, 1 unexpected error, 2 usage (a request without a usable role
object is one), 9 busy (a pre-accept decline).
`
