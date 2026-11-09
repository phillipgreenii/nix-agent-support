// Tests of the Info and Context modals and their buttons: one test per button opening its modal
// (observed through a recording modal host), the content tests, the toggles from a button, a slash
// command and the host's own close, and the freedom-boundary choices documented in the README.
import { expect, test } from 'claude-code/testing'
import { registerComponents } from './components'
import { bandLabels, clearBandButtons, pressButton, renderBand } from './band'
import { recordCompaction, contextSpec } from './context-modal'
import { infoSpec, resetSessionFacts, actorOf } from './info-modal'
import { hostClosed, resetModals, setModalHost, type ModalSpec, CLOSE_ACTION, isModalOpen } from './modal'
import { setOptions } from './options'
import { defineSource, initSources, setValue } from './source'
import { hydrateToggles, isOpen, resetToggles, setOpen } from './toggles'
import { registerToggleCommands, TOGGLE_COMMANDS } from './toggleCommands'
import { DEFAULT_OPTIONS } from './types'
import type { Usage } from './usage'
import { fakeClock, settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds

// A clean world: sources started, every module registry reset, the modal host recording.
async function world(usage?: Usage) {
  resetToggles()
  resetModals()
  clearBandButtons()
  resetSessionFacts()
  const sh = stubHost()
  initSources(sh.host, fakeClock())
  setOptions(DEFAULT_OPTIONS)
  defineSource<Usage>({ id: 'usage', intervalMs: 0 })
  defineSource<{ branch?: string; detached?: string; worktree?: string }>({ id: 'gitHead', intervalMs: 0 })
  defineSource<{ number: number; reviewState: string }>({ id: 'pr', intervalMs: 0 })
  defineSource<string>({ id: 'sessionName', intervalMs: 0 })
  if (usage) setValue('usage', usage)
  const opened: ModalSpec[] = []
  const closed: string[] = []
  setModalHost({ open: s => void opened.push(s), close: id => void closed.push(id) })
  registerComponents(DEFAULT_OPTIONS, sh.host)
  await settle()
  return { sh, opened, closed }
}

const rows = (s: ModalSpec) => s.items.map(i => `${i.label}: ${i.value}`)

test('the session id button opens the Info modal: one item per line', async () => {
  const w = await world()
  setValue('gitHead', { branch: 'feat', worktree: '/work/repo/.worktrees/feat' })
  setValue('pr', { number: 412, reviewState: 'CHANGES_REQUESTED' })
  const label = bandLabels({ sessionId: 'f4e6ce80-6dd3-48c4-b6a6-6d1663ff0d9a' }, T)[0]
  expect(label.action).toBe('info')
  pressButton(label.action as string)
  await settle()
  expect(w.opened).toHaveLength(1)
  expect(w.opened[0].id).toBe('info')
  expect(rows(w.opened[0])).toEqual([
    'name: sess-1',
    'session id: sess-1',
    'actor: sess-1',
    'version: 2.1.314',
    'repo: owner/repo',
    'worktree: /work/repo/.worktrees/feat',
    'branch: feat',
    'PR: #412 (changes requested)',
  ])
  expect(w.opened[0].items[7].color).toBe('red')
  expect(w.opened[0].actions.map(a => a.id)).toEqual([CLOSE_ACTION])
})

test('the context button opens the Context and usage modal', async () => {
  const w = await world({
    contextPercent: 84.2,
    contextTokens: 168400,
    limits: [
      { kind: 'five_hour', usedPercent: 83, resetsAt: '2026-11-06T18:00:00Z' },
      { kind: 'seven_day', usedPercent: 12 },
    ],
    costUsd: 1.5,
  })
  const label = bandLabels({ sessionId: 's', usage: { contextPercent: 84.2, limits: [] } }, T).find(l => l.id === 'context')
  expect(label?.action).toBe('context')
  pressButton(label?.action as string)
  await settle()
  expect(w.opened).toHaveLength(1)
  expect(w.opened[0].id).toBe('context')
  expect(rows(w.opened[0])).toEqual([
    'context: 84% (168,400 tokens)'.replace(' (', ', ').replace(')', ''),
    '5h limit: 83%, resets 2026-11-06T18:00:00Z',
    '7d limit: 12%',
    'cost: $1.50',
  ])
  expect(w.opened[0].items[0].color).toBe('red')
  expect(w.opened[0].items[1].color).toBe('red')
  expect(w.opened[0].items[2].color).toBe(undefined)
})

test('the Info modal omits branch and worktree when the .git reads fail, and the PR row without a PR value', async () => {
  const w = await world()
  pressButton('info')
  await settle()
  const got = rows(w.opened[0])
  expect(got.some(r => r.startsWith('branch'))).toBe(false)
  expect(got.some(r => r.startsWith('worktree'))).toBe(false)
  expect(got.some(r => r.startsWith('PR'))).toBe(false)
  // with no name the name row repeats the session id
  expect(got[0]).toBe('name: sess-1')
  // a detached head shows as a branch row
  setValue('gitHead', { detached: 'abcdef0123456789' })
  expect(rows(infoSpec()).includes('branch: detached at abcdef01')).toBe(true)
  // the name row appears when a name source has a value
  setValue('sessionName', 'drain-bead')
  expect(rows(infoSpec())[0]).toBe('name: drain-bead')
})

test('the Context modal shows each limit with its reset time only when present', async () => {
  await world({
    limits: [
      { kind: 'five_hour', usedPercent: 10 },
      { kind: 'seven_day', usedPercent: 20, resetsAt: 'tomorrow' },
    ],
  })
  expect(rows(contextSpec())).toEqual(['5h limit: 10%', '7d limit: 20%, resets tomorrow'])
  setValue('usage', { limits: [{ kind: 'seven_day', usedPercent: 20 }] })
  expect(rows(contextSpec())).toEqual(['7d limit: 20%'])
  setValue('usage', { limits: [] })
  expect(rows(contextSpec())).toEqual([])
})

test('the Context modal shows last compaction when the event-driven value exists and omits it otherwise', async () => {
  await world({ contextPercent: 10, limits: [] })
  expect(rows(contextSpec())).toEqual(['context: 10%'])
  recordCompaction(() => Date.UTC(2026, 10, 6, 12, 3))
  expect(rows(contextSpec())).toEqual(['context: 10%', 'last compaction: 2026-11-06 12:03 UTC'])
})

test('context colour is by level: normal, yellow from contextWarn, red from contextAlert', async () => {
  await world({ contextPercent: 59, limits: [] })
  expect(contextSpec().items[0].color).toBe(undefined)
  setValue('usage', { contextPercent: 60, limits: [] })
  expect(contextSpec().items[0].color).toBe('yellow')
  setValue('usage', { contextPercent: 80, limits: [] })
  expect(contextSpec().items[0].color).toBe('red')
})

test('a second press on an open modal closes it (the press toggles)', async () => {
  const w = await world({ contextPercent: 10, limits: [] })
  pressButton('context')
  await settle()
  expect(isOpen('context')).toBe(true)
  expect(isModalOpen('context')).toBe(true)
  pressButton('context')
  expect(isOpen('context')).toBe(false)
  expect(w.closed).toEqual(['context'])
  expect(w.opened).toHaveLength(1)
})

test('the band and each modal toggle from a slash command', async () => {
  const w = await world({ contextPercent: 10, limits: [] })
  const handlers: Record<string, () => { text: string }> = {}
  registerToggleCommands((name: string, m: { command: string }, h: () => { text: string }) => {
    if (name === 'command.run') handlers[m.command] = h
  })
  expect(TOGGLE_COMMANDS.map(c => c.name)).toEqual(['pgd-band', 'pgd-info', 'pgd-context'])
  expect(w.sh.commands.map(c => c.name)).toEqual(['pgd-focus', 'pgd-band', 'pgd-info', 'pgd-context'])
  expect(handlers['pgd-context']().text).toBe('context shown.')
  await settle()
  expect(w.opened.map(s => s.id)).toEqual(['context'])
  expect(handlers['pgd-context']().text).toBe('context hidden.')
  expect(w.closed).toEqual(['context'])
  expect(handlers['pgd-info']().text).toBe('info shown.')
  await settle()
  expect(w.opened.map(s => s.id)).toEqual(['context', 'info'])
  expect(handlers['pgd-band']().text).toBe('band hidden.')
  expect(isOpen('band')).toBe(false)
  expect(handlers['pgd-band']().text).toBe('band shown.')
  expect(isOpen('band')).toBe(true)
})

test('the band draws no toggle button of its own: the engine draws the only [-]', async () => {
  await world({ contextPercent: 10, limits: [] })
  const got = renderBand({ sessionId: 'abcdef12', usage: { contextPercent: 10, limits: [] } }, 80, T)
  expect(got[0].map(l => l.label)).toEqual(['abcdef12', 'ctx 10%', 'Assume', 'Focus', 'Wrap up'])
  expect(got.flat().some(l => l.action === 'band')).toBe(false)
})

test('a modal the host closed (Esc or dismiss) resyncs the toggle state', async () => {
  const w = await world({ contextPercent: 10, limits: [] })
  pressButton('context')
  await settle()
  hostClosed('context')
  expect(isOpen('context')).toBe(false)
  expect(isModalOpen('context')).toBe(false)
  // the next press opens it again, with no close call to the host
  pressButton('context')
  await settle()
  expect(w.opened).toHaveLength(2)
  expect(w.closed).toEqual([])
})

test('a toggle that was open before a hot reload stays open after hydration', async () => {
  await world({ contextPercent: 10, limits: [] })
  setOpen('band', false)
  setOpen('info', true)
  const keep = { band: false, info: true }
  resetToggles()
  hydrateToggles(keep)
  expect(isOpen('band')).toBe(false)
  expect(isOpen('info')).toBe(true)
})

test('the actor is the session id cut to eight characters and an ellipsis', () => {
  expect(actorOf('f4e6ce80-6dd3-48c4')).toBe('f4e6ce80…')
  expect(actorOf('short')).toBe('short')
})
