import { registerKind, type Candidate, type Decline, type LinkContext, type LinkKind } from './links'

// The github-pr rule kind: pull request references in assistant text become links to the pull
// request. Scanner forms: `owner/repo#N`; `PR N` and `PR #N` with PR in uppercase only (a
// lowercase `pr 5 times` is prose); `pull request N`, `pull request #N` and `pull #N` in any case.
// A bare `#N` is never scanned. The link text is the whole token.
//
// Repo resolution, first hit wins: the explicit owner/repo (its owner must be the owner of a known
// repo, else unknown-repo); the nearest earlier known `owner/repo` written in full in the same
// paragraph (a bare repo name never counts; a mention an earlier rule already rewrote is not
// visible); with sessionRepoFallback, the session repo; else no-repo. Owner and repo comparisons
// ignore case; the link keeps the case the text used.
const TOKEN =
  /(?<![\w-])(?:([A-Za-z0-9-]+)\/([\w.-]+)#(\d+)|(PR)[ \t]+#?(\d+)|pull[ \t]+request[ \t]+#?(\d+)|pull[ \t]+#(\d+))(?!\w)/gi
// An owner/repo written in full (not part of a longer path or word).
const MENTION = /(?<![\w/\\.-])([A-Za-z0-9-]+)\/([\w.-]+)/g
const OWNER = /^[A-Za-z0-9-]+$/
const REPO = /^(?!\.{1,2}$)[\w.-]+$/
const HOST = /^[A-Za-z0-9.-]+(?::\d+)?$/

interface PrData {
  n: string
  explicit?: { owner: string; repo: string }
}

function scan(text: string): Candidate[] {
  const out: Candidate[] = []
  for (const m of text.matchAll(TOKEN)) {
    const [token, owner, repo, nExplicit, pr, nPr, nLong, nPull] = m
    if (pr !== undefined && pr !== 'PR') continue // lowercase pr is prose
    const data: PrData = owner !== undefined ? { n: nExplicit, explicit: { owner, repo } } : { n: nPr ?? nLong ?? nPull }
    out.push({ start: m.index!, end: m.index! + token.length, text: token, data })
  }
  return out
}

// A candidate sitting in a path is not a reference: preceded by / or \, followed by . and a word
// character, or followed by : and a digit.
function adjacent(text: string, c: Candidate): boolean {
  const before = text[c.start - 1]
  if (before === '/' || before === '\\') return true
  const next = text[c.end]
  const after = text[c.end + 1] ?? ''
  return (next === '.' && /\w/.test(after)) || (next === ':' && /\d/.test(after))
}

// The nearest earlier known owner/repo in the same paragraph as the candidate, as written.
function mentionBefore(text: string, start: number, known: Set<string>): string | undefined {
  const head = text.slice(0, start)
  const breaks = [...head.matchAll(/\n[ \t]*\n/g)]
  const last = breaks.length > 0 ? breaks[breaks.length - 1] : undefined
  const paragraph = last ? head.slice(last.index! + last[0].length) : head
  let found: string | undefined
  for (const m of paragraph.matchAll(MENTION)) {
    const repo = m[2].replace(/\.+$/, '')
    const end = m.index! + m[1].length + 1 + repo.length
    if (paragraph[end] === '/') continue // a path below the repo, not a mention of it
    const slug = `${m[1]}/${repo}`
    if (known.has(slug.toLowerCase())) found = slug
  }
  return found
}

function build(fallback: boolean): LinkKind {
  return {
    scan,
    resolve(c: Candidate, text: string, context?: LinkContext): string | Decline {
      if (adjacent(text, c)) return { decline: 'adjacent' }
      if (!context) return { decline: 'no-context' }
      const data = c.data as PrData
      const known = new Set((context.knownRepos ?? []).map(r => r.toLowerCase()))
      const owners = new Set((context.githubOwners ?? []).map(o => o.toLowerCase()))
      let slug: string | undefined
      if (data.explicit) {
        if (!owners.has(data.explicit.owner.toLowerCase())) return { decline: 'unknown-repo' }
        slug = `${data.explicit.owner}/${data.explicit.repo}`
      } else {
        slug = mentionBefore(text, c.start, known)
        if (slug === undefined && fallback) slug = context.repoSlug
        if (slug === undefined || slug === '') return { decline: 'no-repo' }
      }
      const [owner, repo, ...rest] = slug.split('/')
      const host = context.githubHost ?? 'github.com'
      if (rest.length > 0 || !OWNER.test(owner ?? '') || !REPO.test(repo ?? '') || !HOST.test(host)) {
        return { decline: 'unsafe' }
      }
      return `https://${host}/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/pull/${data.n}`
    },
  }
}

// Registers the kind with the engine (idempotent); register.ts calls it when the mod loads.
export function registerGithubPrKind(): void {
  registerKind('github-pr', (rule: { options?: { sessionRepoFallback?: boolean } }) =>
    build(rule.options?.sessionRepoFallback === true),
  )
}
