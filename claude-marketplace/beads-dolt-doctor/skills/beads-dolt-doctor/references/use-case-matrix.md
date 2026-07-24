# Config use-case matrix

How each context on this machine gets its `bd`, whether it sees the shell
environment, and whether it can therefore auto-start a competing dolt server.

| Context                    | How it starts          | Sees shell env? | How it gets the `BEADS_DOLT_AUTO_START=0` default | Real `bd` consumer today?                      |
| -------------------------- | ---------------------- | --------------- | ------------------------------------------------- | ---------------------------------------------- |
| User CLI shell             | interactive login      | yes             | overlay `bd` wrapper                              | yes                                            |
| GUI app (VS Code)          | launchd `gui/UID`      | no              | overlay `bd` wrapper                              | yes (extension — being removed)                |
| Per-user launchd agent     | plist, `gui/UID`       | no              | overlay `bd` wrapper                              | yes (`pg-pr-sync`)                             |
| Root/system launchd daemon | plist, `system` domain | no              | overlay `bd` wrapper                              | none today (only Caddy proxy); forward-looking |

**Takeaway:** the overlay `bd` **wrapper** is the only mechanism common to every
row. `home.packages` and `home.sessionVariables` reach the shell row but **miss
the launchd/daemon rows** (GUI apps and daemons do not inherit the interactive
shell environment). This is why enforcement lives in the overlay/`package.nix`
wrapper, and why a rogue almost always comes from a caller that runs a **bare or
unmanaged** `bd` (not the overlay `bd`) or invokes `dolt sql-server` directly.
