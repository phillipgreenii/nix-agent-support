// The session-mode component against the REAL session-mode binary, in a throwaway state directory
// and with the session id passed in the run's env (a mock of the tool does not count).
//
// This file imports node modules, which the engine's own test loader refuses (the same as
// pr.realgh.test.ts); the nix check test-pgd-mod runs it under node with pkgs.session-mode on its
// inputs. It is fast, single-process and hermetic, so it stays in the unit gate.
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { expect, test } from 'claude-code/testing'
import { alertRow, resetAlertState } from './alerts'
import { clearBandButtons, pressButton, renderBand } from './band'
import { registerComponents } from './components'
import { registerGitHeadHooks } from './gitHead'
import { resetModals, setModalHost, pressAction, type ModalSpec } from './modal'
import { setOptions } from './options'
import { setSessionModeClock, WRAP_UP_MODE } from './session-mode'
import { initSources, readCached } from './source'
import { resetToggles } from './toggles'
import { DEFAULT_OPTIONS, type PgdOptions } from './types'
import { fakeClock, settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const only: PgdOptions = { ...DEFAULT_OPTIONS, components: { ...DEFAULT_OPTIONS.components, beads: false, prStatus: false } }

type Answer = { exitCode: number; stdout?: string } | Error
interface Call {
  argv: string[]
  env: Record<string, string>
}

// One session: sources started on a fake clock, the component registered, a recording modal host.
async function world(run: (argv: string[], env: Record<string, string>) => Promise<unknown>, options = only) {
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

const ids = (): string[] => renderBand({ sessionId: 'sess-1' }, 200, T).flat().map(l => l.id)


function realRunner(dir: string) {
  return async (argv: string[], env: Record<string, string>) => {
    const r = spawnSync(argv[0], argv.slice(1), {
      env: { PATH: process.env.PATH ?? '', HOME: dir, SESSION_MODE_STATE_DIR: `${dir}/state`, ...env },
      encoding: 'utf8',
      timeout: 20_000,
    })
    if (r.error) throw r.error
    return { exitCode: r.status ?? 1, stdout: r.stdout, stderr: r.stderr }
  }
}

test('the real session-mode: show gives exit 2 with no record, then mode, state and detail; Stop marks it stopping', async () => {
  const dir = mkdtempSync(`${tmpdir()}/pgd-session-mode-`)
  mkdirSync(`${dir}/state`)
  const real = realRunner(dir)
  const w = await world(real)
  // no record: the tool exits 2, so there is no value and no button
  expect(readCached('sessionMode')).toBe(undefined)
  expect(ids().includes('mode')).toBe(false)
  // a throwaway record made by the real tool, in the throwaway state directory
  const made = await real(['session-mode', 'start', WRAP_UP_MODE, '--detail', 'closing'], { CLAUDE_CODE_SESSION_ID: 'sess-1' })
  expect(made.exitCode).toBe(0)
  pressButton('mode')
  await settle()
  expect(w.opened).toHaveLength(0) // nothing to show before the next read
  setSessionModeClock(() => w.clock.now() + 60_000)
  const hooks: Record<string, (...a: unknown[]) => unknown> = {}
  registerGitHeadHooks((n: string, fn: (...a: unknown[]) => unknown) => void (hooks[n] = fn))
  hooks['turn.complete'](undefined, {}, (e: unknown) => e)
  await settle()
  expect(readCached('sessionMode')).toEqual({ mode: WRAP_UP_MODE, state: 'running', detail: 'closing' })
  expect(alertRow()).toBe('wrapping up')
  // another session id sees no record
  const other = await real(['session-mode', 'show'], { CLAUDE_CODE_SESSION_ID: 'sess-2' })
  expect(other.exitCode).toBe(2)
  // Stop through the modal runs the real set-status stopping and the source then shows it
  pressButton('mode')
  await settle()
  pressButton('mode')
  await settle()
  expect(w.opened.length > 0).toBe(true)
  pressAction('mode', 'stop')
  await settle()
  expect(readCached('sessionMode')).toEqual({ mode: WRAP_UP_MODE, state: 'stopping', detail: 'closing' })
  const shown = await real(['session-mode', 'show'], { CLAUDE_CODE_SESSION_ID: 'sess-1' })
  expect(JSON.parse(shown.stdout).state).toBe('stopping')
  // finished: the real record stays, so the button stays (as "wrapped up"), the alert goes
  const fin = await real(['session-mode', 'set-status', 'finished'], { CLAUDE_CODE_SESSION_ID: 'sess-1' })
  expect(fin.exitCode).toBe(0)
  setSessionModeClock(() => w.clock.now() + 120_000)
  hooks['turn.complete'](undefined, {}, (e: unknown) => e)
  await settle()
  expect(readCached('sessionMode')).toEqual({ mode: WRAP_UP_MODE, state: 'finished', detail: 'closing' })
  const drawn = renderBand({ sessionId: 'sess-1' }, 200, T).flat().find(l => l.id === 'mode')
  expect(drawn?.label).toBe('wrapped up [closing]')
  expect(drawn?.level).toBe('green')
  expect(alertRow()).toBe('')
  rmSync(dir, { recursive: true, force: true })
})
