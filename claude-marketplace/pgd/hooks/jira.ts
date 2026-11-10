import { registerKind, type Candidate, type Decline, type LinkKind } from './links'

// The jira rule kind: issue keys from an allowlist of project keys become links to
// `<baseUrl>/<key>`. The scanner is built from the allowlist, `\b(?:K1|K2)-[0-9]+\b`, case
// sensitive with the keys regex-escaped, so UTF-8 or SHA-256 never match unless listed. An empty
// allowlist makes the rule inactive (it links nothing). A candidate sitting in a path is declined
// as adjacent: preceded by / or \, followed by . and a word character, or followed by : and a
// digit. A / that directly follows a complete allowlisted key-number token does not count, so
// `ABC-1/ABC-2` links both keys while `a/ABC-12` stays plain.
const escape = (s: string): string => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

function build(rule: { options?: { baseUrl?: string; keys?: string[] } }): LinkKind {
  const keys = rule.options?.keys ?? []
  const baseUrl = (rule.options?.baseUrl ?? '').replace(/\/+$/, '')
  if (keys.length === 0) return { scan: () => [], resolve: () => ({ decline: 'unsafe' }) }
  const alt = keys.map(escape).join('|')
  const token = new RegExp(`\\b(?:${alt})-[0-9]+\\b`, 'g')
  const keyBefore = new RegExp(`\\b(?:${alt})-[0-9]+$`)
  return {
    scan(text: string): Candidate[] {
      return [...text.matchAll(token)].map(m => ({ start: m.index!, end: m.index! + m[0].length, text: m[0] }))
    },
    resolve(c: Candidate, text: string): string | Decline {
      const before = text[c.start - 1]
      if (before === '\\') return { decline: 'adjacent' }
      if (before === '/' && !keyBefore.test(text.slice(0, c.start - 1))) return { decline: 'adjacent' }
      const next = text[c.end]
      const after = text[c.end + 1] ?? ''
      if ((next === '.' && /\w/.test(after)) || (next === ':' && /\d/.test(after))) return { decline: 'adjacent' }
      return `${baseUrl}/${c.text}`
    },
  }
}

// Registers the kind with the engine (idempotent); register.ts calls it when the mod loads.
export function registerJiraKind(): void {
  registerKind('jira', build)
}
