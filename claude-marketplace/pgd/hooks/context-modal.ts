import { pgdEnabled, getOptions } from './options'
import { contextLevel } from './band'
import { defineSource, readCached, requestInvalidate, setValue } from './source'
import type { ModalSpec, ModalItem } from './modal'
import type { Usage, Limit } from './usage'

// The Context and usage modal, one item per line: context percent and tokens, last compaction
// (only when the event-driven lastCompaction value exists), the five-hour and seven-day limits
// each with its reset time (a limit only when present, its reset time only when the value has
// one) and cost. Literal colours: yellow and red by the same levels as the band's context label;
// a limit uses the limit threshold.
export interface LastCompaction {
  at: number
}

export function startLastCompaction(): void {
  defineSource<LastCompaction>({ id: 'lastCompaction', intervalMs: 0, feeds: ['modal'] })
}

// Called by the one session.compact hook (gitHead.ts registers it; the engine refuses a second
// one without a matcher) after the compaction ran. setValue redraws when the value changed.
export function recordCompaction(now: () => number = Date.now): void {
  if (!pgdEnabled()) return
  setValue('lastCompaction', { at: now() })
  requestInvalidate()
}

// "2026-11-06 12:03 UTC": a stable form that needs no locale.
export function formatTime(ms: number): string {
  return new Date(ms).toISOString().slice(0, 16).replace('T', ' ') + ' UTC'
}

export function formatTokens(n: number): string {
  return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

function limitItem(l: Limit, label: string, warnAt: number): ModalItem {
  const reset = l.resetsAt ? `, resets ${l.resetsAt}` : ''
  const item: ModalItem = { label, value: `${Math.round(l.usedPercent)}%${reset}` }
  if (l.usedPercent >= warnAt) item.color = 'red'
  return item
}

export function contextSpec(): ModalSpec {
  const t = getOptions()?.alerts.thresholds ?? { contextWarn: 60, contextAlert: 80, limit: 80 }
  const usage = readCached<Usage>('usage')
  const items: ModalItem[] = []
  if (typeof usage?.contextPercent === 'number') {
    const level = contextLevel(usage.contextPercent, t)
    const tokens = typeof usage.contextTokens === 'number' ? `, ${formatTokens(usage.contextTokens)} tokens` : ''
    items.push({
      label: 'context',
      value: `${Math.round(usage.contextPercent)}%${tokens}`,
      ...(level !== 'normal' ? { color: level } : {}),
    })
  } else if (typeof usage?.contextTokens === 'number') {
    items.push({ label: 'context', value: `${formatTokens(usage.contextTokens)} tokens` })
  }
  const compacted = readCached<LastCompaction>('lastCompaction')
  if (compacted) items.push({ label: 'last compaction', value: formatTime(compacted.at) })
  const five = usage?.limits.find(l => l.kind === 'five_hour')
  const seven = usage?.limits.find(l => l.kind === 'seven_day')
  if (five) items.push(limitItem(five, '5h limit', t.limit))
  if (seven) items.push(limitItem(seven, '7d limit', t.limit))
  if (typeof usage?.costUsd === 'number') items.push({ label: 'cost', value: `$${usage.costUsd.toFixed(2)}` })
  return { id: 'context', title: 'Context and usage', items, actions: [] }
}
