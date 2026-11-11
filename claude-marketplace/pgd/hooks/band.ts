import { getOptions } from './options'
import { readCached } from './source'
import type { AlertThresholds, ModApi } from './types'
import type { Usage } from './usage'
import { timedDraw } from './metrics'
import { isOpen, toggle } from './toggles'
import { markBandDrawn } from './modal'
import type { AssumeValue } from './assume'

// The phase 1 band: plain labels (no buttons yet) for the session id, model and effort, and the
// context percent, the last coloured by level. renderBand is a plain data description, so a later
// phase can turn each label into a button without changing its text. The band hook reads cached
// values only (readCached): it never starts a fetch, a process or a file read.
export type Level = 'normal' | 'yellow' | 'red' | 'green' | 'magenta' | 'dim'

export interface BandRow {
  id: string
  label: string
  level?: Level
  // The id of the modal (or toggle) a press on this label opens.
  action?: string
}

// Buttons other components add after the three labels, sorted by order (mode 40, assume 50, focus
// 60, wrap up 70). label returns undefined when the button is not drawn.
export interface BandButton {
  id: string
  order: number
  label: (v: BandValues) => string | undefined
  action?: string
  // The button's colour level, from the same cached values; undefined draws it plain.
  level?: (v: BandValues) => Level | undefined
}

let buttons: BandButton[] = []

export function registerBandButton(b: BandButton): void {
  buttons = [...buttons.filter(x => x.id !== b.id), b].sort((x, y) => x.order - y.order)
}

export function clearBandButtons(): void {
  buttons = []
  directActions.clear()
}

// A press on a band button toggles its modal: the first press opens it, a second press on an open
// modal closes it (the band toggle behaves the same way).
export function pressButton(action: string): void {
  const direct = directActions.get(action)
  if (direct) {
    direct()
    return
  }
  toggle(action)
}

// An action that opens no modal: pressing it runs the handler instead of toggling a modal.
const directActions = new Map<string, () => void>()

export function registerDirectAction(action: string, run: () => void): void {
  directActions.set(action, run)
}

export interface BandValues {
  sessionId: string
  // The session name (registry entry), when one exists; the first button shows it in place of the id.
  sessionName?: string
  usage?: Usage
  model?: string
  effort?: string
  // The AWS profile the session runs with (the assume source's role); unset or empty is undefined.
  assumeRole?: string
}

// A level boundary value belongs to the higher level.
export function contextLevel(percent: number, t: AlertThresholds): Level {
  if (percent >= t.contextAlert) return 'red'
  if (percent >= t.contextWarn) return 'yellow'
  return 'normal'
}

// An item whose condition is false is not drawn: no placeholder, never a "no X" row.
export function bandLabels(v: BandValues, t: AlertThresholds): BandRow[] {
  const labels: BandRow[] = []
  if (v.sessionId) labels.push({ id: 'session', label: v.sessionName || v.sessionId.slice(0, 8), action: 'info' })
  if (v.model) {
    // a button only while components.modelEffort is on (the band hook also leaves the model out when off)
    labels.push({
      id: 'model',
      label: v.effort ? `${v.model} · ${v.effort}` : v.model,
      ...(getOptions()?.components.modelEffort ? { action: 'model' } : {}),
    })
  }
  const pct = v.usage?.contextPercent
  if (typeof pct === 'number') {
    labels.push({ id: 'context', label: `ctx ${Math.round(pct)}%`, level: contextLevel(pct, t), action: 'context' })
  }
  for (const b of buttons) {
    const label = b.label(v)
    if (!label) continue
    const level = b.level?.(v)
    labels.push({ id: b.id, label, ...(level ? { level } : {}), ...(b.action ? { action: b.action } : {}) })
  }
  return labels
}

// A button draws its label inside "[ " and " ]": four extra columns the label text does not carry.
export const BUTTON_CHROME = 4
// The engine's own [-] control sits at the right edge, drawn over the row: one gap plus three
// columns. Every row keeps that much free so the control never covers a label.
export const ENGINE_RESERVE = 4

// The columns a label occupies once drawn.
export function drawnWidth(l: BandRow): number {
  return l.label.length + (l.action ? BUTTON_CHROME : 0)
}

// Labels run left to right, one space apart, and wrap onto a new row (whole, never cut) when the
// next label would pass the width minus the engine control's reserve; a label wider than that gets
// a row of its own.
export function renderBand(v: BandValues, width: number, t: AlertThresholds): BandRow[][] {
  const avail = width - ENGINE_RESERVE
  const rows: BandRow[][] = []
  let used = 0
  for (const l of bandLabels(v, t)) {
    const w = drawnWidth(l)
    const cur = rows[rows.length - 1]
    if (cur && used + 1 + w <= avail) {
      cur.push(l)
      used += 1 + w
    } else {
      rows.push([l])
      used = w
    }
  }
  return rows
}

// yellow, red and green are Text colours; dim is the Text dim property (dimColor); normal is plain.
function textStyle(level: Level | undefined): Record<string, unknown> {
  if (level === 'dim') return { dimColor: true }
  return level && level !== 'normal' ? { color: level } : {}
}

type Next =(e: unknown) => unknown

// The AbovePrompt render hook. It awaits next(e) first and nests that answer above its own rows,
// so another plugin's band survives; with nothing to draw it returns that answer untouched.
export async function bandHook($: ModApi, e: ModApi, next: Next): Promise<unknown> {
  const inner = await next(e)
  const options = getOptions()
  if (!options?.enable || e?.props?.hasSurvey) return inner
  // The engine's own collapse cannot be observed; the plugin's toggle state is. A hidden band
  // answers next(e) untouched and starts nothing.
  if (!isOpen('band')) return inner
  markBandDrawn()
  const model = readCached<string>('model')
  const rows = renderBand(
    {
      sessionId: String((await $.session.id()) ?? ''),
      sessionName: readCached<string>('sessionName'),
      usage: readCached<Usage>('usage'),
      model: options.components.modelEffort ? model : undefined,
      effort: options.components.modelEffort ? readCached<string>('effort') : undefined,
      assumeRole: options.components.assume ? readCached<AssumeValue>('assume')?.role : undefined,
    },
    e?.props?.bodyColumns ?? e?.viewport?.columns ?? 80,
    options.alerts.thresholds,
  )
  if (rows.length === 0) return inner
  const { Box, Text, Button } = $.ui.resolve(e)
  const own = rows.map((row, i) =>
    Box({
      key: `pgd-band-${i}`,
      flexDirection: 'row',
      flexShrink: 0,
      children: row.flatMap((l, j) => {
        const text = textStyle(l.level)
        const item = l.action
          ? Button({
              key: l.id,
              onPress: () => pressButton(l.action as string),
              children: Text({ key: `${l.id}-text`, children: l.label, ...text }),
            })
          : Text({ key: l.id, children: l.label, ...text })
        return j === 0 ? [item] : [Text({ key: `${l.id}-gap`, children: ' ' }), item]
      }),
    }),
  )
  const children = inner === undefined || inner === null ? own : [inner, ...own]
  return Box({ flexDirection: 'column', children })
}

export function registerBandHooks(on: ModApi): void {
  on('ui.render', { component: 'AbovePrompt' }, ($, e, next) =>
    getOptions()?.enable ? timedDraw('band', () => bandHook($, e, next)) : bandHook($, e, next),
  )
}
