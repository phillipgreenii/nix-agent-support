# wait-for-agents-to-finish

> Wait until all AI agents finish working.
> Keeps Mac awake while waiting and provides progress updates.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps>.

- Wait with default settings (2 hour timeout):

`wait-for-agents-to-finish`

- Wait with custom timeout and keep Mac awake:

`wait-for-agents-to-finish --maximum-wait {{3600}} --caffeinate`

- Wait with short timeout and frequent checks:

`wait-for-agents-to-finish --maximum-wait {{60}} --time-between-checks {{2}}`

- Require 5 consecutive idle checks before declaring all agents finished:

`wait-for-agents-to-finish --consecutive-idle-checks {{5}}`
