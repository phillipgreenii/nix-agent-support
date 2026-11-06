import { getOptions } from './options'
import { registerBandButton } from './band'
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
//  - Active: a record whose state is not "finished". A finished mode draws no button.
//  - Wrapping up: the mode wrap-up-session while active.
//  - Label: the mode name, with " (stopping)" appended while it is stopping. Actions: stop and close.
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

// The clock the throttle reads (tests inject a fake one).
export function setSessionModeClock(now: () => number): void {
  nowFn = now
}

export function isActive(v: unknown): v is ModeValue {
  const m = v as Partial<ModeValue> | undefined
  return !!m && typeof m.mode === 'string' && m.state !== 'finished'
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
  try {
    r = await host.process.run(SHOW, { env: await sessionEnv(host), timeoutMs: MODE_TIMEOUT_MS })
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
  if (!isActive(v)) return undefined
  return {
    id: 'mode',
    title: 'Session mode',
    items: [
      { label: 'mode', value: v.mode },
      { label: 'state', value: v.state },
      ...(v.detail ? [{ label: 'detail', value: v.detail }] : []),
    ],
    actions: [
      { id: 'stop', label: 'Stop' },
      { id: 'close', label: 'Close' },
    ],
  }
}

export function startSessionMode(_options: PgdOptions, host: ModApi): void {
  lastReadAt = undefined
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
    label: () => {
      const v = readCached<ModeValue>('sessionMode')
      if (!isActive(v)) return undefined
      return v.state === 'stopping' ? `${v.mode} (stopping)` : v.mode
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
