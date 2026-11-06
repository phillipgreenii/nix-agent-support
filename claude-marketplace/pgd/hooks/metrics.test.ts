import { expect, test } from 'claude-code/testing'
import { record, resetMetrics, setMetricsClock, snapshot, timedDraw } from './metrics'
import { alertHook, registerAlertHooks } from './alerts'
import { defineSource, initSources, readCached, requestInvalidate, setValue } from './source'
import { DEFAULT_OPTIONS } from './types'
import { setOptions } from './options'
import { fakeClock, settle, stubHost } from './testkit'

const find = (name: string, labels: Record<string, string> = {}) =>
  snapshot().find(s => s.name === name && JSON.stringify(s.labels) === JSON.stringify(labels))

function setup() {
  resetMetrics()
  const clock = fakeClock()
  const stub = stubHost()
  initSources(stub.host, { now: clock.now, setTimer: clock.setTimer })
  return { clock, stub }
}

test('counters add and histograms keep a running sum and count', () => {
  resetMetrics()
  record('pgd_invalidate_total', 1)
  record('pgd_invalidate_total', 1)
  record('pgd_fetch_duration_seconds', 0.5, { source: 'a' })
  record('pgd_fetch_duration_seconds', 1.5, { source: 'a' })
  expect(find('pgd_invalidate_total')?.value).toBe(2)
  expect(find('pgd_fetch_duration_seconds_sum', { source: 'a' })?.value).toBe(2)
  expect(find('pgd_fetch_duration_seconds_count', { source: 'a' })?.value).toBe(2)
})

test('a fetch records count and duration; a failure records the error kind', async () => {
  const { clock } = setup()
  let t = 10
  setMetricsClock(() => t)
  defineSource<number>({
    id: 'ok',
    intervalMs: 1000,
    feeds: ['band'],
    fetch: async () => {
      t += 0.25
      return 1
    },
  })
  defineSource<number>({
    id: 'bad',
    intervalMs: 1000,
    feeds: ['band'],
    fetch: async () => {
      throw new SyntaxError('bad json')
    },
  })
  await clock.advance(0)
  expect(find('pgd_fetch_total', { source: 'ok' })?.value).toBe(1)
  expect(find('pgd_fetch_duration_seconds_sum', { source: 'ok' })?.value).toBe(0.25)
  expect(find('pgd_fetch_total', { source: 'bad' })?.value).toBe(1)
  expect(find('pgd_fetch_errors_total', { source: 'bad', kind: 'parse' })?.value).toBe(1)
  expect(find('pgd_cache_misses_total', { source: 'ok' })?.value).toBe(1)
})

test('error kinds: timeout, exit, parse and other', async () => {
  const { clock } = setup()
  const errs: Record<string, unknown> = {
    t: new Error('request timed out'),
    x: Object.assign(new Error('failed'), { exitCode: 2 }),
    o: new Error('boom'),
  }
  for (const [id, err] of Object.entries(errs)) {
    defineSource<number>({
      id,
      intervalMs: 1000,
      feeds: ['band'],
      fetch: async () => {
        throw err
      },
    })
  }
  await clock.advance(0)
  expect(find('pgd_fetch_errors_total', { source: 't', kind: 'timeout' })?.value).toBe(1)
  expect(find('pgd_fetch_errors_total', { source: 'x', kind: 'exit' })?.value).toBe(1)
  expect(find('pgd_fetch_errors_total', { source: 'o', kind: 'other' })?.value).toBe(1)
})

test('a read of the cache records a hit or a miss per source', async () => {
  const { clock } = setup()
  defineSource<number>({ id: 'ev', intervalMs: 0 })
  expect(readCached('ev')).toBe(undefined)
  expect(find('pgd_cache_misses_total', { source: 'ev' })?.value).toBe(1)
  setValue('ev', 5)
  expect(readCached('ev')).toBe(5)
  expect(find('pgd_cache_hits_total', { source: 'ev' })?.value).toBe(1)
  await clock.advance(0)
})

test('invalidate: one fired call, further requests in the same turn are coalesced', async () => {
  setup()
  resetMetrics()
  requestInvalidate()
  requestInvalidate()
  requestInvalidate()
  await settle()
  expect(find('pgd_invalidate_total')?.value).toBe(1)
  expect(find('pgd_invalidate_coalesced_total')?.value).toBe(2)
})

test('timedDraw records count and duration per surface for sync and async draws', async () => {
  resetMetrics()
  let t = 0
  setMetricsClock(() => t)
  expect(timedDraw('band', () => ((t += 0.5), 'x'))).toBe('x')
  await timedDraw('alert', async () => {
    t += 0.25
  })
  expect(find('pgd_draw_total', { surface: 'band' })?.value).toBe(1)
  expect(find('pgd_draw_duration_seconds_sum', { surface: 'band' })?.value).toBe(0.5)
  expect(find('pgd_draw_total', { surface: 'alert' })?.value).toBe(1)
  expect(find('pgd_draw_duration_seconds_sum', { surface: 'alert' })?.value).toBe(0.25)
})

test('the registered alert hook times its draw, and a draw starts no fetch, process or file read', async () => {
  const { stub } = setup()
  setOptions({ ...DEFAULT_OPTIONS })
  let registered: ((...a: unknown[]) => unknown) | undefined
  registerAlertHooks((_ev: string, _sel: unknown, fn: (...a: unknown[]) => unknown) => {
    registered = fn
  })
  const before = stub.calls.length
  const status: string[] = []
  registered!({ ui: { status: (r: string) => status.push(r) } }, { viewport: { columns: 80 } }, (e: unknown) => e)
  expect(find('pgd_draw_total', { surface: 'alert' })?.value).toBe(1)
  expect(stub.calls.length).toBe(before)
  expect(typeof alertHook).toBe('function')
})
