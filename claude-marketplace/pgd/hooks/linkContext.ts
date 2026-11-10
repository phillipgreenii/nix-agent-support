import { defineSource, setValue } from './source'
import type { LinkContext } from './links'
import { repoSlugOf } from './pr'
import type { ModApi, PgdOptions } from './types'

// The link context snapshot: one synchronous value the render hook reads (through
// linkContext()) so it never awaits a host call while drawing. It is a module-level value written
// only by the linkContext source's fetch (and by the one synchronous seed at session start), and
// the source is refreshed, forced past the throttle, at session.start and on every turn.complete.
//  - The source has intervalMs 0 and no feeds: it is event-driven, never polled.
//  - Every fetch re-reads host.session.cwd() and host.session.repo() (the one-shot slug in pr.ts
//    is never refreshed, so it is not reused). A read that fails or answers nothing usable keeps
//    that field's last value; a repo read that answers "no repo" clears the slug.
//  - The seed copies githubHost, githubOwners and knownRepos from options.github before the first
//    fetch, so a token-complete form links at once; the fetch adds cwd, repoSlug and home.
//  - The source value is the snapshot, so the source layer's own JSON comparison invalidates
//    ui.render only when a refreshed value differs.
export const LINK_CONTEXT_SOURCE = 'linkContext'

let snapshot: LinkContext = {}
let configuredRepos: string[] = []
let configuredHost: string | undefined

// The current snapshot (a copy, so a reader cannot change it).
export function linkContext(): LinkContext {
  return {
    ...snapshot,
    ...(snapshot.githubOwners ? { githubOwners: [...snapshot.githubOwners] } : {}),
    ...(snapshot.knownRepos ? { knownRepos: [...snapshot.knownRepos] } : {}),
  }
}

// Forget everything (a reload and the tests start from here).
export function resetLinkContext(): void {
  snapshot = {}
  configuredRepos = []
  configuredHost = undefined
}

function uniqueCaseInsensitive(items: string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const s of items) {
    const k = s.toLowerCase()
    if (seen.has(k)) continue
    seen.add(k)
    out.push(s)
  }
  return out
}

// The github fields: the session repo plus the configured repos, and the owners of both.
function githubFields(repoSlug: string | undefined): Pick<LinkContext, 'githubHost' | 'githubOwners' | 'knownRepos'> {
  const repos = uniqueCaseInsensitive([...(repoSlug ? [repoSlug] : []), ...configuredRepos])
  const owners = uniqueCaseInsensitive(repos.map(r => r.split('/')[0]))
  return {
    ...(configuredHost !== undefined ? { githubHost: configuredHost } : {}),
    githubOwners: owners,
    knownRepos: repos,
  }
}

function seedSnapshot(options: PgdOptions): void {
  configuredHost = options.github.host
  configuredRepos = [...options.github.repos]
  snapshot = githubFields(undefined)
}

export async function fetchLinkContext(host: ModApi): Promise<LinkContext> {
  let { cwd, repoSlug, home } = snapshot
  try {
    const v = await host.session.cwd()
    if (typeof v === 'string' && v !== '') cwd = v
  } catch {
    // keep the last cwd
  }
  try {
    repoSlug = repoSlugOf(await host.session.repo())
  } catch {
    // keep the last slug
  }
  try {
    const v = await host.env.home()
    if (typeof v === 'string' && v !== '') home = v
  } catch {
    // keep the last home
  }
  snapshot = {
    ...(cwd !== undefined ? { cwd } : {}),
    ...(repoSlug !== undefined ? { repoSlug } : {}),
    ...(home !== undefined ? { home } : {}),
    ...githubFields(repoSlug),
  }
  return linkContext()
}

// The component behind components.links: seed the snapshot, then define the source. The caller
// (register.ts at session.start, gitHead.ts at turn.complete) forces the refreshes.
export function startLinkContext(options: PgdOptions, _host: ModApi): void {
  resetLinkContext()
  seedSnapshot(options)
  defineSource<LinkContext>({
    id: LINK_CONTEXT_SOURCE,
    intervalMs: 0,
    fetch: ({ host: h }) => fetchLinkContext(h),
  })
  // Hand the seed to the source, so a first fetch that finds nothing new does not invalidate.
  setValue(LINK_CONTEXT_SOURCE, linkContext())
}
