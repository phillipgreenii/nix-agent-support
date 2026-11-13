import { registerBandButton, type Level } from './band'
import { closeModal, registerModalBuilder, type ModalItem, type ModalSpec } from './modal'
import { defineSource, readCached, requestInvalidate, setValue } from './source'
import type { ModApi, PgdOptions } from './types'

// The Assume component: a band button showing the AWS profile the session's own processes run
// with, coloured by the profile name, the Assume modal, and the "login expired" alert (in
// alerts.ts, which reads this source). The source is seeded once at session start from the Claude
// process's AWS_PROFILE (facade env.awsProfile()); it polls once a minute (intervalMs 60000, feeds
// the band and the alert), and Apply and Clear update it with setValue.
//
// Choices made by the builder (documented in the package README, each covered by assume.test.ts):
//  - Roles: the profile names in the AWS config file (the file named by AWS_CONFIG_FILE, else
//    ~/.aws/config), read once through host.fs.read; [default] and [profile NAME] sections count,
//    any other section (for example sso-session) does not.
//    The modal lists them alphabetically (case-insensitive), not in file order.
//  - Apply: sets AWS_PROFILE, AWS_REGION, AWS_DEFAULT_REGION and GRANTED_COMMAND through four
//    facade closures (fixed literal names), never runs assume. The region variables are set only
//    when the profile has a region key in the config.
//  - Clear: the host cannot unset a variable and the aws CLI reads an empty AWS_PROFILE as a profile
//    named "" (every call fails), so when the config has a [default] section AWS_PROFILE becomes
//    "default" and the region variables take that section's region (empty when it has none);
//    without a [default] section no value is safe (AWS_PROFILE=default also fails there), so all
//    four are set to empty strings and aws needs --profile or an Apply. GRANTED_COMMAND is always
//    emptied. The stored role is cleared either way. The session's starting profile is
//    not restored: it may itself be a role (prod), which Clear exists to drop.
//  - Login state: the alert is about the SSO login, not about the role credentials (they expire
//    about hourly and renew on their own). Each poll reads the SSO token cache file of the sso-session
//    (default zip; ZR_SSO_SESSION overrides; directory AWS_SSO_CACHE_DIR, else ~/.aws/sso/cache; file
//    <sha1 of the session name>.json) through host.fs.read and keeps only a state word: valid,
//    expired-refreshable, expired-unrefreshable or missing. The rule is the one the expired-auth
//    sensor and the SessionStart hook use (parseSsoLogin mirrors the sensor). Only expiresAt and the
//    presence of accessToken and refreshToken are looked at; token values are never stored or logged.
//    Nothing here logs in or runs aws.
//  - Check: the one-minute poll reads the cache file; a change of state makes the source layer ask for
//    one redraw, so the alert row appears or goes without another event.
export const ASSUME_ID = 'assume'
export const ASSUME_ORDER = 50
export const ASSUME_LABEL = 'Assume'

// The state of the SSO login: valid (access token unexpired), expired-refreshable (expired but a
// refresh token exists, the next AWS call renews it), expired-unrefreshable (expired and no refresh
// token) or missing (no cache file, or one that is unreadable or lacks expiresAt or accessToken).
export type SsoLogin = 'valid' | 'expired-refreshable' | 'expired-unrefreshable' | 'missing'

export interface AssumeValue {
  role?: string
  login?: SsoLogin
}

// The login states that need a person to run aws sso login (the sensor's alert condition: state
// below 0.25, which is expired-unrefreshable and missing).
export function loginNeedsAction(login: SsoLogin | undefined): boolean {
  return login === 'expired-unrefreshable' || login === 'missing'
}

// The colour rule, matching the profile name ignoring case: unset or empty is dim, a name
// containing prod is red (checked first, so a name with both is red), otherwise a name containing
// dev is green, any other value is yellow.
export function assumeLevel(profile: string | undefined): Level {
  if (!profile) return 'dim'
  const p = profile.toLowerCase()
  if (p.includes('prod')) return 'red'
  if (p.includes('dev')) return 'green'
  return 'yellow'
}

export function assumeLabel(profile: string | undefined): string {
  return profile ? profile : ASSUME_LABEL
}

export interface AwsConfig {
  roles: string[]
  regions: Record<string, string>
}

// The profile names and their region key from AWS config text.
export function parseAwsConfig(text: string): AwsConfig {
  const roles: string[] = []
  const regions: Record<string, string> = {}
  let cur: string | undefined
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '' || line.startsWith('#') || line.startsWith(';')) continue
    const h = /^\[\s*(.*?)\s*\]$/.exec(line)
    if (h) {
      const m = /^profile\s+(.+)$/.exec(h[1])
      cur = m ? m[1].trim() : h[1] === 'default' ? 'default' : undefined
      if (cur !== undefined && !roles.includes(cur)) roles.push(cur)
      continue
    }
    const kv = /^region\s*=\s*(\S+)\s*$/.exec(line)
    if (cur !== undefined && kv) regions[cur] = kv[1]
  }
  return { roles, regions }
}

// SHA-1 of an ASCII string as lowercase hex (the CLI names an sso-session's token file after it).
// Pure code: the engine's module loader allows no node: imports.
export function sha1Hex(text: string): string {
  const bytes = Array.from(text, c => c.charCodeAt(0) & 0xff)
  const bitLen = bytes.length * 8
  bytes.push(0x80)
  while (bytes.length % 64 !== 56) bytes.push(0)
  for (let i = 7; i >= 0; i--) bytes.push(i >= 4 ? 0 : (bitLen >>> (i * 8)) & 0xff)
  let [h0, h1, h2, h3, h4] = [0x67452301, 0xefcdab89 | 0, 0x98badcfe | 0, 0x10325476, 0xc3d2e1f0 | 0]
  const rol = (x: number, n: number) => (x << n) | (x >>> (32 - n))
  for (let o = 0; o < bytes.length; o += 64) {
    const w: number[] = []
    for (let i = 0; i < 16; i++) w[i] = (bytes[o + 4 * i] << 24) | (bytes[o + 4 * i + 1] << 16) | (bytes[o + 4 * i + 2] << 8) | bytes[o + 4 * i + 3]
    for (let i = 16; i < 80; i++) w[i] = rol(w[i - 3] ^ w[i - 8] ^ w[i - 14] ^ w[i - 16], 1)
    let [a, b, c, d, e] = [h0, h1, h2, h3, h4]
    for (let i = 0; i < 80; i++) {
      const [f, k] =
        i < 20 ? [(b & c) | (~b & d), 0x5a827999] : i < 40 ? [b ^ c ^ d, 0x6ed9eba1] : i < 60 ? [(b & c) | (b & d) | (c & d), 0x8f1bbcdc | 0] : [b ^ c ^ d, 0xca62c1d6 | 0]
      const t = (rol(a, 5) + f + e + k + w[i]) | 0
      ;[e, d, c, b, a] = [d, c, rol(b, 30), a, t]
    }
    h0 = (h0 + a) | 0
    h1 = (h1 + b) | 0
    h2 = (h2 + c) | 0
    h3 = (h3 + d) | 0
    h4 = (h4 + e) | 0
  }
  return [h0, h1, h2, h3, h4].map(h => (h >>> 0).toString(16).padStart(8, '0')).join('')
}

// The login state from the token cache file's text at epoch milliseconds now. Mirrors the
// expired-auth sensor: expiresAt and accessToken must be present (else missing); an unexpired
// expiresAt is valid; expired with a non-empty refreshToken is expired-refreshable; otherwise
// expired-unrefreshable. Only expiresAt and whether the two token fields are present are read; no
// token value is kept or returned.
export function parseSsoLogin(text: string, now: number): SsoLogin {
  try {
    const o = JSON.parse(text) as { expiresAt?: unknown; accessToken?: unknown; refreshToken?: unknown }
    if (typeof o.expiresAt !== 'string' || o.accessToken === undefined) return 'missing'
    const t = Date.parse(/(Z|[+-]\d\d:?\d\d)$/.test(o.expiresAt) ? o.expiresAt : `${o.expiresAt}Z`)
    if (Number.isNaN(t)) return 'missing'
    if (t > now) return 'valid'
    return o.refreshToken ? 'expired-refreshable' : 'expired-unrefreshable'
  } catch {
    return 'missing'
  }
}

// The stored role and login state live in the source's cached value (one source of truth).
const stored = (): AssumeValue => readCached<AssumeValue>(ASSUME_ID) ?? {}
let selected: string | undefined
let config: AwsConfig = { roles: [], regions: {} }
let configLoad: Promise<void> | undefined
let nowFn: () => number = () => Date.now()

// The clock the login check reads (tests inject a fake one).
export function setAssumeClock(now: () => number): void {
  nowFn = now
}

// The role Apply uses: the one chosen in the modal, else the current role when the config lists it.
function chosenRole(): string | undefined {
  return selected ?? (stored().role !== undefined && config.roles.includes(stored().role as string) ? stored().role : undefined)
}

function cleanValue(v: AssumeValue): AssumeValue {
  return {
    ...(v.role !== undefined ? { role: v.role } : {}),
    ...(v.login !== undefined ? { login: v.login } : {}),
  }
}

// Publishes the role; the login state is not the role's, so it is carried over.
function publish(v: AssumeValue): void {
  setValue(ASSUME_ID, cleanValue({ login: stored().login, ...v }))
}

// Read the config once; a missing or unreadable file leaves the role list empty.
function loadConfig(host: ModApi): Promise<void> {
  configLoad ??= (async () => {
    try {
      const env = await host.env.awsConfigFile()
      const path = typeof env === 'string' && env !== '' ? env : `${String((await host.env.home()) ?? '')}/.aws/config`
      config = parseAwsConfig(String(await host.fs.read(path)))
    } catch {
      config = { roles: [], regions: {} }
    }
  })()
  return configLoad
}

async function seed(host: ModApi): Promise<void> {
  let role: string | undefined
  try {
    const v = await host.env.awsProfile()
    role = typeof v === 'string' && v !== '' ? v : undefined
  } catch {
    role = undefined
  }
  // an Apply or Clear that already happened wins over the session-start value
  const cur = stored()
  if (cur.role === undefined) publish({ role })
}

// Read the SSO token cache file for the login state; any failure to read is missing, as the sensor
// treats an absent file. Only the state word leaves this function.
async function readLogin(host: ModApi, now: number): Promise<SsoLogin> {
  try {
    const session = String((await host.env.ssoSession()) ?? '') || 'zip'
    if (!/^[A-Za-z0-9._-]+$/.test(session)) return 'missing'
    const dir = String((await host.env.ssoCacheDir()) ?? '') || `${String((await host.env.home()) ?? '')}/.aws/sso/cache`
    return parseSsoLogin(String(await host.fs.read(`${dir}/${sha1Hex(session)}.json`)), now)
  } catch {
    return 'missing'
  }
}

// The one-minute check: the stored role with the current login state (the source layer asks for
// one redraw when the value changed).
async function check(host: ModApi, now: number): Promise<AssumeValue> {
  await loadConfig(host)
  const login = await readLogin(host, now)
  return cleanValue({ ...stored(), login })
}

// A sorted copy for the modal (case-insensitive, stable); config.roles keeps the file order.
function sortedRoles(roles: string[]): string[] {
  return [...roles].sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()))
}

export function assumeSpec(): ModalSpec {
  const cur = stored()
  const sel = chosenRole()
  const items: ModalItem[] = [
    { label: 'current', value: cur.role ?? 'none' },
    ...(cur.login !== undefined ? [{ label: 'login', value: cur.login }] : []),
    ...(config.roles.length === 0 ? [{ label: 'roles', value: 'none found in the AWS config' }] : []),
    ...sortedRoles(config.roles).map(r => ({
      label: 'role',
      value: r,
      ...(r === sel ? { current: true } : {}),
      action: { id: `role:${r}`, label: 'Choose' },
    })),
  ]
  return {
    id: ASSUME_ID,
    title: 'Assume',
    items,
    actions: [
      { id: 'apply', label: 'Apply' },
      { id: 'clear', label: 'Clear' },
    ],
  }
}

async function apply(host: ModApi): Promise<void> {
  const role = chosenRole()
  if (role === undefined) return
  const region = config.regions[role]
  try {
    await host.env.setAwsProfile(role)
    if (region !== undefined) {
      await host.env.setAwsRegion(region)
      await host.env.setAwsDefaultRegion(region)
    }
    await host.env.setGrantedCommand(role)
  } catch {
    // a denied set leaves the stored role as it was
    return
  }
  publish({ role })
  closeModal(ASSUME_ID)
}

async function clear(host: ModApi): Promise<void> {
  await loadConfig(host)
  const hasDefault = config.roles.includes('default')
  const region = hasDefault ? (config.regions.default ?? '') : ''
  try {
    await host.env.setAwsProfile(hasDefault ? 'default' : '')
    await host.env.setAwsRegion(region)
    await host.env.setAwsDefaultRegion(region)
    await host.env.setGrantedCommand('')
  } catch {
    return
  }
  selected = undefined
  publish({})
  closeModal(ASSUME_ID)
}

export function startAssume(_options: PgdOptions, host: ModApi): void {
  selected = undefined
  config = { roles: [], regions: {} }
  configLoad = undefined
    defineSource<AssumeValue>({
    id: ASSUME_ID,
    intervalMs: 60_000,
    feeds: ['alert', 'band'],
    fetch: ({ host: h }) => check(h, nowFn()),
  })
  registerBandButton({
    id: ASSUME_ID,
    order: ASSUME_ORDER,
    action: ASSUME_ID,
    label: v => assumeLabel(v.assumeRole),
    level: v => assumeLevel(v.assumeRole),
  })
  registerModalBuilder(
    ASSUME_ID,
    assumeSpec,
    async actionId => {
      if (actionId.startsWith('role:')) {
        selected = actionId.slice('role:'.length)
        requestInvalidate()
      } else if (actionId === 'apply') await apply(host)
      else if (actionId === 'clear') await clear(host)
    },
    () => loadConfig(host),
  )
  void seed(host)
}
