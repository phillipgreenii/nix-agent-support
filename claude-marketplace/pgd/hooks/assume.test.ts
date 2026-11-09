// Tests of the Assume button's display slice: label and colour from the session's AWS_PROFILE,
// read once at session start through the facade; the band's green and dim levels; the opt-out.
import { expect, test } from 'claude-code/testing'
import { bandHook, bandLabels, clearBandButtons } from './band'
import { registerComponents } from './components'
import { assumeLevel } from './assume'
import { setOptions } from './options'
import { initSources, readCached } from './source'
import { settle, stubHost } from './testkit'
import { DEFAULT_OPTIONS } from './types'

const T = DEFAULT_OPTIONS.alerts.thresholds
const only = {
  ...DEFAULT_OPTIONS,
  components: { beads: false, assume: true, sessionMode: false, prStatus: false, sessionName: false, links: false, modelEffort: false, focus: false },
}

async function world(profile: string | undefined, options = only) {
  clearBandButtons()
  const sh = stubHost()
  sh.state.awsProfile = profile
  initSources(sh.host)
  setOptions(options)
  registerComponents(options, sh.host)
  await settle()
  return sh
}
const button = () => {
  const v = { sessionId: 'sess-1', assumeRole: (readCached('assume') as { role?: string } | undefined)?.role }
  return bandLabels(v, T).find(l => l.id === 'assume')
}

test('unset and empty AWS_PROFILE read Assume at level dim', async () => {
  for (const p of [undefined, '']) {
    await world(p)
    expect(button()).toEqual({ id: 'assume', label: 'Assume', level: 'dim' })
  }
})

test('dev is green, prod is red, both is red, anything else is yellow, any case', async () => {
  const cases: Array<[string, string]> = [
    ['example-dev', 'green'],
    ['EXAMPLE-DEV', 'green'],
    ['example-prod', 'red'],
    ['Example-PROD', 'red'],
    ['example-dev-prod', 'red'],
    ['example-other', 'yellow'],
  ]
  for (const [name, level] of cases) {
    await world(name)
    expect(button()).toEqual({ id: 'assume', label: name, level })
  }
  expect(assumeLevel(undefined)).toBe('dim')
})

test('the profile is read once at session start through env.awsProfile and a draw starts nothing', async () => {
  const sh = await world('example-dev')
  expect(sh.calls.filter(c => c === 'env.awsProfile')).toHaveLength(1)
  const before = sh.calls.length
  button()
  button()
  expect(sh.calls.slice(before).filter(c => c !== 'ui.invalidate')).toEqual([])
  expect(sh.calls.filter(c => c === 'env.awsProfile')).toHaveLength(1)
})

test('with components.assume off nothing is registered and the facade is not called', async () => {
  const off = { ...only, components: { ...only.components, assume: false } }
  const sh = await world('example-dev', off)
  expect(bandLabels({ sessionId: 'sess-1' }, T).find(l => l.id === 'assume')).toBe(undefined)
  expect(readCached('assume')).toBe(undefined)
  expect(sh.calls.includes('env.awsProfile')).toBe(false)
})

const flat = (n: any): any[] =>
  n === null || typeof n !== 'object' ? [] : Array.isArray(n) ? n.flatMap(flat) : [n, ...flat(n.children)]

function stub$() {
  const el = (type: string) => (p: Record<string, unknown>) => ({ type, ...p })
  return {
    session: { id: async () => 'abcdef1234567890' },
    ui: { resolve: () => ({ Box: el('Box'), Text: el('Text'), Button: el('Button') }) },
  }
}

test('the band draws green as a Text colour and dim as the Text dim property', async () => {
  for (const [profile, expected] of [
    ['example-dev', { color: 'green' }],
    ['example-prod', { color: 'red' }],
    ['example-other', { color: 'yellow' }],
    [undefined, { dimColor: true }],
  ] as Array<[string | undefined, Record<string, unknown>]>) {
    await world(profile)
    const tree = await bandHook(stub$(), { props: { bodyColumns: 80 } }, async () => undefined)
    const t = flat(tree).find(n => n.type === 'Text' && n.key === 'assume')
    expect(t === undefined).toBe(false)
    expect({ color: t.color, dimColor: t.dimColor }).toEqual({ color: undefined, dimColor: undefined, ...expected })
  }
})
