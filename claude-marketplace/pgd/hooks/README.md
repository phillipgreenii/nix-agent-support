# pgd alert row

The alert row (`alerts.ts`) is the status-line row the engine prefixes with the plugin name,
`pgd`. `fitAlerts` fits it to the width itself and drops the lowest-priority items first.

- Width source: a pass-through `ui.render` hook on `PromptHint` records `e.viewport.columns`,
  then sets the row with `$.ui.status`. The band hook is not used, because the engine may
  collapse the band. That `PromptHint` keeps drawing while the band is collapsed is UNVERIFIED
  until the live collapse check; if it does not, the site is the one line to change in
  `registerAlertHooks`. With no width read the row is unfitted and the engine's own ellipsis
  applies.
- Dolt probe: `bd dolt test` with a 5 second timeout, at most every 30 seconds, shared across
  sessions, defined only while `components.beads` is on.
