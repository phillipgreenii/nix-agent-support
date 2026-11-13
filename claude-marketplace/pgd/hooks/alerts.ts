import { getOptions } from './options'
import { readCached } from './source'
import type { AlertThresholds, ModApi } from './types'
import type { Usage } from './usage'
import { timedDraw } from './metrics'
import { isWrapUp } from './session-mode'
import { listContributions } from './contributions'
import { loginNeedsAction, type SsoLogin } from './assume'

// The alert row (the status line): one aggregated row listing only the alerts whose condition is
// true, highest priority first. The engine prefixes the row with a warning glyph and the plugin
// name (pgd) and cuts a long row mid-word, so fitAlerts drops the lowest-priority items itself
// when the width runs out. Priorities (larger is higher): wrapping up 100 (session-mode
// packet), dolt unavailable 80, rate limit near cap 70, context excessive 60, login expired 50
// (assume packet); a later packet adds its entry to ALERT_ITEMS. Every item states a true
// condition; an absent thing is never a "no X" item. contextWarn plays no role in this row.
export interface AlertInputs {
  usage?: Usage
  dolt?: boolean
  thresholds: AlertThresholds
  mode?: unknown
  assume?: { role?: string; login?: SsoLogin }
  now: number
}

export interface AlertItem {
  id: string
  priority: number
  condition: (v: AlertInputs) => boolean
  text: (v: AlertInputs) => string
}

// The limits at or above the threshold, the fullest first.
function nearCap(v: AlertInputs): Array<{ kind: string; usedPercent: number }> {
  return (v.usage?.limits ?? [])
    .filter(l => typeof l.usedPercent === 'number' && l.usedPercent >= v.thresholds.limit)
    .sort((a, b) => b.usedPercent - a.usedPercent)
}

const LIMIT_NAME: Record<string, string> = { five_hour: '5h', seven_day: '7d' }

export const ALERT_ITEMS: AlertItem[] = [
  {
    id: 'wrapping-up',
    priority: 100,
    condition: v => isWrapUp(v.mode),
    text: () => 'wrapping up',
  },
  {
    id: 'dolt',
    priority: 80,
    condition: v => v.dolt === false,
    text: () => 'dolt unavailable',
  },
  {
    id: 'limit',
    priority: 70,
    condition: v => nearCap(v).length > 0,
    text: v =>
      nearCap(v)
        .map(l => `${LIMIT_NAME[l.kind] ?? l.kind} limit ${Math.round(l.usedPercent)}%`)
        .join(' · '),
  },
  {
    id: 'context',
    priority: 60,
    condition: v => {
      const p = v.usage?.contextPercent
      return typeof p === 'number' && p >= v.thresholds.contextAlert
    },
    text: v => `context ${Math.round(v.usage?.contextPercent ?? 0)}%`,
  },
  {
    // the SSO login is expired and cannot refresh, or its cache file is missing; an unread state
    // (no poll yet) and a refreshable one never raise it
    id: 'login-expired',
    priority: 50,
    condition: v => loginNeedsAction(v.assume?.login),
    text: () => 'login expired',
  },
].sort((a, b) => b.priority - a.priority)

// The engine's prefix, "⚠ pgd: ", counted in characters against the width.
export const PREFIX_LENGTH = 7
const SEPARATOR = ' · '

// The true items joined with the separator, added greedily in priority order; the first item that
// does not fit is dropped with every lower one. Empty when none is true or even the first does not
// fit (the row is omitted rather than cut). width is the full host width.
export function fitAlerts(inputs: AlertInputs, width: number): string {
  const room = width - PREFIX_LENGTH
  let row = ''
  // Each live alert contribution is one more item ranked by its priority (larger is higher);
  // the sort is stable, so a built-in item wins a tie.
  const contributed: AlertItem[] = listContributions('alert').map(c => ({
    id: `contrib:${c.id}`,
    priority: c.priority,
    condition: () => true,
    text: () => c.text,
  }))
  const items = [...ALERT_ITEMS, ...contributed].sort((a, b) => b.priority - a.priority)
  for (const item of items) {
    if (!item.condition(inputs)) continue
    const text = item.text(inputs)
    const next = row === '' ? text : `${row}${SEPARATOR}${text}`
    if ([...next].length > room) break
    row = next
  }
  return row
}

// Width captured from a pass-through hook on a site that keeps drawing while the engine has
// collapsed the band (the band hook is not a reliable width source then). Undefined until read.
let lastWidth: number | undefined
// The value last sent to $.ui.status; undefined means cleared. `sent` is false until the first
// call after a (re)load, so that render always sends one and removes any stale row.
let lastRow: string | undefined
let sent = false

export function resetAlertState(): void {
  lastWidth = undefined
  lastRow = undefined
  sent = false
}

export function currentWidth(): number | undefined {
  return lastWidth
}

// The row for the current cached values, or undefined when no width has been read (the engine's
// own ellipsis then applies to the row, so the unfitted row is used).
export function alertRow(): string {
  const options = getOptions()
  if (!options?.enable) return ''
  const inputs: AlertInputs = {
    usage: readCached<Usage>('usage'),
    dolt: options.components.beads ? readCached<boolean>('dolt') : undefined,
    mode: options.components.sessionMode ? readCached('sessionMode') : undefined,
    assume: options.components.assume ? readCached<{ role?: string; login?: SsoLogin }>('assume') : undefined,
    thresholds: options.alerts.thresholds,
    now: Date.now(),
  }
  return fitAlerts(inputs, lastWidth ?? Number.MAX_SAFE_INTEGER)
}

type Next = (e: unknown) => unknown

// A pass-through ui.render hook on PromptHint: it records e.viewport.columns, then sets the status
// row ($.ui.status is called here, inside the callback, and takes no width). It reads cached values
// only and returns next(e) unchanged. The row is set only when it changed, and cleared with undefined when no alert is true.
export function alertHook($: ModApi, e: ModApi, next: Next): unknown {
  const options = getOptions()
  if (options?.enable) {
    const cols = e?.viewport?.columns
    if (typeof cols === 'number' && cols > 0) lastWidth = cols
    // An empty string would still draw the bare prefix; undefined is the documented clear.
    const row = alertRow() || undefined
    if (!sent || row !== lastRow) {
      sent = true
      lastRow = row
      $.ui.status(row)
    }
  }
  return next(e)
}

export function registerAlertHooks(on: ModApi): void {
  on('ui.render', { component: 'PromptHint' }, ($, e, next) =>
    getOptions()?.enable ? timedDraw('alert', () => alertHook($, e, next)) : alertHook($, e, next),
  )
}
