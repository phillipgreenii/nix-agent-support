import { registerBandButton } from './band'
import { listContributions } from './contributions'
import { registerModalBuilder, type ModalItem, type ModalSpec } from './modal'
import type { PgdOptions, ModApi } from './types'

// The Focus pane: a band button (order 60) that toggles a pane listing the contributions pushed to
// the focus surface, one row per item (kind, id, title, and an open button when the item has a
// url). Rows come from listContributions('focus'), already ordered by priority (larger first) and
// then by time added (earlier first). Nothing from the pane is drawn in the band. The pane rides
// the modal pane framework (its own pane id, pgd-focus), so it can be open beside another pane.
// Registered only when components.focus is on; with it off nothing here is registered.
export const FOCUS_ID = 'focus'
export const FOCUS_LABEL = 'Focus'
export const FOCUS_TITLE = 'Focus'
export const FOCUS_ORDER = 60
export const FOCUS_EMPTY_TEXT = 'Nothing tracked for this session.'
export const OPEN_PREFIX = 'open:'
export const OPEN_LABEL = '[ open ]'
export const DEFAULT_KIND = 'item'

export interface FocusRow {
  kind: string
  id: string
  title: string
  url?: string
  // The action id of the row's open button; present only when the item has a url.
  open?: string
}

// A contribution id of the form `kind:rest` gives the row kind `kind` and the row id `rest` (split
// at the first colon, both parts non-empty); any other id gives kind `item` and the whole id. The
// title is the first non-empty line of the text, trimmed; a text with none falls back to the id.
export function deriveKindAndId(contributionId: string): { kind: string; id: string } {
  const i = contributionId.indexOf(':')
  if (i > 0 && i < contributionId.length - 1) {
    return { kind: contributionId.slice(0, i), id: contributionId.slice(i + 1) }
  }
  return { kind: DEFAULT_KIND, id: contributionId }
}

export function deriveTitle(text: string, fallback: string): string {
  const line = text.split('\n').map(l => l.trim()).find(l => l !== '')
  return line ?? fallback
}

// The pane rows, read from cached store values only (no fetch, process or file read).
export function focusRows(): FocusRow[] {
  return listContributions('focus').map(c => {
    const { kind, id } = deriveKindAndId(c.id)
    const row: FocusRow = { kind, id, title: deriveTitle(c.text, id) }
    if (c.url) {
      row.url = c.url
      row.open = `${OPEN_PREFIX}${c.id}`
    }
    return row
  })
}

export function focusSpec(): ModalSpec {
  const rows = focusRows()
  const items: ModalItem[] =
    rows.length === 0
      ? [{ label: '', value: FOCUS_EMPTY_TEXT }]
      : rows.map(r => ({
          label: r.kind,
          value: `${r.id}  ${r.title}`,
          ...(r.open ? { action: { id: r.open, label: OPEN_LABEL } } : {}),
        }))
  return { id: FOCUS_ID, title: FOCUS_TITLE, items, actions: [] }
}

// Opening follows the item's url from an action (never from a draw): `open` on macOS, then
// `xdg-open` when that fails. Only http and https urls are opened.
export async function openUrl(host: ModApi, url: string): Promise<void> {
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return
  }
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') return
  for (const bin of ['open', 'xdg-open']) {
    try {
      const r = await host.process.run([bin, url], { timeoutMs: 10_000 })
      if (!r || r.exitCode === 0 || r.exitCode === undefined) return
    } catch {
      // try the next opener
    }
  }
}

export function startFocusPane(_options: PgdOptions, host: ModApi): void {
  registerModalBuilder(FOCUS_ID, focusSpec, async actionId => {
    if (!actionId.startsWith(OPEN_PREFIX)) return
    const id = actionId.slice(OPEN_PREFIX.length)
    const c = listContributions('focus').find(x => x.id === id)
    if (c?.url) await openUrl(host, c.url)
  })
  registerBandButton({ id: FOCUS_ID, order: FOCUS_ORDER, label: () => FOCUS_LABEL, action: FOCUS_ID })
  void host.command.register({ name: 'pgd-focus', description: 'Show or hide the pgd Focus pane' })
}
