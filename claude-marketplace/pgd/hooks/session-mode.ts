import { getOptions } from './options'
import { registerBandButton, type BandValues, type Level } from './band'
import { registerModalBuilder, type ModalSpec } from './modal'
import { defineSource, readCached, refresh } from './source'
import type { ModApi, PgdOptions } from './types'

// The session-mode component: the mode this session runs (drain-beads, wrap-up-session, ...), read
// through `session-mode show` with the session id passed in the run's env (the tool reads
// CLAUDE_CODE_SESSION_ID), a band button drawn only while a mode is active, the Session mode modal
// with a Stop action (`session-mode set-status stopping`) and the "wrapping up" alert (in
// alerts.ts, which asks isWrapUp). session-mode is expected on PATH from the existing session-mode
// module. The display runs no git command and the tool is used as it is.
//
// Choices made by the builder (documented in the package README, each covered by
// session-mode.test.ts):
//  - Source: id sessionMode, event-driven (intervalMs 0, no timer); it is read once at start, on
//    turn.complete and after a stop action.
//  - Throttle: a read is skipped when the previous one began less than ten seconds ago; a stop
//    action bypasses the throttle once so the button reflects the stop at once.
//  - Parsing: `session-mode show` prints one JSON record { kind, state, detail?, ... }; the value is
//    { mode: kind, state, detail? }. Exit 2 (no record) is the value undefined, not a failure; any
//    other failure (other exit, rejection, timeout, tool absent, unparseable output) also leaves the
//    value undefined and the source layer backs off.
//  - Visible: a record in ANY state, finished included, draws the button; it is hidden only while
//    no record exists for this session (operator, 2026-11-11: "if never set, it can remain hidden,
//    if ever set, it stays until the session clears"). The session clears when the session id
//    changes (after /clear: the record was read for another id) or the record is removed (`show`
//    exits 2, the fetch resolves undefined and the cached value is cleared).
//  - Active: a record whose state is not "finished". Only an active mode offers Stop.
//  - Wrapping up: the mode wrap-up-session while active (the alert stays active-only).
//  - Label: "<mode>: running|stopping|finished" plus " [detail]" when there is one; wrap-up-session
//    reads "wrapping up" while active and "wrapped up" once finished. Colour (the old status line):
//    mode magenta, wrapping up and stopping yellow, wrapped up green, other finished dim.
//    Actions: stop (not while finished) and close.
export const MODE_INTERVAL_MS = 10_000
export const MODE_TIMEOUT_MS = 10_000
export const WRAP_UP_MODE = 'wrap-up-session'

export interface ModeValue {
  mode: string
  state: string
  detail?: string
}

let nowFn: () => number = () => Date.now()
let lastReadAt: number | undefined
// The session id the cached record was read for; a band draw for another id hides the button.
let readForSession: string | undefined

// The clock the throttle reads (tests inject a fake one).
export function setSessionModeClock(now: () => number): void {
  nowFn = now
}

export function isActive(v: unknown): v is ModeValue {
  const m = v as Partial<ModeValue> | undefined
  return !!m && typeof m.mode === 'string' && m.state !== 'finished'
}

// A record exists (any state): the button stays until the session clears.
export function hasRecord(v: unknown): v is ModeValue {
  const m = v as Partial<ModeValue> | undefined
  return !!m && typeof m.mode === 'string'
}

export function modeLabel(v: ModeValue): string {
  const detail = v.detail ? ` [${v.detail}]` : ''
  if (v.mode === WRAP_UP_MODE) return `${v.state === 'finished' ? 'wrapped up' : 'wrapping up'}${detail}`
  const state = v.state === 'stopping' ? 'stopping' : v.state === 'finished' ? 'finished' : 'running'
  return `${v.mode}: ${state}${detail}`
}

export function modeLevel(v: ModeValue): Level {
  if (v.state === 'finished') return v.mode === WRAP_UP_MODE ? 'green' : 'dim'
  if (v.state === 'stopping' || v.mode === WRAP_UP_MODE) return 'yellow'
  return 'magenta'
}

export function isWrapUp(v: unknown): boolean {
  return isActive(v) && v.mode === WRAP_UP_MODE
}

export function parseMode(text: string): ModeValue {
  const o = JSON.parse(text) as { kind?: unknown; state?: unknown; detail?: unknown }
  if (typeof o.kind !== 'string' || typeof o.state !== 'string') throw new SyntaxError('parse: no kind or state')
  return { mode: o.kind, state: o.state, ...(typeof o.detail === 'string' && o.detail !== '' ? { detail: o.detail } : {}) }
}

const SHOW = ['session-mode', 'show']
const STOP = ['session-mode', 'set-status', 'stopping']

async function sessionEnv(host: ModApi): Promise<Record<string, string>> {
  return { CLAUDE_CODE_SESSION_ID: String((await host.session.id()) ?? '') }
}

const exitOf = (x: unknown): number | undefined => {
  const c = (x as { exitCode?: unknown; code?: unknown } | undefined)
  return typeof c?.exitCode === 'number' ? c.exitCode : typeof c?.code === 'number' ? c.code : undefined
}

export async function fetchMode(host: ModApi): Promise<ModeValue | undefined> {
  lastReadAt = nowFn()
  let r: unknown
  const env = await sessionEnv(host)
  readForSession = env.CLAUDE_CODE_SESSION_ID
  try {
    r = await host.process.run(SHOW, { env, timeoutMs: MODE_TIMEOUT_MS })
  } catch (err) {
    if (exitOf(err) === 2) return undefined
    throw err
  }
  const code = exitOf(r)
  if (code === 2) return undefined
  if (typeof code === 'number' && code !== 0) throw new Error(`exit ${code}`)
  return parseMode(String((r as { stdout?: unknown } | undefined)?.stdout ?? ''))
}

export function modeSpec(): ModalSpec | undefined {
  const v = readCached<ModeValue>('sessionMode')
  if (!hasRecord(v)) return undefined
  return {
    id: 'mode',
    title: 'Session mode',
    items: [
      { label: 'mode', value: v.mode },
      { label: 'state', value: v.state },
      ...(v.detail ? [{ label: 'detail', value: v.detail }] : []),
    ],
    actions: [
      ...(isActive(v) ? [{ id: 'stop', label: 'Stop' }] : []),
    ],
  }
}

// The cached record, only while it belongs to the session being drawn.
function shown(bv: BandValues): ModeValue | undefined {
  const v = readCached<ModeValue>('sessionMode')
  if (!hasRecord(v)) return undefined
  if (readForSession !== undefined && bv.sessionId && bv.sessionId !== readForSession) return undefined
  return v
}

export function startSessionMode(_options: PgdOptions, host: ModApi): void {
  lastReadAt = undefined
  readForSession = undefined
  defineSource<ModeValue>({
    id: 'sessionMode',
    intervalMs: 0,
    feeds: ['band', 'modal', 'alert'],
    fetch: ({ host: h }) => fetchMode(h),
  })
  registerBandButton({
    id: 'mode',
    order: 40,
    action: 'mode',
    label: bv => {
      const v = shown(bv)
      return v ? modeLabel(v) : undefined
    },
    level: bv => {
      const v = shown(bv)
      return v ? modeLevel(v) : undefined
    },
  })
  registerModalBuilder('mode', modeSpec, async actionId => {
    if (actionId !== 'stop') return
    try {
      await host.process.run(STOP, { env: await sessionEnv(host), timeoutMs: MODE_TIMEOUT_MS })
    } catch {
      // a failed stop is not shown; the refresh below reads the record as it is
    }
    refresh('sessionMode', { force: true })
  })
  refresh('sessionMode', { force: true })
}

// A turn finished: read the record again, at most once per ten seconds. It needs no `$`.
export function onSessionModeTurnComplete(): void {
  const o = getOptions()
  if (!o?.enable || !o.components.sessionMode) return
  if (lastReadAt !== undefined && nowFn() - lastReadAt < MODE_INTERVAL_MS) return
  refresh('sessionMode')
}
