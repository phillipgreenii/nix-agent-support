// Tests of the alert row: which items appear, their order, the width fitting, the thresholds, the
// width source (a pass-through hook on PromptHint) and that the alert sources keep running while
// the band is collapsed.
import { expect, test } from 'claude-code/testing'
import {
  ALERT_ITEMS,
  alertHook,
  alertRow,
  fitAlerts,
  registerAlertHooks,
  resetAlertState,
  type AlertInputs,
} from './alerts'
import { startDolt } from './dolt'
import { setOptions } from './options'
import { defineSource, initSources, setValue, setVisible } from './source'
import { fakeClock, settle, stubHost } from './testkit'
import { DEFAULT_OPTIONS } from './types'

const T = DEFAULT_OPTIONS.alerts.thresholds
const base = (over: Partial<AlertInputs> = {}): AlertInputs => ({ thresholds: T, now: 0, ...over })
const limits = (five: number, seven: number) => ({
  limits: [
    { kind: 'five_hour' as const, usedPercent: five },
    { kind: 'seven_day' as const, usedPercent: seven },
  ],
})

test('items are ordered by priority, highest first', () => {
  const p = ALERT_ITEMS.map(i => i.priority)
  expect(p).toEqual([...p].sort((a, b) => b - a))
  expect(ALERT_ITEMS.map(i => i.id)).toEqual(['wrapping-up', 'dolt', 'limit', 'context'])
})

test('only true conditions appear, highest priority first', () => {
  const all = base({ dolt: false, usage: { contextPercent: 91, ...limits(85, 10) } })
  expect(fitAlerts(all, 200)).toBe('dolt unavailable · 5h limit 85% · context 91%')
  expect(fitAlerts(base({ usage: { contextPercent: 91, ...limits(5, 10) } }), 200)).toBe('context 91%')
  expect(fitAlerts(base({ dolt: true, usage: { contextPercent: 10, ...limits(5, 10) } }), 200)).toBe('')
})

test('an absent usage, context or dolt value raises no item and no "no X" text', () => {
  expect(fitAlerts(base(), 200)).toBe('')
  expect(fitAlerts(base({ usage: { limits: [] } }), 200)).toBe('')
  for (const item of ALERT_ITEMS) {
    expect(item.condition(base())).toBe(false)
    const text = item.text(base({ dolt: false, usage: { contextPercent: 99, ...limits(99, 99) } }))
    expect(/^no\b|\bno \w+/.test(text)).toBe(false)
  }
})

test('the limit item fires at or above the threshold on either window, using the option', () => {
  const limit = ALERT_ITEMS.find(i => i.id === 'limit')!
  expect(limit.condition(base({ usage: limits(79, 79) }))).toBe(false)
  expect(limit.condition(base({ usage: limits(80, 0) }))).toBe(true)
  expect(limit.condition(base({ usage: limits(0, 80) }))).toBe(true)
  expect(limit.text(base({ usage: limits(0, 83) }))).toBe('7d limit 83%')
  const strict = { ...T, limit: 95 }
  expect(limit.condition(base({ thresholds: strict, usage: limits(90, 90) }))).toBe(false)
  expect(limit.condition(base({ thresholds: strict, usage: limits(95, 0) }))).toBe(true)
})

test('the context item fires at or above contextAlert and ignores contextWarn', () => {
  const ctx = ALERT_ITEMS.find(i => i.id === 'context')!
  expect(ctx.condition(base({ usage: { limits: [], contextPercent: 79 } }))).toBe(false)
  expect(ctx.condition(base({ usage: { limits: [], contextPercent: 80 } }))).toBe(true)
  expect(ctx.condition(base({ thresholds: { ...T, contextAlert: 90 }, usage: { limits: [], contextPercent: 85 } }))).toBe(false)
  expect(ctx.condition(base({ thresholds: { ...T, contextWarn: 10 }, usage: { limits: [], contextPercent: 70 } }))).toBe(false)
})

test('the row drops the lowest-priority items by width and never cuts text', () => {
  const all = base({ dolt: false, usage: { contextPercent: 91, ...limits(85, 10) } })
  const full = 'dolt unavailable · 5h limit 85% · context 91%'
  const need = 7 + full.length
  expect(fitAlerts(all, need)).toBe(full)
  expect(fitAlerts(all, need - 1)).toBe('dolt unavailable · 5h limit 85%')
  expect(fitAlerts(all, 7 + 'dolt unavailable · 5h limit 85%'.length - 1)).toBe('dolt unavailable')
  // a lower item that would fit alone is still dropped once a higher one did not fit
  expect(fitAlerts(all, 7 + 'dolt unavailable'.length + 3)).toBe('dolt unavailable')
  // not even the first fits: the row is omitted, not cut
  expect(fitAlerts(all, 7 + 'dolt unavailable'.length - 1)).toBe('')
  expect(fitAlerts(all, 5)).toBe('')
})

// The engine's hook registry stub: records (name, selector, fn).
function hooks() {
  const seen: Array<{ name: string; sel: unknown; fn: (...a: unknown[]) => unknown }> = []
  registerAlertHooks((name: string, sel: unknown, fn: (...a: unknown[]) => unknown) => {
    seen.push({ name, sel, fn })
  })
  return seen
}

function statusRecorder() {
  const rows: string[] = []
  return { $: { ui: { status: (r: string) => rows.push(r) } }, rows }
}

test('the width is the viewport columns read by a pass-through hook on PromptHint', () => {
  const [h] = hooks()
  expect(h.name).toBe('ui.render')
  expect((h.sel as { component: string }).component).toBe('PromptHint')
  resetAlertState()
  const clock = fakeClock()
  const s = stubHost()
  setOptions({ ...DEFAULT_OPTIONS, components: { ...DEFAULT_OPTIONS.components, beads: false } })
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  setValue('usage', 1) // no such source yet: a no-op
  const { $, rows } = statusRecorder()
  const e = { viewport: { columns: 7 + 'context 91%'.length } }
  // the hook passes the event through unchanged
  expect(h.fn($, e, (x: unknown) => x)).toBe(e)
  expect(rows).toEqual([]) // nothing true yet
})

function bootDolt(opts = DEFAULT_OPTIONS) {
  const clock = fakeClock()
  const s = stubHost()
  resetAlertState()
  setOptions(opts)
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  return { clock, s }
}

test('the hook sets the status row fitted to the captured width, also after the band collapses', async () => {
  const { clock, s } = bootDolt()
  startDolt(DEFAULT_OPTIONS, s.host)
  // a usage source stand-in: an event-driven source holding the cached usage
  defineSource({ id: 'usage', intervalMs: 0, feeds: ['alert'] })
  setValue('usage', { contextPercent: 91, ...limits(85, 0) })
  await clock.advance(1)
  const [h] = hooks()
  const { $, rows } = statusRecorder()
  const pass = (x: unknown) => x
  const narrow = 7 + '5h limit 85%'.length
  h.fn($, { viewport: { columns: narrow } }, pass)
  expect(rows).toEqual(['5h limit 85%'.length ? 'dolt unavailable'.length <= 5 ? '' : '5h limit 85%' : ''])
  setVisible('band', false)
  h.fn($, { viewport: { columns: 200 } }, pass)
  expect(rows[rows.length - 1]).toBe('5h limit 85% · context 91%')
  // with no width ever read the row is left to the engine's ellipsis: unfitted
  resetAlertState()
  expect(alertRow()).toBe('5h limit 85% · context 91%')
})

test('a draw starts no fetch, process or file read', async () => {
  const { clock, s } = bootDolt()
  defineSource({ id: 'usage', intervalMs: 0, feeds: ['alert'] })
  setValue('usage', { contextPercent: 91, limits: [] })
  await clock.advance(1)
  s.calls.length = 0
  const { $ } = statusRecorder()
  alertHook($, { viewport: { columns: 80 } }, (x: unknown) => x)
  expect(s.calls).toEqual([])
})

test('the alert sources keep running while the band is collapsed', async () => {
  const { clock, s } = bootDolt()
  startDolt(DEFAULT_OPTIONS, s.host)
  setVisible('band', false)
  await clock.advance(1)
  await settle()
  const first = s.calls.filter(c => c === 'process.run').length
  expect(first).toBe(1)
  await clock.advance(31_000)
  expect(s.calls.filter(c => c === 'process.run').length).toBe(2)
})
