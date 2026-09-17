# pg-wi-flow-identity

> ceta input processor that stamps a per-agent `PG_WI_FLOW_IDENT` onto `pg-wi-flow` invocations. Not meant to be run directly; wired into `programs.claude-extended-tool-approver.inputProcessors` by pg-wi-flow's home-manager module.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Show what it would rewrite a `pg-wi-flow` invocation into (for debugging, from a shell with `CETA_SESSION_ID` set):

`CETA_SESSION_ID={{session-id}} pg-wi-flow-identity "pg-wi-flow next"`

- Decline (no rewrite, exit 1) when the command does not invoke `pg-wi-flow`:

`CETA_SESSION_ID={{session-id}} pg-wi-flow-identity "{{some-other-command}}"`
