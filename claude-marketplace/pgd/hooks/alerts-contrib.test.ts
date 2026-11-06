// Tests of the join of contributed alerts into the fitted alert row.
import { expect, test } from 'claude-code/testing'
import { fitAlerts, type AlertInputs } from './alerts'
import { initContributions, putContribution, removeContribution } from './contributions'
import { fakeClock } from './testkit'
import { DEFAULT_OPTIONS } from './types'

const base = (over: Partial<AlertInputs> = {}): AlertInputs => ({
  thresholds: DEFAULT_OPTIONS.alerts.thresholds,
  now: 0,
  ...over,
})

function fresh() {
  const clock = fakeClock()
  initContributions({ now: clock.now, setTimer: clock.setTimer, requestInvalidate: () => undefined })
  return clock
}
const a = (id: string, priority: number, text: string, extra: object = {}) =>
  putContribution({ id, surface: 'alert', priority, text, ...extra })

test('an alert contribution alone appears in the row', () => {
  fresh()
  a('x', 5, 'hello')
  expect(fitAlerts(base(), 200)).toBe('hello')
})

test('contributions rank with the built-in items by priority, larger first', () => {
  fresh()
  a('top', 90, 'top thing')
  a('mid', 65, 'mid thing')
  a('low', 10, 'low thing')
  const v = base({ dolt: false, usage: { contextPercent: 91, limits: [{ kind: 'five_hour', usedPercent: 85 }] } })
  expect(fitAlerts(v, 400)).toBe(
    'top thing · dolt unavailable · 5h limit 85% · mid thing · context 91% · low thing',
  )
})

test('a contribution that does not fit is dropped with every lower one', () => {
  fresh()
  a('x', 90, 'aaaa')
  a('y', 80, 'bbbb')
  a('z', 70, 'cccc')
  expect(fitAlerts(base(), 7 + 4 + 3 + 4)).toBe('aaaa · bbbb')
})

test('an expired or removed contribution leaves the row unchanged otherwise', async () => {
  const clock = fresh()
  a('t', 90, 'temp', { ttlMs: 100 })
  a('k', 10, 'kept')
  expect(fitAlerts(base(), 200)).toBe('temp · kept')
  await clock.advance(100)
  expect(fitAlerts(base(), 200)).toBe('kept')
  removeContribution('k')
  expect(fitAlerts(base(), 200)).toBe('')
})

test('a focus contribution never joins the alert row', () => {
  fresh()
  putContribution({ id: 'f', surface: 'focus', priority: 99, text: 'pane item' })
  expect(fitAlerts(base(), 200)).toBe('')
})
