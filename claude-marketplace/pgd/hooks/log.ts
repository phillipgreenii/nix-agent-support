import type { ModApi } from './types'

// One JSON line per event, appended to $XDG_STATE_HOME/pgd/<session-id>.log (or
// ~/.local/state/pgd/ when unset), rotated by size to <session-id>.log.1 (an older .1 is
// replaced). Never read on a draw: the file is only ever written. Choices made by the builder:
//  - $.fs.write replaces a file and corrupts under concurrent calls, so the logger keeps the
//    current file's text in memory and writes the whole text through one serial chain; log() only
//    enqueues and returns, so it never blocks a draw. A failed write drops the line (logging
//    never throws).
//  - Cap: 256 KiB. When the next line would pass the cap, the current text is written to .log.1
//    and a fresh file starts.
export type Level = 'debug' | 'info' | 'warn' | 'error'
export const LOG_CAP_BYTES = 256 * 1024

let host: ModApi
let buffer = ''
let chain: Promise<void> = Promise.resolve()
let nowIso = (): string => new Date().toISOString()

export function initLog(h: ModApi, clock?: () => string): void {
  host = h
  buffer = ''
  chain = Promise.resolve()
  nowIso = clock ?? (() => new Date().toISOString())
}

async function logPath(): Promise<string | undefined> {
  const id = String((await host.session.id()) ?? '')
  if (!id) return undefined
  const state = await host.env.stateHome()
  const base = state || `${(await host.env.home()) ?? ''}/.local/state`
  return `${base}/pgd/${id}.log`
}

export function log(level: Level, source: string, message: string, errorKind?: string): void {
  if (!host) return
  const line = `${JSON.stringify({ time: nowIso(), level, source, message, errorKind })}\n`
  chain = chain
    .then(async () => {
      const path = await logPath()
      if (!path) return
      if (buffer.length > 0 && buffer.length + line.length > LOG_CAP_BYTES) {
        await host.fs.write(`${path}.1`, buffer)
        buffer = ''
      }
      buffer += line
      await host.fs.write(path, buffer)
    })
    .catch(() => undefined)
}

// Resolves when every queued line has been written (tests await it).
export function flushLog(): Promise<void> {
  return chain
}
