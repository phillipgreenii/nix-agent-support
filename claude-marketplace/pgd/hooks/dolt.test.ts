// Tests of the dolt probe: gating on components.beads, the 30 second interval, the shared store.
import { expect, test } from 'claude-code/testing'
import { ALERT_ITEMS } from './alerts'
import { registerComponents } from './components'
import { DOLT_INTERVAL_MS, probeDolt } from './dolt'
import { setOptions } from './options'
import { initSources, readCached } from './source'
import { fakeClock, stubHost } from './testkit'
import { DEFAULT_OPTIONS, type PgdOptions } from './types'

const withBeads = (beads: boolean): PgdOptions => ({
  ...DEFAULT_OPTIONS,
  components: { ...DEFAULT_OPTIONS.components, beads, sessionMode: false, sessionName: false },
})

function boot(options: PgdOptions, exitCode = 0) {
  const clock = fakeClock()
  const s = stubHost()
  s.host.process.run = async (argv: string[], init: unknown) => {
    s.calls.push('process.run')
    s.calls.push(`argv ${argv.join(' ')} ${JSON.stringify(init)}`)
    return { exitCode }
  }
  setOptions(options)
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  registerComponents(options, s.host)
  return { clock, s }
}

const runs = (calls: string[]) => calls.filter(c => c === 'process.run').length

test('with beads off there is no probe, no timer and no dolt value', async () => {
  const { clock, s } = boot(withBeads(false))
  await clock.advance(120_000)
  expect(runs(s.calls)).toBe(0)
  expect(clock.pending()).toBe(0)
  expect(readCached('dolt')).toBe(undefined)
})

test('with beads on the probe runs at start and at most every 30 seconds', async () => {
  const { clock, s } = boot(withBeads(true))
  await clock.advance(1)
  expect(runs(s.calls)).toBe(1)
  expect(readCached('dolt')).toBe(true)
  await clock.advance(DOLT_INTERVAL_MS - 2)
  expect(runs(s.calls)).toBe(1)
  await clock.advance(2)
  expect(runs(s.calls)).toBe(2)
  await clock.advance(DOLT_INTERVAL_MS)
  expect(runs(s.calls)).toBe(3)
})

test('a non-zero exit, and a rejected run, make dolt unavailable', async () => {
  const bad = boot(withBeads(true), 1)
  await bad.clock.advance(1)
  expect(readCached('dolt')).toBe(false)
  const host = { process: { run: async () => Promise.reject(new Error('timeout')) } }
  expect(await probeDolt(host)).toBe(false)
  const dolt = ALERT_ITEMS.find(i => i.id === 'dolt')!
  expect(dolt.condition({ dolt: false, thresholds: DEFAULT_OPTIONS.alerts.thresholds, now: 0 })).toBe(true)
})

test('the probe passes a timeout to the run', async () => {
  const { clock, s } = boot(withBeads(true))
  await clock.advance(1)
  expect(s.calls.some(c => c.startsWith('argv bd dolt test') && c.includes('timeoutMs'))).toBe(true)
})

test('a fresh shared dolt entry means the probe is not run again', async () => {
  const clock = fakeClock()
  const s = stubHost()
  setOptions(withBeads(true))
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  s.storeData.set('shared:dolt', { value: false, fetchedAt: clock.now() - 1000 })
  registerComponents(withBeads(true), s.host)
  await clock.advance(1)
  expect(runs(s.calls)).toBe(0)
  expect(readCached('dolt')).toBe(false)
})
