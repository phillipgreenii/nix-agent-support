// Tests of the shared-source option of defineSource and of createSharedStore, against a stub
// plugin store (the facade's store closures) under a fake clock.
import { expect, test } from 'claude-code/testing'
import { defineSource, initSources, readCached } from './source'
import { createSharedStore } from './sharedStore'
import { fakeClock, stubHost } from './testkit'

function setup() {
  const clock = fakeClock()
  const stub = stubHost()
  initSources(stub.host, { now: clock.now, setTimer: clock.setTimer })
  return { clock, stub }
}

test('a fresh shared entry is used and starts no fetch', async () => {
  const { clock, stub } = setup()
  stub.storeData.set('shared:s', { value: 7, fetchedAt: clock.now() - 100 })
  let n = 0
  defineSource<number>({
    id: 's',
    intervalMs: 1000,
    shared: true,
    feeds: ['band'],
    fetch: async () => ++n,
  })
  await clock.advance(0)
  expect(n).toBe(0)
  expect(readCached('s')).toBe(7)
  expect(stub.calls).toContain('ui.invalidate')
})

test('a stale entry is fetched once and written back as { value, fetchedAt }', async () => {
  const { clock, stub } = setup()
  stub.storeData.set('shared:s', { value: 7, fetchedAt: clock.now() - 5000 })
  let n = 0
  defineSource<number>({
    id: 's',
    intervalMs: 1000,
    shared: true,
    feeds: ['band'],
    fetch: async () => ++n + 10,
  })
  await clock.advance(0)
  expect(n).toBe(1)
  expect(readCached('s')).toBe(11)
  expect(stub.storeData.get('shared:s')).toEqual({ value: 11, fetchedAt: clock.now() })
  expect(stub.calls.filter(c => c === 'store.set shared:s')).toHaveLength(1)
})

test('a missing entry is fetched and written; a failed fetch writes nothing', async () => {
  const { clock, stub } = setup()
  defineSource<number>({
    id: 'bad',
    intervalMs: 1000,
    shared: true,
    feeds: ['band'],
    fetch: async () => {
      throw new Error('boom')
    },
  })
  await clock.advance(0)
  expect(stub.storeData.has('shared:bad')).toBe(false)
  defineSource<number>({ id: 'ok', intervalMs: 1000, shared: true, feeds: ['band'], fetch: async () => 3 })
  await clock.advance(0)
  expect(stub.storeData.get('shared:ok')).toEqual({ value: 3, fetchedAt: clock.now() })
})

test('keyed sources do not read each other and an undefined key is neither fetched nor shared', async () => {
  const { clock, stub } = setup()
  stub.storeData.set('shared:pr:a', { value: 'A', fetchedAt: clock.now() })
  let nB = 0
  defineSource<string>({
    id: 'pr',
    intervalMs: 1000,
    shared: { key: () => 'b' },
    feeds: ['band'],
    fetch: async () => {
      nB += 1
      return 'B'
    },
  })
  await clock.advance(0)
  expect(nB).toBe(1)
  expect(readCached('pr')).toBe('B')
  expect(stub.storeData.get('shared:pr:b')).toEqual({ value: 'B', fetchedAt: clock.now() })
  expect(stub.storeData.get('shared:pr:a')).toEqual({ value: 'A', fetchedAt: clock.now() })

  const u = setup()
  let nU = 0
  defineSource<string>({
    id: 'none',
    intervalMs: 1000,
    shared: { key: () => undefined },
    feeds: ['band'],
    fetch: async () => {
      nU += 1
      return 'x'
    },
  })
  await u.clock.advance(0)
  expect(nU).toBe(0)
  expect(readCached('none')).toBe(undefined)
  expect(u.stub.calls.filter(c => c.startsWith('store.'))).toHaveLength(0)
})

test('a throwing store makes the call unshared with no file read or written', async () => {
  const { clock, stub } = setup()
  stub.storeFail.on = true
  let n = 0
  defineSource<number>({
    id: 's',
    intervalMs: 1000,
    shared: true,
    feeds: ['band'],
    fetch: async () => ++n,
  })
  await clock.advance(0)
  expect(n).toBe(1)
  expect(readCached('s')).toBe(1)
  expect(stub.calls.filter(c => c.startsWith('fs.'))).toHaveLength(0)
  // the store is tried again on the next call
  stub.storeFail.on = false
  await clock.advance(1100)
  expect(n).toBe(2)
  expect(stub.storeData.get('shared:s')).toEqual({ value: 2, fetchedAt: clock.now() - 100 })
})

test('a source without shared never touches the store', async () => {
  const { clock, stub } = setup()
  defineSource<number>({ id: 's', intervalMs: 1000, feeds: ['band'], fetch: async () => 1 })
  await clock.advance(0)
  expect(readCached('s')).toBe(1)
  expect(stub.calls.filter(c => c.startsWith('store.'))).toHaveLength(0)
})

test('createSharedStore reads malformed entries as absent and swallows write errors', async () => {
  const bad = createSharedStore({
    store: {
      get: () => ({ nope: 1 }),
      set: () => {
        throw new Error('x')
      },
    },
    now: () => 5,
  })
  expect(await bad.read('shared:a')).toBe(undefined)
  await bad.write('shared:a', 1)
  const none = createSharedStore({ store: undefined, now: () => 5 })
  expect(await none.read('shared:a')).toBe(undefined)
})
