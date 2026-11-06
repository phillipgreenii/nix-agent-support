import { getOptions } from './options'
import { defineSource, refresh } from './source'
import type { ModApi, PgdOptions } from './types'

// The session name source. Claude Code keeps a registry entry per session at
// ~/.claude/sessions/<pid>.json holding sessionId, name, nameSource and nameSince. The first fetch
// lists that directory through the facade and reads each .json entry until one has a sessionId
// equal to the session id; the matched path is cached, and later fetches re-read only that path.
// The name is shown when nameSource is not `derived`; otherwise (a derived name, a missing,
// malformed or other-session entry, an unreadable registry) the value is undefined and the band
// shows the session id. The transcript is never read and no process is run.
//
// Choices the design leaves to the builder (documented in the README and covered by tests):
//  - A malformed or partially written entry yields undefined (the id fallback) and keeps the cached
//    path, because a half-written file is transient.
//  - A read of the cached path that fails (the file is gone) or that holds another sessionId (the
//    pid was reused) drops the cached path and discovers again within the same fetch.
export const SESSION_NAME_ID = 'sessionName'
export const SESSION_NAME_INTERVAL_MS = 10_000

interface Entry {
  sessionId?: unknown
  name?: unknown
  nameSource?: unknown
}

export function parseEntry(text: string): Entry | undefined {
  try {
    const v: unknown = JSON.parse(text)
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Entry) : undefined
  } catch {
    return undefined
  }
}

// The name an entry supplies: a non-empty string name whose nameSource is a string other than
// `derived`.
export function nameOf(entry: Entry): string | undefined {
  if (typeof entry.nameSource !== 'string' || entry.nameSource === 'derived') return undefined
  return typeof entry.name === 'string' && entry.name.trim() !== '' ? entry.name : undefined
}

let cachedPath: string | undefined

export function resetSessionName(): void {
  cachedPath = undefined
}

async function readText(host: ModApi, path: string): Promise<string | undefined> {
  try {
    const t = await host.fs.read(path)
    return typeof t === 'string' ? t : undefined
  } catch {
    return undefined
  }
}

async function discover(host: ModApi, dir: string, sessionId: string): Promise<string | undefined> {
  let names: string[]
  try {
    const list: Array<{ name?: string; kind?: string }> = (await host.fs.list(dir)) ?? []
    names = list
      .filter(f => typeof f.name === 'string' && f.name.endsWith('.json') && (f.kind === undefined || f.kind === 'file'))
      .map(f => f.name as string)
  } catch {
    return undefined
  }
  for (const n of names) {
    const path = `${dir}/${n}`
    const text = await readText(host, path)
    const entry = text === undefined ? undefined : parseEntry(text)
    if (entry && entry.sessionId === sessionId) {
      cachedPath = path
      return path
    }
  }
  return undefined
}

export async function fetchSessionName(host: ModApi): Promise<string | undefined> {
  const sessionId = String((await host.session.id()) ?? '')
  if (!sessionId) return undefined
  if (cachedPath !== undefined) {
    const text = await readText(host, cachedPath)
    if (text !== undefined) {
      const entry = parseEntry(text)
      if (!entry) return undefined // malformed or partly written: id fallback, keep the path
      if (entry.sessionId === sessionId) return nameOf(entry)
    }
    cachedPath = undefined // gone, or another session now owns the path: discover again
  }
  const home = await host.env.home()
  if (!home) return undefined
  const path = await discover(host, `${home}/.claude/sessions`, sessionId)
  if (path === undefined) return undefined
  const entry = parseEntry((await readText(host, path)) ?? '')
  return entry ? nameOf(entry) : undefined
}

export function startSessionName(_options: PgdOptions, _host: ModApi): void {
  resetSessionName()
  defineSource<string>({
    id: SESSION_NAME_ID,
    intervalMs: SESSION_NAME_INTERVAL_MS,
    feeds: ['band', 'modal'],
    fetch: ({ host: h }) => fetchSessionName(h),
  })
}

// Re-read after turn.complete (the source layer throttles to one fetch per interval). A hook that
// takes `$` from its own callback is not needed: the fetch reads through the facade.
export function refreshSessionName(): void {
  const o = getOptions()
  if (o?.enable && o.components.sessionName) refresh(SESSION_NAME_ID)
}
