// Tests of the Assume button's display slice: label and colour from the session's AWS_PROFILE,
// read once at session start through the facade; the band's green and dim levels; the opt-out.
import { expect, test } from 'claude-code/testing'
import { bandHook, bandLabels, clearBandButtons } from './band'
import { registerComponents } from './components'
import { assumeLevel, assumeSpec, parseAwsConfig, parseExpiration, setAssumeClock } from './assume'
import { alertRow, resetAlertState } from './alerts'
import { pressAction, resetModals } from './modal'
import { setOptions } from './options'
import { initSources, readCached, setValue } from './source'
import { fakeClock, settle, stubHost } from './testkit'
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
    expect(button()).toEqual({ id: 'assume', label: 'Assume', level: 'dim', action: 'assume' })
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
    expect(button()).toEqual({ id: 'assume', label: name, level, action: 'assume' })
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
    const t = flat(tree).find(n => n.type === 'Text' && n.key === 'assume-text')
    expect(t === undefined).toBe(false)
    expect({ color: t.color, dimColor: t.dimColor }).toEqual({ color: undefined, dimColor: undefined, ...expected })
  }
})

// ---- the modal, Apply, Clear and the expiry alert ----

const CONFIG = [
  '[default]',
  'region = us-west-2',
  '',
  '[sso-session corp]',
  'sso_region = us-east-1',
  '',
  '[profile example-dev]',
  'region = us-east-1',
  '',
  '[profile example-prod]',
  'sso_session = corp',
  '',
].join('\n')
const CONFIG_PATH = '/cfg/aws-config'

async function modalWorld(profile?: string, options = only, config = CONFIG) {
  resetModals()
  resetAlertState()
  clearBandButtons()
  const clock = fakeClock()
  setAssumeClock(clock.now)
  const sh = stubHost({ [CONFIG_PATH]: config })
  sh.state.awsProfile = profile
  sh.state.awsConfigFile = CONFIG_PATH
  initSources(sh.host, clock)
  setOptions(options)
  registerComponents(options, sh.host)
  await clock.advance(0)
  return { sh, clock }
}

// Every action id a modal offers: the per-row buttons and the action row.
const actionIds = (): string[] => {
  const spec = assumeSpec()
  return [...spec.items.flatMap(i => (i.action ? [i.action.id] : [])), ...spec.actions.map(a => a.id)]
}

const credentials = (expiration: string) =>
  JSON.stringify({ Version: 1, AccessKeyId: 'AKIAEXAMPLE', Expiration: expiration, SecretAccessKey: 'x', SessionToken: 'y' })

test('parseAwsConfig keeps default and profile sections and their region, nothing else', () => {
  expect(parseAwsConfig(CONFIG)).toEqual({
    roles: ['default', 'example-dev', 'example-prod'],
    regions: { default: 'us-west-2', 'example-dev': 'us-east-1' },
  })
  expect(parseAwsConfig('')).toEqual({ roles: [], regions: {} })
})

test('parseExpiration reads only a valid Expiration field', () => {
  expect(parseExpiration(credentials('2026-11-09T23:11:19-05:00'))).toBe(Date.parse('2026-11-09T23:11:19-05:00'))
  expect(parseExpiration('{}')).toBe(undefined)
  expect(parseExpiration('not json')).toBe(undefined)
  expect(parseExpiration('{"Expiration":"soon"}')).toBe(undefined)
})

test('the button now opens the assume modal, and the label and level are unchanged', async () => {
  await modalWorld('example-dev')
  const rows = bandLabels({ sessionId: 'sess-1', assumeRole: 'example-dev' }, T)
  expect(rows.find(r => r.id === 'assume')).toEqual({ id: 'assume', label: 'example-dev', level: 'green', action: 'assume' })
})

test('the config is read once through fs.read and the modal offers role:<name>, apply, clear and close', async () => {
  const { sh } = await modalWorld()
  expect(sh.calls.filter(c => c === `fs.read ${CONFIG_PATH}`)).toHaveLength(1)
  expect(actionIds()).toEqual([
    'role:default',
    'role:example-dev',
    'role:example-prod',
    'apply',
    'clear',
    'close',
  ])
  assumeSpec()
  assumeSpec()
  expect(sh.calls.filter(c => c === `fs.read ${CONFIG_PATH}`)).toHaveLength(1)
})

test('without AWS_CONFIG_FILE the file is ~/.aws/config', async () => {
  resetModals()
  clearBandButtons()
  const sh = stubHost({ '/home/u/.aws/config': CONFIG })
  const clock = fakeClock()
  initSources(sh.host, clock)
  setOptions(only)
  registerComponents(only, sh.host)
  await clock.advance(0)
  expect(sh.calls).toContain('fs.read /home/u/.aws/config')
  expect(assumeSpec().items.filter(i => i.label === 'role')).toHaveLength(3)
})

test('choosing a role marks it current in the modal', async () => {
  await modalWorld()
  pressAction('assume', 'role:example-prod')
  expect(assumeSpec().items.filter(i => i.current).map(i => i.value)).toEqual(['example-prod'])
})

test('Apply sets exactly the four names, with the profile region, and never runs assume', async () => {
  const { sh } = await modalWorld()
  sh.host.process.run = async (argv: string[]) => {
    sh.calls.push(`run ${argv.join(' ')}`)
    return { exitCode: 0, stdout: credentials('2026-11-10T12:00:00Z') }
  }
  pressAction('assume', 'role:example-dev')
  pressAction('assume', 'apply')
  await settle()
  expect(sh.envSets).toEqual([
    { name: 'AWS_PROFILE', value: 'example-dev' },
    { name: 'AWS_REGION', value: 'us-east-1' },
    { name: 'AWS_DEFAULT_REGION', value: 'us-east-1' },
    { name: 'GRANTED_COMMAND', value: 'example-dev' },
  ])
  const runs = sh.calls.filter(c => c.startsWith('run '))
  expect(runs).toEqual(['run aws configure export-credentials --profile example-dev --format process'])
  expect(runs.some(c => c.includes('assume example'))).toBe(false)
  expect(readCached('assume')).toEqual({ role: 'example-dev', expiresAt: Date.parse('2026-11-10T12:00:00Z') })
})

test('Apply on a profile without a region sets only the profile and the granted command', async () => {
  const { sh } = await modalWorld()
  pressAction('assume', 'role:example-prod')
  pressAction('assume', 'apply')
  await settle()
  expect(sh.envSets.map(e => e.name)).toEqual(['AWS_PROFILE', 'GRANTED_COMMAND'])
  expect(readCached('assume')).toEqual({ role: 'example-prod' })
})

test('the expiry lookup runs with an explicit timeout well above 30 seconds', async () => {
  const { sh } = await modalWorld()
  let seen: unknown
  sh.host.process.run = async (_argv: string[], init: unknown) => {
    seen = init
    return { exitCode: 0, stdout: '{}' }
  }
  pressAction('assume', 'role:example-dev')
  pressAction('assume', 'apply')
  await settle()
  expect((seen as { timeoutMs: number }).timeoutMs > 30_000).toBe(true)
})

test('a failed expiry lookup leaves the role without an expiry, and no alert', async () => {
  const { sh } = await modalWorld()
  sh.host.process.run = async () => {
    throw new Error('timed out')
  }
  pressAction('assume', 'role:example-dev')
  pressAction('assume', 'apply')
  await settle()
  expect(readCached('assume')).toEqual({ role: 'example-dev' })
  expect(alertRow()).toBe('')
})

test('a denied env.set leaves the stored role as it was', async () => {
  const { sh } = await modalWorld('example-dev')
  sh.envSetFail.on = true
  pressAction('assume', 'role:example-prod')
  pressAction('assume', 'apply')
  await settle()
  expect(readCached('assume')).toEqual({ role: 'example-dev' })
})

test('Apply with nothing chosen and no listed current role does nothing', async () => {
  const { sh } = await modalWorld()
  pressAction('assume', 'apply')
  await settle()
  expect(sh.envSets).toEqual([])
})

test('Clear sets the four names to empty strings and clears the stored role and expiry', async () => {
  const { sh } = await modalWorld('example-dev')
  setValue('assume', { role: 'example-dev', expiresAt: 5 })
  pressAction('assume', 'clear')
  await settle()
  expect(sh.envSets).toEqual([
    { name: 'AWS_PROFILE', value: '' },
    { name: 'AWS_REGION', value: '' },
    { name: 'AWS_DEFAULT_REGION', value: '' },
    { name: 'GRANTED_COMMAND', value: '' },
  ])
  expect(readCached('assume')).toEqual({})
  expect(button()).toEqual({ id: 'assume', label: 'Assume', level: 'dim', action: 'assume' })
})

test('the assume expired alert shows only once an expiry has passed', async () => {
  await modalWorld()
  const now = Date.now()
  setValue('assume', { role: 'example-dev', expiresAt: now + 60_000 })
  expect(alertRow()).toBe('')
  setValue('assume', { role: 'example-dev', expiresAt: now - 1 })
  expect(alertRow()).toBe('assume expired')
  setValue('assume', { role: 'example-dev' })
  expect(alertRow()).toBe('')
})

test('the one-minute check asks for one redraw when the expiry passes, and not again', async () => {
  const { sh, clock } = await modalWorld('example-dev')
  setValue('assume', { role: 'example-dev', expiresAt: clock.now() + 90_000 })
  await settle()
  const invalidates = () => sh.calls.filter(c => c === 'ui.invalidate').length
  await clock.advance(60_000)
  const before = invalidates()
  await clock.advance(60_000)
  expect(invalidates()).toBe(before + 1)
  await clock.advance(180_000)
  expect(invalidates()).toBe(before + 1)
})

test('with components.assume off there is no modal, alert, source, timer or $.env.set call', async () => {
  const off = { ...only, components: { ...only.components, assume: false } }
  const { sh, clock } = await modalWorld('example-dev', off)
  expect(readCached('assume')).toBe(undefined)
  expect(clock.pending()).toBe(0)
  expect(alertRow()).toBe('')
  pressAction('assume', 'apply')
  await settle()
  expect(sh.envSets).toEqual([])
  expect(sh.calls.some(c => c.startsWith('fs.read'))).toBe(false)
  expect(bandLabels({ sessionId: 'sess-1' }, T).find(l => l.id === 'assume')).toBe(undefined)
})

test('the modal lists roles alphabetically, ignoring case, while the config order is kept', async () => {
  const unsorted = ['[profile zeta]', '[default]', '[profile Beta]', '[profile alpha]', '[profile beta2]'].join('\n')
  await modalWorld('zeta', only, unsorted)
  expect(parseAwsConfig(unsorted).roles).toEqual(['zeta', 'default', 'Beta', 'alpha', 'beta2'])
  expect(assumeSpec().items.filter(i => i.label === 'role').map(i => i.value)).toEqual([
    'alpha',
    'Beta',
    'beta2',
    'default',
    'zeta',
  ])
  expect(assumeSpec().items.filter(i => i.current).map(i => i.value)).toEqual(['zeta'])
})
