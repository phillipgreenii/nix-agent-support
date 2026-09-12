package main

const usageLine = "usage: pg-router-ccpool-handler <register|self-status|dispatch|query> [flags]"

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
  query         INTF-SOURCE: answer a pull query (reads the source.query
                 request as JSON on stdin, writes source.query-reply JSON to
                 stdout) — always zero events until docket pg2-oju6w's Task
                 5.8 wires the built-in beads source for real

Exit codes: 0 ok, 1 unexpected error, 2 usage, 9 busy (a pre-accept decline).
`
