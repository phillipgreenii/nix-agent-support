// Tests of component registration, run by `claude plugin test` and by the nix check
// test-pgd-mod (which runs every *.test.ts under the mod directory under node, with a
// shim for the engine's testing kit).
import { expect, test } from 'claude-code/testing'
import { registerComponents } from './components'
import { DEFAULT_OPTIONS, parseOptions, type PgdOptions } from './types'

// A stub host that records every call made through it, at any depth.
function recorder(): { $: unknown; calls: string[] } {
  const calls: string[] = []
  const make = (path: string): unknown =>
    new Proxy(() => undefined, {
      get: (_t, k) => make(`${path}.${String(k)}`),
      apply: () => {
        calls.push(path)
        return undefined
      },
    })
  return { $: make('$'), calls }
}

const allOff = (o: PgdOptions): PgdOptions => ({
  ...o,
  components: Object.fromEntries(
    Object.keys(o.components).map(k => [k, false]),
  ) as unknown as PgdOptions['components'],
})

test('every component off registers nothing but the pgd noun', () => {
  const { $, calls } = recorder()
  registerComponents(allOff(DEFAULT_OPTIONS), $)
  // the toggle slash commands are registered whenever enable is true
  expect(calls).toEqual(['$.engine.create', '$.command.register', '$.command.register', '$.command.register'])
})

test('enable false short-circuits registration even with every component on', () => {
  const { $, calls } = recorder()
  registerComponents({ ...DEFAULT_OPTIONS, enable: false }, $)
  expect(calls).toEqual([])
})

test('parseOptions defaults to every component on, no links, thresholds 60/80/80', () => {
  for (const raw of [undefined, null, '', 'not json', '[]', '{}']) {
    expect(parseOptions(raw)).toEqual(DEFAULT_OPTIONS)
  }
})

test('parseOptions reads rendered values and ignores wrongly typed ones', () => {
  const o = parseOptions(
    JSON.stringify({
      enable: false,
      components: { beads: false, assume: 'no' },
      links: [{ name: 'a', pattern: 'p', url: 'u' }, { name: 1 }],
      alerts: { thresholds: { contextWarn: 50, limit: 'x' } },
    }),
  )
  expect(o.enable).toBe(false)
  expect(o.components.beads).toBe(false)
  expect(o.components.assume).toBe(true)
  expect(o.links).toEqual([{ name: 'a', pattern: 'p', url: 'u' }])
  expect(o.alerts.thresholds).toEqual({ contextWarn: 50, contextAlert: 80, limit: 80 })
})
