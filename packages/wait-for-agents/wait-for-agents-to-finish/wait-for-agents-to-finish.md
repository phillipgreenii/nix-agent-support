# wait-for-agents-to-finish

> Wait until no AI agent is actively working; delegates to `pa-monitor wait-until-agents-finished`.
> Optionally keeps the Mac awake while waiting.
> Exit 0 means "idle reached", not "work finished": a session blocked on a usage limit counts as idle.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps>.

- Wait with default settings (2 hour timeout):

`wait-for-agents-to-finish`

- Wait with custom timeout and keep Mac awake:

`wait-for-agents-to-finish --maximum-wait {{3600}} --caffeinate`

- Require 5 consecutive idle checks before declaring all agents finished:

`wait-for-agents-to-finish --consecutive-idle-checks {{5}}`
