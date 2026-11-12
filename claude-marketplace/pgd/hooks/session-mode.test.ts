// Tests of the session-mode component: the source (`session-mode show` with the session id in the
// run's env), the band button, the Session mode modal with its Stop action, the "wrapping up" alert,
// the opt-out and the choices documented in the README. The first tests run the REAL session-mode
// binary against a throwaway state directory (a mock of the tool does not count); the node module
// is reached through process.getBuiltinModule, so the file has no import the engine loader refuses.
import { expect, test } from 'claude-code/testing'
import { alertRow, resetAlertState } from './alerts'
import { clearBandButtons, pressButton, renderBand } from './band'
import { registerComponents } from './components'
import { registerGitHeadHooks } from './gitHead'
import { resetModals, setModalHost, pressAction, type ModalSpec } from './modal'
import { setOptions } from './options'
import { MODE_INTERVAL_MS, modeSpec, parseMode, setSessionModeClock, WRAP_UP_MODE } from './session-mode'
import { initSources, readCached, refresh, setValue } from './source'
import { resetToggles } from './toggles'
import { DEFAULT_OPTIONS, type PgdOptions } from './types'
import { fakeClock, settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const only = (sessionMode: boolean): PgdOptions => ({
  ...DEFAULT_OPTIONS,
  components: { ...DEFAULT_OPTIONS.components, beads: false, assume: false, prStatus: false, sessionName: false, links: false, sessionMode },
})

type Answer = { exitCode: number; stdout?: string } | Error
interface Call {
  argv: string[]
  env: Record<string, string>
}

// One session: sources started on a fake clock, the component registered, a recording modal host.
async function world(run: (argv: string[], env: Record<string, string>) => Promise<unknown>, options = only(true)) {
  resetToggles()
  resetModals()
  clearBandButtons()
  resetAlertState()
  const clock = fakeClock()
  const sh = stubHost()
  const calls: Call[] = []
  sh.host.process.run = async (argv: string[], init: { env?: Record<string, string> }) => {
    sh.calls.push('process.run')
    calls.push({ argv, env: init?.env ?? {} })
    return run(argv, init?.env ?? {})
  }
  const opened: ModalSpec[] = []
  setModalHost({ open: s => void opened.push(s), close: () => undefined })
  setOptions(options)
  setSessionModeClock(clock.now)
  initSources(sh.host, { now: clock.now, setTimer: clock.setTimer })
  registerComponents(options, sh.host)
  await clock.advance(1)
  return { clock, sh, calls, opened }
}

const record = (kind: string, state = 'running', detail?: string): Answer => ({
  exitCode: 0,
  stdout: JSON.stringify({ kind, state, ...(detail ? { detail } : {}) }),
})
const ids = (): string[] => renderBand({ sessionId: 'sess-1' }, 200, T).flat().map(l => l.id)

test('the session id goes in the run env and the source holds mode, state and detail', async () => {
  const w = await world(async () => record('drain-beads', 'running', 'P1 only'))
  expect(w.calls[0].argv).toEqual(['session-mode', 'show'])
  expect(w.calls[0].env).toEqual({ CLAUDE_CODE_SESSION_ID: 'sess-1' })
  expect(readCached('sessionMode')).toEqual({ mode: 'drain-beads', state: 'running', detail: 'P1 only' })
})

test('exit 2 (no record) and a failed read draw no button', async () => {
  const none = await world(async () => ({ exitCode: 2 }))
  expect(readCached('sessionMode')).toBe(undefined)
  expect(ids().includes('mode')).toBe(false)
  const bad = await world(async () => ({ exitCode: 1 }))
  expect(readCached('sessionMode')).toBe(undefined)
  expect(ids().includes('mode')).toBe(false)
  void bad
  const junk = await world(async () => ({ exitCode: 0, stdout: 'not json' }))
  expect(ids().includes('mode')).toBe(false)
  void junk
  const rejected = await world(async () => {
    throw Object.assign(new Error('boom'), { exitCode: 2 })
  })
  expect(readCached('sessionMode')).toBe(undefined)
  void rejected
  void none
})

const modeRow = (sid = 'sess-1') => renderBand({ sessionId: sid }, 200, T).flat().find(l => l.id === 'mode')

test('a record in any state keeps the button, with state, detail and the old-line colour', async () => {
  await world(async () => record('drain-beads', 'running', 'P1 only'))
  expect(modeRow()).toEqual({ id: 'mode', label: 'drain-beads: running [P1 only]', level: 'magenta', action: 'mode' })
  setValue('sessionMode', { mode: 'drain-beads', state: 'stopping' })
  expect(modeRow()).toEqual({ id: 'mode', label: 'drain-beads: stopping', level: 'yellow', action: 'mode' })
  setValue('sessionMode', { mode: 'drain-beads', state: 'finished', detail: 'done' })
  expect(modeRow()).toEqual({ id: 'mode', label: 'drain-beads: finished [done]', level: 'dim', action: 'mode' })
  setValue('sessionMode', { mode: WRAP_UP_MODE, state: 'running' })
  expect(modeRow()).toEqual({ id: 'mode', label: 'wrapping up', level: 'yellow', action: 'mode' })
  setValue('sessionMode', { mode: WRAP_UP_MODE, state: 'finished' })
  expect(modeRow()).toEqual({ id: 'mode', label: 'wrapped up', level: 'green', action: 'mode' })
})

test('a finished record read from the tool is drawn; a later undefined read clears the button', async () => {
  let answer: Answer = record('drain-beads', 'finished')
  const w = await world(async () => answer)
  expect(modeRow()?.label).toBe('drain-beads: finished')
  answer = { exitCode: 2 }
  setSessionModeClock(() => w.clock.now() + 60_000)
  refresh('sessionMode', { force: true })
  await settle()
  expect(readCached('sessionMode')).toBe(undefined)
  expect(ids().includes('mode')).toBe(false)
})

test('a new session id hides the record read for the old one', async () => {
  await world(async () => record('drain-beads'))
  expect(modeRow('sess-1')?.id).toBe('mode')
  expect(modeRow('sess-2')).toBe(undefined)
})

test('Stop is offered only while the mode is not finished', async () => {
  const w = await world(async () => record('drain-beads'))
  setValue('sessionMode', { mode: 'drain-beads', state: 'finished' })
  expect(modeSpec()?.actions.map(a => a.id)).toEqual([])
  setValue('sessionMode', { mode: 'drain-beads', state: 'running' })
  expect(modeSpec()?.actions.map(a => a.id)).toEqual(['stop'])
  void w
})

test('the modal lists mode, state and detail one per line; Stop runs set-status stopping and refreshes', async () => {
  let state = 'running'
  const w = await world(async argv => {
    if (argv[1] === 'set-status') {
      state = 'stopping'
      return { exitCode: 0 }
    }
    return record('drain-beads', state, 'P1 only')
  })
  pressButton('mode')
  await settle()
  expect(w.opened).toHaveLength(1)
  expect(w.opened[0].title).toBe('Session mode')
  expect(w.opened[0].items.map(i => `${i.label}: ${i.value}`)).toEqual(['mode: drain-beads', 'state: running', 'detail: P1 only'])
  expect(w.opened[0].actions.map(a => a.id)).toEqual(['stop'])
  // a press shortly after the first read: Stop still reads at once (the throttle is bypassed)
  pressAction('mode', 'stop')
  await settle()
  const stop = w.calls.find(c => c.argv[1] === 'set-status')
  expect(stop?.argv).toEqual(['session-mode', 'set-status', 'stopping'])
  expect(stop?.env).toEqual({ CLAUDE_CODE_SESSION_ID: 'sess-1' })
  expect(readCached('sessionMode')).toEqual({ mode: 'drain-beads', state: 'stopping', detail: 'P1 only' })
  expect(w.calls.filter(c => c.argv[1] === 'show')).toHaveLength(2)
})

test('the wrapping up alert appears only while a wrap-up mode is active', async () => {
  await world(async () => record(WRAP_UP_MODE))
  expect(alertRow()).toBe('wrapping up')
  setValue('sessionMode', { mode: WRAP_UP_MODE, state: 'finished' })
  expect(alertRow()).toBe('')
  setValue('sessionMode', { mode: 'drain-beads', state: 'running' })
  expect(alertRow()).toBe('')
  setValue('sessionMode', undefined)
  expect(alertRow()).toBe('')
  setValue('sessionMode', { mode: WRAP_UP_MODE, state: 'stopping' })
  expect(alertRow()).toBe('wrapping up')
})

test('with components.sessionMode off nothing is registered: no read, button, modal, source, timer or alert', async () => {
  const w = await world(async () => record(WRAP_UP_MODE), only(false))
  expect(w.calls).toHaveLength(0)
  expect(w.sh.calls).toEqual([])
  setValue('sessionMode', { mode: WRAP_UP_MODE, state: 'running' })
  expect(readCached('sessionMode')).toBe(undefined)
  expect(ids().includes('mode')).toBe(false)
  expect(alertRow()).toBe('')
  pressButton('mode')
  await settle()
  expect(w.opened).toHaveLength(0)
  expect(w.clock.pending()).toBe(0)
  const hooks: Record<string, (...a: unknown[]) => unknown> = {}
  registerGitHeadHooks((n: string, fn: (...a: unknown[]) => unknown) => void (hooks[n] = fn))
  hooks['turn.complete'](undefined, {}, (e: unknown) => e)
  await w.clock.advance(MODE_INTERVAL_MS * 3)
  expect(w.calls).toHaveLength(0)
})

test('a turn.complete read is skipped when the previous read is younger than the interval', async () => {
  const w = await world(async () => record('drain-beads'))
  const hooks: Record<string, (...a: unknown[]) => unknown> = {}
  registerGitHeadHooks((n: string, fn: (...a: unknown[]) => unknown) => void (hooks[n] = fn))
  const shows = (): number => w.calls.filter(c => c.argv[1] === 'show').length
  expect(shows()).toBe(1)
  await w.clock.advance(2000)
  hooks['turn.complete'](undefined, {}, (e: unknown) => e)
  await w.clock.advance(0)
  expect(shows()).toBe(1)
})

test('a record written mid-turn (no turn.complete) is picked up by the poll, running then finished', async () => {
  let current: Answer = { exitCode: 2 }
  const w = await world(async () => current)
  expect(alertRow()).toBe('')
  expect(ids().includes('mode')).toBe(false)
  current = record(WRAP_UP_MODE, 'running')
  await w.clock.advance(MODE_INTERVAL_MS + 1)
  expect(readCached('sessionMode')).toEqual({ mode: WRAP_UP_MODE, state: 'running' })
  expect(alertRow()).toBe('wrapping up')
  expect(ids().includes('mode')).toBe(true)
  current = record(WRAP_UP_MODE, 'finished')
  await w.clock.advance(MODE_INTERVAL_MS + 1)
  expect(alertRow()).toBe('')
  expect(readCached('sessionMode')).toEqual({ mode: WRAP_UP_MODE, state: 'finished' })
})

test('parseMode maps kind, state and detail; a record without kind or state is a parse failure', () => {
  expect(parseMode('{"kind":"k","state":"running","detail":"d"}')).toEqual({ mode: 'k', state: 'running', detail: 'd' })
  expect(parseMode('{"kind":"k","state":"stopping"}')).toEqual({ mode: 'k', state: 'stopping' })
  let threw = false
  try {
    parseMode('{"state":"running"}')
  } catch {
    threw = true
  }
  expect(threw).toBe(true)
})
