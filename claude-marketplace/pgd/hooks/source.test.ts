// Tests of the source layer (source.ts) under a fake clock: cache-aside TTL, one fetch in flight,
// failure backoff, change-compared and coalesced invalidation, the idle rule and visibility.
import { expect, test } from 'claude-code/testing'
import {
  defineSource,
  initSources,
  noteEvent,
  readCached,
  refresh,
  setValue,
  setVisible,
} from './source'
import { fakeClock, settle, stubHost } from './testkit'

function setup() {
  const clock = fakeClock()
  const stub = stubHost()
  initSources(stub.host, { now: clock.now, setTimer: clock.setTimer })
  return { clock, stub }
}

test('a TTL hit serves the cache and a TTL miss fetches once more', async () => {
  const { clock } = setup()
  let n = 0
  defineSource<number>({
    id: 's',
    intervalMs: 1000,
    feeds: ['band'],
    fetch: async () => ++n,
  })
  await clock.advance(0)
  expect(n).toBe(1)
  expect(readCached('s')).toBe(1)
  await clock.advance(500)
  expect(n).toBe(1) // inside the interval: a hit
  expect(readCached('s')).toBe(1)
  await clock.advance(600)
  expect(n).toBe(2) // past the interval: a miss
  expect(readCached('s')).toBe(2)
})

test('readCached is undefined before the first fetch and for an unknown id', async () => {
  setup()
  defineSource<number>({ id: 'x', intervalMs: 1000, feeds: ['modal'], fetch: async () => 1 })
  expect(readCached('x')).toBe(undefined)
  expect(readCached('nope')).toBe(undefined)
})

test('exactly one fetch is in flight per source', async () => {
  const { clock } = setup()
  let calls = 0
  let release: (v: number) => void = () => {}
  defineSource<number>({
    id: 's',
    intervalMs: 100,
    feeds: ['band'],
    fetch: () => {
      calls += 1
      return new Promise<number>(r => {
        release = r
      })
    },
  })
  await clock.advance(5000)
  refresh('s')
  await settle()
  expect(calls).toBe(1)
  release(7)
  await settle()
  expect(readCached('s')).toBe(7)
  await clock.advance(150)
  expect(calls).toBe(2)
})

test('a failed fetch backs off and does not spin', async () => {
  const { clock } = setup()
  const at: number[] = []
  defineSource<number>({
    id: 's',
    intervalMs: 1000,
    feeds: ['band'],
    fetch: async () => {
      at.push(clock.now())
      throw new Error('down')
    },
  })
  await clock.advance(60_000)
  expect(at.length > 1).toBe(true)
  expect(at.length <= 8).toBe(true) // 60 attempts without a backoff
  for (let i = 1; i < at.length; i++) {
    expect(at[i] - at[i - 1] > 1000).toBe(true) // strictly greater than the interval
  }
  expect(readCached('s')).toBe(undefined)
})

test('a success after failures returns to the plain interval', async () => {
  const { clock } = setup()
  let fail = true
  let n = 0
  defineSource<number>({
    id: 's',
    intervalMs: 1000,
    feeds: ['band'],
    fetch: async () => {
      n += 1
      if (fail) throw new Error('down')
      return n
    },
  })
  await clock.advance(5000)
  fail = false
  await clock.advance(10_000)
  const before = n
  await clock.advance(10_000)
  expect(n - before).toBe(10)
})

test('a redraw is requested only when a cached value changed by value', async () => {
  const { clock, stub } = setup()
  defineSource<{ a: number }>({ id: 's', intervalMs: 0, feeds: ['band'] })
  setValue('s', { a: 1 })
  await clock.advance(0)
  expect(stub.calls.filter(c => c === 'ui.invalidate')).toHaveLength(1)
  setValue('s', { a: 1 }) // JSON-equal: no redraw
  await clock.advance(0)
  expect(stub.calls.filter(c => c === 'ui.invalidate')).toHaveLength(1)
  setValue('s', { a: 2 })
  await clock.advance(0)
  expect(stub.calls.filter(c => c === 'ui.invalidate')).toHaveLength(2)
})

test('several changes in one event turn produce one invalidate', async () => {
  const { clock, stub } = setup()
  defineSource<number>({ id: 'a', intervalMs: 0 })
  defineSource<number>({ id: 'b', intervalMs: 0 })
  setValue('a', 1)
  setValue('b', 2)
  setValue('a', 3)
  await clock.advance(0)
  expect(stub.calls.filter(c => c === 'ui.invalidate')).toHaveLength(1)
})

test('setValue for a source that was never started is a no-op', async () => {
  const { clock, stub } = setup()
  setValue('ghost', 1)
  await clock.advance(0)
  expect(readCached('ghost')).toBe(undefined)
  expect(stub.calls).toEqual([])
})

test('a draw (readCached) starts no fetch, process or file read', async () => {
  const { clock, stub } = setup()
  defineSource<number>({ id: 's', intervalMs: 0, fetch: async () => 1 })
  setValue('s', 5)
  await clock.advance(0)
  stub.calls.length = 0
  for (const id of ['s', 'missing', 'usage', 'model', 'effort', 'gitHead']) readCached(id)
  expect(stub.calls).toEqual([])
})

test('the idle rule multiplies every interval by four until the next event', async () => {
  const { clock } = setup()
  let n = 0
  defineSource<number>({ id: 's', intervalMs: 1000, feeds: ['band'], fetch: async () => ++n })
  await clock.advance(5 * 60 * 1000 + 1000) // five minutes without an event
  const before = n
  await clock.advance(40_000)
  const idleFetches = n - before
  expect(idleFetches >= 9 && idleFetches <= 11).toBe(true) // 40 s / 4 s, not 40 s / 1 s
  noteEvent()
  const mid = n
  await clock.advance(10_000)
  expect(n - mid >= 9).toBe(true) // back to the plain 1 s interval
})

test('a source feeding only an undrawn surface drives no timer', async () => {
  const { clock } = setup()
  const n = { modal: 0, alert: 0, none: 0, band: 0 }
  defineSource<number>({ id: 'm', intervalMs: 1000, feeds: ['modal'], fetch: async () => ++n.modal })
  defineSource<number>({ id: 'a', intervalMs: 1000, feeds: ['alert'], fetch: async () => ++n.alert })
  defineSource<number>({ id: 'n', intervalMs: 1000, fetch: async () => ++n.none })
  defineSource<number>({ id: 'b', intervalMs: 1000, feeds: ['band'], fetch: async () => ++n.band })
  await clock.advance(5000)
  expect(n.modal).toBe(0)
  expect(n.none).toBe(0)
  expect(n.alert > 0 && n.band > 0).toBe(true)
  setVisible('modal', true)
  await clock.advance(5000)
  expect(n.modal > 0).toBe(true)
  setVisible('band', false)
  setVisible('modal', false)
  const b = n.band
  const m = n.modal
  await clock.advance(5000)
  expect(n.band).toBe(b) // collapsed band: its timer stops
  expect(n.modal).toBe(m)
  expect(n.alert > 5).toBe(true) // the alert source survives a collapse
})
