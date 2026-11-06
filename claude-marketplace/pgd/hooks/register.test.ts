// Tests of the mod's registration: the hooks it registers at registration time.
import { expect, test } from 'claude-code/testing'
import { register } from './register'
import { listContributions } from './contributions'

test('registering the mod registers the band and alert ui.render hooks and session.start', () => {
  const seen: Array<{ name: string; component?: string }> = []
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(register as any)((name: string, sel: unknown) => {
    seen.push({ name, component: (sel as { component?: string } | undefined)?.component })
  })
  const renders = seen.filter(s => s.name === 'ui.render').map(s => s.component)
  expect(renders).toContain('AbovePrompt')
  expect(renders).toContain('PromptHint')
  expect(seen.map(s => s.name)).toContain('session.start')
})

test('session.start registers the pgd noun and wires the contribution store', async () => {
  const handlers: Array<(...a: unknown[]) => unknown> = []
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(register as any)((name: string, ...rest: unknown[]) => {
    if (name === 'session.start') handlers.push(rest[rest.length - 1] as (...a: unknown[]) => unknown)
  })
  const created: Array<Record<string, (a: unknown) => Promise<void>>> = []
  const make = (path: string): unknown =>
    new Proxy(() => undefined, {
      get: (_t, k) => (path === '$.engine' && k === 'create' ? (m: never) => void created.push(m) : make(`${path}.${String(k)}`)),
      apply: () => Promise.resolve(undefined),
    })
  await handlers[0](make('$'), {}, async () => undefined)
  expect(created).toHaveLength(1)
  await created[0].put({ id: 'r', surface: 'alert', priority: 1, text: 'wired' })
  expect(listContributions('alert').map(c => c.text)).toEqual(['wired'])
})
