// Tests of the pgd noun: put, remove, validation, the focus gate and that the noun is registered
// by registerComponents and carries data only.
import { expect, test } from 'claude-code/testing'
import { registerComponents } from './components'
import { initContributions, listContributions } from './contributions'
import { pgdMethods } from './pgd-noun'
import { fakeClock, stubHost } from './testkit'
import { DEFAULT_OPTIONS, type PgdOptions } from './types'

function fresh(): void {
  const clock = fakeClock()
  initContributions({ now: clock.now, setTimer: clock.setTimer, requestInvalidate: () => undefined })
}

async function rejection(p: Promise<unknown>): Promise<string | undefined> {
  try {
    await p
    return undefined
  } catch (e) {
    return (e as Error).message
  }
}

const noFocus = (): PgdOptions => ({ ...DEFAULT_OPTIONS, components: { ...DEFAULT_OPTIONS.components, focus: false } })

test('put returns a Promise, adds then replaces, and remove removes', async () => {
  fresh()
  const n = pgdMethods(DEFAULT_OPTIONS)
  const p = n.put({ id: 'a', surface: 'alert', priority: 1, text: 'one' })
  expect(p instanceof Promise).toBe(true)
  expect(await p).toBe(undefined)
  await n.put({ id: 'a', surface: 'alert', priority: 2, text: 'two' })
  expect(listContributions('alert').map(c => c.text)).toEqual(['two'])
  const r = n.remove({ id: 'a' })
  expect(r instanceof Promise).toBe(true)
  expect(await r).toBe(undefined)
  expect(listContributions('alert')).toEqual([])
})

test('remove of an unknown id resolves undefined', async () => {
  fresh()
  expect(await pgdMethods(DEFAULT_OPTIONS).remove({ id: 'nope' })).toBe(undefined)
})

test('invalid put arguments reject with an Error naming the bad field', async () => {
  fresh()
  const n = pgdMethods(DEFAULT_OPTIONS)
  const good = { id: 'a', surface: 'alert', priority: 1, text: 't' }
  const cases: Array<[string, Record<string, unknown>]> = [
    ['id', { ...good, id: undefined }],
    ['id', { ...good, id: 5 }],
    ['priority', { ...good, priority: '1' }],
    ['surface', { ...good, surface: 'band' }],
    ['text', { ...good, text: undefined }],
    ['url', { ...good, url: 'https://e.test' }],
    ['ttlMs', { ...good, ttlMs: -1 }],
    ['color', { ...good, color: 3 }],
  ]
  for (const [field, args] of cases) {
    const msg = await rejection(n.put(args as never))
    expect(msg !== undefined && msg.includes(field)).toBe(true)
  }
  expect(listContributions('alert')).toEqual([])
})

test('a focus contribution may carry a url and is held for the Focus pane', async () => {
  fresh()
  await pgdMethods(DEFAULT_OPTIONS).put({ id: 'f', surface: 'focus', priority: 3, text: 'x', url: 'https://e.test' })
  expect(listContributions('focus')).toEqual([
    { id: 'f', surface: 'focus', priority: 3, text: 'x', url: 'https://e.test' },
  ])
})

test('with components.focus off a focus contribution resolves undefined and is not stored', async () => {
  fresh()
  const n = pgdMethods(noFocus())
  expect(await n.put({ id: 'f', surface: 'focus', priority: 3, text: 'x' })).toBe(undefined)
  expect(listContributions('focus')).toEqual([])
  await n.put({ id: 'a', surface: 'alert', priority: 3, text: 'x' })
  expect(listContributions('alert')).toHaveLength(1)
})

test('the noun has no action field: an action property is not stored', async () => {
  fresh()
  await pgdMethods(DEFAULT_OPTIONS).put({ id: 'a', surface: 'alert', priority: 1, text: 't', action: 'x' } as never)
  expect(Object.keys(listContributions('alert')[0]).sort()).toEqual(['id', 'priority', 'surface', 'text'])
})

test('registerComponents registers the noun with put and remove when enable is true', () => {
  const s = stubHost()
  const created = s.nouns
  registerComponents(DEFAULT_OPTIONS, s.host)
  expect(created).toHaveLength(1)
  expect(Object.keys(created[0] as object).sort()).toEqual(['put', 'remove'])
  expect(s.calls).toEqual([])
})

test('the noun is registered even when every component is off, and not when enable is false', () => {
  const created: unknown[] = []
  const host = {
    engine: { create: (m: unknown) => void created.push(m) },
    command: { register: async () => undefined },
  }
  const allOff = {
    ...DEFAULT_OPTIONS,
    components: Object.fromEntries(Object.keys(DEFAULT_OPTIONS.components).map(k => [k, false])),
  } as unknown as PgdOptions
  registerComponents(allOff, host)
  expect(created).toHaveLength(1)
  registerComponents({ ...DEFAULT_OPTIONS, enable: false }, host)
  expect(created).toHaveLength(1)
})
