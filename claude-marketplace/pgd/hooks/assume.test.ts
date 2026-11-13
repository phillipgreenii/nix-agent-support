// Tests of the Assume button's display slice: label and colour from the session's AWS_PROFILE,
// read once at session start through the facade; the band's green and dim levels; the opt-out.
import { expect, test } from 'claude-code/testing'
import { bandHook, bandLabels, clearBandButtons } from './band'
import { registerComponents } from './components'
import { assumeLevel, assumeSpec, parseAwsConfig, parseSsoLogin, setAssumeClock, sha1Hex } from './assume'
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

// The token cache file of the default sso-session (zip) under the stub's home, as the CLI names it.
const CACHE_FILE = `/home/u/.aws/sso/cache/${sha1Hex('zip')}.json`
const FAR = '2999-01-01T00:00:00Z'
const SECRET = 'SECRET-TOKEN-VALUE-do-not-leak'
const HEALTHY = { expiresAt: FAR, accessToken: SECRET, refreshToken: SECRET }
const cacheFile = (o: Record<string, unknown>) => JSON.stringify(o)

async function modalWorld(profile?: string, options = only, config = CONFIG, files: Record<string, string> = {}) {
  resetModals()
  resetAlertState()
  clearBandButtons()
  const clock = fakeClock()
  setAssumeClock(clock.now)
  const sh = stubHost({ [CONFIG_PATH]: config, [CACHE_FILE]: cacheFile(HEALTHY), ...files })
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

test('parseAwsConfig keeps default and profile sections and their region, nothing else', () => {
  expect(parseAwsConfig(CONFIG)).toEqual({
    roles: ['default', 'example-dev', 'example-prod'],
    regions: { default: 'us-west-2', 'example-dev': 'us-east-1' },
  })
  expect(parseAwsConfig('')).toEqual({ roles: [], regions: {} })
})

test('sha1Hex matches the digest the CLI uses to name the cache file', () => {
  expect(sha1Hex('zip')).toBe('f13e27693c85aed522df8c3fcb0bb0110ca54e14')
  expect(sha1Hex('')).toBe('da39a3ee5e6b4b0d3255bfef95601890afd80709')
  expect(sha1Hex('a'.repeat(100))).toBe('7f9000257a4918d7072655ea468540cdcbd42e0c')
})

const NOW = Date.parse('2026-11-12T12:00:00Z')

test('parseSsoLogin: healthy refreshable session is valid', () => {
  expect(parseSsoLogin(cacheFile({ ...HEALTHY, expiresAt: '2026-11-12T12:30:00Z' }), NOW)).toBe('valid')
})

test('parseSsoLogin: expired access token with no refresh token is expired-unrefreshable', () => {
  expect(parseSsoLogin(cacheFile({ expiresAt: '2026-11-12T11:00:00Z', accessToken: SECRET }), NOW)).toBe('expired-unrefreshable')
  expect(parseSsoLogin(cacheFile({ expiresAt: '2026-11-12T11:00:00Z', accessToken: SECRET, refreshToken: '' }), NOW)).toBe('expired-unrefreshable')
})

test('parseSsoLogin: access token expired but refreshable is expired-refreshable', () => {
  expect(parseSsoLogin(cacheFile({ ...HEALTHY, expiresAt: '2026-11-12T11:00:00Z' }), NOW)).toBe('expired-refreshable')
})

test('parseSsoLogin: a valid access token without a refresh token is still valid (the sensor reads it so)', () => {
  expect(parseSsoLogin(cacheFile({ expiresAt: '2026-11-12T12:30:00Z', accessToken: SECRET }), NOW)).toBe('valid')
})

test('parseSsoLogin: unparseable text, missing expiresAt or accessToken, a bad date are missing', () => {
  expect(parseSsoLogin('not json', NOW)).toBe('missing')
  expect(parseSsoLogin('{}', NOW)).toBe('missing')
  expect(parseSsoLogin(cacheFile({ accessToken: SECRET }), NOW)).toBe('missing')
  expect(parseSsoLogin(cacheFile({ expiresAt: '2026-11-12T12:30:00Z' }), NOW)).toBe('missing')
  expect(parseSsoLogin(cacheFile({ expiresAt: 'soon', accessToken: SECRET }), NOW)).toBe('missing')
})

test('parseSsoLogin reads a timezone-less expiresAt as UTC, like the sensor', () => {
  expect(parseSsoLogin(cacheFile({ ...HEALTHY, expiresAt: '2026-11-12T12:30:00' }), NOW)).toBe('valid')
})

test('the button now opens the assume modal, and the label and level are unchanged', async () => {
  await modalWorld('example-dev')
  const rows = bandLabels({ sessionId: 'sess-1', assumeRole: 'example-dev' }, T)
  expect(rows.find(r => r.id === 'assume')).toEqual({ id: 'assume', label: 'example-dev', level: 'green', action: 'assume' })
})

test('the config is read once through fs.read and the modal offers role:<name>, apply and clear', async () => {
  const { sh } = await modalWorld()
  expect(sh.calls.filter(c => c === `fs.read ${CONFIG_PATH}`)).toHaveLength(1)
  expect(actionIds()).toEqual([
    'role:default',
    'role:example-dev',
    'role:example-prod',
    'apply',
    'clear',
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
  pressAction('assume', 'role:example-dev')
  pressAction('assume', 'apply')
  await settle()
  expect(sh.envSets).toEqual([
    { name: 'AWS_PROFILE', value: 'example-dev' },
    { name: 'AWS_REGION', value: 'us-east-1' },
    { name: 'AWS_DEFAULT_REGION', value: 'us-east-1' },
    { name: 'GRANTED_COMMAND', value: 'example-dev' },
  ])
  // nothing is run: no assume, no aws call (Apply never logs in)
  expect(sh.calls.some(c => c === 'process.run' || c.startsWith('run '))).toBe(false)
  expect(readCached('assume')).toEqual({ role: 'example-dev', login: 'valid' })
})

test('Apply on a profile without a region sets only the profile and the granted command', async () => {
  const { sh } = await modalWorld()
  pressAction('assume', 'role:example-prod')
  pressAction('assume', 'apply')
  await settle()
  expect(sh.envSets.map(e => e.name)).toEqual(['AWS_PROFILE', 'GRANTED_COMMAND'])
  expect(readCached('assume')).toEqual({ role: 'example-prod', login: 'valid' })
})

test('a denied env.set leaves the stored role as it was', async () => {
  const { sh } = await modalWorld('example-dev')
  sh.envSetFail.on = true
  pressAction('assume', 'role:example-prod')
  pressAction('assume', 'apply')
  await settle()
  expect(readCached('assume')).toEqual({ role: 'example-dev', login: 'valid' })
})

test('Apply with nothing chosen and no listed current role does nothing', async () => {
  const { sh } = await modalWorld()
  pressAction('assume', 'apply')
  await settle()
  expect(sh.envSets).toEqual([])
})

test('Clear points AWS_PROFILE at default (with its region) so the aws CLI still works, and clears the role', async () => {
  const { sh } = await modalWorld('example-dev')
  setValue('assume', { role: 'example-dev', login: 'valid' })
  pressAction('assume', 'clear')
  await settle()
  expect(sh.envSets).toEqual([
    { name: 'AWS_PROFILE', value: 'default' },
    { name: 'AWS_REGION', value: 'us-west-2' },
    { name: 'AWS_DEFAULT_REGION', value: 'us-west-2' },
    { name: 'GRANTED_COMMAND', value: '' },
  ])
  expect(readCached('assume')).toEqual({ login: 'valid' })
  expect(button()).toEqual({ id: 'assume', label: 'Assume', level: 'dim', action: 'assume' })
})

test('Clear without a default profile in the config sets all four names to empty strings (no safe value exists)', async () => {
  const { sh } = await modalWorld('example-dev', only, '[profile example-dev]\nregion = us-east-1\n')
  pressAction('assume', 'clear')
  await settle()
  expect(sh.envSets).toEqual([
    { name: 'AWS_PROFILE', value: '' },
    { name: 'AWS_REGION', value: '' },
    { name: 'AWS_DEFAULT_REGION', value: '' },
    { name: 'GRANTED_COMMAND', value: '' },
  ])
  expect(readCached('assume')).toEqual({ login: 'valid' })
})

const LOGIN_STATES: Array<[string, Record<string, unknown> | undefined, string, string]> = [
  ['healthy refreshable session', HEALTHY, 'valid', ''],
  ['expired session', { expiresAt: '2000-01-01T00:00:00Z', accessToken: SECRET }, 'expired-unrefreshable', 'login expired'],
  ['access token expired but refreshable', { ...HEALTHY, expiresAt: '2000-01-01T00:00:00Z' }, 'expired-refreshable', ''],
  ['missing refresh token on an expired token', { expiresAt: '2000-01-01T00:00:00Z', accessToken: SECRET }, 'expired-unrefreshable', 'login expired'],
  ['missing cache file', undefined, 'missing', 'login expired'],
]

test('the login expired alert follows the SSO cache file: healthy and refreshable stay quiet, expired or missing raise it', async () => {
  for (const [name, file, login, row] of LOGIN_STATES) {
    resetAlertState()
    const { sh, clock } = await modalWorld('example-dev')
    setAssumeClock(() => Date.now())
    if (file) sh.files.set(CACHE_FILE, cacheFile(file))
    else sh.files.delete(CACHE_FILE)
    await clock.advance(60_000)
    expect([name, (readCached('assume') as { login?: string }).login, alertRow()]).toEqual([name, login, row])
  }
})

test('a healthy session more than an hour after Apply raises no alert (the role credentials renew on their own)', async () => {
  const { clock } = await modalWorld('example-dev')
  pressAction('assume', 'role:example-dev')
  pressAction('assume', 'apply')
  await settle()
  await clock.advance(3 * 3_600_000)
  expect(alertRow()).toBe('')
})

test('the one-minute check asks for one redraw when the login state changes, and not again', async () => {
  const { sh, clock } = await modalWorld('example-dev')
  const invalidates = () => sh.calls.filter(c => c === 'ui.invalidate').length
  await clock.advance(60_000)
  await settle()
  const before = invalidates()
  await clock.advance(60_000)
  await settle()
  expect(invalidates()).toBe(before)
  sh.files.set(CACHE_FILE, cacheFile({ expiresAt: '1970-01-01T00:00:00Z', accessToken: SECRET }))
  await clock.advance(60_000)
  await settle()
  expect(invalidates()).toBe(before + 1)
  await clock.advance(60_000)
  await settle()
  expect(invalidates()).toBe(before + 1)
})

test('the session name and cache directory come from ZR_SSO_SESSION and AWS_SSO_CACHE_DIR when set', async () => {
  resetModals()
  clearBandButtons()
  const clock = fakeClock()
  const path = `/c/${sha1Hex('corp-2')}.json`
  const sh = stubHost({ [path]: cacheFile(HEALTHY) })
  sh.state.ssoSession = 'corp-2'
  sh.state.ssoCacheDir = '/c'
  initSources(sh.host, clock)
  setOptions(only)
  registerComponents(only, sh.host)
  await clock.advance(0)
  expect(sh.calls).toContain(`fs.read ${path}`)
  expect((readCached('assume') as { login?: string }).login).toBe('valid')
})

test('an invalid session name is missing and no file is read', async () => {
  resetModals()
  clearBandButtons()
  const clock = fakeClock()
  const sh = stubHost()
  sh.state.ssoSession = '../etc'
  initSources(sh.host, clock)
  setOptions(only)
  registerComponents(only, sh.host)
  await clock.advance(0)
  expect(sh.calls.some(c => c.startsWith('fs.read /home/u/.aws/sso'))).toBe(false)
  expect((readCached('assume') as { login?: string }).login).toBe('missing')
})

test('token values are never stored, shown in the modal or logged', async () => {
  const logged: string[] = []
  const spies = ['log', 'info', 'warn', 'error', 'debug'] as const
  const saved = spies.map(k => console[k])
  for (const k of spies) console[k] = (...a: unknown[]) => void logged.push(a.map(String).join(' '))
  try {
    const { sh } = await modalWorld('example-dev')
    pressAction('assume', 'role:example-dev')
    pressAction('assume', 'apply')
    await settle()
    const seen = JSON.stringify([readCached('assume'), assumeSpec(), alertRow(), sh.calls, sh.writes, sh.envSets, logged])
    expect(seen.includes(SECRET)).toBe(false)
    expect(seen.includes('accessToken') || seen.includes('refreshToken')).toBe(false)
    expect(Object.keys(readCached('assume') as object).sort()).toEqual(['login', 'role'])
  } finally {
    spies.forEach((k, i) => (console[k] = saved[i]))
  }
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
