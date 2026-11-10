// Shared types contract of the display mod. Type-only (plus the defaults and parser below), so a
// test run with stripped types loads it without the engine.

// The host API handed to every hook as `$`. Declared locally rather than imported from the
// engine's declarations: the node tests run with stripped types and no engine, and a component
// only ever touches `$` through members the engine documents.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type ModApi = any

export interface PgdComponents {
  beads: boolean
  assume: boolean
  sessionMode: boolean
  prStatus: boolean
  sessionName: boolean
  links: boolean
  modelEffort: boolean
  focus: boolean
}

// A link rule is a union by kind. An entry without `kind` is a regex rule, so every entry written
// before the kinds existed keeps working unchanged.
export interface RegexLink {
  kind?: 'regex'
  name: string
  pattern: string
  url: string
}

export interface GithubPrLink {
  kind: 'github-pr'
  name: string
  options: { sessionRepoFallback: boolean }
}

export interface JiraLink {
  kind: 'jira'
  name: string
  options: { baseUrl: string; keys: string[] }
}

export interface FileRefLink {
  kind: 'file-ref'
  name: string
  options: { localTemplate: string }
}

export type PgdLink = RegexLink | GithubPrLink | JiraLink | FileRefLink

export const DEFAULT_LOCAL_TEMPLATE = 'file://{path}:{line}'
const LOCAL_TEMPLATE_SCHEMES = ['file', 'vscode', 'vscode-insiders', 'cursor', 'zed']
const JIRA_KEY = /^[A-Z][A-Z0-9_]*$/
const GITHUB_REPO = /^[^/\s]+\/[^/\s]+$/

export interface PgdGithub {
  host: string
  repos: string[]
}

export interface AlertThresholds {
  contextWarn: number
  contextAlert: number
  limit: number
}

export interface PgdOptions {
  enable: boolean
  components: PgdComponents
  links: PgdLink[]
  github: PgdGithub
  alerts: { thresholds: AlertThresholds }
  // Metrics export: with no endpoint the exporter is a no-op.
  metrics: { endpoint?: string }
}

// Defaults match today's shell line: every component on, no links, thresholds 60 / 80 / 80.
export const DEFAULT_OPTIONS: PgdOptions = {
  enable: true,
  components: {
    beads: true,
    assume: true,
    sessionMode: true,
    prStatus: true,
    sessionName: true,
    links: true,
    modelEffort: true,
    focus: true,
  },
  links: [],
  github: { host: 'github.com', repos: [] },
  alerts: { thresholds: { contextWarn: 60, contextAlert: 80, limit: 80 } },
  metrics: {},
}

// The options plus one message for every rule (or github entry) that was skipped. parseOptions
// runs before the log has a host, so the caller logs the messages once the log is set up.
export interface ParsedOptions {
  options: PgdOptions
  skipped: string[]
}

type Rec = Record<string, unknown>
const obj = (v: unknown): Rec => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Rec) : {})
const str = (v: unknown): string | undefined => (typeof v === 'string' ? v : undefined)

// Validate one link entry: the rule, or the reason it is skipped.
function parseLink(raw: unknown): { rule: PgdLink } | { reason: string } {
  const l = obj(raw)
  const name = str(l.name)
  if (name === undefined) return { reason: 'has no name' }
  const kind = l.kind == null ? 'regex' : l.kind
  const opts = obj(l.options)
  switch (kind) {
    case 'regex': {
      const pattern = str(l.pattern)
      const url = str(l.url)
      if (pattern === undefined || url === undefined) return { reason: 'needs a pattern and a url' }
      return { rule: l.kind == null ? { name, pattern, url } : { kind: 'regex', name, pattern, url } }
    }
    case 'github-pr': {
      const fb = opts.sessionRepoFallback
      if (fb != null && typeof fb !== 'boolean') return { reason: 'sessionRepoFallback must be a boolean' }
      return { rule: { kind: 'github-pr', name, options: { sessionRepoFallback: fb === true } } }
    }
    case 'jira': {
      const baseUrl = str(opts.baseUrl)
      let okUrl = false
      try {
        const u = new URL(baseUrl ?? '')
        okUrl = u.protocol === 'http:' || u.protocol === 'https:'
      } catch {
        okUrl = false
      }
      if (baseUrl === undefined || !okUrl) return { reason: 'jira baseUrl must be an http or https URL' }
      const keys = opts.keys == null ? [] : opts.keys
      if (!Array.isArray(keys) || !keys.every(k => typeof k === 'string' && JIRA_KEY.test(k))) {
        return { reason: 'jira keys must be project keys matching ^[A-Z][A-Z0-9_]*$' }
      }
      if (keys.length === 0) return { reason: 'inactive: jira keys is empty' }
      return { rule: { kind: 'jira', name, options: { baseUrl, keys: keys as string[] } } }
    }
    case 'file-ref': {
      const t = opts.localTemplate == null ? DEFAULT_LOCAL_TEMPLATE : opts.localTemplate
      if (typeof t !== 'string') return { reason: 'localTemplate must be a string' }
      const scheme = /^([A-Za-z][A-Za-z0-9+.-]*):/.exec(t)?.[1]?.toLowerCase()
      if (scheme === undefined || !LOCAL_TEMPLATE_SCHEMES.includes(scheme)) {
        return { reason: `localTemplate scheme must be one of ${LOCAL_TEMPLATE_SCHEMES.join(', ')}` }
      }
      if (!t.includes('{path}')) return { reason: 'localTemplate must contain {path}' }
      return { rule: { kind: 'file-ref', name, options: { localTemplate: t } } }
    }
    default:
      return { reason: `unknown kind "${String(kind)}"` }
  }
}

export function parseOptions(raw: string | undefined | null): PgdOptions {
  return parseOptionsDetailed(raw).options
}

// Parse the rendered options (JSON). Missing or malformed input yields the defaults; a present
// field overrides its default only when it has the right type. Unknown fields are ignored and a
// JSON null reads as absent. A rule that fails validation is skipped alone, with one message.
export function parseOptionsDetailed(raw: string | undefined | null): ParsedOptions {
  const d = DEFAULT_OPTIONS
  const skipped: string[] = []
  let o: Rec = {}
  try {
    o = obj(raw ? JSON.parse(raw) : {})
  } catch {
    o = {}
  }
  const comps = obj(o.components)
  const components = { ...d.components }
  for (const k of Object.keys(components) as Array<keyof PgdComponents>) {
    if (typeof comps[k] === 'boolean') components[k] = comps[k] as boolean
  }
  const th = obj(obj(o.alerts).thresholds)
  const thresholds = { ...d.alerts.thresholds }
  for (const k of Object.keys(thresholds) as Array<keyof AlertThresholds>) {
    if (typeof th[k] === 'number') thresholds[k] = th[k] as number
  }
  const links: PgdLink[] = []
  if (Array.isArray(o.links)) {
    o.links.forEach((entry: unknown, i: number) => {
      const r = parseLink(entry)
      if ('rule' in r) links.push(r.rule)
      else {
        const n = str(obj(entry).name)
        skipped.push(`link rule ${n ? `"${n}"` : `#${i + 1}`} skipped: ${r.reason}`)
      }
    })
  }
  const gh = obj(o.github)
  const repos: string[] = []
  if (Array.isArray(gh.repos)) {
    for (const r of gh.repos) {
      if (typeof r === 'string' && GITHUB_REPO.test(r)) repos.push(r)
      else skipped.push(`github repo ${JSON.stringify(r)} skipped: must be owner/repo`)
    }
  }
  const github: PgdGithub = {
    host: typeof gh.host === 'string' && gh.host !== '' ? gh.host : d.github.host,
    repos,
  }
  const endpoint = obj(o.metrics).endpoint
  return {
    options: {
      enable: typeof o.enable === 'boolean' ? o.enable : d.enable,
      components,
      links,
      github,
      alerts: { thresholds },
      metrics: typeof endpoint === 'string' && endpoint !== '' ? { endpoint } : {},
    },
    skipped,
  }
}
