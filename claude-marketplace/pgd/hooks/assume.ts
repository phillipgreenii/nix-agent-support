import { registerBandButton, type Level } from './band'
import { closeModal, registerModalBuilder, type ModalItem, type ModalSpec } from './modal'
import { defineSource, readCached, requestInvalidate, setValue } from './source'
import type { ModApi, PgdOptions } from './types'

// The Assume component: a band button showing the AWS profile the session's own processes run
// with, coloured by the profile name, the Assume modal, and the "assume expired" alert (in
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
//  - Clear: sets the four variables to empty strings and clears the stored role and expiry.
//  - Expiry: the Expiration of `aws configure export-credentials --profile NAME --format process`,
//    run once after each Apply with a five-minute timeout (a lapsed SSO login can open a keychain
//    prompt and a browser window, which does not fit the 30-second default). Only the Expiration
//    field is read; the credentials are never stored or logged. A failed or timed out lookup leaves
//    no expiry, and a missing expiry never raises the alert. A role set before the session started
//    has no expiry for the same reason.
//  - Check: the one-minute poll returns the stored value; the first poll after the expiry passes
//    asks for one redraw so the alert row appears.
export const ASSUME_ID = 'assume'
export const ASSUME_ORDER = 50
export const ASSUME_LABEL = 'Assume'

export interface AssumeValue {
  role?: string
  expiresAt?: number
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

export const EXPIRY_TIMEOUT_MS = 300_000

// The Expiration of the process-format credentials JSON, as epoch milliseconds; anything else is
// undefined. Nothing else in the text is read or kept.
export function parseExpiration(text: string): number | undefined {
  try {
    const o = JSON.parse(text) as { Expiration?: unknown }
    if (typeof o.Expiration !== 'string') return undefined
    const t = Date.parse(o.Expiration)
    return Number.isNaN(t) ? undefined : t
  } catch {
    return undefined
  }
}

// The stored role and expiry live in the source's cached value (one source of truth).
const stored = (): AssumeValue => readCached<AssumeValue>(ASSUME_ID) ?? {}
let selected: string | undefined
let config: AwsConfig = { roles: [], regions: {} }
let configLoad: Promise<void> | undefined
let notifiedExpiry: number | undefined
let nowFn: () => number = () => Date.now()

// The clock the expiry check reads (tests inject a fake one).
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
    ...(v.expiresAt !== undefined ? { expiresAt: v.expiresAt } : {}),
  }
}

function publish(v: AssumeValue): void {
  setValue(ASSUME_ID, cleanValue(v))
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
  if (cur.role === undefined && cur.expiresAt === undefined) publish({ role })
}

// The one-minute check: the stored value, and one redraw when its expiry has just passed.
async function check(host: ModApi, now: number): Promise<AssumeValue> {
  await loadConfig(host)
  const t = stored().expiresAt
  if (t !== undefined && t <= now && notifiedExpiry !== t) {
    notifiedExpiry = t
    requestInvalidate()
  }
  return stored()
}

async function lookupExpiry(host: ModApi, role: string): Promise<void> {
  let expiresAt: number | undefined
  try {
    const r = await host.process.run(['aws', 'configure', 'export-credentials', '--profile', role, '--format', 'process'], {
      timeoutMs: EXPIRY_TIMEOUT_MS,
    })
    expiresAt = parseExpiration(String((r as { stdout?: unknown } | undefined)?.stdout ?? ''))
  } catch {
    expiresAt = undefined
  }
  // a later Apply or Clear made this answer stale
  if (stored().role !== role || expiresAt === undefined) return
  publish({ role, expiresAt })
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
    ...(cur.expiresAt !== undefined ? [{ label: 'expires', value: new Date(cur.expiresAt).toISOString() }] : []),
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
      { id: 'close', label: 'Close' },
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
  await lookupExpiry(host, role)
}

async function clear(host: ModApi): Promise<void> {
  try {
    await host.env.setAwsProfile('')
    await host.env.setAwsRegion('')
    await host.env.setAwsDefaultRegion('')
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
  notifiedExpiry = undefined
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
