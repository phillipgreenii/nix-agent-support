import { registerKind, type Candidate, type Decline, type LinkContext, type LinkKind } from './links'

// The file-ref rule kind: `path:line` (optionally `:col` or a range `-line2`) becomes a link.
// Local form (default): the path is made absolute (cwd and home come from the context snapshot,
// no existence check) and rendered through options.localTemplate with {path} (absolute, leading
// slash included), {line} and {col} (1 when the token has none). Remote form: only when the text
// before the first colon is an owner/repo in the snapshot's knownRepos. Every path segment is
// percent-encoded, with ( ) ! ' * encoded too so the markdown link cannot end early.
const DEFAULT_TEMPLATE = 'file://{path}:{line}'
const TLDS = new Set(['com', 'net', 'org', 'io', 'dev', 'edu', 'gov', 'co', 'app', 'ai', 'info', 'biz'])
// extensions macOS launches or runs when a file: URL is opened
const LAUNCHABLE = new Set(['app', 'command', 'pkg', 'dmg', 'sh', 'workflow', 'terminal', 'tool'])

// a run of path characters, at most one `prefix:` before the final `:line`, then `:col` or `-line2`
const CHARS = '[\\w.~@+()/-]'
const TOKEN = new RegExp(`(?<![\\w:])${CHARS}+(?::${CHARS}+)?:\\d+(?::\\d+|-\\d+)?(?![\\w-])(?!\\.\\d)`, 'g')
const LOCAL = /^([^:]+):(\d+)(?::(\d+)|-\d+)?$/
const REMOTE = /^([^:/]+\/[^:/]+):([^:]+):(\d+)(?::\d+|-\d+)?$/

interface Parsed {
  prefix?: string
  path: string
  line: string
  col: string
}

function validPath(path: string): boolean {
  if (path.includes('/')) return /[^/.]/.test(path)
  const m = /^[^.]+\.([A-Za-z]{1,8})$/.exec(path)
  return m !== null && !TLDS.has(m[1].toLowerCase())
}

function parse(token: string): Parsed | undefined {
  const r = REMOTE.exec(token)
  if (r && validPath(r[2])) return { prefix: r[1], path: r[2], line: r[3], col: '1' }
  const l = LOCAL.exec(token)
  if (l && validPath(l[1])) return { path: l[1], line: l[2], col: l[3] ?? '1' }
  return undefined
}

const encodeSegment = (s: string): string =>
  encodeURIComponent(s).replace(/[()!'*]/g, c => `%${c.charCodeAt(0).toString(16).toUpperCase()}`)
const encodePath = (p: string): string => p.split('/').map(encodeSegment).join('/')

function normalize(path: string): string {
  const out: string[] = []
  for (const seg of path.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') out.pop()
    else out.push(seg)
  }
  return `/${out.join('/')}`
}

const fill = (template: string, values: Record<string, string>): string =>
  template.replace(/\{(path|line|col)\}/g, (_m, k: string) => values[k])

function build(rule: { options?: { localTemplate?: string } }): LinkKind {
  const template = rule.options?.localTemplate ?? DEFAULT_TEMPLATE
  if (!template.includes('{path}')) throw new Error('localTemplate has no {path}')
  const fileScheme = /^file:/i.test(template)
  return {
    scan(text: string): Candidate[] {
      const out: Candidate[] = []
      for (const m of text.matchAll(TOKEN)) {
        let start = m.index!
        let token = m[0]
        // a leading ( is punctuation, not part of the path
        while (token.startsWith('(')) {
          token = token.slice(1)
          start++
        }
        const data = parse(token)
        if (data) out.push({ start, end: start + token.length, text: token, data })
      }
      return out
    },
    resolve(c: Candidate, _text: string, context?: LinkContext): string | Decline {
      const p = c.data as Parsed
      if (p.prefix !== undefined) {
        if (!context?.knownRepos?.includes(p.prefix)) return { decline: 'unknown-repo' }
        const host = context.githubHost ?? 'github.com'
        return `https://${host}/${p.prefix}/blob/HEAD/${encodePath(p.path.replace(/^\/+/, ''))}#L${p.line}`
      }
      let abs: string
      if (p.path.startsWith('/')) abs = p.path
      else if (p.path.startsWith('~/')) {
        if (context?.home === undefined) return { decline: 'no-context' }
        abs = normalize(`${context.home}/${p.path.slice(2)}`)
      } else {
        if (context?.cwd === undefined) return { decline: 'no-context' }
        abs = normalize(`${context.cwd}/${p.path}`)
      }
      if (fileScheme) {
        const ext = /\.([^./]+)$/.exec(abs)?.[1]?.toLowerCase()
        if (ext !== undefined && LAUNCHABLE.has(ext)) return { decline: 'unsafe' }
      }
      return fill(template, { path: encodePath(abs), line: p.line, col: p.col })
    },
  }
}

// Registers the kind with the engine (idempotent); register.ts calls it when the mod loads.
export function registerFileRefKind(): void {
  registerKind('file-ref', build)
}
