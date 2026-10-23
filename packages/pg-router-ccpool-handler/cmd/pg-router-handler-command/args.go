package main

const usageLine = "usage: pg-router-handler-command <dispatch|postStartup|preShutdown|validate|version|help> [flags]"

const helpText = `pg-router-handler-command — the command adapter for pg-router.

` + usageLine + `

A [[role]] that sets handler = "<this binary>" runs its templated argv as a
subprocess. Every request is JSON on stdin; its "role" object ({"name",
"config"}) names the role and carries its [role.config], which this adapter
decodes strictly on every call (an unknown key or a wrong type is an error):

  [role.config]
  strictBaseEnv = true          # optional: run under the minimal base env
  [role.config.command]
  argv = ["/path/to/tool", "{{.BeadID}}"]   # required; each element is templated

Subcommands:
  dispatch      INTF-HANDLER: run one dispatched event (reads the
                 handler.dispatch request on stdin, writes handler.dispatch-reply
                 JSON to stdout). The argv runs once, in its own process group;
                 success is exit 0, exit 9 is a busy decline (reason
                 command-busy), anything else is a failure carrying the stderr
                 tail. --config (or PG_ROUTER_CCPOOL_HANDLER_CONFIG) names the
                 handler-wide launch config JSON.
  postStartup   INTF-HANDLER: no-op reply; the command adapter holds no sessions.
  preShutdown   INTF-HANDLER: no-op reply; the command adapter holds no sessions.
  validate      INTF-HANDLER (optional): reads the handler.validate request on
                 stdin and replies {problems, warnings} on stdout, exit 0 even
                 when it lists problems. Reports every problem it finds.
  version       print the version.
  help          print this text.

Exit codes: 0 ok, 1 unexpected error, 2 usage (a request without a usable role
object is one), 9 busy (a pre-accept decline).
`
