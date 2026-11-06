# pgd

A session display mod: a band, an alert row and modals drawn from cached sources. Component
options live in the Home Manager module `phillipgreenii.programs.pgd`; the mod internals are in
`hooks/README.md`.

## The pgd noun (contributions)

Another bundle pushes data to the display through the `pgd` noun. Every method takes one object and
returns a Promise, and only data crosses the boundary: there is no callback argument and no action
field (contributed buttons are out of scope). A contributor pushes when its state changes; the
display never calls back into it.

| Method       | Argument                                                | Effect                                           |
| ------------ | ------------------------------------------------------- | ------------------------------------------------ |
| `pgd.put`    | `{ id, surface, priority, text, color?, ttlMs?, url? }` | Adds or replaces the contribution with that `id` |
| `pgd.remove` | `{ id }`                                                | Removes it; an unknown `id` is not an error      |

- `surface` is `alert` (plain text that joins the alert row, ranked by `priority`, larger first) or
  `focus` (an item held for the Focus pane; only a focus item may carry `url`).
- `ttlMs`: the contribution disappears that long after its last `put`, and nothing else changes. A
  contributor that stops refreshing therefore disappears. Without `ttlMs` it never expires.
- `color` is stored; the alert row is plain text and does not use it.
- A bad argument rejects with an `Error` that names the field.
- With `components.focus` off, a `focus` put resolves and stores nothing. The noun itself is
  registered whenever `enable` is true.

The types contract is [`types/pgd.d.ts`](types/pgd.d.ts) and the one documented example is
[`types/example.ts`](types/example.ts); the flake check `test-pgd-types` compiles the example
against the contract with `tsc --noEmit`.

## Modals and toggles

The session id label and the context label in the band are buttons. Each opens its own modal pane
(the Info modal and the Context and usage modal), drawn from the same cached sources as the band,
so a label and its modal never disagree. A modal never uses `holdToasts`. The band, Info and
Context are governed by the top-level `enable` option only; they have no component flag.

- Info rows, one per line: name, session id, actor, version, repo, worktree, branch, PR. A row
  whose value is missing is omitted (no branch and worktree rows when the `.git` reads fail, no PR
  row without a PR value).
- Context and usage rows: context percent and tokens, last compaction, the five-hour and seven-day
  limits each with its reset time, and cost. Each row appears only when its value exists, and a
  reset time only when the limit carries one.

### Choices the design left to the builder

Each of these is covered by a test in `hooks/modals.test.ts`, `hooks/modal.test.ts` or
`hooks/toggles.test.ts`.

| Choice                          | Decision                                                                                                               |
| ------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| What `pressButton` does         | It toggles: the first press opens the modal, a second press on the open modal closes it.                               |
| Close action                    | Always added with id `close`; the framework closes the modal itself and `onAction` never receives it.                  |
| Info name row without a source  | Omitted (no session name source exists yet); the session id has its own row.                                           |
| Band collapse button            | A `[-]` button after the labels (order 100, action `band`); a hidden band draws nothing, so use `/pgd-band`.           |
| Toggle ids                      | `band`, `info`, `context`. The band is open and each modal closed by default.                                          |
| Slash commands                  | `/pgd-band`, `/pgd-info`, `/pgd-context`, no arguments; each toggles and answers `<id> shown.` or `<id> hidden.`.      |
| Host-closed pane (Esc, dismiss) | A `ui.close` with origin `person` for a `pgd-` pane resyncs the toggle state to closed.                                |
| Colours                         | Literal `yellow`, `red`, `green`; context yellow from `contextWarn`, red from `contextAlert`; limits red from `limit`. |
| Actor                           | The session id cut to 8 characters and an ellipsis.                                                                    |
| Value formats                   | Tokens with thousands separators, cost `$1.50`, time `YYYY-MM-DD HH:MM UTC`, reset time as the engine gives it.        |
| `lastCompaction` source         | Event-driven, defined by the modal component, set from the single `session.compact` hook in `gitHead.ts`; it redraws.  |
| Open modal on invalidate        | Rebuilt on each draw from the live builder, so values stay current.                                                    |
| Toggle persistence              | An engine atom (`pgd.toggles`), hydrated at `session.start`; it survives a hot reload.                                 |
| Pane ids                        | `pgd-<modal id>`.                                                                                                      |
