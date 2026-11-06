// Tests of the Focus pane: empty state, ordering, per-row open, the button and /pgd-focus toggles,
// nothing drawn in the band, the off state, and the freedom-boundary choices documented in the README.
import { expect, test } from 'claude-code/testing'
import { clearBandButtons, pressButton, renderBand } from './band'
import { registerComponents } from './components'
import { initContributions, listContributions } from './contributions'
import { deriveKindAndId, deriveTitle, focusRows, focusSpec, openUrl, FOCUS_EMPTY_TEXT } from './focus-pane'
import { currentSpec, isModalOpen, pressAction, resetModals, setModalHost, type ModalSpec } from './modal'
import { pgdMethods } from './pgd-noun'
import { setOptions } from './options'
import { isOpen, onToggle, resetToggles } from './toggles'
import { registerFocusToggleCommand } from './toggleCommands'
import { DEFAULT_OPTIONS } from './types'
import { fakeClock, settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const offOptions = { ...DEFAULT_OPTIONS, components: { ...DEFAULT_OPTIONS.components, focus: false } }

function world(options = DEFAULT_OPTIONS) {
  resetToggles()
  resetModals()
  clearBandButtons()
  const clock = fakeClock()
  initContributions({ now: clock.now, setTimer: clock.setTimer, requestInvalidate: () => undefined })
  const sh = stubHost()
  const opened: ModalSpec[] = []
  const closed: string[] = []
  setModalHost({ open: s => void opened.push(s), close: id => void closed.push(id) })
  setOptions(options)
  registerComponents(options, sh.host)
  return { sh, opened, closed, noun: pgdMethods(options) }
}
const put = (w: ReturnType<typeof world>, id: string, priority: number, extra: object = {}) =>
  w.noun.put({ id, surface: 'focus', priority, text: `title of ${id}`, ...extra })
const band = () => renderBand({ sessionId: 'sess-1' }, 200, T).flat()

test('an empty pane reads Nothing tracked for this session.', () => {
  world()
  expect(focusRows()).toEqual([])
  expect(focusSpec().items).toEqual([{ label: '', value: 'Nothing tracked for this session.' }])
  expect(FOCUS_EMPTY_TEXT).toBe('Nothing tracked for this session.')
})

test('rows are ordered by priority then time added, with open only when the item has a url', async () => {
  const w = world()
  await put(w, 'bead:b-low', 1)
  await put(w, 'jira:J-1', 9, { url: 'https://e.test/J-1' })
  await put(w, 'pr:77', 9)
  await put(w, 'thread:t', 5, { url: 'https://e.test/t' })
  expect(focusRows().map(r => `${r.kind}/${r.id}/${r.open ?? '-'}`)).toEqual([
    'jira/J-1/open:jira:J-1',
    'pr/77/-',
    'thread/t/open:thread:t',
    'bead/b-low/-',
  ])
  const items = focusSpec().items
  expect(items[0]).toEqual({
    label: 'jira',
    value: 'J-1  title of jira:J-1',
    action: { id: 'open:jira:J-1', label: '[ open ]' },
  })
  expect(items[1].action).toBe(undefined)
})

test('the Focus button opens and closes the pane, and nothing from it is drawn in the band', async () => {
  const w = world()
  await put(w, 'bead:b-1', 1)
  const btn = band().find(l => l.id === 'focus')
  expect(btn).toEqual({ id: 'focus', label: 'Focus', action: 'focus' })
  const ids = band().map(l => l.id)
  expect(ids.includes('focus')).toBe(true)
  pressButton('focus')
  await settle()
  expect(isOpen('focus')).toBe(true)
  expect(isModalOpen('focus')).toBe(true)
  expect(w.opened.map(s => s.id)).toEqual(['focus'])
  expect(w.opened[0].items[0].value).toContain('b-1')
  expect(band().map(l => l.label).join(' ').includes('b-1')).toBe(false)
  pressButton('focus')
  expect(isOpen('focus')).toBe(false)
  expect(w.closed).toEqual(['focus'])
})

test('the pane is its own pane id, so it opens beside another pane', async () => {
  const w = world()
  pressButton('info')
  pressButton('focus')
  await settle()
  expect(isModalOpen('info')).toBe(true)
  expect(isModalOpen('focus')).toBe(true)
  expect(w.opened.map(s => s.id).sort()).toEqual(['focus', 'info'])
  expect(currentSpec('focus')?.id).toBe('focus')
})

test('the Focus pane toggles through toggles.ts from /pgd-focus', async () => {
  const w = world()
  const handlers: Record<string, () => { text: string }> = {}
  registerFocusToggleCommand((name: string, m: { command: string }, h: () => { text: string }) => {
    if (name === 'command.run') handlers[m.command] = h
  })
  const told: string[] = []
  const off = onToggle((id, open) => void told.push(`${id}=${open}`))
  expect(handlers['pgd-focus']().text).toBe('focus shown.')
  await settle()
  expect(w.opened.map(s => s.id)).toEqual(['focus'])
  expect(handlers['pgd-focus']().text).toBe('focus hidden.')
  expect(told).toEqual(['focus=true', 'focus=false'])
  expect(w.sh.commands.map(c => c.name)).toContain('pgd-focus')
  off()
})

test('with components.focus off there is no button, pane, command or contribution storage', async () => {
  const w = world(offOptions)
  expect(band().find(l => l.id === 'focus')).toBe(undefined)
  expect(w.sh.commands.map(c => c.name).includes('pgd-focus')).toBe(false)
  await put(w, 'bead:b-1', 1)
  expect(listContributions('focus')).toEqual([])
  pressButton('focus')
  await settle()
  expect(w.opened).toEqual([])
  const handlers: Record<string, () => { text: string }> = {}
  registerFocusToggleCommand((name: string, m: { command: string }, h: () => { text: string }) => {
    if (name === 'command.run') handlers[m.command] = h
  })
  expect(handlers['pgd-focus']().text).toBe('pgd focus is disabled.')
})

test('a draw of the pane rows starts no fetch, process or file read', async () => {
  const w = world()
  await put(w, 'bead:b-1', 1, { url: 'https://e.test/1' })
  focusRows()
  focusSpec()
  expect(w.sh.calls.filter(c => c !== 'ui.invalidate')).toEqual([])
})

test('pressing a row open runs the opener from the action with the item url, never from a draw', async () => {
  const w = world()
  await put(w, 'jira:J-1', 1, { url: 'https://e.test/J-1' })
  pressButton('focus')
  await settle()
  expect(w.sh.calls.includes('process.run')).toBe(false)
  pressAction('focus', 'open:jira:J-1')
  await settle()
  expect(w.sh.calls.filter(c => c === 'process.run')).toHaveLength(1)
})

test('openUrl opens only http and https urls', async () => {
  const sh = stubHost()
  await openUrl(sh.host, 'file:///etc/passwd')
  await openUrl(sh.host, 'not a url')
  expect(sh.calls).toEqual([])
  await openUrl(sh.host, 'https://e.test/x')
  expect(sh.calls).toEqual(['process.run'])
})

test('kind and title derive from the contribution id and text', () => {
  expect(deriveKindAndId('bead:pg2-1')).toEqual({ kind: 'bead', id: 'pg2-1' })
  expect(deriveKindAndId('a:b:c')).toEqual({ kind: 'a', id: 'b:c' })
  expect(deriveKindAndId('plain')).toEqual({ kind: 'item', id: 'plain' })
  expect(deriveKindAndId(':x')).toEqual({ kind: 'item', id: ':x' })
  expect(deriveKindAndId('x:')).toEqual({ kind: 'item', id: 'x:' })
  expect(deriveTitle('\n  first line \nsecond', 'id')).toBe('first line')
  expect(deriveTitle('  ', 'id')).toBe('id')
})
