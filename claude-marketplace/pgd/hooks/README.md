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

## Components

`components.ts` registers a component only when its flag is true: `beads`, `prStatus`,
`sessionMode`, `modelEffort`, `focus`, `sessionName`, `assume` and `links`. The Info and Context
modals and the Wrap up button have no flag and follow `enable` alone. The Home Manager module
(`home/programs/pgd/default.nix`) exposes the same eight flags under
`phillipgreenii.programs.pgd.components`.

## The `pgd.put` contract

`pgd-noun.ts` adds the `pgd` noun to `$` (data in, Promise out, no callback).

- `put({ id, surface, priority, text, color?, ttlMs?, url? })`: `id` is a non-empty string,
  `surface` MUST be `alert` or `focus`, `priority` a finite number, `text` a string, `ttlMs` a
  positive number. Only a `focus` contribution MAY carry a `url`. An invalid field rejects with
  `pgd.put: invalid <field>: <why>`.
- `remove({ id })` drops the contribution.
- A put with an existing `id` replaces it. `ttlMs` expires the item and the display redraws.
  `contributions.ts` lists a surface by larger `priority` first, then earlier put first.
- With `enable` false, or a `focus` put while `components.focus` is false, the call resolves and
  stores nothing.
