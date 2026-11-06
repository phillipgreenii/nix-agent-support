import { defineSource, onChange, readCached, refresh, setValue } from './source'
import type { GitHead } from './gitHead'
import type { ModApi, PgdOptions } from './types'

// The PR source behind components.prStatus: the PR number and review state of the current branch,
// from `gh pr view`, run through host.process.run with the branch and the repo passed explicitly
// (`gh pr view <branch> -R <owner>/<repo> --json number,reviewDecision`). With both passed gh runs
// no git, so it cannot contend for a git lock; a bare `gh pr view` needs git. The display itself
// never runs a git command. gh is expected on PATH from the module that installs it.
//
// Choices made by the builder (documented in the package README, each covered by pr.test.ts):
//  - Interval: at most one fetch every five minutes (intervalMs 300000), shared across sessions
//    through the store under the key "<owner/name>#<branch>", so a session that finds the entry
//    fresh does not fetch and two branches of one repo never read each other's entry.
//  - Branch change: the cached value is cleared at once (the old branch's PR is never shown for the
//    new branch) and one forced refresh bypasses the throttle and the failure backoff. A change
//    while the Info modal is closed is remembered and fetched when the modal next opens.
//  - No branch (detached or no .git) or no repo: no key, so no fetch and no PR row.
//  - Repo: host.session.repo() is read once at start and memoized; name is used when it is
//    "owner/repo", otherwise the remote URL is parsed. Until then the key is undefined.
//  - Empty reviewDecision (no review required or none yet): the value is kept with an empty review
//    state, and the PR row shows the number alone, uncoloured.
//  - A failed gh call (non-zero exit, rejection, timeout, gh absent, unparseable output, no PR for
//    the branch): the value becomes undefined (the row is omitted), nothing else changes, and the
//    source layer backs off (doubling, capped at ten minutes) before the next attempt.
export const PR_INTERVAL_MS = 300_000
export const PR_TIMEOUT_MS = 15_000

export interface PrValue {
  number: number
  reviewState: string
}

let repoSlug: string | undefined
let requestedKey: string | undefined // the key of the newest fetch request, so one change asks once

export function repoSlugOf(repo: { name?: string | null; remote?: string | null } | null | undefined): string | undefined {
  if (!repo) return undefined
  if (typeof repo.name === 'string' && /^[^/\s]+\/[^/\s]+$/.test(repo.name)) return repo.name
  const m = /[:/]([^/:\s]+)\/([^/\s]+?)(?:\.git)?\/?$/.exec(repo.remote ?? '')
  return m ? `${m[1]}/${m[2]}` : undefined
}

function currentBranch(): string | undefined {
  return readCached<GitHead>('gitHead')?.branch
}

export function prKey(): string | undefined {
  const b = currentBranch()
  return repoSlug && b ? `${repoSlug}#${b}` : undefined
}

export function prArgv(repo: string, branch: string): string[] {
  return ['gh', 'pr', 'view', branch, '-R', repo, '--json', 'number,reviewDecision']
}

export function parsePr(text: string): PrValue {
  const o = JSON.parse(text) as { number?: unknown; reviewDecision?: unknown }
  if (typeof o.number !== 'number') throw new SyntaxError('parse: no PR number')
  return { number: o.number, reviewState: typeof o.reviewDecision === 'string' ? o.reviewDecision : '' }
}

export async function fetchPr(host: ModApi): Promise<PrValue | undefined> {
  const branch = currentBranch()
  if (!repoSlug || !branch) return undefined
  try {
    const r = await host.process.run(prArgv(repoSlug, branch), { timeoutMs: PR_TIMEOUT_MS })
    if (typeof r?.exitCode === 'number' && r.exitCode !== 0) throw new Error(`exit ${r.exitCode}`)
    return parsePr(String(r?.stdout ?? ''))
  } catch (err) {
    setValue('pr', undefined)
    throw err
  }
}

export function startPr(_options: PgdOptions, host: ModApi): void {
  repoSlug = undefined
  requestedKey = undefined
  let lastBranch: string | undefined
  defineSource<PrValue>({
    id: 'pr',
    intervalMs: PR_INTERVAL_MS,
    shared: { key: prKey },
    feeds: ['modal'],
    fetch: ({ host: h }) => fetchPr(h),
  })
  // One forced refresh per key: the repo arriving and the branch arriving (in either order, or
  // while a fetch is in flight) ask for the same PR once.
  const requestFetch = (): void => {
    const k = prKey()
    if (k === undefined || k === requestedKey) return
    requestedKey = k
    refresh('pr', { force: true })
  }
  const branchChanged = (): void => {
    const b = currentBranch()
    if (b === lastBranch) return
    lastBranch = b
    setValue('pr', undefined)
    requestFetch()
  }
  onChange('gitHead', branchChanged)
  void (async () => {
    try {
      repoSlug = repoSlugOf(await host.session.repo())
    } catch {
      repoSlug = undefined
    }
    requestFetch()
  })()
}
