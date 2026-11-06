import { getOptions } from './options'
import { readCached } from './source'
import type { AlertThresholds, ModApi } from './types'
import type { Usage } from './usage'

// The phase 1 band: plain labels (no buttons yet) for the session id, model and effort, and the
// context percent, the last coloured by level. renderBand is a plain data description, so a later
// phase can turn each label into a button without changing its text. The band hook reads cached
// values only (readCached): it never starts a fetch, a process or a file read.
export type Level = 'normal' | 'yellow' | 'red'

export interface BandRow {
  id: string
  label: string
  level?: Level
}

export interface BandValues {
  sessionId: string
  usage?: Usage
  model?: string
  effort?: string
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
  if (v.sessionId) labels.push({ id: 'session', label: v.sessionId.slice(0, 8) })
  if (v.model) {
    labels.push({ id: 'model', label: v.effort ? `${v.model} · ${v.effort}` : v.model })
  }
  const pct = v.usage?.contextPercent
  if (typeof pct === 'number') {
    labels.push({ id: 'context', label: `ctx ${Math.round(pct)}%`, level: contextLevel(pct, t) })
  }
  return labels
}

// Labels run left to right, one space apart, and wrap onto a new row when the next label would
// pass the width; a label wider than the width gets a row of its own.
export function renderBand(v: BandValues, width: number, t: AlertThresholds): BandRow[][] {
  const rows: BandRow[][] = []
  let used = 0
  for (const l of bandLabels(v, t)) {
    const cur = rows[rows.length - 1]
    if (cur && used + 1 + l.label.length <= width) {
      cur.push(l)
      used += 1 + l.label.length
    } else {
      rows.push([l])
      used = l.label.length
    }
  }
  return rows
}

type Next = (e: unknown) => unknown

// The AbovePrompt render hook. It awaits next(e) first and nests that answer above its own rows,
// so another plugin's band survives; with nothing to draw it returns that answer untouched.
export async function bandHook($: ModApi, e: ModApi, next: Next): Promise<unknown> {
  const inner = await next(e)
  const options = getOptions()
  if (!options?.enable || e?.props?.hasSurvey) return inner
  const model = readCached<string>('model')
  const rows = renderBand(
    {
      sessionId: String((await $.session.id()) ?? ''),
      usage: readCached<Usage>('usage'),
      model: options.components.modelEffort ? model : undefined,
      effort: options.components.modelEffort ? readCached<string>('effort') : undefined,
    },
    e?.props?.bodyColumns ?? e?.viewport?.columns ?? 80,
    options.alerts.thresholds,
  )
  if (rows.length === 0) return inner
  const { Box, Text } = $.ui.resolve(e)
  const own = rows.map((row, i) =>
    Box({
      key: `pgd-band-${i}`,
      flexDirection: 'row',
      children: row.map((l, j) =>
        Text({
          key: l.id,
          children: j === 0 ? l.label : ` ${l.label}`,
          ...(l.level && l.level !== 'normal' ? { color: l.level } : {}),
        }),
      ),
    }),
  )
  const children = inner === undefined || inner === null ? own : [inner, ...own]
  return Box({ flexDirection: 'column', children })
}

export function registerBandHooks(on: ModApi): void {
  on('ui.render', { component: 'AbovePrompt' }, bandHook)
}
