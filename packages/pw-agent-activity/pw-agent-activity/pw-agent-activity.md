# pw-agent-activity

> Wait for all AI agents to finish; delegates to `agent-activity-api wait`.
> Every option is forwarded verbatim, so `agent-activity-api help` is the authoritative option list.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Wait with default settings (2 hour timeout):

`pw-agent-activity`

- Wait with a custom timeout:

`pw-agent-activity --maximum-wait {{3600}}`

- Wait and keep the Mac awake:

`pw-agent-activity --maximum-wait {{3600}} --caffeinate`

- Poll less often:

`pw-agent-activity --time-between-checks {{30}}`
