import { defineSource, readCached, refresh } from './source'
import type { ModApi } from './types'

// The compaction marker source. The PostCompact hook of the session-mode plugin writes
// <transcript_dir>/<session_id>.compaction.json beside the transcript (ADR 0096): one line,
// {"ts":<epoch seconds>,"trigger":"manual|auto|unknown"}. This source reads it through the facade's
// fs.read, so the "last compaction" survives a restart or a resume (the in-memory lastCompaction
// event does not). The transcript itself is never read and no process runs.
//
// Choices the design leaves to the builder (covered by compaction.test.ts):
//  - Path: the facade has no transcript path, so the directory is derived the way Claude Code names
//    it: <claude config dir or ~/.claude>/projects/<cwd with every non-alphanumeric character
//    replaced by "-">. A marker that is not found there leaves the value undefined (no note).
//  - Reads: event-driven, never polled and no timer: one read at the first turn.complete and one after each
//    session.compact (this process sees its own compactions). A missing file is a value (absent),
//    not a failure, and a malformed or zero-byte file is also absent.
//  - Window: the note is shown while the marker's age is below COMPACTION_WINDOW_MS (30 minutes,
//    the default window of the old status line). The cached value is not cleared at the end of
//    the window; the age is computed at draw time, so the note disappears at the next redraw.
//  - Note: a suffix of the context label, "<glyph><age>" with age "<1m", "Nm" or "Nh".
export const COMPACTION_ID = 'compactionMarker'
export const COMPACTION_WINDOW_MS = 30 * 60 * 1000
export const COMPACTION_GLYPH = '↻'

export interface CompactionMarker {
  // epoch seconds, as the hook wrote it
  ts: number
  trigger: 'manual' | 'auto' | 'unknown'
}

let nowFn: () => number = () => Date.now()

// The clock the note's age reads (tests inject a fake one).
export function setCompactionClock(now: () => number): void {
  nowFn = now
}

// ts MUST be a positive integer (ADR 0096 item 7); a trigger other than manual or auto is unknown.
export function parseMarker(text: string): CompactionMarker | undefined {
  let o: unknown
  try {
    o = JSON.parse(text)
  } catch {
    return undefined
  }
  const m = o as { ts?: unknown; trigger?: unknown } | null
  if (!m || typeof m !== 'object' || typeof m.ts !== 'number' || !Number.isInteger(m.ts) || m.ts <= 0) return undefined
  return { ts: m.ts, trigger: m.trigger === 'manual' || m.trigger === 'auto' ? m.trigger : 'unknown' }
}

export const projectDirName = (cwd: string): string => cwd.replace(/[^A-Za-z0-9]/g, '-')

export async function markerPath(host: ModApi): Promise<string | undefined> {
  const id = String((await host.session.id()) ?? '')
  const cwd = String((await host.session.cwd()) ?? '')
  if (!id || !cwd) return undefined
  const config = await host.env.claudeConfigDir()
  const home = config ? undefined : await host.env.home()
  const base = config || (home ? `${home}/.claude` : undefined)
  return base === undefined ? undefined : `${base}/projects/${projectDirName(cwd)}/${id}.compaction.json`
}

export async function fetchCompaction(host: ModApi): Promise<CompactionMarker | undefined> {
  const path = await markerPath(host)
  if (path === undefined) return undefined
  let text: unknown
  try {
    text = await host.fs.read(path)
  } catch {
    return undefined // no marker (never compacted) or unreadable: no note
  }
  return typeof text === 'string' ? parseMarker(text) : undefined
}

export function startCompaction(_host: ModApi): void {
  defineSource<CompactionMarker>({
    id: COMPACTION_ID,
    intervalMs: 0,
    feeds: ['band', 'modal'],
    fetch: ({ host: h }) => fetchCompaction(h),
  })
  readOnce = false
}

// The first turn.complete reads the marker (so a restart or resume shows the note); later calls do
// nothing. Registration itself makes no host call.
let readOnce = false
export function readCompactionOnce(): void {
  if (readOnce) return
  readOnce = true
  refresh(COMPACTION_ID)
}

// After a compaction ran: read the marker now (forced past the throttle).
export function refreshCompaction(): void {
  refresh(COMPACTION_ID, { force: true })
}

// The cached marker's time in epoch milliseconds, or undefined.
export function markerAt(): number | undefined {
  const m = readCached<CompactionMarker>(COMPACTION_ID)
  return m ? m.ts * 1000 : undefined
}

export function formatAge(ageMs: number): string {
  const minutes = Math.floor(Math.max(0, ageMs) / 60_000)
  if (minutes < 1) return '<1m'
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h`
}

// The note for a marker time while it is recent ("↻14m"), else undefined. A negative age (clock
// skew) is clamped to zero.
export function compactionNote(at: number | undefined, now: number = nowFn()): string | undefined {
  if (at === undefined) return undefined
  const age = Math.max(0, now - at)
  return age < COMPACTION_WINDOW_MS ? `${COMPACTION_GLYPH}${formatAge(age)}` : undefined
}

// The note from the cached marker, for the band's context label.
export function currentCompactionNote(): string | undefined {
  return compactionNote(markerAt())
}
