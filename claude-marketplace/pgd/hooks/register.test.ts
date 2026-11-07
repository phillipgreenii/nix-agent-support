// Tests of the mod's registration: the hooks it registers at registration time.
import { expect, test } from 'claude-code/testing'
import { register } from './register'
import { listContributions } from './contributions'
import { setOptions } from './options'

test('registering the mod registers the band and alert ui.render hooks and session.start', () => {
  const seen: Array<{ name: string; component?: string }> = []
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(register as any)((name: string, sel: unknown) => {
    seen.push({ name, component: (sel as { component?: string } | undefined)?.component })
  })
  const renders = seen.filter(s => s.name === 'ui.render').map(s => s.component)
  expect(renders).toContain('AbovePrompt')
  expect(renders).toContain('PromptHint')
  expect(renders).toContain('AssistantMessage')
  expect(seen.map(s => s.name)).toContain('session.start')
})

test('an engine.create hook adds the pgd noun to $ and its put stores a contribution once enabled', async () => {
  const hooks: Array<{ name: string; fn: (...a: unknown[]) => unknown }> = []
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(register as any)((name: string, ...rest: unknown[]) => {
    hooks.push({ name, fn: rest[rest.length - 1] as (...a: unknown[]) => unknown })
  })
  const create = hooks.find(h => h.name === 'engine.create')
  expect(create !== undefined).toBe(true)
  // the fold: the steps beneath return `$` as built so far; this hook adds its noun
  const built = (await create!.fn({}, {}, async () => ({ other: 1 }))) as {
    other: number
    pgd: { put: (a: unknown) => Promise<void>; remove: (a: unknown) => Promise<void> }
  }
  expect(built.other).toBe(1)
  expect(Object.keys(built.pgd).sort()).toEqual(['put', 'remove'])
  // before session.start parses the options the noun is inert
  setOptions(undefined as never)
  await built.pgd.put({ id: 'r', surface: 'alert', priority: 1, text: 'early' })
  expect(listContributions('alert')).toEqual([])
  // session.start wires the store and the options; the noun then stores
  const start = hooks.find(h => h.name === 'session.start')!
  const make = (path: string): unknown =>
    new Proxy(() => undefined, {
      get: (_t, k) => make(`${path}.${String(k)}`),
      apply: () => Promise.resolve(undefined),
    })
  await start.fn(make('$'), {}, async () => undefined)
  await built.pgd.put({ id: 'r', surface: 'alert', priority: 1, text: 'wired' })
  expect(listContributions('alert').map(c => c.text)).toEqual(['wired'])
})
