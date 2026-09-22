package main

const usageLine = "usage: pg-router-ccpool-handler <register|self-status|dispatch|postStartup|preShutdown|query|pool-capacity> [flags]"

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
                 directly (e.g. by a periodic timer): reports each named
                 ccpool pool's live capacity as Prometheus exposition-format
                 text on stdout (--pool <name>=<dir>, repeatable; bead
                 pg2-mr0sl's per-role dedicated-pool metric)

Exit codes: 0 ok, 1 unexpected error, 2 usage, 9 busy (a pre-accept decline).
`
