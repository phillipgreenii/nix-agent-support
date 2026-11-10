import { pgdEnabled } from './options'
import { defineSource, noteEvent, refresh } from './source'
import { LINK_CONTEXT_SOURCE } from './linkContext'
import type { ModApi } from './types'
import { recordCompaction } from './context-modal'
import { onSessionModeTurnComplete } from './session-mode'
import { refreshSessionName } from './session-name'

// The branch and worktree source. The display never runs a git command: it reads <cwd>/.git
// through host.fs.read. A .git file holds `gitdir: <path>` (a worktree; a relative path is
// resolved against the cwd and is the worktree value) and HEAD is read from that directory; a .git
// directory gives <cwd>/.git/HEAD and no worktree. Because the host reads a directory as a
// rejection, a failed read of .git itself falls back to <cwd>/.git/HEAD; a missing HEAD makes the
// value undefined without counting as a failure. Every fetch re-reads host.session.cwd(), so a
// change of directory shows at the next fetch (at most 10 seconds).
export interface GitHead {
  branch?: string
  detached?: string
  worktree?: string
}

const HEX_ID = /^[0-9a-f]{40}([0-9a-f]{24})?$/i

// Join a (possibly relative) path onto a base directory, resolving . and .. segments.
function resolvePath(base: string, p: string): string {
  if (p.startsWith('/') || !base) return p
  const out: string[] = []
  for (const seg of `${base}/${p}`.split('/')) {
    if (seg === '..') out.pop()
    else if (seg !== '.' && seg !== '') out.push(seg)
  }
  return '/' + out.join('/')
}

function pointerPath(pointerText: string, baseDir?: string): string | undefined {
  const m = /^gitdir:\s*(.+?)\s*$/m.exec(pointerText.trim())
  if (!m) return undefined
  return baseDir ? resolvePath(baseDir, m[1]) : m[1]
}

export function parseGitHead(headText: string, pointerText?: string, baseDir?: string): GitHead {
  const out: GitHead = {}
  const head = headText.trim()
  const ref = /^ref:\s*(\S+)/.exec(head)
  if (ref) out.branch = ref[1].replace(/^refs\/heads\//, '')
  else if (HEX_ID.test(head)) out.detached = head
  if (pointerText !== undefined) {
    const w = pointerPath(pointerText, baseDir)
    if (w !== undefined) out.worktree = w
  }
  return out
}

async function readOrUndefined(host: ModApi, path: string): Promise<string | undefined> {
  try {
    const t = await host.fs.read(path)
    return typeof t === 'string' ? t : undefined
  } catch {
    return undefined
  }
}

export async function fetchGitHead(host: ModApi): Promise<GitHead | undefined> {
  const cwd = await host.session.cwd()
  const pointer = await readOrUndefined(host, `${cwd}/.git`)
  const gitdir = pointer === undefined ? undefined : pointerPath(pointer, cwd)
  const headText = await readOrUndefined(host, `${gitdir ?? `${cwd}/.git`}/HEAD`)
  if (headText === undefined) return undefined
  return parseGitHead(headText, gitdir === undefined ? undefined : pointer, cwd)
}

export function startGitHead(_host: ModApi): void {
  defineSource<GitHead>({
    id: 'gitHead',
    intervalMs: 10000,
    feeds: ['band', 'modal'],
    fetch: ({ host: h }) => fetchGitHead(h),
  })

}

// Refetch triggers (they need no `$`: the fetch reads through the facade).
export function registerGitHeadHooks(on: ModApi): void {
  on('turn.complete', (_$: unknown, e: unknown, next: (e: unknown) => unknown) => {
    if (pgdEnabled()) {
      noteEvent()
      refresh('gitHead')
      refresh(LINK_CONTEXT_SOURCE, { force: true })
      refreshSessionName()
      onSessionModeTurnComplete()
    }
    return next(e)
  })
  // The one session.compact hook (the engine refuses a second without a matcher): it also records
  // the time of the compaction for the Context modal, once the compaction has run.
  on('session.compact', async (_$: unknown, e: unknown, next: (e: unknown) => unknown) => {
    const result = await next(e)
    if (pgdEnabled()) {
      noteEvent()
      refresh('gitHead')
      recordCompaction()
    }
    return result
  })
}
