import type { ModApi } from './types'
import type { Usage } from './usage'

// The rate-limit capture: each time the usage figures change, append one record to
// <claude dir>/projects/<slug>/<session-id>.status.jsonl, the file pa-monitor's limit reading, the
// recorder's claude-status source and the corpus monitor already read. The retired shell status
// line wrapper was its only writer, so this mod now is. Record shape: ts (epoch seconds) and
// session_id, plus whichever of five_hour_pct, five_hour_resets_at, seven_day_pct and
// seven_day_resets_at (epoch seconds) are present.
// Choices made by the builder:
//  - Only those fields are written. A percentage outside 0..100 or not finite, and an unparseable
//    reset, read as absent: never 0 and never clamped to a made-up value (a bogus epoch-sized
//    percentage once flooded the file, so an out-of-range value is dropped).
//  - A record is written only when the validated values differ from the last record written (the
//    timestamp is not compared), so an idle session adds nothing.
//  - $.fs.write replaces a file, so the session's text is kept in memory (seeded from the existing
//    file on the first write, so a resumed session keeps its history) and written whole through one
//    serial chain; a failed write drops that record and never throws into the hook.
export interface StatusRecord {
  ts: number
  session_id: string
  five_hour_pct?: number
  five_hour_resets_at?: number
  seven_day_pct?: number
  seven_day_resets_at?: number
}

// Claude Code names a project directory by turning every character that is not a letter or digit
// into "-".
export function projectSlug(cwd: string): string {
  return cwd.replace(/[^A-Za-z0-9]/g, '-')
}

function validPct(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 && v <= 100 ? v : undefined
}

function epoch(iso: string | undefined): number | undefined {
  if (iso === undefined) return undefined
  const ms = Date.parse(iso)
  return Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : undefined
}

// Build the record for one usage reading, or undefined when it carries no limit value at all.
export function buildRecord(usage: Usage, sessionId: string, nowSeconds: number): StatusRecord | undefined {
  const rec: StatusRecord = { ts: nowSeconds, session_id: sessionId }
  let any = false
  for (const l of usage.limits) {
    const pct = validPct(l.usedPercent)
    const rst = epoch(l.resetsAt)
    if (l.kind === 'five_hour') {
      if (pct !== undefined) rec.five_hour_pct = pct
      if (rst !== undefined) rec.five_hour_resets_at = rst
    } else {
      if (pct !== undefined) rec.seven_day_pct = pct
      if (rst !== undefined) rec.seven_day_resets_at = rst
    }
    if (pct !== undefined || rst !== undefined) any = true
  }
  return any ? rec : undefined
}

// The compared part of a record: everything but ts.
function signature(r: StatusRecord): string {
  return JSON.stringify([r.five_hour_pct, r.five_hour_resets_at, r.seven_day_pct, r.seven_day_resets_at])
}

let host: ModApi
let buffer: string | undefined
let lastSig: string | undefined
let chain: Promise<void> = Promise.resolve()
let nowMs: () => number = () => Date.now()

export function initLimitsCapture(h: ModApi, clock?: () => number): void {
  host = h
  buffer = undefined
  lastSig = undefined
  chain = Promise.resolve()
  nowMs = clock ?? (() => Date.now())
}

async function capturePath(id: string): Promise<string | undefined> {
  const cwd = String((await host.session.cwd()) ?? '')
  if (!id || !cwd) return undefined
  const configDir = await host.env.claudeConfigDir()
  const base = configDir || `${(await host.env.home()) ?? ''}/.claude`
  return `${base}/projects/${projectSlug(cwd)}/${id}.status.jsonl`
}

// Record one usage reading; returns without waiting for the write (flushLimitsCapture waits).
export function captureLimits(usage: Usage): void {
  if (!host) return
  // The record is stamped when the reading arrives, not when the serial chain reaches it.
  const ts = Math.floor(nowMs() / 1000)
  chain = chain
    .then(async () => {
      const id = String((await host.session.id()) ?? '')
      const rec = buildRecord(usage, id, ts)
      if (!rec) return
      const sig = signature(rec)
      if (sig === lastSig) return
      const path = await capturePath(id)
      if (!path) return
      if (buffer === undefined) {
        let seed = ''
        try {
          seed = String(await host.fs.read(path))
        } catch {
          // no file yet: a fresh session
        }
        buffer = seed && !seed.endsWith('\n') ? `${seed}\n` : seed
      }
      const next = `${buffer}${JSON.stringify(rec)}\n`
      await host.fs.write(path, next)
      buffer = next
      lastSig = sig
    })
    .catch(() => undefined)
}

// Resolves when every queued record has been handled (tests await it).
export function flushLimitsCapture(): Promise<void> {
  return chain
}
