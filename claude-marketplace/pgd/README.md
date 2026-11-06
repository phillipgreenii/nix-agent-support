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
