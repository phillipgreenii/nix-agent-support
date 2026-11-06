// Tests of the Wrap up button: always drawn, pre-fills the wrap-up skill's slash command, never
// submits and opens no modal; the command text matches the skill; the choices are documented.
import { expect, test } from 'claude-code/testing'
import { clearBandButtons, pressButton, renderBand } from './band'
import { registerComponents } from './components'
import { resetModals, setModalHost, type ModalSpec } from './modal'
import { setOptions } from './options'
import { resetToggles } from './toggles'
import { DEFAULT_OPTIONS } from './types'
import { WRAP_UP_COMMAND, WRAP_UP_LABEL } from './wrap-up'
import { settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const off = {
  ...DEFAULT_OPTIONS,
  components: { beads: false, assume: false, sessionMode: false, prStatus: false, sessionName: false, links: false, modelEffort: false, focus: false },
}

function world(options = off) {
  resetToggles()
  resetModals()
  clearBandButtons()
  const sh = stubHost()
  const opened: ModalSpec[] = []
  setModalHost({ open: s => void opened.push(s), close: () => undefined })
  setOptions(options)
  registerComponents(options, sh.host)
  return { sh, opened }
}
const row = () =>
  renderBand({ sessionId: 'sess-1' }, 200, T)
    .flat()
    .find(l => l.id === 'wrapup')

test('the button is drawn with every component off, before the band toggle', () => {
  world()
  expect(row()).toEqual({ id: 'wrapup', label: WRAP_UP_LABEL, action: 'wrapup' })
  const ids = renderBand({ sessionId: 'sess-1' }, 200, T).flat().map(l => l.id)
  expect(ids[ids.length - 2]).toBe('wrapup')
})

test('no button when enable is false', () => {
  world({ ...off, enable: false })
  expect(row()).toBeUndefined()
})

test('pressing pre-fills the slash command, never submits, and opens no modal', async () => {
  const w = world()
  pressButton('wrapup')
  await settle()
  expect(w.sh.fills).toEqual([{ text: '/session-wrapup:wrap-up-session', mode: 'replace' }])
  expect(w.sh.submits).toEqual([])
  expect(w.opened).toEqual([])
})

test('a second press fills again (no toggle state)', async () => {
  const w = world()
  pressButton('wrapup')
  pressButton('wrapup')
  await settle()
  expect(w.sh.fills).toHaveLength(2)
})

test('the command text is the plugin-qualified wrap-up-session skill (session-wrapup plugin)', () => {
  expect(WRAP_UP_COMMAND).toBe('/session-wrapup:wrap-up-session')
})

test('the glyph choice: the label is plain text with no glyph', () => {
  world()
  expect(row()?.label).toBe('Wrap up')
})
