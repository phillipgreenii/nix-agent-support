package main

const usageLine = "usage: pg-router-ccpool-handler <register|self-status|dispatch|postStartup|preShutdown|validate|query|pool-capacity|origin|split> [flags]"

const helpText = `pg-router-ccpool-handler — the ccpool/command participant for pg-router.

` + usageLine + `

Subcommands:
  register      register this participant with a running pg-router core
                 (--socket/--token or PG_ROUTER_SOCKET/PG_ROUTER_TOKEN;
                 --id, --self healthy|degraded|unavailable)
  self-status   push this participant's own health to the core (reads the
                 cli.self-status request as JSON on stdin)
  dispatch      INTF-HANDLER: run one dispatched event (reads the
                 handler.dispatch request as JSON on stdin, writes
                 handler.dispatch-reply JSON to stdout)
  postStartup   INTF-HANDLER: once-per-process-lifetime startup hook,
                 dispatched right after the core boots (pg2-oju6w.15). No-op
                 today, built for symmetry with preShutdown; reads the
                 handler.postStartup request as JSON on stdin, writes
                 handler.postStartup-reply JSON to stdout.
  preShutdown   INTF-HANDLER: once-per-process-lifetime shutdown hook,
                 dispatched at the same point the core used to sweep every
                 pg-router-prefixed ccpool session itself (pg2-oju6w.15) —
                 that sweep now runs HERE instead. Reads the
                 handler.preShutdown request as JSON on stdin, writes
                 handler.preShutdown-reply JSON to stdout.
  validate      INTF-HANDLER (optional): the one validation entry point, used by
                 the nix build, pg-router's startup and its doctor. Reads the
                 handler.validate request as JSON on stdin and writes
                 handler.validate-reply JSON ({problems, warnings}) to stdout;
                 exit 0 means it ran (empty problems = valid). Checks the role
                 JSON strictly (--role-config or PG_ROUTER_CCPOOL_HANDLER_ROLE;
                 an unknown key or a wrong type is a problem), the sessionEnv
                 keys (a secret-looking key is a warning), and that each pool
                 directory (ccpool.poolDir, else CCPOOL_POOL) exists. Runs no
                 payload and never runs or reads pg-wrap or its spec.
                 --skip-pool-dirs skips ONLY the pool-directory check, for the
                 nix build sandbox where the pools do not exist yet; startup and
                 doctor never pass it. --config is accepted and not checked.
  query         INTF-SOURCE: answer a pull query (reads the source.query
                 request as JSON on stdin, writes source.query-reply JSON to
                 stdout) — answers zero events when unconfigured, or the
                 real "bd ready" results when --query-config (or
                 PG_ROUTER_CCPOOL_HANDLER_QUERY) names its beads-backed
                 label/title-prefix/item-type filters (docket pg2-oju6w's
                 Task 5.8; --config/PG_ROUTER_CCPOOL_HANDLER_CONFIG supplies
                 RepoRoot/BeadsPrefix)

  pool-capacity NOT part of the wire contract above (never spawned by
                 pg-router core) — an operational/observability tool run
                 directly (e.g. by a periodic timer): makes ccpool emit
                 each named pool's capacity as the ccpool_pool_capacity OTLP
                 gauge (--pool <name>=<dir>, repeatable; OTEL_* env from the
                 caller; failures on stderr; bead pg2-mr0sl / pg2-om899.6)

  origin        NOT part of the wire contract — an operator tool for the
                 per-origin availability probe (INV-CCH-10): origin status,
                 origin ignore <key>, origin unignore <key>. Configured by
                 the launch config's originProbe block (bead pg2-4gi2c)

  split         NOT part of the wire contract — the split-triage role's write
                 path (bead pg2-47rsh): split apply <bead-id> (JSON plan on
                 stdin) / split unsplittable <bead-id> (reason on stdin).
                 See: split --help

Exit codes: 0 ok, 1 unexpected error, 2 usage, 9 busy (a pre-accept decline).
`
